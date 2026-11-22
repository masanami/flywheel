package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルはサイクルの API（docs/features/m4-ui-server.md §IF / API「書き込みの API」）。
// 1 周は server の中では走らせず、自分と同じ実行ファイルの `cycle` を別プロセスとして起動する
// （周の排他・予算・記録は CLI の cycle と同じ経路〔core〕に乗る）。

const (
	// cycleSignalWait は周の開始の合図を待つ最大の時間（本番の値）。
	cycleSignalWait = 3 * time.Second
	// cycleStderrTail はエラーの message に入れる子の標準エラーの末尾の最大バイト数。
	cycleStderrTail = 2048
)

// cycleStartedSuffix は周の開始の合図の行の末尾（CLI の `cycle` が書く「周 <ID> を開始した」）。
const cycleStartedSuffix = "を開始した"

// signalWriter は完結した行のうち、開始の合図の行の形（cycleStartedSuffix で終わる行）を 1 度だけ通知し、
// 以後は読み捨てる Writer。ほかの行は合図にしない。
type signalWriter struct {
	mu     sync.Mutex
	once   sync.Once
	signal chan struct{}
	buf    []byte
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf == nil && len(p) == 0 {
		return 0, nil
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		if strings.HasSuffix(line, cycleStartedSuffix) {
			w.once.Do(func() { close(w.signal) })
		}
	}
	// 改行の無い長い出力で溜め続けない（合図の行は短い）。
	if len(w.buf) > 4096 {
		w.buf = w.buf[len(w.buf)-4096:]
	}
	return len(p), nil
}

// tailWriter は書かれたバイト列の末尾 limit バイトだけを保持する Writer（読み捨てを兼ねる）。
type tailWriter struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.limit {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-w.limit:]...)
	}
	return len(p), nil
}

// String は保持した末尾を、UTF-8 の文字の途中から始まらないよう先頭を詰めて返す。
func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.buf
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	return string(b)
}

// CycleLaunch は、サイクルを切り離して始める処理の結果。
// Started は周の開始の合図を見たこと（または合図も終了も無いまま待ち時間が過ぎたこと）、AlreadyRunning は、始めたプロセスが
// 排他で断られたこと（ほかの操作が先に始めた）。どちらも偽なら、プロセスは周を始めずに終了コード 0 で終わった（始めるものが無かった）。
type CycleLaunch struct {
	Started        bool
	AlreadyRunning bool
}

// CycleStarter は workspace で trigger のサイクルを切り離して始める処理（StartCycle と同じ形。試験が差し替える）。
type CycleStarter func(workspace, trigger string) (CycleLaunch, error)

// cycleChildExit は、周を始める前に起動した子が 0 以外の終了コードで終わったことを表すエラー。
type cycleChildExit struct {
	code   int
	stderr string
}

func (e *cycleChildExit) Error() string {
	return fmt.Sprintf("サイクルを始める前に、起動したプロセスが終了した（終了コード %d）: %s", e.code, e.stderr)
}

// cycleLockedMarker は、`flywheel cycle` が排他で断られたときに標準エラーへ書く行の、エラーコードの部分
// （internal/cli の CodeLocked。server は cli を import しないので、値をここに写す）。
const cycleLockedMarker = "(locked)"

// StartCycle は workspace のワークスペースで、自分と同じ実行ファイルの `cycle --trigger <trigger>` を別プロセスとして起動し、
// 周の開始の合図か子の終了か待ち時間の経過まで待って戻る（サイクルの終わりは待たない）。CLI の操作と server の「サイクルを回す」が共有する。
// 子が排他で断られたときは AlreadyRunning（エラーにしない）。起動できない・ほかの理由で子が 0 以外で終わったときはエラー。
func StartCycle(workspace, trigger string) (CycleLaunch, error) {
	return NewCycleStarter(defaultExecutable)(workspace, trigger)
}

// NewCycleStarter は、実行ファイルの解決を差し替えられる StartCycle の形（試験が偽の実行ファイルを渡す）。
func NewCycleStarter(executable func() (string, error)) CycleStarter {
	return NewCycleStarterWait(executable, cycleSignalWait)
}

// NewCycleStarterWait は、合図を待つ時間も渡せる版。
func NewCycleStarterWait(executable func() (string, error), signalWait time.Duration) CycleStarter {
	return func(workspace, trigger string) (CycleLaunch, error) {
		l, err := launchCycle(executable, signalWait, workspace, trigger)
		var ce *cycleChildExit
		if errors.As(err, &ce) && strings.Contains(ce.stderr, cycleLockedMarker) {
			return CycleLaunch{AlreadyRunning: true}, nil
		}
		return l, err
	}
}

// defaultExecutable は自分自身の実行ファイル。go test のバイナリ（名前が .test で終わる）は、サイクルの子として起動すると
// テストを再帰的に走らせてしまうので、起動できないものとして扱う。
func defaultExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved // 欄の返事ごとの一時のシンボリックリンクを、サイクルの子（長く走り、次のサイクルも起動する）の実行ファイルにしない。
	}
	if strings.HasSuffix(filepath.Base(exe), ".test") {
		return "", fmt.Errorf("テストのバイナリ（%s）はサイクルの子として起動できない", exe)
	}
	return exe, nil
}

// envWithoutChatMark は環境から対話の欄の印を除く。承認などを欄から実行した後に始めるサイクルの子は、欄の返事の一部ではなく、
// 印を引き継ぐと、子の `cycle` が欄の一覧の検査で断られて始まらない。印の検査は、親のコマンドが済ませている。
func envWithoutChatMark(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, core.ChatEnvSession+"=") || strings.HasPrefix(kv, core.ChatEnvMessage+"=") || strings.HasPrefix(kv, core.ChatEnvTools+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func launchCycle(executable func() (string, error), signalWait time.Duration, workspacePath, trigger string) (CycleLaunch, error) {
	exe, err := executable()
	if err != nil {
		return CycleLaunch{}, fmt.Errorf("サイクルを起動できない: %w", err)
	}
	cmd := exec.Command(exe, "cycle", "--trigger", trigger, "--workspace", workspacePath)
	cmd.Dir = workspacePath
	cmd.Env = envWithoutChatMark(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out := &signalWriter{signal: make(chan struct{})}
	errTail := &tailWriter{limit: cycleStderrTail}
	cmd.Stdout, cmd.Stderr = out, errTail
	if err := cmd.Start(); err != nil {
		return CycleLaunch{}, fmt.Errorf("サイクルを起動できない: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(signalWait)
	defer timer.Stop()
	select {
	case <-out.signal:
		return CycleLaunch{Started: true}, nil
	case werr := <-done:
		// Wait は出力の複写の終了後に返るので、合図が先に届いていればここで見える。
		select {
		case <-out.signal:
			return CycleLaunch{Started: true}, nil
		default:
		}
		code := 0
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			code = ee.ExitCode()
		} else if werr != nil {
			code = -1
		}
		if code == 0 {
			return CycleLaunch{}, nil // 合図の前に終了コード 0 で終わった（サイクルを始めなかった）。
		}
		return CycleLaunch{}, &cycleChildExit{code: code, stderr: errTail.String()}
	case <-timer.C:
		return CycleLaunch{Started: true}, nil // 合図も終了も無い。子は止めない（Wait は goroutine が回収する）。
	}
}

// StartCycleAfterOperation は、操作（approve・reject・answer など）が成立した後に、サイクルを始めるかを決めて、始める。
// 操作が成立していないときは呼ばない（本人確認の失敗・版の食い違いのときは、呼び出し側が呼ばない）。
// 理由の決め方: noCycle（--no-cycle）か宣言の auto_cycle が偽なら disabled、生きている排他があれば already_running
// （古い排他は走っていない扱い）、いま始められるものが無ければ no_work。始めるときは start を 1 回呼び、
// エラー・start が nil のときは unavailable、子が排他で断られたら already_running。
// 操作そのものの成否には影響しない（呼び出し側は、返り値で終了コードを変えない）。
func StartCycleAfterOperation(ctx context.Context, st *core.Store, trigger string, noCycle bool, start CycleStarter) view.CycleStart {
	return view.FromCycleStartReason(decideAndStartCycle(ctx, st, trigger, noCycle, start))
}

func decideAndStartCycle(ctx context.Context, st *core.Store, trigger string, noCycle bool, start CycleStarter) core.CycleStartReason {
	reason := decideCycleStart(ctx, st, noCycle)
	if reason != core.CycleStartStarted {
		return reason
	}
	return startDecidedCycle(st.Workspace(), trigger, start)
}

// decideCycleStart は、始めるかどうかの判定だけを行う（読み取りだけ。子は起動しない）。始めてよいときは started を返す。
func decideCycleStart(ctx context.Context, st *core.Store, noCycle bool) core.CycleStartReason {
	if noCycle {
		return core.CycleStartDisabled
	}
	agent, err := core.CheckDeclarations(st.Workspace())
	if err != nil {
		return core.CycleStartUnavailable
	}
	reason, err := st.CheckCycleStart(ctx, agent)
	if err != nil {
		return core.CycleStartUnavailable
	}
	return reason
}

// startDecidedCycle は、判定が済んだ後に切り離して始める（ストアを使わない。画面の口はワークスペースの借りを返してから呼ぶ）。
func startDecidedCycle(workspace, trigger string, start CycleStarter) core.CycleStartReason {
	if start == nil {
		return core.CycleStartUnavailable
	}
	l, err := start(workspace, trigger)
	switch {
	case err != nil:
		return core.CycleStartUnavailable
	case l.AlreadyRunning:
		return core.CycleStartAlreadyRunning
	case !l.Started:
		return core.CycleStartNoWork
	}
	return core.CycleStartStarted
}

// cycleHandler は {name} のワークスペースの GET（状態）と POST（1 周の起動）。
func cycleHandler(fleet *Fleet, launch CycleStarter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			cycleStatusHandler(fleet, w, r)
		case http.MethodPost:
			cycleStartHandler(fleet, launch, w, r)
		default:
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
		}
	}
}

func cycleStatusHandler(fleet *Fleet, w http.ResponseWriter, r *http.Request) {
	st, release, le := fleet.Acquire(r.Context(), r.PathValue("name"))
	if le != nil {
		writeError(w, le.Status, le.Code, le.Message)
		return
	}
	defer release()
	cs, err := st.ReadCycleStatus(r.Context())
	if err != nil {
		writeCoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view.FromCycleStatus(cs))
}

// cycleStartHandler は、生きているロックがあれば起動せず 409 locked。heartbeat が回収の時間を過ぎた古いロックは走っていない扱いで、
// 起動した CLI の cycle が回収する（承認などの操作の後にサイクルを始める判定と同じ）。「進むものが無い」の数えは、人が明示して
// 始める操作なので、CLI の cycle と同じ CountCycleWorkInWorkspace の Total（学び・改修の提案などだけが残るときも始める）。読み取りと子の BeginCycle の間の競合で子が排他で断られたときも 409 locked にする。きっかけは ui のまま。
func cycleStartHandler(fleet *Fleet, launch CycleStarter, w http.ResponseWriter, r *http.Request) {
	st, release, le := fleet.Acquire(r.Context(), r.PathValue("name"))
	if le != nil {
		writeError(w, le.Status, le.Code, le.Message)
		return
	}
	cs, err := st.ReadCycleStatus(r.Context())
	path := st.Workspace()
	// 子を起動する前に、CLI の cycle と同じ口で、サイクルを回すと進むものを数える。宣言を読めない・数えに失敗したときは
	// 数えを当てにせず子を起動する（子が宣言の不備を報告する）。
	nothing := false
	if err == nil && cs.State != core.CycleStateRunning {
		if agent, derr := core.CheckDeclarations(path); derr == nil && agent != nil {
			if work, werr := st.CountCycleWorkInWorkspace(r.Context(), agent); werr == nil && work.Total == 0 {
				nothing = true
			}
		}
	}
	release()
	if err != nil {
		writeCoreError(w, err)
		return
	}
	if cs.State == core.CycleStateRunning {
		writeCoreError(w, core.ErrLocked)
		return
	}
	if nothing {
		writeJSON(w, http.StatusOK, view.CycleStarted{Started: false})
		return
	}
	l, err := launch(path, core.CycleTriggerUI)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, err.Error())
		return
	}
	if l.AlreadyRunning {
		writeCoreError(w, core.ErrLocked)
		return
	}
	writeJSON(w, http.StatusOK, view.CycleStarted{Started: l.Started})
}

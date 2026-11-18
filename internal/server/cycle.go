package server

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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

// cycleTriggerUI は UI から起動した周の契機。
const cycleTriggerUI = "ui"

// cycleLauncher は workspacePath のワークスペースで 1 周を別プロセスとして起動し、周の開始の合図
// （子の標準出力のうち「を開始した」で終わる行）か子の終了か待ち時間の経過まで待って、周を始めたかを返す。
// 合図の前に子が終了コード 0 で終わったときは、周を始めていない（false。エラーにしない）。0 以外で終わったとき、
// または起動できなかったときはエラー（子の終了コードと標準エラーの末尾を含む）。
type cycleLauncher func(workspacePath string) (started bool, err error)

const (
	// cycleSignalWait は周の開始の合図を待つ最大の時間。
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

// execLauncher は executable が返す実行ファイルを `cycle --trigger ui --workspace <path>` で起動する。
// 子の標準出力・標準エラーは合図か終了までだけ見て、その後は読み捨てる（記録は残さない）。
// server の終了で子を止めないよう、子は別のセッションにする。
func execLauncher(executable func() (string, error)) cycleLauncher {
	return func(workspacePath string) (bool, error) {
		exe, err := executable()
		if err != nil {
			return false, fmt.Errorf("サイクルを起動できない: %w", err)
		}
		cmd := exec.Command(exe, "cycle", "--trigger", cycleTriggerUI, "--workspace", workspacePath)
		cmd.Dir = workspacePath
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		out := &signalWriter{signal: make(chan struct{})}
		errTail := &tailWriter{limit: cycleStderrTail}
		cmd.Stdout, cmd.Stderr = out, errTail
		if err := cmd.Start(); err != nil {
			return false, fmt.Errorf("サイクルを起動できない: %w", err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		timer := time.NewTimer(cycleSignalWait)
		defer timer.Stop()
		select {
		case <-out.signal:
			return true, nil
		case werr := <-done:
			// Wait は出力の複写の終了後に返るので、合図が先に届いていればここで見える。
			select {
			case <-out.signal:
				return true, nil
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
				return false, nil // 合図の前に終了コード 0 で終わった（周を始めなかった）。
			}
			return false, fmt.Errorf("周の開始の前にサイクルが終了した（終了コード %d）: %s", code, errTail.String())
		case <-timer.C:
			return true, nil // 合図も終了も無い。子は止めない（Wait は goroutine が回収する）。
		}
	}
}

var defaultLauncher = execLauncher(os.Executable)

// cycleHandler は {name} のワークスペースの GET（状態）と POST（1 周の起動）。
func cycleHandler(fleet *Fleet, launch cycleLauncher) http.HandlerFunc {
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

// cycleStartHandler は、ロックの行があれば（heartbeat の新旧によらず）起動せず 409 locked。
// 古いロックの回収は次の CLI の cycle に任せる。読み取りと子の BeginCycle の間の競合で子が
// locked で終わることは受け入れる（起動直後の失敗は last_result で見る）。
func cycleStartHandler(fleet *Fleet, launch cycleLauncher, w http.ResponseWriter, r *http.Request) {
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
	if err == nil && !cs.LockHeld {
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
	if cs.LockHeld {
		writeCoreError(w, core.ErrLocked)
		return
	}
	if nothing {
		writeJSON(w, http.StatusOK, view.CycleStarted{Started: false})
		return
	}
	started, err := launch(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view.CycleStarted{Started: started})
}

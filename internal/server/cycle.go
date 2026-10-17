package server

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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
// （子の標準出力の最初の 1 行）か子の終了か待ち時間の経過まで待って返す。合図の前に子が終了した
// とき、または起動できなかったときはエラー（子の終了コードと標準エラーの末尾を含む）。
type cycleLauncher func(workspacePath string) error

const (
	// cycleSignalWait は周の開始の合図を待つ最大の時間。
	cycleSignalWait = 3 * time.Second
	// cycleStderrTail はエラーの message に入れる子の標準エラーの末尾の最大バイト数。
	cycleStderrTail = 2048
)

// signalWriter は最初の改行までに書かれた行を合図として 1 度だけ通知し、以後は読み捨てる Writer。
type signalWriter struct {
	once   sync.Once
	signal chan struct{}
	seen   bool
}

func (w *signalWriter) Write(p []byte) (int, error) {
	if !w.seen && bytes.IndexByte(p, '\n') >= 0 {
		w.seen = true
		w.once.Do(func() { close(w.signal) })
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
	return func(workspacePath string) error {
		exe, err := executable()
		if err != nil {
			return fmt.Errorf("サイクルを起動できない: %w", err)
		}
		cmd := exec.Command(exe, "cycle", "--trigger", cycleTriggerUI, "--workspace", workspacePath)
		cmd.Dir = workspacePath
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		out := &signalWriter{signal: make(chan struct{})}
		errTail := &tailWriter{limit: cycleStderrTail}
		cmd.Stdout, cmd.Stderr = out, errTail
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("サイクルを起動できない: %w", err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		timer := time.NewTimer(cycleSignalWait)
		defer timer.Stop()
		select {
		case <-out.signal:
			return nil
		case werr := <-done:
			// Wait は出力の複写の終了後に返るので、合図が先に届いていればここで見える。
			select {
			case <-out.signal:
				return nil
			default:
			}
			code := 0
			var ee *exec.ExitError
			if errors.As(werr, &ee) {
				code = ee.ExitCode()
			}
			return fmt.Errorf("周の開始の前にサイクルが終了した（終了コード %d）: %s", code, errTail.String())
		case <-timer.C:
			return nil // 合図も終了も無い。子は止めない（Wait は goroutine が回収する）。
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
	release()
	if err != nil {
		writeCoreError(w, err)
		return
	}
	if cs.LockHeld {
		writeCoreError(w, core.ErrLocked)
		return
	}
	if err := launch(path); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view.CycleStarted{Started: true})
}

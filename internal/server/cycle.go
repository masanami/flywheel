package server

import (
	"net/http"
	"os"
	"os/exec"
	"syscall"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルはサイクルの API（docs/features/m4-ui-server.md §IF / API「書き込みの API」）。
// 1 周は server の中では走らせず、自分と同じ実行ファイルの `cycle` を別プロセスとして起動する
// （周の排他・予算・記録は CLI の cycle と同じ経路〔core〕に乗る）。

// cycleTriggerUI は UI から起動した周の契機。
const cycleTriggerUI = "ui"

// cycleLauncher は workspacePath のワークスペースで 1 周を別プロセスとして起動し、
// 終了を待たずに返す。起動できなければエラー。
type cycleLauncher func(workspacePath string) error

// execLauncher は executable が返す実行ファイルを `cycle --trigger ui --workspace <path>` で起動する。
// 標準入出力は捨て、goroutine で Wait して回収する。server の終了で子を止めないよう、
// 子は別のセッションにする（端末の割り込みや親のプロセスグループへのシグナルを受けない）。
func execLauncher(executable func() (string, error)) cycleLauncher {
	return func(workspacePath string) error {
		exe, err := executable()
		if err != nil {
			return err
		}
		cmd := exec.Command(exe, "cycle", "--trigger", cycleTriggerUI, "--workspace", workspacePath)
		cmd.Dir = workspacePath
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
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
		writeError(w, http.StatusInternalServerError, CodeInternalError, "サイクルを起動できない: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view.CycleStarted{Started: true})
}

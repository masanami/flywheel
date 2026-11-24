package flow

// 切り離した run。flow start・flow resume は、run を始めた（または解決した）後、flywheel 自身の実行ファイルを
// 新しいセッションの子プロセス（runner）として `flywheel flow __run-detached` で起動し直して run を進めさせ、自分は
// runner が run を引き継いだ（別の PID を記録した）のを確かめてから戻る。runner は呼び出し元の寿命・待ち時間の
// 上限・シグナルから切り離されるので、呼び出し元が止められても run は取り消されない。常駐のデーモンは持たない
// （runner は run が待ち〔gate〕か終端に達したら終わる）。生きているかは PID とプロセスの開始時刻の一致で見る。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/masanami/flywheel/internal/flow/runstate"
)

// DetachedRunnerCmd は切り離した runner の内部の入口（flywheel help に出さない。人が呼ぶものではない）。
const DetachedRunnerCmd = "__run-detached"

// detachWait は、起動した runner が run を引き継いだ（runner_started を記録した）ことを確かめるまでの待ち時間の上限。
var detachWait = 30 * time.Second

// selfExecutable は runner として起動する実行ファイル（既定は自分自身。試験が差し替える）。
var selfExecutable = os.Executable

// 子（runner）へ明示して渡す環境変数。HARNESS_SCRIPTS_DIR・HARNESS_PLUGIN_VERSION は解決したプラグインの値。
const (
	ScriptsDirEnv     = "HARNESS_SCRIPTS_DIR"
	PluginVersionEnv  = "HARNESS_PLUGIN_VERSION"
	PermissionModeEnv = "HARNESS_CLAUDE_PERMISSION_MODE"
)

// runnerPass は runner が読む環境変数のうち、env.Getenv の値で上書きして渡すもの（空なら足さない）。
// HARNESS_CLAUDE_BIN・HARNESS_GIT_BIN・HARNESS_GH_BIN は試験の偽の実行ファイル専用。
var runnerPass = []string{"HOME", "USER", "PATH", "HARNESS_CLAUDE_BIN", "HARNESS_GIT_BIN", "HARNESS_GH_BIN", PermissionModeEnv}

// runnerEnv は runner の環境: このプロセスの環境に、runnerPass を env.Getenv の値で、extra を重ねたもの。
func runnerEnv(env Env, extra map[string]string) []string {
	over := map[string]string{}
	for _, k := range runnerPass {
		if v := env.Getenv(k); v != "" {
			over[k] = v
		}
	}
	for k, v := range extra {
		if v != "" {
			over[k] = v
		}
	}
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := over[k]; !ok {
			out = append(out, kv)
		}
	}
	for k, v := range over {
		out = append(out, k+"="+v)
	}
	return out
}

// detach は run を進める runner を、新しいセッションの子プロセスとして起動する。標準入力は閉じ（/dev/null）、
// 標準出力と標準エラーは run ディレクトリの runner.log へ流す。作業ディレクトリは cwd（run を始めたディレクトリ）。
// runner が run を引き継いだ（runner_started に自分の PID を記録した）か、run が終わったのを確かめてから戻る。
func detach(env Env, run *runstate.Run, cwd string, extra map[string]string) (int, error) {
	exe, err := selfExecutable()
	if err != nil {
		return 0, fmt.Errorf("切り離して進めるための flywheel の実行ファイルを特定できない: %w", err)
	}
	logPath := filepath.Join(run.Dir, runstate.RunnerLogFile)
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer func() { _ = logf.Close() }()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return 0, err
	}
	defer func() { _ = null.Close() }()
	cmd := exec.Command(exe, "flow", DetachedRunnerCmd, "--workspace", env.Workspace, run.ID)
	cmd.Dir = cwd
	cmd.Env = runnerEnv(env, extra)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("切り離した runner を起動できない: %w", err)
	}
	pid := cmd.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	took := func() bool {
		st, _, err := run.Load()
		return err == nil && (st.PID == pid || runstate.Terminal(st.Status))
	}
	deadline := time.Now().Add(detachWait)
	for {
		if took() {
			return pid, nil
		}
		select {
		case werr := <-exited:
			if took() {
				return pid, nil
			}
			return pid, fmt.Errorf("切り離した runner（pid %d）が run を引き継ぐ前に終わった（%w）。%s を見ること", pid, werr, logPath)
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			if took() { // 期限の直前に引き継いだ runner は止めない
				return pid, nil
			}
			// 失敗として返す以上、遅れて引き継いで run を進めないよう、起動した runner を止める（新しいセッションの長なので
			// プロセスグループごと）。run は runner の居ない running として残り、次の status・resume が interrupted にする。
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			return pid, fmt.Errorf("切り離した runner（pid %d）が %s のうちに run を引き継がなかった。%s を見ること", pid, detachWait, logPath)
		}
	}
}

// RunDetached は切り離した runner の本体: run のロックを取り、run を始めたプロセスから run を引き継いで
// （runner_started）、待ちか終端に達するまで進める。結果の契約の JSON は標準出力（runner.log）へ出す。
func RunDetached(env Env, id string) int {
	env = env.withDefaults()
	run, err := openRun(env, id)
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return ExitFailed
	}
	unlock, err := run.RunnerLock()
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return ExitFailed
	}
	defer unlock()
	st, _, err := run.Load()
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return ExitFailed
	}
	if st.Status != runstate.StatusRunning {
		logf(env.Stderr, "flywheel flow: run %s is %s; nothing for a runner to advance\n", st.RunID, st.Status)
		return ExitFailed
	}
	eng, err := reopen(run, st)
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return ExitFailed
	}
	tools(eng, env)
	// 引き継ぐのは、run を始めたプロセス（親）か、runner の居ない run だけ。ほかに生きている runner が居れば進めない。
	parent := os.Getppid()
	_, err = run.AppendIf(func(cur *runstate.State) ([]runstate.Event, error) {
		if cur.Status != runstate.StatusRunning {
			return nil, fmt.Errorf("run %s is %s; nothing for a runner to advance", cur.RunID, cur.Status)
		}
		if cur.PID != parent && cur.PID != os.Getpid() && runstate.RunnerAlive(cur) {
			return nil, fmt.Errorf("run %s is being advanced by pid %d", cur.RunID, cur.PID)
		}
		return []runstate.Event{{Type: runstate.EvRunnerStarted, RunnerStarted: &runstate.RunnerStarted{
			PID: os.Getpid(), PIDStart: runstate.SelfStart(), Command: "detached"}}}, nil
	})
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return ExitFailed
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	st, err = eng.Loop(ctx)
	if err != nil {
		logf(env.Stderr, "flywheel flow: run %s stopped with an internal error: %v\n", run.ID, err)
		return ExitFailed
	}
	writeContract(env.Stdout, env.Stderr, contractFromView(newView(st), ""))
	return ExitOK
}

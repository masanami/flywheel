// Package flow は claude-harness の「フロー（YAML）と、それを進める仕組み」の入口（start・status・resume・cancel と、
// 切り離して進める __run-detached）と、接続契約（版 1）の形の出力を持つ。claude-harness（masanami/claude-harness）の
// main b4d6154 の runtime/internal/cli から写して直したもの。
//
// 置き場は呼び出し元が渡すワークスペース（cli が --workspace から決める）の値だけから決める（HARNESS_* の置き場の環境変数は読まない）: 実行のフォルダは <ws>/.flywheel/flow/runs/<実行の識別>/、
// 資材は <ws>/.flywheel/flow/runtime/<版>/。定義の上書き（.claude-harness/overrides/）は読まず、埋め込んだ定義を読む。
//
// このパッケージは internal/flow/workflow・runstate・engine・assets と標準ライブラリだけを import する。
// core・ストア・invoker・server・view・adapters・cli は import しない（internal/cli だけがこのパッケージを import する）。
package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/masanami/flywheel/internal/flow/assets"
	"github.com/masanami/flywheel/internal/flow/engine"
	"github.com/masanami/flywheel/internal/flow/runstate"
	"github.com/masanami/flywheel/internal/flow/workflow"
)

// 終了コード。契約の面（start・status・resume・cancel）の終了コードは「JSON を出力できたか」が基本で、
// 切り離して進める側に引き渡せなかった start・resume と、始める前の確かめに通らなかった start は 0 でない。
const (
	ExitOK     = 0
	ExitFailed = 1
)

// Env は flow のコマンドが外界から受け取るもの（試験が差し替える）。
type Env struct {
	// Workspace はワークスペースの絶対パス（--workspace から直接決める）。
	Workspace string
	Stdout    io.Writer
	Stderr    io.Writer
	Getenv    func(string) string
	Getwd     func() (string, error)
	// LookPath は claude-harness-run の探し方（nil なら Getenv("PATH") の上を探す）。
	LookPath func(string) (string, error)
	// Version は動いている flywheel のバイナリの版（コミット）。資材の展開先の版に使う。
	Version string
}

func (env Env) withDefaults() Env {
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	if env.Getwd == nil {
		env.Getwd = os.Getwd
	}
	if env.Stdout == nil {
		env.Stdout = io.Discard
	}
	if env.Stderr == nil {
		env.Stderr = io.Discard
	}
	return env
}

func runsDir(workspace string) string {
	return filepath.Join(workspace, ".flywheel", "flow", "runs")
}

func openRun(env Env, id string) (*runstate.Run, error) {
	return runstate.Open(runsDir(env.Workspace), id)
}

// tools は run を進めるプロセスが起動する外部コマンドを環境変数から決める（空なら PATH のもの）。
// HARNESS_CLAUDE_BIN・HARNESS_GIT_BIN・HARNESS_GH_BIN は試験の偽の実行ファイル専用。
func tools(eng *engine.Engine, env Env) {
	eng.ClaudeBin = env.Getenv("HARNESS_CLAUDE_BIN")
	eng.PermissionMode = env.Getenv(PermissionModeEnv)
	eng.DelegatedRun = env.Getenv(DelegatedRunPassEnv)
	eng.GitBin = env.Getenv("HARNESS_GIT_BIN")
	eng.GhBin = env.Getenv("HARNESS_GH_BIN")
}

// reopen は run を記録した定義で開き直す。
func reopen(run *runstate.Run, st *runstate.State) (*engine.Engine, error) {
	return engine.Reopen(run, st)
}

// flowName は start に渡せるフローの名前（埋め込んだ定義の名前。パスは受け付けない）。
var flowName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// StartOptions は Start に渡すもの。
type StartOptions struct {
	// Name はフローの名前（埋め込んだ定義。例: ticket）。
	Name string
	// Inputs は --input NAME=VALUE。
	Inputs []string
	// Models は --model <変数>=<値>（そのフローが定義している変数だけ）。定義の既定の値に重ねた解決値を、実行の始まりの記録に持つ。
	Models []string
	// PermissionMode は llm 種類が起動する claude へ --permission-mode で渡すモード（空なら渡さない）。
	PermissionMode string
	// DelegatedRun は --delegated-run の値（委譲する run の ID）。claude の子の環境の FLYWHEEL_DELEGATED_RUN になる。
	DelegatedRun string
}

// Start は run を始め、切り離した runner に進めさせて、runner が引き継いだのを確かめてから戻る。
// 標準出力に契約の JSON を 1 つ出す。始める前の確かめ（プラグインの場所と版）に通らなければ、実行のフォルダを作らず、
// 標準エラーに理由を出して 0 でない終了コードで終わる。
func Start(env Env, o StartOptions) int {
	env = env.withDefaults()
	if !flowName.MatchString(o.Name) {
		logf(env.Stderr, "flywheel flow: フローの名前 %q は使えない（埋め込んだ定義の名前を渡す。例: ticket）\n", o.Name)
		return ExitFailed
	}
	plugin, err := ResolvePlugin(env.Getenv, env.LookPath)
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return ExitFailed
	}
	if err := ensureGitignore(env.Workspace); err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return ExitFailed
	}
	layout, err := ensureAssets(env)
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return ExitFailed
	}
	path := filepath.Join(layout.WorkflowDir, o.Name+".yaml")
	if _, err := os.Stat(path); err != nil {
		return writeContract(env.Stdout, env.Stderr, failedDoc(nil, fmt.Sprintf("no run was started: flow %q is not embedded", o.Name)))
	}
	// 上書き（.claude-harness/overrides/）は読まない: 埋め込んだ定義そのものを読む。
	wf, err := workflow.LoadWithOverrides(path, workflow.Options{ScriptsDir: plugin.ScriptsDir}, nil)
	if err != nil {
		logf(env.Stderr, "flywheel flow: workflow is not valid; no run was started\n%v\n", err)
		return writeContract(env.Stdout, env.Stderr, failedDoc(nil, fmt.Sprintf("no run was started: workflow is not valid: %v", err)))
	}
	inputs, err := parseInputs(wf, o.Inputs)
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return writeContract(env.Stdout, env.Stderr, failedDoc(nil, fmt.Sprintf("no run was started: %v", err)))
	}
	given, err := ParseModelFlags(o.Models)
	var models map[string]string
	if err == nil {
		models, err = workflow.ResolveModels(wf, given)
	}
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return writeContract(env.Stdout, env.Stderr, failedDoc(nil, fmt.Sprintf("no run was started: %v", err)))
	}
	cwd, err := env.Getwd()
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return ExitFailed
	}
	eng, err := engine.Start(engine.StartParams{
		RunsDir: runsDir(env.Workspace), WF: wf, Inputs: inputs, ScriptsDir: plugin.ScriptsDir, WorkflowDir: layout.WorkflowDir,
		Cwd: cwd, Origin: "flow", CLIVersion: env.Version, Embedded: true, Models: models,
	})
	if err != nil {
		logf(env.Stderr, "flywheel flow: cannot start the run: %v\n", err)
		return ExitFailed
	}
	logf(env.Stderr, "flywheel flow: run %s started (%s)\n", eng.Run.ID, eng.Run.Dir)
	extra := map[string]string{ScriptsDirEnv: plugin.ScriptsDir, PluginVersionEnv: plugin.Version, PermissionModeEnv: o.PermissionMode, DelegatedRunPassEnv: o.DelegatedRun}
	_, derr := detach(env, eng.Run, cwd, extra)
	st, _, lerr := eng.Run.Load()
	if lerr != nil {
		id := eng.Run.ID
		logf(env.Stderr, "flywheel flow: run %s: %v\n", id, errors.Join(derr, lerr))
		return writeContractExit(env, failedDoc(&id, errors.Join(derr, lerr).Error()), ExitFailed)
	}
	if derr != nil {
		logf(env.Stderr, "flywheel flow: run %s: %v\n", eng.Run.ID, derr)
		return writeContractExit(env, contractFromView(newView(st), derr.Error()), ExitFailed)
	}
	return writeContract(env.Stdout, env.Stderr, contractFromView(newView(st), ""))
}

// writeContractExit は契約の JSON を出し、書けたら want の終了コードで終わる（書けなければ ExitFailed）。
func writeContractExit(env Env, d contractDoc, want int) int {
	if code := writeContract(env.Stdout, env.Stderr, d); code != ExitOK {
		return code
	}
	return want
}

// versionDir は資材の展開先の版の名前。刻印の無いビルド（unknown）と未コミットの変更つきのビルド（+dirty）は、
// 同じ名前で中身の違う写しを上書きしないよう、中身の印を足して分ける。
func versionDir(version, digest string) string {
	if version == "" || version == "unknown" || strings.Contains(version, "+dirty") || strings.Contains(version, "/") {
		v := strings.NewReplacer("/", "_", "+", "-").Replace(version)
		if v == "" {
			v = "unknown"
		}
		return v + "-" + digest[:12]
	}
	return version
}

func ensureAssets(env Env) (assets.Layout, error) {
	if !assets.Available(assets.FS) {
		return assets.Layout{}, assets.ErrNotEmbedded
	}
	digest, err := assets.Digest(assets.FS)
	if err != nil {
		return assets.Layout{}, err
	}
	return assets.Ensure(assets.FS, assets.RuntimeRoot(env.Workspace, versionDir(env.Version, digest)))
}

// ensureGitignore は <ws>/.flywheel/.gitignore が在って flow/ の行が無ければ足す。ファイルが無ければ作らない。
func ensureGitignore(workspace string) error {
	path := filepath.Join(workspace, ".flywheel", ".gitignore")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s を読めない: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if t := strings.TrimSpace(line); t == "flow/" || t == "/flow/" || t == "flow" || t == "/flow" {
			return nil
		}
	}
	add := "flow/\n"
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		add = "\n" + add
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("%s を開けない: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(add); err != nil {
		return fmt.Errorf("%s へ書けない: %w", path, err)
	}
	return nil
}

// parseInputs は --input NAME=VALUE を宣言された型で読む。integer・string はそのまま、array・object は JSON で渡す。
// 値が空の文字列（NAME=）は型を見る前に読み飛ばし、入力が無いものとして扱う。
func parseInputs(wf *workflow.Workflow, raw []string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	seen := map[string]bool{} // 同じ名前を 2 度渡す誤りは、空の値でも数える
	for _, kv := range raw {
		name, val, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("--input %q must be NAME=VALUE", kv)
		}
		in := wf.Input(name)
		if in == nil {
			return nil, fmt.Errorf("input %q is not declared by workflow %s", name, wf.ID)
		}
		if seen[name] {
			return nil, fmt.Errorf("input %q given twice", name)
		}
		seen[name] = true
		if val == "" {
			// 値が空の文字列の入力は、型に関わらず「無い」と読む（呼び出し元の差し込みが空になる場合。run_started.inputs に入れない）。
			continue
		}
		var v any
		switch in.Type.Name {
		case "string":
			v = val
		case "integer":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("input %q must be an integer", name)
			}
			v = n
		default:
			dec := json.NewDecoder(strings.NewReader(val))
			dec.UseNumber()
			if err := dec.Decode(&v); err != nil {
				return nil, fmt.Errorf("input %q must be JSON of type %s", name, in.Type)
			}
			if err := checkType(v, in.Type); err != nil {
				return nil, fmt.Errorf("input %q: %w", name, err)
			}
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out[name] = b
	}
	for _, in := range wf.Inputs {
		if _, ok := out[in.Name]; in.Required && !ok {
			return nil, fmt.Errorf("input %q is required (--input %s=...)", in.Name, in.Name)
		}
	}
	return out, nil
}

func checkType(v any, t *workflow.Type) error {
	switch t.Name {
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("want string")
		}
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("want integer")
		}
		if _, err := n.Int64(); err != nil {
			return fmt.Errorf("want integer")
		}
	case "object":
		if _, ok := v.(map[string]any); !ok {
			return fmt.Errorf("want object")
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("want array")
		}
		if t.Items != nil {
			for _, it := range arr {
				if err := checkType(it, t.Items); err != nil {
					return fmt.Errorf("array item: %w", err)
				}
			}
		}
	}
	return nil
}

// Status は run の状態を契約の JSON で出す。runner が落ちて running のまま残ったステップ実行は interrupted にして、
// rerun か abort の待ちにする（子プロセスが残っていれば触らない）。run を読めなければ、渡された識別で failed を出す。
func Status(env Env, id string) int {
	env = env.withDefaults()
	run, err := openRun(env, id)
	if err != nil {
		return writeContract(env.Stdout, env.Stderr, failedDoc(&id, err.Error()))
	}
	st, _, err := run.Load()
	if err != nil {
		return writeContract(env.Stdout, env.Stderr, failedDoc(&id, err.Error()))
	}
	if !runstate.Terminal(st.Status) {
		if next, err := engine.Interrupt(run); err != nil {
			logf(env.Stderr, "flywheel flow: warning: cannot record the interruption: %v\n", err)
		} else {
			st = next
		}
		// pr-head のゲートで待っている unit の PR の先頭を取り直す（読めなければ記録のまま）。
		st = engine.RefreshHeads(context.Background(), run, st, env.Getenv("HARNESS_GH_BIN"), env.Getenv("HARNESS_GIT_BIN"), st.Cwd, env.Stderr)
	}
	d := contractFromView(newView(st), "")
	// 待っている run は、いまのプラグインでは resume できない（無い・版が範囲の外）なら、ゲートの操作ではなく
	// 「人が直してから確かめ直す」を出す（ゲートの操作を出すと、呼び出し元は同じ resume を繰り返して同じ拒否に当たり続ける）。
	if st.Status == runstate.StatusWaiting {
		if _, perr := ResolvePlugin(env.Getenv, env.LookPath); perr != nil {
			d.RequestedAction = blockedAction(st.RunID, perr.Error())
		}
	}
	return writeContract(env.Stdout, env.Stderr, d)
}

// ResumeOptions は Resume に渡すもの。
type ResumeOptions struct {
	ID          string
	Unit        string
	Input       string
	HasInput    bool
	Note        string
	HasNote     bool
	ApprovedRef string
	// PermissionMode は、切り離した runner が起動する claude へ --permission-mode で渡すモード（空なら環境変数の値）。
	PermissionMode string
	// DelegatedRun は、切り離した runner が起動する claude の環境の FLYWHEEL_DELEGATED_RUN にする値（空なら環境変数の値）。
	DelegatedRun string
}

// Resume はゲートの解決（入力の検証・観測・記録）をこのプロセスで行い（拒否はその場で返す。状態は変えない）、その後の
// 進行を切り離した runner に渡してすぐ戻る。標準出力に、そのときの状態を契約の JSON で出す。
func Resume(env Env, o ResumeOptions) int {
	env = env.withDefaults()
	id := o.ID
	plugin, perr := ResolvePlugin(env.Getenv, env.LookPath)
	run, err := openRun(env, id)
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return writeContract(env.Stdout, env.Stderr, failedDoc(&id, err.Error()))
	}
	// out は状態を読み直して契約の JSON を出す（note は進めなかった理由）。
	out := func(note string, code int) int {
		st, _, lerr := run.Load()
		if lerr != nil {
			return writeContract(env.Stdout, env.Stderr, failedDoc(&id, lerr.Error()))
		}
		d := contractFromView(newView(st), note)
		if code == ExitOK && perr != nil && !runstate.Terminal(st.Status) {
			d.RequestedAction = blockedAction(id, perr.Error())
		}
		return writeContractExit(env, d, code)
	}
	if perr != nil {
		// プラグインを使えない（無い・版が範囲の外）: 何も記録せず、続けられない理由と現在地を返す。
		logf(env.Stderr, "flywheel flow: %v\n", perr)
		return out(perr.Error(), ExitOK)
	}
	st, _, err := run.Load()
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return writeContract(env.Stdout, env.Stderr, failedDoc(&id, err.Error()))
	}
	if runstate.Terminal(st.Status) {
		msg := fmt.Sprintf("run %s already ended as %s; nothing to resume", st.RunID, st.Status)
		logf(env.Stderr, "flywheel flow: %s\n", msg)
		return out(msg, ExitOK)
	}
	// 同じ run を 2 つのプロセスが進めない（切り離した runner が進めている run を横から進めない）。
	// ゲートを解く前にこのロックを取る: 別の runner が持っていれば、何も記録せずにここで拒否する。
	lock, err := run.RunnerLock()
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return out(err.Error(), ExitOK)
	}
	locked := true
	unlock := func() {
		if locked {
			locked = false
			lock()
		}
	}
	defer unlock()
	// runner が落ちていれば、残った子プロセスを止め、実行中だったステップを interrupted にする（自動で再実行しない）。
	engine.StopOrphans(st, 5*time.Second)
	if st, err = engine.Interrupt(run); err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return out(err.Error(), ExitOK)
	}
	eng, err := reopen(run, st)
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return out(err.Error(), ExitOK)
	}
	tools(eng, env)
	if o.PermissionMode != "" {
		eng.PermissionMode = o.PermissionMode
	}
	if o.DelegatedRun != "" {
		eng.DelegatedRun = o.DelegatedRun
	}
	req := engine.Resolution{
		Unit: o.Unit, Input: o.Input, HasInput: o.HasInput, Note: o.Note, HasNote: o.HasNote,
		ApprovedRef: o.ApprovedRef, Actor: "resume", User: env.Getenv("USER"), Channel: "non-tty",
	}
	if _, err := eng.CheckResolution(st, req); err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return out(err.Error(), ExitOK)
	}
	ctx, stop := signalContext()
	defer stop()
	st, err = eng.ResolveGate(ctx, req)
	if err != nil {
		var ref *engine.RefusedError
		if errors.As(err, &ref) {
			logf(env.Stderr, "flywheel flow: %v\n", err)
			return out(err.Error(), ExitOK)
		}
		msg := fmt.Sprintf("run %s stopped with an internal error: %v", run.ID, err)
		logf(env.Stderr, "flywheel flow: %s\n", msg)
		return out(msg, ExitFailed)
	}
	if st.Status != runstate.StatusRunning {
		return out("", ExitOK)
	}
	unlock()
	// runner の作業ディレクトリは、呼び出し元のもの（run を始めたディレクトリはステップが使う。eng.Cwd）。
	cwd, err := env.Getwd()
	if err != nil {
		cwd = eng.Cwd
	}
	extra := map[string]string{ScriptsDirEnv: plugin.ScriptsDir, PluginVersionEnv: plugin.Version, PermissionModeEnv: o.PermissionMode, DelegatedRunPassEnv: o.DelegatedRun}
	if _, err := detach(env, run, cwd, extra); err != nil {
		logf(env.Stderr, "flywheel flow: run %s: %v\n", run.ID, err)
		// 印（interrupted）のあるラウンドは、runner の居ない running のまま返さず、その場で失敗で終える。印の無い
		// ラウンドは AbandonMarked が何も記録せず、そのときの状態（running）を返して、次の status・resume に任せる。
		_, _ = engine.AbandonMarked(run)
		return out(err.Error(), ExitFailed)
	}
	return out("", ExitOK)
}

// blockedAction は、このプラグインの版・場所では run を続けられないときの requested_action。state は run の状態のまま、
// ゲートの操作の代わりに「人が直してから確かめ直す」を出す（ゲートの操作を出すと、呼び出し元は同じ resume を繰り返して
// 同じ拒否に当たり続ける）。
func blockedAction(runID, why string) *requestedAction {
	return &requestedAction{Kind: actionObserve, Decider: contractHuman, Text: fmt.Sprintf(
		"this flywheel cannot go on with run %s: %s. A person fixes that outside the contract; then check again with "+
			"flywheel flow status --workspace <workspace> %s and follow the requested_action it returns",
		runID, why, runID)}
}

// cancel の待ち時間。runner は子プロセスへ SIGTERM を送り、猶予（5 秒）内に終わらなければ SIGKILL を送る。
var cancelWait = 20 * time.Second

// Cancel は run を止める: 子プロセスを止めて停止を記録する。標準出力に、そのときの状態を契約の JSON で出す。
func Cancel(env Env, id string) int {
	env = env.withDefaults()
	run, err := openRun(env, id)
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return writeContract(env.Stdout, env.Stderr, failedDoc(&id, err.Error()))
	}
	out := func(note string) int {
		st, _, lerr := run.Load()
		if lerr != nil {
			return writeContract(env.Stdout, env.Stderr, failedDoc(&id, lerr.Error()))
		}
		return writeContract(env.Stdout, env.Stderr, contractFromView(newView(st), note))
	}
	fail := func(format string, args ...any) int {
		msg := fmt.Sprintf(format, args...)
		logf(env.Stderr, "flywheel flow: %s\n", msg)
		return out(msg)
	}
	st, _, err := run.Load()
	if err != nil {
		logf(env.Stderr, "flywheel flow: %v\n", err)
		return writeContract(env.Stdout, env.Stderr, failedDoc(&id, err.Error()))
	}
	switch st.Status {
	case runstate.StatusCancelled:
		return out("")
	case runstate.StatusSucceeded, runstate.StatusFailed:
		return fail("run %s already ended as %s; nothing to cancel", st.RunID, st.Status)
	}
	const channel = "non-tty"
	if _, err := run.Append(runstate.Event{Type: runstate.EvCancelRequested, CancelRequested: &runstate.CancelRequested{Actor: "cli", Channel: channel}}); err != nil {
		return fail("cannot record the cancel request: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.Dir, runstate.CancelFile), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return fail("%v", err)
	}
	deadline := time.Now().Add(cancelWait)
	for {
		st, _, err = run.Load()
		if err != nil {
			return fail("%v", err)
		}
		if runstate.Terminal(st.Status) {
			break
		}
		if st.Status == runstate.StatusWaiting || !runstate.RunnerAlive(st) {
			// runner が居ない（待ちで止まっている run、または落ちた run）。子プロセスが残っていれば止め、停止を自分で記録する。
			// 待機中の run の PID は終わった runner のもので、別のプロセスに再利用されていても待たない。
			for _, u := range st.Units {
				if x := u.Running(); x != nil {
					engine.StopOrphan(x.PID, 5*time.Second)
				}
			}
			if _, err := engine.AppendEnding(run, func(cur *runstate.State) ([]runstate.Event, error) {
				if runstate.Terminal(cur.Status) {
					return nil, nil
				}
				return engine.CancelEvents(cur, "cli", channel, "runner was not alive; stopped by cancel"), nil
			}); err != nil {
				// runner が同時に終えた等。読み直して終端なら良い。
				if st2, _, lerr := run.Load(); lerr == nil && runstate.Terminal(st2.Status) {
					break
				}
				return fail("cannot record the stop: %v", err)
			}
			continue
		}
		if time.Now().After(deadline) {
			return fail("run %s did not stop within %s (runner pid %d is alive); the cancel request stays recorded", st.RunID, cancelWait, st.PID)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st.Status != runstate.StatusCancelled {
		return fail("run %s ended as %s before the cancel took effect", st.RunID, st.Status)
	}
	return out("")
}

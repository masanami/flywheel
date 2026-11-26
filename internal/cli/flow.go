package cli

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/flow"
)

// このファイルは `flywheel flow`（start・status・resume・cancel と、切り離して進める内部の入口 __run-detached）の
// 配線を持つ。進め方・契約の JSON の組み立てはすべて internal/flow にあり、ここは引数を渡して終了コードを返すだけ。
// 組み込みのフローは内部向けのコマンドで、flywheel help には「内部向け」の印つきで出す（__run-detached は出さない）。

// rawExit は、コマンドが標準出力・標準エラーへ自分で書き終えたことと、終了コードを表す Run の戻り値。
// 契約の JSON は 1 つの文書をそのまま標準出力へ出すので、共通の `{ok...}` の包みや `--json` の切り替えを通さない。
type rawExit struct{ code int }

// flowWorkspace は --workspace の値から直接ワークスペースの絶対パスを決める（無ければ FLYWHEEL_WORKSPACE、
// それも無ければカレントディレクトリ。親へは遡らない）。
func flowWorkspace(a Args) (string, error) {
	ws := a.Values["workspace"]
	if ws == "" {
		ws = os.Getenv("FLYWHEEL_WORKSPACE")
	}
	if ws == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		ws = wd
	}
	return filepath.Abs(ws)
}

// flowWorkspaceCheck はワークスペースが在る（<ws>/.flywheel が在る）ことを確かめる。無い場所に実行の置き場を作らない。
func flowWorkspaceCheck(ws string) *Error {
	if fi, err := os.Stat(filepath.Join(ws, ".flywheel")); err != nil || !fi.IsDir() {
		return NewError(CodeStoreNotFound, fmt.Sprintf("%s はワークスペースではない（.flywheel が無い）。--workspace で指定するか、flywheel init を先に実行する", ws))
	}
	return nil
}

func flowEnv(a Args) (flow.Env, *Error) {
	ws, err := flowWorkspace(a)
	if err != nil {
		return flow.Env{}, NewError(CodeInternalError, fmt.Sprintf("ワークスペースを決められない: %v", err))
	}
	if cerr := flowWorkspaceCheck(ws); cerr != nil {
		return flow.Env{}, cerr
	}
	return flow.Env{
		Workspace: ws, Stdout: a.Stdout, Stderr: a.Stderr, Getenv: os.Getenv, Getwd: os.Getwd,
		LookPath: a.LookPath, Version: binaryCommit(a),
	}, nil
}

// flowLaunchFlags は start・resume が必須で受ける --permission-mode（default・acceptEdits・auto のみ）と --delegated-run を検査する。
// 欠落・範囲外は使い方の誤り（実行の記録に触れる前に返す）。
func flowLaunchFlags(a Args) (mode, delegated string, err *Error) {
	mode, hasMode := a.Values["permission-mode"]
	if !hasMode || mode == "" {
		return "", "", NewError(CodeUsageError, "--permission-mode は必須です（default・acceptEdits・auto のいずれか）")
	}
	if !slices.Contains([]string{string(core.PermissionModeDefault), string(core.PermissionModeAcceptEdits), string(core.PermissionModeAuto)}, mode) {
		return "", "", NewError(CodeUsageError, fmt.Sprintf("--permission-mode は default・acceptEdits・auto のいずれかを指定してください: %q", mode))
	}
	delegated = a.Values["delegated-run"]
	if delegated == "" {
		return "", "", NewError(CodeUsageError, "--delegated-run は必須です（委譲する run の ID）")
	}
	return mode, delegated, nil
}

// flowBudgetFlag は start の --budget-usd を検査して返す（無ければ 0＝定義の値）。受け付けるのはフロー spec-draft だけで、
// 正の有限の数でなければ使い方の誤り。
func flowBudgetFlag(a Args) (float64, *Error) {
	raw, ok := a.Values["budget-usd"]
	if !ok {
		return 0, nil
	}
	if a.Positional[0] != flow.SpecDraftFlow {
		return 0, NewError(CodeUsageError, fmt.Sprintf("--budget-usd はフロー %s のときだけ指定できます", flow.SpecDraftFlow))
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || !(v > 0) || math.IsInf(v, 0) {
		return 0, NewError(CodeUsageError, fmt.Sprintf("--budget-usd は正の数を指定してください: %q", raw))
	}
	return v, nil
}

// flowStart は flow.Start（試験が引数の写しを確かめるために差し替える）。
var flowStart = flow.Start

func runFlowStart(a Args) (any, error) {
	mode, delegated, uerr := flowLaunchFlags(a)
	if uerr != nil {
		return nil, uerr
	}
	budget, uerr := flowBudgetFlag(a)
	if uerr != nil {
		return nil, uerr
	}
	env, cerr := flowEnv(a)
	if cerr != nil {
		return nil, cerr
	}
	return rawExit{flowStart(env, flow.StartOptions{Name: a.Positional[0], Inputs: a.Multi["input"], PermissionMode: mode, DelegatedRun: delegated, BudgetUSD: budget})}, nil
}

func runFlowStatus(a Args) (any, error) {
	env, cerr := flowEnv(a)
	if cerr != nil {
		return nil, cerr
	}
	return rawExit{flow.Status(env, a.Positional[0])}, nil
}

// flowResume は flow.Resume（試験が引数の写しを確かめるために差し替える）。
var flowResume = flow.Resume

func runFlowResume(a Args) (any, error) {
	mode, delegated, uerr := flowLaunchFlags(a)
	if uerr != nil {
		return nil, uerr
	}
	env, cerr := flowEnv(a)
	if cerr != nil {
		return nil, cerr
	}
	o := flow.ResumeOptions{ID: a.Positional[0], Unit: a.Values["unit"], ApprovedRef: a.Values["approved-ref"], PermissionMode: mode, DelegatedRun: delegated}
	_, o.HasInput = a.Values["input"]
	o.Input = a.Values["input"]
	_, o.HasNote = a.Values["note"]
	o.Note = a.Values["note"]
	return rawExit{flowResume(env, o)}, nil
}

func runFlowCancel(a Args) (any, error) {
	env, cerr := flowEnv(a)
	if cerr != nil {
		return nil, cerr
	}
	return rawExit{flow.Cancel(env, a.Positional[0])}, nil
}

func runFlowRunDetached(a Args) (any, error) {
	env, cerr := flowEnv(a)
	if cerr != nil {
		return nil, cerr
	}
	return rawExit{flow.RunDetached(env, a.Positional[0])}, nil
}

// flowCommands は flow のコマンドの登録。いずれもストアを開かない（置き場は --workspace から直接決める）。
func flowCommands() []Command {
	return []Command{
		{
			Path: []string{"flow", "start"}, MinPositional: 1, MaxPositional: 1,
			Flags: []flagDef{
				{Name: "input", HasValue: true, Repeatable: true},
				// 権限モードと、委譲の子の目印に使う run の ID。必須化と使い道は後続のタスクが足す（いまは受けるだけ）。
				{Name: "permission-mode", HasValue: true},
				{Name: "delegated-run", HasValue: true},
				{Name: "budget-usd", HasValue: true},
			},
			Run: runFlowStart,
		},
		{Path: []string{"flow", "status"}, MinPositional: 1, MaxPositional: 1, Run: runFlowStatus},
		{
			Path: []string{"flow", "resume"}, MinPositional: 1, MaxPositional: 1,
			Flags: []flagDef{
				{Name: "unit", HasValue: true}, {Name: "input", HasValue: true}, {Name: "note", HasValue: true},
				{Name: "approved-ref", HasValue: true}, {Name: "permission-mode", HasValue: true}, {Name: "delegated-run", HasValue: true},
			},
			Run: runFlowResume,
		},
		{Path: []string{"flow", "cancel"}, MinPositional: 1, MaxPositional: 1, Run: runFlowCancel},
		{Path: []string{"flow", flow.DetachedRunnerCmd}, MinPositional: 1, MaxPositional: 1, Run: runFlowRunDetached},
	}
}

package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/masanami/flywheel/internal/adapters/github"
	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
)

// runPlanAuto は `flywheel plan --auto [<C-ID>]` の実装（#85。
// docs/features/m3-invoker-delegation.md §J2 計画）。宣言の読み込み・invoker と
// 上流の取得の組み立て・周の開始と終了だけを行い、対象の選び方・上流の入力・出力の
// 写像・読んだ記録は core.Store.PlanAutoJ2 に委ねる（P2「CLI は core の公開 API
// だけを呼ぶ」）。
//
// 読み込み順（classify --auto と同じ形）: agent.json の読み込み・検証 →
// RequirePositionFile → connectors.json の読み込み・検証（無ければ config_not_found）
// → claude の有無 → 周の開始（BeginCycle trigger "plan --auto"）→ PlanAutoJ2 →
// EndCycle。どの段で失敗しても、それより後の段（claude の起動・上流の取得）は
// 行わない。
func runPlanAuto(a Args) (any, error) {
	ctx := context.Background()
	ws := a.Store.Workspace()

	decl, err := core.LoadAgentDeclaration(ws)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	if err := decl.RequirePositionFile(ws); err != nil {
		return nil, mapCoreErr(err)
	}
	conn, err := core.LoadConnectorsDeclaration(ws)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	launcher := invoker.NewLauncher()
	if err := launcher.Available(ctx); err != nil {
		if errors.Is(err, invoker.ErrClaudeNotFound) {
			return nil, NewError(CodeInvokerUnavailable, err.Error())
		}
		return nil, NewError(CodeInternalError, err.Error())
	}

	cyc, err := a.Store.BeginCycle(ctx, core.BeginCycleInput{
		Trigger:   "plan --auto",
		BudgetUSD: decl.CycleBudgetUSD,
		Exclusive: false,
	})
	if err != nil {
		return nil, mapCoreErr(err)
	}

	var challengeID *string
	if len(a.Positional) == 1 {
		challengeID = &a.Positional[0]
	}

	res, runErr := a.Store.PlanAutoJ2(ctx, core.J2AutoInput{
		ChallengeID: challengeID,
		AgentDecl:   decl,
		ConnDecl:    conn,
		Invoker:     launcher,
		Upstream:    newUpstreamThreadSource(),
		CycleID:     cyc.ID,
	})

	endResult := core.CycleResultCompleted
	if runErr != nil {
		endResult = core.CycleResultAborted
	}
	// 周は起動できたかどうかに関わらず必ず終える（classify --auto と同じ）。
	if _, endErr := a.Store.EndCycle(ctx, cyc.ID, endResult); endErr != nil && runErr == nil {
		return nil, mapCoreErr(endErr)
	}
	if runErr != nil {
		return nil, mapCoreErr(runErr)
	}

	return textOutput{
		json: map[string]any{"phase": judgmentPhaseJSON("plan", res)},
		text: judgmentPhaseText("計画の対象はありませんでした\n", res),
	}, nil
}

// newUpstreamThreadSource は J2 の起動の直前の上流の取得（`gh`。すべて GET）を
// 組み立てる。`gh` が PATH に無いときも組み立ては失敗させず、呼ばれたときに失敗を
// 返す取得を渡す: 取り込み元の対応の無い課題の J2 は `gh` を呼ばないので、`gh` が
// 無い環境でも計画できる。対応のある課題は、取得の失敗として起動されず、
// not_started に upstream_fetch_failed で出る（計画する時点の上流を読めないまま
// 計画しない）。gh の 1 回の呼び出しの時間の上限は ingest と同じ
// （ingestGHTimeout。テストだけが書き換える）。
func newUpstreamThreadSource() core.UpstreamThreadSource {
	client, err := github.New(github.Options{Timeout: ingestGHTimeout})
	if err != nil {
		return unavailableThreadSource{err: err}
	}
	return client
}

// unavailableThreadSource は上流を取得できない環境（`gh` が PATH に無い等）を表す
// core.UpstreamThreadSource。どの取得も、組み立て時の失敗をそのまま返す。
type unavailableThreadSource struct{ err error }

func (u unavailableThreadSource) GetIssueThread(context.Context, string, int) (core.UpstreamIssueThread, error) {
	return core.UpstreamIssueThread{}, fmt.Errorf("上流を取得できません: %w", u.err)
}

func (u unavailableThreadSource) GetReferencedIssue(context.Context, string, int) (core.UpstreamIssue, error) {
	return core.UpstreamIssue{}, fmt.Errorf("上流を取得できません: %w", u.err)
}

// newBranchSource は委譲の後の照合が使う、リモートのブランチと PR の取得（`gh api` の
// GET だけ）を組み立てる。`gh` が PATH に無いときは、どの取得も失敗する実装を返す
// （照合は取得の失敗を結果に書き、成果物を確かめられなかったものとして扱う）。
func newBranchSource() core.UpstreamBranchSource {
	client, err := github.New(github.Options{Timeout: ingestGHTimeout})
	if err != nil {
		return unavailableBranchSource{err: err}
	}
	return client
}

type unavailableBranchSource struct{ err error }

func (u unavailableBranchSource) BranchExists(context.Context, string, string) (bool, error) {
	return false, fmt.Errorf("上流を取得できません: %w", u.err)
}

func (u unavailableBranchSource) ListPullRequestsByHead(context.Context, string, string) ([]core.UpstreamPullRequest, error) {
	return nil, fmt.Errorf("上流を取得できません: %w", u.err)
}

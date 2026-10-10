package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/masanami/flywheel/internal/adapters/github"
	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
	"github.com/masanami/flywheel/internal/view"
)

// runVerifyAuto は `flywheel verify --auto [<C-ID>]` の実装（J5。docs/features/
// m3-invoker-delegation.md §J5 検証）。宣言の読み込み・invoker・上流と PR のチェックの取得の
// 組み立て・周の開始と終了だけを行い、対象の選び方・CI の待ち・入力・出力の写像は
// core.Store.VerifyAutoJ5 に委ねる（P2「CLI は core の公開 API だけを呼ぶ」）。
//
// 読み込み順（run と同じ形）: agent.json の読み込み・検証 → claude の有無 → 周の開始
// （BeginCycle trigger "verify --auto"）→ VerifyAutoJ5 → EndCycle。どの段で失敗しても、それより
// 後の段（claude の起動・gh の取得）は行わない。`--auto` と `--result met` などの同時指定は
// 引数の検査（OneOfGroups）が usage_error にする。
func runVerifyAuto(a Args) (any, error) {
	ctx := context.Background()
	ws := a.Store.Workspace()

	decl, err := core.LoadAgentDeclaration(ws)
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
		Trigger:   "verify --auto",
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

	res, runErr := a.Store.VerifyAutoJ5(ctx, core.J5AutoInput{
		ChallengeID: challengeID,
		AgentDecl:   decl,
		Invoker:     launcher,
		Upstream:    newUpstreamThreadSource(),
		Checks:      newCheckSource(),
		CycleID:     cyc.ID,
	})

	endResult := core.CycleResultCompleted
	if runErr != nil {
		endResult = core.CycleResultAborted
	}
	// 周は起動できたかどうかに関わらず必ず終える（plan --auto と同じ）。
	if _, endErr := a.Store.EndCycle(ctx, cyc.ID, endResult); endErr != nil && runErr == nil {
		return nil, mapCoreErr(endErr)
	}
	if runErr != nil {
		return nil, mapCoreErr(runErr)
	}

	return textOutput{
		json: view.PhaseResponse{Phase: view.FromJudgmentPhase("verify", false, res)},
		text: judgmentPhaseText("検証の対象はありませんでした\n", res),
	}, nil
}

// newCheckSource は J5 の起動の前の PR のチェックの取得（`gh api` の GET だけ）を組み立てる。
// `gh` が PATH に無いときは、どの取得も失敗する実装を返す（PR の無い課題は取得を呼ばないので
// `gh` が無くても検証できる。PR のある課題は、取得の失敗として起動されず、not_started に
// upstream_fetch_failed で出る）。
func newCheckSource() core.UpstreamCheckSource {
	client, err := github.New(github.Options{Timeout: ingestGHTimeout})
	if err != nil {
		return unavailableCheckSource{err: err}
	}
	return client
}

type unavailableCheckSource struct{ err error }

func (u unavailableCheckSource) GetPullRequestChecks(context.Context, string, int) (core.UpstreamPullRequestChecks, error) {
	return core.UpstreamPullRequestChecks{}, fmt.Errorf("上流を取得できません: %w", u.err)
}

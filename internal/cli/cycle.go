package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/adapters/git"
	"github.com/masanami/flywheel/internal/adapters/github"
	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
)

// defaultCycleTrigger は `--trigger` を省略したときの周の契機
// （docs/features/m3-invoker-delegation.md §一括の操作（サイクル）「省略時は manual」）。
const defaultCycleTrigger = "manual"

// runCycle は `flywheel cycle [--trigger <t>]` の実装（#86。同 §一括の操作（サイクル））。
// 宣言の読み込み・invoker と上流の取得の組み立てだけを行い、段の順・排他・周の開始と終了・
// 枠超過の伝播は core.Store.RunCycle に委ねる（P2「CLI は core の公開 API だけを呼ぶ」。
// 独自の遷移の規則を持たない）。
//
// 読み込み順（個別の操作と同じ形。どの段で失敗しても、それより後は行わず周も始めない）:
// agent.json の読み込み・検証 → RequirePositionFile → sources.json（無ければ取り込みの段は
// skipped。不備なら config_invalid）→ connectors.json（無ければ計画・委譲・検証の段は skipped。
// 不備なら config_invalid）→ 取り込みの段があれば gh の有無（無ければ
// upstream_unavailable）→ claude の有無（無ければ invoker_unavailable）→ RunCycle。
// 宣言の不備は環境の不備（gh・claude の不在）より先に報告する。
// 生きている別の cycle があれば core が locked を返す。周の中の run の失敗は結果に示すだけで、
// 終了コードは 0 のまま。
func runCycle(a Args) (any, error) {
	ctx := context.Background()
	ws := a.Store.Workspace()

	// --trigger の指定の有無を区別する（`--trigger ""` を省略の manual に化けさせない。
	// ingest の --source と同じ形）。空文字列は core が validation_failed にする。
	trigger := defaultCycleTrigger
	if v, ok := a.Values["trigger"]; ok {
		trigger = v
	}

	agent, err := core.LoadAgentDeclaration(ws)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	if err := agent.RequirePositionFile(ws); err != nil {
		return nil, mapCoreErr(err)
	}

	// 宣言はすべて読み・検証してから（どれかが不備なら config_invalid）、gh・claude の有無を
	// 確かめる。読み込みはストアを変えないので、どの失敗でも周は始まらない。
	var selected []core.SourceEntry
	hasSources := false
	sources, err := core.LoadSourcesDeclaration(ws)
	switch {
	case err == nil:
		sel, selErr := core.SelectSources(sources, nil)
		if selErr != nil {
			return nil, mapCoreErr(selErr)
		}
		selected, hasSources = sel, true
	case errors.Is(err, core.ErrConfigNotFound):
		// sources.json が無い: 取り込みの段は skipped（エラーにしない）。
	default:
		return nil, mapCoreErr(err)
	}

	var conn *core.ConnectorsDeclaration
	hasConnectors := false
	connDecl, err := core.LoadConnectorsDeclaration(ws)
	switch {
	case err == nil:
		conn, hasConnectors = connDecl, true
	case errors.Is(err, core.ErrConfigNotFound):
		// connectors.json が無い: 計画・委譲・検証の段は skipped（J2・J3・J5 を起動せず、エラーにしない）。
	default:
		return nil, mapCoreErr(err)
	}

	var ingest *core.CycleIngestInput
	var ghClient *github.Client
	if hasSources {
		client, err := newIngestClient()
		if err != nil {
			return nil, err
		}
		ghClient = client
		ingest = &core.CycleIngestInput{Sources: selected, Upstream: client, Channel: core.ChannelCLI}
	}
	var threads core.UpstreamThreadSource
	if hasConnectors {
		if ghClient != nil {
			threads = ghClient
		} else {
			threads = newUpstreamThreadSource()
		}
	}

	launcher := invoker.NewLauncher()
	if err := launcher.Available(ctx); err != nil {
		if errors.Is(err, invoker.ErrClaudeNotFound) {
			return nil, NewError(CodeInvokerUnavailable, err.Error())
		}
		return nil, NewError(CodeInternalError, err.Error())
	}

	// 委譲と検証の段の口は、接続ツールの宣言があるときだけ組み立てる（無ければ段は skipped）。
	// 型つき nil を core へ渡さないよう、宣言が無いときは nil のインターフェース値のままにする。
	var (
		delegate  core.DelegationInvoker
		slotGit   core.SlotGit
		reconcile core.UpstreamBranchSource
		checks    core.UpstreamCheckSource
	)
	if hasConnectors {
		delegate, slotGit, reconcile, checks = launcher, git.New(), newBranchSource(), newCheckSource()
	}

	res, err := a.Store.RunCycle(ctx, core.CycleRunInput{
		Trigger:   trigger,
		AgentDecl: agent,
		Ingest:    ingest,
		ConnDecl:  conn,
		Invoker:   launcher,
		Upstream:  threads,
		Delegate:  delegate,
		Git:       slotGit,
		Reconcile: reconcile,
		Checks:    checks,
		Predictor: launcher,
	})
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: cycleResultJSON(res), text: cycleResultText(res)}, nil
}

// cycleResultJSON は core.CycleRunResult を §IF / API「cycle の JSON 出力」の形へ変換する:
// {"cycle": {…}, "config_defaults_used": […], "phases": […], "rate_limited": bool}。
func cycleResultJSON(res *core.CycleRunResult) map[string]any {
	c := res.Cycle
	phases := make([]any, 0, len(res.Phases))
	for _, p := range res.Phases {
		phases = append(phases, cyclePhaseJSON(p))
	}
	return map[string]any{
		"cycle": map[string]any{
			"id":         c.ID,
			"trigger":    c.Trigger,
			"started_at": FormatTimestamp(c.StartedAt),
			"ended_at":   nullableTimePtr(c.EndedAt),
			"result":     string(c.Result),
			"budget_usd": c.BudgetUSD,
			"spent_usd":  c.SpentUSD,
		},
		"config_defaults_used": res.ConfigDefaultsUsed,
		"phases":               phases,
		"rate_limited":         res.RateLimited,
	}
}

// cyclePhaseJSON は段 1 つを phases の 1 要素の形へ変換する。取り込みの段は
// {"phase","skipped","result"}（result は ingest --json と同じ形。skipped なら null）、
// 分類・計画・委譲・検証の段は {"phase","skipped","items","not_started"}（skipped なら空。
// 委譲の段は serial_groups も持つ）。
func cyclePhaseJSON(p core.CyclePhaseResult) map[string]any {
	if p.Phase == core.CyclePhaseIngest {
		var result any
		if p.Ingest != nil {
			result = ingestResultJSON(p.Ingest)
		}
		return map[string]any{"phase": string(p.Phase), "skipped": p.Skipped, "result": result}
	}
	res := &core.JudgmentAutoResult{Items: p.Items, NotStarted: p.NotStarted, SerialGroups: p.SerialGroups}
	out := judgmentPhaseJSON(string(p.Phase), res)
	if p.Phase == core.CyclePhaseRun {
		out = delegationPhaseJSON(res)
	}
	out["skipped"] = p.Skipped
	return out
}

// cycleResultText は --json 無しの cycle の表示（形式の安定は保証しない）。
func cycleResultText(res *core.CycleRunResult) string {
	var b strings.Builder
	c := res.Cycle
	fmt.Fprintf(&b, "周 %s（契機: %s）: %s（消費 %.4f / 上限 %.2f USD）\n", c.ID, c.Trigger, c.Result, c.SpentUSD, c.BudgetUSD)
	for _, p := range res.Phases {
		switch {
		case p.Skipped:
			fmt.Fprintf(&b, "  %s: スキップ\n", p.Phase)
		case p.Phase == core.CyclePhaseIngest:
			fmt.Fprintf(&b, "  %s: 取り込み元 %d 件\n", p.Phase, len(p.Ingest.Sources))
		default:
			fmt.Fprintf(&b, "  %s: 処理 %d 件・未起動 %d 件\n", p.Phase, len(p.Items), len(p.NotStarted))
		}
	}
	if res.RateLimited {
		b.WriteString("  枠超過を記録したため、以降の判断の呼び出しは起動しませんでした\n")
	}
	return b.String()
}

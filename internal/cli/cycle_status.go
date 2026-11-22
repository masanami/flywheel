package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは `flywheel cycle status`・`flywheel cycle stop`（docs/features/core-redesign.md
// §走っているサイクルを見る・止める）。値と表示の区分は core が決め、ここは読んで出すだけ。
// 委譲の子の環境からの実行は、コマンドの照合より前に runWithLookPath が断る。

// watchStateWords は見ているタスクの様子の語。
var watchStateWords = map[core.WatchState]string{
	core.WatchStateWorking:      "作業中",
	core.WatchStateVerifying:    "検証待ち",
	core.WatchStateWaitingOwner: "あなた待ち",
}

func runCycleStatus(a Args) (any, error) {
	cs, err := a.Store.ReadCycleStatus(context.Background())
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.FromCycleStatus(cs), text: cycleStatusText(cs, time.Now())}, nil
}

func runCycleStop(a Args) (any, error) {
	res, err := a.Store.RequestCycleStop(context.Background())
	if err != nil {
		return nil, mapCoreErr(err)
	}
	text := "走っているサイクルは無いので、何もしませんでした。\n"
	if res.Requested {
		text = fmt.Sprintf("周 %s に止める指示を残しました。段の切れ目で読んで終えます（段の途中では効きません。委譲先の実行は取り消しません）。\n", res.CycleID)
	}
	return textOutput{json: view.FromCycleStopResult(res), text: text}, nil
}

// cycleStatusText は --json 無しの `cycle status` の表示（形式の安定は保証しない）。now は経過時間の基準。
func cycleStatusText(cs *core.CycleStatus, now time.Time) string {
	var b strings.Builder
	if r := cs.Running; r != nil {
		fmt.Fprintf(&b, "サイクルが走っています（始まった時刻: %s・きっかけ: %s・回り %d 回）\n", view.FormatTimestamp(r.StartedAt), r.Trigger, r.Rounds)
		fmt.Fprintf(&b, "  作業中 %d 件 / 上限 %d 件\n", r.ActiveRuns, r.MaxParallelRuns)
		fmt.Fprintf(&b, "  費用 %.4f / 上限 %.2f USD\n", r.SpentUSD, r.BudgetUSD)
		if r.StopRequested {
			b.WriteString("  止める指示を受けています（段の途中では読まないので、次の段の切れ目まで続きます）\n")
		}
		if len(r.Watching) == 0 {
			b.WriteString("  見ているタスクはありません\n")
		}
		for _, w := range r.Watching {
			target := w.ChallengeID
			if w.TaskID != "" {
				target += " " + w.TaskID
			}
			fmt.Fprintf(&b, "  %s  %s  %s  経過 %s  %.4f USD\n", target, w.Title, watchStateWords[w.State], elapsedText(now.Sub(w.RunStartedAt)), w.SpentUSD)
		}
		return b.String()
	}
	if cs.State == core.CycleStateStale {
		b.WriteString("サイクルは走っていません（排他が古いまま残っています。次のサイクルが回収します）\n")
	} else {
		b.WriteString("サイクルは走っていません\n")
	}
	if cs.LastEndedAt == nil {
		b.WriteString("終わったサイクルはまだありません\n")
		return b.String()
	}
	fmt.Fprintf(&b, "最後のサイクルの終わった時刻: %s\n", view.FormatTimestamp(*cs.LastEndedAt))
	if cs.LastEndReason == nil {
		return b.String()
	}
	fmt.Fprintf(&b, "終わった理由: %s\n", *cs.LastEndReason)
	switch *cs.LastEndReason {
	case core.CycleEndBudget:
		b.WriteString("費用の上限で止まりました。続けるには、宣言の cycle_budget_usd（サイクルの費用の上限）を上げて、サイクルを始め直してください\n")
	case core.CycleEndStalled:
		b.WriteString("進まないので止まりました\n")
	}
	return b.String()
}

// elapsedText は経過時間を、秒・分・時間のうち大きい単位 1 つで書く。
func elapsedText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(max(d, 0).Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d 時間 %d 分", int(d.Hours()), int(d.Minutes())%60)
	}
}

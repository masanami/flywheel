package cli

import "github.com/masanami/flywheel/internal/core"

// このファイルは `--auto` の個別の操作（`classify --auto`・`plan --auto`）が共有する
// 出力の組み立てを持つ（docs/features/m3-invoker-delegation.md §IF / API
// 「`--auto` の個別の操作と `run` の `--json` は、`cycle` の `phases` の 1 要素と同じ形を
// `{"phase": {…}}` で返す」）。判断点ごとに同じ写像を複製せず、ここへ集める。

// judgmentPhaseJSON は res を `cycle` の `phases` の 1 要素と同じ形（phase 名・
// skipped・items・not_started）へ変換する。phase は `classify | plan` の閉集合の値。
func judgmentPhaseJSON(phase string, res *core.JudgmentAutoResult) map[string]any {
	return map[string]any{
		"phase":       phase,
		"skipped":     false,
		"items":       judgmentItemsJSON(res.Items),
		"not_started": notStartedJSON(res.NotStarted),
	}
}

// delegationPhaseJSON は委譲の段（run）の phase の要素: 判断の段と同じ形に、直列化グループの
// `serial_groups`（[{"repo", "challenges", "reasons", "prediction_head_sha"}]。無ければ []）を足す。
func delegationPhaseJSON(res *core.JudgmentAutoResult) map[string]any {
	out := judgmentPhaseJSON("run", res)
	out["serial_groups"] = serialGroupsJSON(res.SerialGroups)
	return out
}

func serialGroupsJSON(groups []core.SerialGroup) []map[string]any {
	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		challenges := append([]string{}, g.Challenges...)
		reasons := make([]string, 0, len(g.Reasons))
		for _, r := range g.Reasons {
			reasons = append(reasons, string(r))
		}
		out = append(out, map[string]any{
			"repo": g.Repo, "challenges": challenges, "reasons": reasons,
			"prediction_head_sha": nullableStringPtr(g.PredictionHeadSHA),
		})
	}
	return out
}

func judgmentItemsJSON(items []core.JudgmentAutoItem) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"challenge_id": it.ChallengeID,
			"run_id":       it.RunID,
			"result":       string(it.Result),
			"outcome":      nullableString(it.Outcome),
			"status":       nullableStringPtr(it.Status),
		})
	}
	return out
}

func notStartedJSON(items []core.NotStarted) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"challenge_id": it.ChallengeID,
			"reason":       string(it.Reason),
		})
	}
	return out
}

// judgmentPhaseText は --json 無しの `--auto` の個別の操作の表示（形式の安定は
// 保証しない）。対象が無いときは emptyMessage を返す。補足（Note・Detail）は
// JSON 出力の形に含めず、ここにだけ出す。
func judgmentPhaseText(emptyMessage string, res *core.JudgmentAutoResult) string {
	if len(res.Items) == 0 && len(res.NotStarted) == 0 {
		return emptyMessage
	}
	s := ""
	for _, it := range res.Items {
		s += it.ChallengeID + "\t" + it.RunID + "\t" + string(it.Result) + "\t" + it.Outcome
		if it.Note != "" {
			s += "\t" + it.Note
		}
		s += "\n"
	}
	for _, ns := range res.NotStarted {
		s += ns.ChallengeID + "\t(未起動)\t" + string(ns.Reason)
		if ns.Detail != "" {
			s += "\t" + ns.Detail
		}
		s += "\n"
	}
	return s
}

package cli

import "github.com/masanami/flywheel/internal/core"

// このファイルは `--auto` の個別の操作（`classify --auto`・`plan --auto`）が共有する
// 出力の組み立てを持つ（docs/features/m3-invoker-delegation.md §IF / API
// 「`--auto` の個別の操作と `run` の `--json` は、`cycle` の `phases` の 1 要素と同じ形を
// `{"phase": {…}}` で返す」）。判断点ごとに同じ写像を複製せず、ここへ集める。

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

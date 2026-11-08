package invoker

import (
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// priorArtifactLabel は、前の委譲が残した PR の値を入れるデータの区画のラベル。
const priorArtifactLabel = "前の成果物"

// 前の PR の続け方の固定の文。区画の外に置く（区画の中の指示には従わない）。続け先として
// 渡してよい値（ブランチ・PR の URL・head）はデータの区画に入れ、この文はそれを指す。
const (
	priorArtifactOpenText = "前の起動が作った PR は、まだ開いている。この依頼は、その PR の続きである。" +
		"「前の成果物」の区画のブランチ（無ければ PR の head のブランチ）を checkout して、同じ PR へ push で続ける。" +
		"新しいブランチも新しい PR も作らない。"
	priorArtifactClosedText  = "前の起動が作った PR は閉じている（マージ済みを含む）。続ける先は無いので、必要なら新しい PR を作ってよい。"
	priorArtifactUnknownText = "前の起動が作った PR の状態を確かめられなかった。「前の成果物」の区画の PR URL について `gh pr view <PR の URL>` で状態を確かめる。" +
		"開いていれば、区画のブランチ（無ければ PR の head のブランチ）を checkout して、同じ PR へ push で続け、新しいブランチも新しい PR も作らない。" +
		"閉じていれば（マージ済みを含む）、新しい PR を作ってよい。"
)

// priorArtifactParts は前の成果物の、区画の外の固定の文と、値のデータの区画（値を渡さない状態は空）を返す。
// prior が nil・状態が未知の値のときは、どちらも空。閉じた PR・マージ済みの PR は、値が入っていても
// 続け先として渡さない。
func priorArtifactParts(prior *core.PriorArtifact) (fixedText, data string) {
	if prior == nil {
		return "", ""
	}
	switch prior.State {
	case core.PriorArtifactClosed, core.PriorArtifactMerged:
		return priorArtifactClosedText, ""
	case core.PriorArtifactOpen:
		fixedText = priorArtifactOpenText
	case core.PriorArtifactUnknown:
		fixedText = priorArtifactUnknownText
	default:
		return "", ""
	}
	var lines []string
	if prior.Branch != "" {
		lines = append(lines, "ブランチ: "+prior.Branch)
	}
	if prior.PRURL != "" {
		lines = append(lines, "PR: "+prior.PRURL)
	}
	if prior.HeadSHA != "" {
		lines = append(lines, "head: "+prior.HeadSHA)
	}
	if len(lines) == 0 {
		return fixedText, ""
	}
	return fixedText, WrapDataSection(priorArtifactLabel, strings.Join(lines, "\n"))
}

// PriorArtifactTexts は前の成果物の固定の文を、状態の名前つきで返す（禁止語の検査が埋め込みの
// 文面と同じに見られるようにするため）。
func PriorArtifactTexts() map[string]string {
	return map[string]string{
		"open": priorArtifactOpenText, "closed": priorArtifactClosedText, "unknown": priorArtifactUnknownText,
	}
}

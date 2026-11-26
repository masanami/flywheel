package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// このファイルは、計画の出力の受入基準の受け持ち（タスクの covers と、根の deferred_criteria）の検査と、
// 検査に使った一覧の計画への書き足し、計画の本文の節を持つ。
// 突き合わせる一覧は、core が仕様の文書を読んで作ったもの（判断点の出力から作らない）。

// MaxDeferredReasonRunes は受け持たない基準の理由の文字数の上限（2000 文字は通し、2001 文字は不正）。
const MaxDeferredReasonRunes = 2000

// j2RawDeferred は計画の出力の deferred_criteria の要素（この計画で受け持たない受入基準）の生の形。
type j2RawDeferred struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// checkPlanCoverage は計画の受け持ちを一覧 criteria と突き合わせる。criteria が nil（区画「受入基準」を渡していない計画）のときは、
// 識別が 1 件でもあれば不正で、漏れは無い。通れば空、落ちれば理由の文を返す（識別も基準の文も、出力の中身も写さない）。
func checkPlanCoverage(tasks []j2Task, deferred []j2RawDeferred, criteria *planAcceptanceCriteria) string {
	var items []acceptanceCriterion
	if criteria != nil {
		items = criteria.Items
	}
	known := make(map[string]bool, len(items))
	for _, c := range items {
		known[c.ID] = true
	}
	covered := map[string]bool{}
	for i, t := range tasks {
		own := map[string]bool{}
		for _, id := range t.Covers {
			if !known[id] {
				return fmt.Sprintf("tasks[%d].covers has an unknown criterion", i)
			}
			if own[id] {
				return fmt.Sprintf("tasks[%d].covers has a duplicate criterion", i)
			}
			own[id], covered[id] = true, true
		}
	}
	skipped := map[string]bool{}
	for i, d := range deferred {
		if !known[d.ID] {
			return fmt.Sprintf("deferred_criteria[%d].id is an unknown criterion", i)
		}
		if covered[d.ID] {
			return fmt.Sprintf("deferred_criteria[%d].id is a duplicate criterion: a task already covers it", i)
		}
		if err := checkText(fmt.Sprintf("deferred_criteria[%d].reason", i), d.Reason, MaxDeferredReasonRunes); err != nil {
			return validationReason(err)
		}
		skipped[d.ID] = true
	}
	missing := 0
	for _, c := range items {
		if !covered[c.ID] && !skipped[c.ID] {
			missing++
		}
	}
	if missing > 0 {
		return fmt.Sprintf("criteria not covered: %d of %d", missing, len(items))
	}
	return ""
}

// addAcceptanceCriteria は検査済みの出力の JSON（空白を除いた spec のオブジェクト）の根の末尾へ、検査に使った一覧
// acceptance_criteria を足す。ほかの欄と並びは変えない。
func addAcceptanceCriteria(spec []byte, criteria planAcceptanceCriteria) []byte {
	// 文字列と構造体だけの値なので、Marshal は失敗しない。
	list, _ := json.Marshal(criteria)
	out := append([]byte{}, spec[:len(spec)-1]...)
	out = append(append(out, `,"acceptance_criteria":`...), list...)
	return append(out, '}')
}

// AcceptanceCoverageHeading は計画の本文で、受入基準の受け持ちを出す節の見出し。
const AcceptanceCoverageHeading = "受入基準の受け持ち"

// writePlanCoverageSection は計画の本文の節「受入基準の受け持ち」を書く。受け持たない基準は一覧の順に 1 基準 1 行で出し、
// 識別は単独で出さず基準の文の先頭を添える。
func writePlanCoverageSection(b *strings.Builder, criteria *planAcceptanceCriteria, deferred []j2RawDeferred) {
	b.WriteString("\n## " + AcceptanceCoverageHeading + "\n\n")
	if criteria == nil {
		b.WriteString("- 仕様の文書が無いので、受入基準の網羅は検査していない\n")
		return
	}
	fmt.Fprintf(b, "- 仕様の文書: %s\n- 受入基準: %d 件\n", oneLine(criteria.Path), len(criteria.Items))
	if len(deferred) == 0 {
		b.WriteString("- 受け持たない基準: なし\n")
		return
	}
	reasons := make(map[string]string, len(deferred))
	for _, d := range deferred {
		if _, seen := reasons[d.ID]; !seen {
			reasons[d.ID] = d.Reason
		}
	}
	b.WriteString("- 受け持たない基準:\n")
	for _, c := range criteria.Items {
		reason, ok := reasons[c.ID]
		if !ok {
			continue
		}
		excerpt, cut := truncateRunes(c.Text, acceptanceExcerptRunes)
		if cut {
			excerpt += "…"
		}
		fmt.Fprintf(b, "  - %s（%s）: %s\n", excerpt, c.ID, oneLine(reason))
	}
}

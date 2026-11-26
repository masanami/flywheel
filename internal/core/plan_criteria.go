package core

import (
	"encoding/json"
	"fmt"
	"slices"
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

// knownCriteria は一覧 criteria（nil は一覧なし）の識別の集合を返す。
func knownCriteria(criteria *planAcceptanceCriteria) map[string]bool {
	known := map[string]bool{}
	if criteria != nil {
		for _, c := range criteria.Items {
			known[c.ID] = true
		}
	}
	return known
}

// checkTaskCovers は 1 つのタスク（理由の文に出す位置は i）の受け持ち covers が、一覧の識別 known だけからなり、重なりが無いことを確かめる。
// 通れば空、落ちれば理由の文を返す。
func checkTaskCovers(i int, covers []string, known map[string]bool) string {
	own := map[string]bool{}
	for _, id := range covers {
		if !known[id] {
			return fmt.Sprintf("tasks[%d].covers has an unknown criterion", i)
		}
		if own[id] {
			return fmt.Sprintf("tasks[%d].covers has a duplicate criterion", i)
		}
		own[id] = true
	}
	return ""
}

// checkPlanCoverage は計画の受け持ちを一覧 criteria と突き合わせる。criteria が nil（区画「受入基準」を渡していない計画）のときは、
// 識別が 1 件でもあれば不正で、漏れは無い。done は済みのタスクが受け持つ識別で、漏れに数えない（一覧に無い識別は何にも当たらない）。
// 通れば空、落ちれば理由の文を返す（識別も基準の文も、出力の中身も写さない）。
func checkPlanCoverage(tasks []j2Task, deferred []j2RawDeferred, criteria *planAcceptanceCriteria, done map[string]bool) string {
	var items []acceptanceCriterion
	if criteria != nil {
		items = criteria.Items
	}
	known := knownCriteria(criteria)
	covered := map[string]bool{}
	for i, t := range tasks {
		if reason := checkTaskCovers(i, t.Covers, known); reason != "" {
			return reason
		}
		for _, id := range t.Covers {
			covered[id] = true
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
		if !covered[c.ID] && !done[c.ID] && !skipped[c.ID] {
			missing++
		}
	}
	if missing > 0 {
		return fmt.Sprintf("criteria not covered: %d of %d", missing, len(items))
	}
	return ""
}

// 下書きをリポジトリへ入れるタスクの検査で落ちたことを見分ける語（実行の記録の理由の文に含める）。
const (
	specLandingMissing    = "spec landing missing"
	specLandingDuplicate  = "duplicate spec landing"
	specLandingNotFirst   = "spec landing not first"
	specLandingUnexpected = "unexpected spec landing"
)

// specLandingTaskLine は計画の本文で、下書きをリポジトリへ入れるタスクの行に出す文。
const specLandingTaskLine = "仕様の文書をリポジトリへ入れる"

// checkPlanSpecLanding は下書きをリポジトリへ入れるタスクの数と順を検査する。criteria は検査に使う受入基準の一覧
// （仕様の文書の場所を持たない課題は nil）、done は下書きを入れた済みのタスクがあるか。下書きを持たない課題では、
// 入れるタスクがあれば不正。下書きを持つ課題では、済みのタスクが入れていなければ新しいタスクにちょうど 1 件あり、
// ほかの新しいタスクは前提をたどるとそのタスクに届く。済みのタスクが入れていれば、新しいタスクには 1 件も無い。
// 前提は検査済み（鍵はすべて tasks の中にあり、循環が無い）であること。通れば空、落ちれば理由の文を返す。
func checkPlanSpecLanding(tasks []j2Task, criteria *planAcceptanceCriteria, done bool) string {
	var landing []string
	for _, t := range tasks {
		if t.LandsSpec {
			landing = append(landing, t.Key)
		}
	}
	switch {
	case criteria == nil || !criteria.Draft:
		if len(landing) > 0 {
			return specLandingUnexpected
		}
		return ""
	case len(landing) > 1 || (done && len(landing) == 1):
		return specLandingDuplicate
	case done:
		return ""
	case len(landing) == 0:
		return specLandingMissing
	}
	reaches := map[string]bool{landing[0]: true}
	// 前提の並びはタスクの並びと一致しないので、増えなくなるまで広げる。
	for grown := true; grown; {
		grown = false
		for _, t := range tasks {
			if !reaches[t.Key] && slices.ContainsFunc(t.DependsOn, func(k string) bool { return reaches[k] }) {
				reaches[t.Key], grown = true, true
			}
		}
	}
	if len(reaches) < len(tasks) {
		return specLandingNotFirst
	}
	return ""
}

// doneSpecLandingTitle は、tasks のうち下書きを入れた済みのタスクの題名を返す（無ければ nil）。
func doneSpecLandingTitle(tasks []Task) *string {
	for _, t := range tasks {
		if t.Status == TaskStatusDone && t.LandsSpec {
			title := t.Title
			return &title
		}
	}
	return nil
}

// specLandingTitle は下書きをリポジトリへ入れるタスクの題名を返す（新しいタスクに無ければ、済みのタスク done。どちらも無ければ空）。
func specLandingTitle(tasks []j2Task, done *string) string {
	for _, t := range tasks {
		if t.LandsSpec {
			return t.Title
		}
	}
	if done != nil {
		return *done
	}
	return ""
}

// coveredCriteriaHeading は、委譲へ渡すタスクの文面で、タスクが受け持つ受入基準を出す節の名前。
const coveredCriteriaHeading = "受け持つ受入基準"

// formatCoveredCriteria は、受け持ち covers の基準を、計画に書き足した一覧 list の順に 1 基準 1 行（文の全文と、括弧に入れた識別）にする。
// 一覧に無い識別は出さない。出す基準が無ければ（covers が空・一覧を持たない計画）空。
func formatCoveredCriteria(list *planAcceptanceCriteria, covers []string) string {
	if list == nil {
		return ""
	}
	own := make(map[string]bool, len(covers))
	for _, id := range covers {
		own[id] = true
	}
	var b strings.Builder
	for _, c := range list.Items {
		if own[c.ID] {
			fmt.Fprintf(&b, "- %s（%s）\n", c.Text, c.ID)
		}
	}
	return b.String()
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
// 識別は単独で出さず基準の文の先頭を添える。一覧が下書きのものなら、仕様の文書の行に下書きであることと、
// リポジトリへ入れるタスクの題名 landingTitle を添え、置き換える元があればその行を出す。
func writePlanCoverageSection(b *strings.Builder, criteria *planAcceptanceCriteria, deferred []j2RawDeferred, landingTitle string) {
	b.WriteString("\n## " + AcceptanceCoverageHeading + "\n\n")
	if criteria == nil {
		b.WriteString("- 仕様の文書が無いので、受入基準の網羅は検査していない\n")
		return
	}
	fmt.Fprintf(b, "- 仕様の文書: %s", oneLine(criteria.Path))
	if criteria.Draft {
		fmt.Fprintf(b, "（下書き。リポジトリへ入れるのは「%s」）", oneLine(landingTitle))
	}
	b.WriteString("\n")
	if criteria.BasePath != nil {
		fmt.Fprintf(b, "- 置き換える元: %s\n", oneLine(*criteria.BasePath))
	}
	fmt.Fprintf(b, "- 受入基準: %d 件\n", len(criteria.Items))
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

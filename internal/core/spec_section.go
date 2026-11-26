package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// このファイルは、計画を作る判断点の入力の区画「受入基準」と、承認した計画に書き足した受入基準の一覧の読み取りを持つ。

// AcceptanceCriteriaSectionLabel は、受入基準を J2 の入力へ渡すデータの区画の名前（J2 の指示文がこの名前で区画を指す）。
const AcceptanceCriteriaSectionLabel = "受入基準"

// acceptanceExcerptRunes は区画に入れる基準の文の長さの上限（コードポイント）。
const acceptanceExcerptRunes = 80

// acceptanceDoneMark は、済みのタスクが受け持った基準の行に付ける印。
const acceptanceDoneMark = "（済み）"

// planAcceptanceCriteria は、計画の構造化した出力の根の acceptance_criteria（仕様の文書の場所と、基準の一覧）。
type planAcceptanceCriteria struct {
	Path  string                `json:"path"`
	Items []acceptanceCriterion `json:"items"`
}

// loadPlanAcceptanceCriteria は計画の構造化した出力 spec（空は出力なし）が持つ受入基準の一覧を返す。
// 一覧を持たない（項目が無いか、基準が 0 件の）計画では nil。
func loadPlanAcceptanceCriteria(spec string) (*planAcceptanceCriteria, error) {
	if spec == "" {
		return nil, nil
	}
	var out struct {
		AcceptanceCriteria *planAcceptanceCriteria `json:"acceptance_criteria"`
	}
	if err := json.Unmarshal([]byte(spec), &out); err != nil {
		return nil, fmt.Errorf("task_plan spec acceptance_criteria: %w", err)
	}
	if out.AcceptanceCriteria == nil || len(out.AcceptanceCriteria.Items) == 0 {
		return nil, nil
	}
	return out.AcceptanceCriteria, nil
}

// formatAcceptanceCriteriaSection は区画「受入基準」の本文。tasks のうち済みのタスクが受け持つ基準の行に、済みの印を付ける。
// 基準の文は先頭だけを入れ、切っても印を付けない。
func formatAcceptanceCriteriaSection(list planAcceptanceCriteria, tasks []Task) string {
	done := map[string]bool{}
	for _, t := range tasks {
		if t.Status != TaskStatusDone {
			continue
		}
		for _, id := range t.Covers {
			done[id] = true
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "仕様の文書: %s\n件数: %d\n", oneLine(list.Path), len(list.Items))
	for _, c := range list.Items {
		mark := ""
		if done[c.ID] {
			mark = acceptanceDoneMark
		}
		excerpt, _ := truncateRunes(c.Text, acceptanceExcerptRunes)
		fmt.Fprintf(&b, "\n- %s%s: %s", c.ID, mark, excerpt)
	}
	return b.String()
}

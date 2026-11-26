package core

import (
	"context"
	"database/sql"
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
	// Draft は仕様の文書が下書きか。BasePath は下書きが丸ごと置き換える元の文書の場所（下書きでないか、場所が無ければ nil）。
	// どちらも持たない一覧は、下書きでないと読む。
	Draft    bool    `json:"draft"`
	BasePath *string `json:"base_path"`
}

// loadPlanAcceptanceCriteria は計画の構造化した出力 spec（空は出力なし）が持つ受入基準の一覧を返す。
// 一覧を持たない（項目 acceptance_criteria が無い）計画では nil。項目があるのに null・基準が 0 件のものは、
// 壊れた出力として誤りにする（core は 1 件以上の一覧だけを書き足す。一覧なしと読むと、検査し直しが黙って外れる）。
func loadPlanAcceptanceCriteria(spec string) (*planAcceptanceCriteria, error) {
	if spec == "" {
		return nil, nil
	}
	var out struct {
		AcceptanceCriteria json.RawMessage `json:"acceptance_criteria"`
	}
	if err := json.Unmarshal([]byte(spec), &out); err != nil {
		return nil, fmt.Errorf("task_plan spec acceptance_criteria: %w", err)
	}
	if out.AcceptanceCriteria == nil {
		return nil, nil
	}
	var list *planAcceptanceCriteria
	if err := json.Unmarshal(out.AcceptanceCriteria, &list); err != nil {
		return nil, fmt.Errorf("task_plan spec acceptance_criteria: %w", err)
	}
	if list == nil || len(list.Items) == 0 {
		return nil, fmt.Errorf("task_plan spec acceptance_criteria: the list is empty")
	}
	return list, nil
}

// doneTaskCovers は、tasks のうち済みのタスクが受け持つ識別の集合を返す。
func doneTaskCovers(tasks []Task) map[string]bool {
	done := map[string]bool{}
	for _, t := range tasks {
		if t.Status != TaskStatusDone {
			continue
		}
		for _, id := range t.Covers {
			done[id] = true
		}
	}
	return done
}

// loadDoneTaskCovers は課題 cid の済みのタスク（計画の版を問わない）が受け持つ識別の集合を読む。
func loadDoneTaskCovers(ctx context.Context, tx *sql.Tx, cid int64) (map[string]bool, error) {
	all, err := listTasks(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	return doneTaskCovers(all), nil
}

// formatPlanAcceptanceCriteriaSection は計画の形の呼び出しの区画「受入基準」の本文。下書きの一覧では、見出しの行に続けて、
// 下書きであること（下書きを入れた済みのタスクがあれば、その題名 doneLanding つき）と、置き換える元の文書
// （無ければ新しい文書であること）の行を足す。doneLanding は計画の検査（checkPlanSpecLanding）へ渡す値と同じもの。
// tasks（済みの印を付ける集合）とは別の集合なので、tasks からは導かない。
func formatPlanAcceptanceCriteriaSection(list planAcceptanceCriteria, tasks []Task, doneLanding *string) string {
	body := formatAcceptanceCriteriaSection(list, tasks)
	if !list.Draft {
		return body
	}
	origin := "新しい文書（いまある文書の直しではない）"
	if list.BasePath != nil {
		origin = "置き換える元: " + oneLine(*list.BasePath)
	}
	state := "下書き（リポジトリにまだ入っていない）"
	if doneLanding != nil {
		state = "下書き（済みのタスク「" + oneLine(*doneLanding) + "」がリポジトリへ入れた）"
	}
	// 見出し（場所と件数の行）と基準の行の間は、空の行 1 つ。
	head, items, _ := strings.Cut(body, "\n\n")
	return head + "\n" + state + "\n" + origin + "\n\n" + items
}

// formatAcceptanceCriteriaSection は区画「受入基準」の本文。tasks のうち済みのタスクが受け持つ基準の行に、済みの印を付ける。
// 基準の文は先頭だけを入れ、切っても印を付けない。
func formatAcceptanceCriteriaSection(list planAcceptanceCriteria, tasks []Task) string {
	done := doneTaskCovers(tasks)
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

package core

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// このファイルは、仕様の下書きを委譲へ渡す部分を持つ: タスクの文面の節と、起動するタスクを選ぶときの下書きの読み直し。
// 下書きかどうかと置き換える元の場所は、承認した計画に書き足した一覧（draft・base_path）から引く。課題のいまの場所は見ない。

const (
	// specLandingHeading は、下書きをリポジトリへ入れるタスクの文面の節の名前（specLandingTaskLine と同じ文）。
	specLandingHeading = specLandingTaskLine
	// specDocumentHeading は、下書きを持つ課題の、入れるタスクでないタスクの文面の節の名前。
	specDocumentHeading = "仕様の文書"
	// SpecDraftSectionLabel は下書きの全文の名前（契約の形の文面のファイルの最後の節と、標準入力のデータの区画）。
	SpecDraftSectionLabel = "仕様の下書きの全文"
)

// specTaskSection は、タスクの文面に足す仕様の文書の節。Heading が空なら節は無い。Body は末尾に改行を持たない。
type specTaskSection struct {
	Heading string
	Body    string
}

// landsSpecDraft は、タスク t が一覧 list の下書きをリポジトリへ入れるタスクか。
func landsSpecDraft(list *planAcceptanceCriteria, t Task) bool {
	return list != nil && list.Draft && t.LandsSpec
}

// specTaskSectionFor は、タスク t の文面に足す節を返す。all は課題のタスク（ID の昇順）で、入れるタスクの題名を引く
// （取りやめを除く最後のもの）。下書きの一覧でなければ節は無い。
func specTaskSectionFor(list *planAcceptanceCriteria, t Task, all []Task) specTaskSection {
	if list == nil || !list.Draft {
		return specTaskSection{}
	}
	if t.LandsSpec {
		place := "置き換える元の文書は無いので、リポジトリの決まりの場所に新しい文書として置く。"
		if list.BasePath != nil {
			place = fmt.Sprintf("置き換える元の文書「%s」を丸ごと置き換える。", oneLine(*list.BasePath))
		}
		return specTaskSection{Heading: specLandingHeading, Body: "- 渡された「" + SpecDraftSectionLabel + "」を、リポジトリの文書としてそのまま入れる。\n" +
			"- 受入基準の文を変えない。\n- " + place}
	}
	var lines []string
	title := ""
	for _, o := range all {
		if o.LandsSpec && o.Status != TaskStatusCancelled {
			title = o.Title
		}
	}
	if title != "" {
		lines = append(lines, "- 仕様の文書をリポジトリへ入れるタスク: "+oneLine(title))
	}
	if list.BasePath != nil {
		lines = append(lines, "- 置き換える元の文書: "+oneLine(*list.BasePath))
	}
	if len(lines) == 0 {
		return specTaskSection{}
	}
	return specTaskSection{Heading: specDocumentHeading, Body: strings.Join(lines, "\n")}
}

// appendSpecDraftToTaskDocument は、契約の形の文面 doc の最後に、下書きの全文の節を足す。節より後ろには何も足さず、
// 中身は draft のまま（末尾の改行も足さない）。
func appendSpecDraftToTaskDocument(doc, draft string) string {
	return doc + "\n# " + SpecDraftSectionLabel + "\n\n" + draft
}

// draftLandingList は課題 cid の承認した計画の、下書きの一覧を返す。下書きの一覧でなければ nil。壊れた一覧も nil にする
// （その計画は、委譲の前の計画の検査が断る。ここで誤りにすると、ほかの課題の委譲まで止まる）。
func draftLandingList(ctx context.Context, tx *sql.Tx, cid int64) (*planAcceptanceCriteria, error) {
	plan, ok, err := loadApprovedPlan(ctx, tx, cid)
	if err != nil || !ok {
		return nil, err
	}
	list, perr := loadPlanAcceptanceCriteria(plan.Spec)
	if perr != nil || list == nil || !list.Draft {
		return nil, nil
	}
	return list, nil
}

// rereadSpecDraft は、計画に書き足した一覧 list の下書きを読み直し、全文を返す。読めないか、抜き出した受入基準の識別の
// 並びが一覧と違うときは、起動しない理由の詳細（下書きの場所と訳）を返す。
func rereadSpecDraft(workspace string, list planAcceptanceCriteria) (text, detail string) {
	data, criteria, err := readSpecDocument(workspace, list.Path)
	if err != nil {
		return "", fmt.Sprintf("下書き %s を読めない: %v", list.Path, err)
	}
	if !slices.EqualFunc(criteria, list.Items, func(a, b acceptanceCriterion) bool { return a.ID == b.ID }) {
		return "", fmt.Sprintf("下書き %s の受入基準の並びが、計画の時点と違う", list.Path)
	}
	return string(data), ""
}

// specDraftNotStarted は、下書きが計画の後に変わったので、タスク taskID を起動しない理由。
func specDraftNotStarted(challengeID, taskID, detail string) NotStarted {
	return NotStarted{ChallengeID: challengeID, Reason: NotStartedSpecDraftChanged, Detail: fmt.Sprintf("タスク %s: %s", taskID, detail)}
}

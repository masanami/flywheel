package core

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// このファイルは、計画を作る判断点の入力の区画「受入基準」（docs/features/request-to-plan.md）を、
// 実ストアと偽の判断の IF で確かめる。

const criteriaDocPath = "docs/spec.md"

// 長い基準は、2 バイト以上の文字を含めて 80 コードポイントで切る位置を確かめる。
var (
	criterionShort = "短い基準"
	criterionHead  = strings.Repeat("長", 79) + "末"
	criterionLong  = criterionHead + "この先は入れない"
	criteriaDoc    = "# 仕様\n\n## 受入基準\n\n- [ ] " + criterionLong + "\n- [ ] " + criterionShort + "\n"
)

// criteriaLine は区画の基準 1 件の行（文は入力の全文。mark は済みの印）。
func criteriaLine(text, excerpt string, done bool) string {
	mark := ""
	if done {
		mark = "（済み）"
	}
	return "- " + acceptanceCriterionID(text) + mark + ": " + excerpt
}

func (f *j2Fixture) setSpec(t *testing.T, id, path string) {
	t.Helper()
	if _, err := f.s.EditChallenge(ctx0, ChannelCLI, id, EditInput{Spec: &path}); err != nil {
		t.Fatalf("EditChallenge(spec): %v", err)
	}
}

func setTaskCovers(t *testing.T, s *Store, cid int64, key string, covers string) {
	t.Helper()
	writeForTest(t, s, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE task SET covers = ? WHERE challenge_id = ? AND key = ?`, covers, cid, key)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			t.Fatalf("rows = %d, %v", n, err)
		}
		return nil
	})
}

func requireLines(t *testing.T, content string, want ...string) {
	t.Helper()
	lines := strings.Split(content, "\n")
	at := 0
	for _, w := range want {
		for at < len(lines) && lines[at] != w {
			at++
		}
		if at == len(lines) {
			t.Fatalf("section lacks the line %q (in this order):\n%s", w, content)
		}
		at++
	}
}

// planInput は分類済の課題の計画を 1 回呼び、判断点の入力を返す。
func planInput(t *testing.T, f *j2Fixture, id string) JudgmentLaunchInput {
	t.Helper()
	var got JudgmentLaunchInput
	inv := succeededInvoker(j2OutputFor(false, nil))
	inv.onInvoke = func(in JudgmentLaunchInput) { got = in }
	if _, err := f.run(t, inv, idPtr(id)); err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if got.Sections == nil {
		t.Fatal("J2 was not invoked")
	}
	return got
}

func TestPlanAutoJ2_Criteria_SectionHasPathCountAndExcerptsInDocumentOrder(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "spec", "P1")
	writeWorkspaceFile(t, f.s.workspace, criteriaDocPath, criteriaDoc)
	f.setSpec(t, ch.ID, criteriaDocPath)

	content, ok := j2SectionText(planInput(t, f, ch.ID), "受入基準")
	if !ok {
		t.Fatal("no section 受入基準")
	}
	requireLines(t, content,
		"仕様の文書: "+criteriaDocPath,
		"件数: 2",
		criteriaLine(criterionLong, criterionHead, false),
		criteriaLine(criterionShort, criterionShort, false),
	)
	if strings.Contains(content, "この先は入れない") || strings.Contains(content, "…") {
		t.Errorf("the excerpt must be cut at 80 code points without a mark:\n%s", content)
	}
}

func TestPlanAutoJ2_Criteria_NoSectionWithoutSpecPath(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "no spec", "P1")
	writeWorkspaceFile(t, f.s.workspace, criteriaDocPath, criteriaDoc)
	if content, ok := j2SectionText(planInput(t, f, ch.ID), "受入基準"); ok {
		t.Errorf("unexpected section 受入基準:\n%s", content)
	}
}

func TestPlanAutoJ2_Criteria_UnreadableDocumentDoesNotInvokeJ2(t *testing.T) {
	t.Parallel()
	pad := func(n int) string { return criteriaDoc + strings.Repeat("x", n-len(criteriaDoc)) }
	for _, tc := range []struct {
		name string
		// arrange は場所を書いた後のワークスペース ws を整える（nil は文書を置かない）。
		arrange func(t *testing.T, ws string)
	}{
		{name: "missing file"},
		{name: "no read permission", arrange: func(t *testing.T, ws string) {
			writeWorkspaceFile(t, ws, criteriaDocPath, criteriaDoc)
			p := filepath.Join(ws, criteriaDocPath)
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			if fh, err := os.Open(p); err == nil {
				_ = fh.Close()
				t.Skip("this user ignores file permissions")
			}
		}},
		{name: "larger than 2 MiB", arrange: func(t *testing.T, ws string) {
			writeWorkspaceFile(t, ws, criteriaDocPath, pad(2<<20+1))
		}},
		{name: "link leads outside the workspace", arrange: func(t *testing.T, ws string) {
			outside := t.TempDir()
			writeWorkspaceFile(t, outside, "o.md", criteriaDoc)
			if err := os.MkdirAll(filepath.Join(ws, "docs"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "o.md"), filepath.Join(ws, criteriaDocPath)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "no criteria", arrange: func(t *testing.T, ws string) {
			writeWorkspaceFile(t, ws, criteriaDocPath, "## 受入基準\n本文だけ\n")
		}},
		{name: "colliding IDs", arrange: func(t *testing.T, ws string) {
			writeWorkspaceFile(t, ws, criteriaDocPath, collidingDoc)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newJ2Fixture(t)
			ch := f.newClassified(t, "unreadable", "P1")
			f.setSpec(t, ch.ID, criteriaDocPath)
			if tc.arrange != nil {
				tc.arrange(t, f.s.workspace)
			}
			before := f.detail(t, ch.ID)
			inv := succeededInvoker(j2OutputFor(false, nil))
			inv.onInvoke = func(JudgmentLaunchInput) { t.Error("J2 must not be invoked") }

			res, err := f.run(t, inv, idPtr(ch.ID))
			if err != nil {
				t.Fatalf("PlanAutoJ2: %v", err)
			}
			if len(res.Items) != 0 || len(res.NotStarted) != 1 {
				t.Fatalf("result = %+v, want one not started", res)
			}
			ns := res.NotStarted[0]
			if ns.ChallengeID != ch.ID || ns.Reason != NotStartedSpecUnreadable || string(ns.Reason) != "spec_unreadable" {
				t.Errorf("not started = %+v, want spec_unreadable", ns)
			}
			if !strings.Contains(ns.Detail, criteriaDocPath) {
				t.Errorf("detail %q lacks the path", ns.Detail)
			}
			after := f.detail(t, ch.ID)
			if after.Status != before.Status || after.Version != before.Version || len(f.runResults(t, ch.ID)) != 0 {
				t.Errorf("state changed: %+v", after)
			}
		})
	}
}

func TestPlanAutoJ2_Criteria_ExactlyTwoMiBIsRead(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "limit size", "P1")
	writeWorkspaceFile(t, f.s.workspace, criteriaDocPath, criteriaDoc+strings.Repeat("x", 2<<20-len(criteriaDoc)))
	f.setSpec(t, ch.ID, criteriaDocPath)
	if _, ok := j2SectionText(planInput(t, f, ch.ID), "受入基準"); !ok {
		t.Fatal("no section 受入基準")
	}
}

func TestPlanAutoJ2_Criteria_UnreadableDocumentDoesNotStopOtherChallenges(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	bad := f.newClassified(t, "unreadable", "P1")
	f.setSpec(t, bad.ID, "docs/missing.md")
	good := f.newClassified(t, "plannable", "P2")

	res, err := f.run(t, succeededInvoker(j2OutputFor(false, nil)), nil)
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != bad.ID || res.NotStarted[0].Reason != NotStartedSpecUnreadable {
		t.Errorf("not started = %+v", res.NotStarted)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != good.ID || res.Items[0].Outcome != "plan" {
		t.Errorf("items = %+v", res.Items)
	}
	if got := f.detail(t, good.ID).Status; got != StatusAwaitingPlanApproval {
		t.Errorf("status = %s, want awaiting_plan_approval", got)
	}
}

func TestReplanJ2_Criteria_OnlyCriteriaCoveredByDoneTasksAreMarkedDone(t *testing.T) {
	t.Parallel()
	f := newReplanJ2Fixture(t, nil)
	writeWorkspaceFile(t, f.s.workspace, criteriaDocPath, "## 受入基準\n- [ ] 済みの基準\n- [ ] 未完のタスクの基準\n- [ ] 誰も持たない基準\n")
	f.setSpec(t, f.id, criteriaDocPath)
	// a は済み、b は着手中。b の受け持ちは済みに数えない。
	setTaskCovers(t, f.s, f.cid(), "a", `["`+acceptanceCriterionID("済みの基準")+`"]`)
	setTaskCovers(t, f.s, f.cid(), "b", `["`+acceptanceCriterionID("未完のタスクの基準")+`"]`)

	in, _ := f.j2Input(t, j2OutputFor(false, nil))
	content, ok := j2SectionText(in, "受入基準")
	if !ok {
		t.Fatal("no section 受入基準")
	}
	requireLines(t, content,
		criteriaLine("済みの基準", "済みの基準", true),
		criteriaLine("未完のタスクの基準", "未完のタスクの基準", false),
		criteriaLine("誰も持たない基準", "誰も持たない基準", false),
	)
}

// setApprovedPlanCriteria は、承認済みの計画の構造化した出力へ、受入基準の一覧（acceptance_criteria）を書き足す。
func setApprovedPlanCriteria(t *testing.T, f *paFixture, path string, texts ...string) {
	t.Helper()
	list := planAcceptanceCriteria{Path: path}
	for _, s := range texts {
		list.Items = append(list.Items, acceptanceCriterion{ID: acceptanceCriterionID(s), Text: s})
	}
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE task_plan SET spec = json_set(spec, '$.acceptance_criteria', json(?)) WHERE challenge_id = ?`, string(b), f.cid())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			t.Fatalf("rows = %d, %v", n, err)
		}
		return nil
	})
}

func expandSections(t *testing.T, f *paFixture) JudgmentLaunchInput {
	t.Helper()
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	if inv.count() != 1 {
		t.Fatalf("J2 calls = %d, want 1", inv.count())
	}
	return inv.calls[0]
}

// 一覧は計画のものを使う。承認の後に文書を書き換えても、場所を直しても変わらない。
func TestExpand_Criteria_SectionComesFromTheApprovedPlanList(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	setApprovedPlanCriteria(t, f, criteriaDocPath, criterionLong, criterionShort)
	writeWorkspaceFile(t, f.s.workspace, criteriaDocPath, "## 受入基準\n- [ ] 書き換えた後の基準\n")
	f.setSpec(t, f.id, "docs/other.md")
	setTaskCovers(t, f.s, f.cid(), "a", `["`+acceptanceCriterionID(criterionShort)+`"]`)
	setTaskCovers(t, f.s, f.cid(), "b", `["`+acceptanceCriterionID(criterionLong)+`"]`)
	f.setTaskStatus(t, "a", TaskStatusDone)

	content, ok := j2SectionText(expandSections(t, f), "受入基準")
	if !ok {
		t.Fatal("no section 受入基準")
	}
	requireLines(t, content,
		"仕様の文書: "+criteriaDocPath,
		"件数: 2",
		criteriaLine(criterionLong, criterionHead, false),
		criteriaLine(criterionShort, criterionShort, true),
	)
	if strings.Contains(content, "書き換えた後の基準") || strings.Contains(content, "この先は入れない") {
		t.Errorf("section:\n%s", content)
	}
}

// 前の計画の版の済みのタスクの受け持ちも、済みに数える。
func TestExpand_Criteria_DoneTaskOfAnEarlierPlanVersionIsMarkedDone(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	setApprovedPlanCriteria(t, f, criteriaDocPath, "前の版で済んだ基準")
	f.setTaskStatus(t, "a", TaskStatusDone)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, covers, created_at, updated_at, budget_impl_micros, budget_review_micros)
			SELECT challenge_id, plan_version + 100, 'old', title, body, done_criteria, repo, operation, 'done', ?, created_at, updated_at, 0, 0
			FROM task WHERE challenge_id = ? AND key = 'a'`, `["`+acceptanceCriterionID("前の版で済んだ基準")+`"]`, f.cid())
		return err
	})

	content, ok := j2SectionText(expandSections(t, f), "受入基準")
	if !ok {
		t.Fatal("no section 受入基準")
	}
	requireLines(t, content, criteriaLine("前の版で済んだ基準", "前の版で済んだ基準", true))
}

// 一覧を持たない計画への「足す」の形には、課題が場所を持っていても区画を入れない。
func TestExpand_Criteria_NoSectionWhenTheApprovedPlanHasNoList(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	writeWorkspaceFile(t, f.s.workspace, criteriaDocPath, criteriaDoc)
	f.setSpec(t, f.id, criteriaDocPath)
	f.setTaskStatus(t, "a", TaskStatusDone)
	if content, ok := j2SectionText(expandSections(t, f), "受入基準"); ok {
		t.Errorf("unexpected section 受入基準:\n%s", content)
	}
}

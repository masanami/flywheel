package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは、受け持つ受入基準を委譲へ渡すことと、計画の作り直し・足す形・検査し直し
// （docs/features/request-to-plan.md）を、実ストアで確かめる。

const (
	handoffCritFirst  = "一覧の 1 番目の基準。" + "長い文は切らずに全文を渡す。長い文は切らずに全文を渡す。長い文は切らずに全文を渡す。長い文は切らずに全文を渡す。長い文は切らずに全文を渡す。末尾 FULL-TEXT-END"
	handoffCritSecond = "一覧の 2 番目の基準"
	handoffCritThird  = "一覧の 3 番目の基準"
	handoffHeading    = "受け持つ受入基準"
	handoffRewritten  = "## 受入基準\n- [ ] 書き換えた後の基準\n"
)

var (
	handoffID1 = acceptanceCriterionID(handoffCritFirst)
	handoffID2 = acceptanceCriterionID(handoffCritSecond)
	handoffID3 = acceptanceCriterionID(handoffCritThird)
)

// handoffListed は計画の出力へ、core が書き足す形の一覧（基準 3 件）を入れる。
func handoffListed(m map[string]any) {
	m["acceptance_criteria"] = planAcceptanceCriteria{Path: criteriaDocPath, Items: []acceptanceCriterion{
		{ID: handoffID1, Text: handoffCritFirst}, {ID: handoffID2, Text: handoffCritSecond}, {ID: handoffID3, Text: handoffCritThird},
	}}
}

// handoffPlan は一覧を持つ 1 タスクの計画の出力。タスクは covers を受け持ち、残りは受け持たない基準にする。
func handoffPlan(covers, deferred []string) func(m map[string]any) {
	return func(m map[string]any) {
		handoffListed(m)
		m["tasks"].([]any)[0].(map[string]any)["covers"] = covers
		var d []any
		for _, id := range deferred {
			d = append(d, covDeferred(id, "後で"))
		}
		m["deferred_criteria"] = d
	}
}

// handoffSpec は main-repo の操作 operation の、基準 2 件を受け持つ承認済みの計画。
func handoffSpec(repo, operation string, mut func(m map[string]any)) string {
	return planSpec(func(m map[string]any) {
		m["repo"], m["operation"] = repo, operation
		task := j2TestTask("a", repo, operation)
		m["tasks"] = []any{task}
		handoffPlan([]string{handoffID2, handoffID1}, []string{handoffID3})(m)
		if mut != nil {
			mut(m)
		}
	})
}

// rewriteSpecAfterApproval は承認の後に、仕様の文書を書き換え、課題の仕様の文書の場所を別の文書へ直す。
func rewriteSpecAfterApproval(t *testing.T, s *Store, id string) {
	t.Helper()
	writeWorkspaceFile(t, s.workspace, criteriaDocPath, handoffRewritten)
	writeWorkspaceFile(t, s.workspace, "docs/other.md", handoffRewritten)
	other := "docs/other.md"
	if _, err := s.EditChallenge(ctx0, ChannelCLI, id, EditInput{Spec: &other}); err != nil {
		t.Fatalf("EditChallenge(spec): %v", err)
	}
}

func renderTaskDoc(t *testing.T, s *Store, cid int64, task Task) string {
	t.Helper()
	var out string
	readForTest(t, s, func(tx *sql.Tx) error {
		plan, ok, err := loadApprovedPlanAnySpec(context.Background(), tx, cid)
		if err != nil || !ok {
			t.Fatalf("approved plan: ok=%v err=%v", ok, err)
		}
		out, err = loadTaskDocument(context.Background(), tx, cid, plan, task)
		return err
	})
	return out
}

// requireCoveredSection は text に、基準 1・2 の全文が一覧の順に識別つきで並ぶ節があることを確かめる。
func requireCoveredSection(t *testing.T, text, heading string) {
	t.Helper()
	want := heading + "- " + handoffCritFirst + "（" + handoffID1 + "）\n- " + handoffCritSecond + "（" + handoffID2 + "）\n"
	if !strings.Contains(text, want) {
		t.Errorf("lacks the section %q:\n%s", want, text)
	}
}

func TestTaskDocument_CoveredCriteria(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", handoffSpec("main-repo", "impl", nil))
	cid, _ := parseChallengeID(id)
	rewriteSpecAfterApproval(t, f.s, id)
	task := taskRows(t, f.s, cid)[0]

	doc := renderTaskDoc(t, f.s, cid, task)
	requireCoveredSection(t, doc, "## "+handoffHeading+"\n\n")
	if strings.Contains(doc, handoffCritThird) || strings.Contains(doc, "書き換えた後の基準") {
		t.Errorf("the document carries a criterion the task does not cover:\n%s", doc)
	}

	t.Run("一覧に無い識別は出さない", func(t *testing.T) {
		tk := task
		tk.Covers = []string{covUnknownID, handoffID3}
		got := renderTaskDoc(t, f.s, cid, tk)
		requireContains(t, got, "- "+handoffCritThird+"（"+handoffID3+"）\n")
		requireAbsent(t, got, covUnknownID)
	})
	t.Run("covers が空なら節が無い", func(t *testing.T) {
		tk := task
		tk.Covers = nil
		requireAbsent(t, renderTaskDoc(t, f.s, cid, tk), handoffHeading)
	})
	t.Run("一覧に無い識別だけなら節が無い", func(t *testing.T) {
		tk := task
		tk.Covers = []string{covUnknownID}
		requireAbsent(t, renderTaskDoc(t, f.s, cid, tk), handoffHeading)
	})
}

func TestTaskDocument_NoCoveredCriteriaSectionWithoutList(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	cid, _ := parseChallengeID(id)
	task := taskRows(t, f.s, cid)[0]
	task.Covers = []string{handoffID1}
	requireAbsent(t, renderTaskDoc(t, f.s, cid, task), handoffHeading)
}

// 指示文だけの形の委譲: 指示文を書く判断点のタスクの区画に節が出て、起動の検査し直しは計画の一覧で通る。
func TestDelegation_BriefFormGetsCoveredCriteriaFromThePlanList(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", handoffSpec("main-brief", "brief", nil))
	rewriteSpecAfterApproval(t, f.s, id)

	f.runDelegation(t, id)

	if len(f.j3Inputs) != 1 {
		t.Fatalf("J3 calls = %d, want 1", len(f.j3Inputs))
	}
	sec, ok := j2SectionText(f.j3Inputs[0], labelTaskSection)
	if !ok {
		t.Fatal("no task section")
	}
	requireCoveredSection(t, sec+"\n", handoffHeading+":\n")
}

func TestDelegation_BriefFormTaskSectionWithoutCoversHasNoCriteria(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", briefSpec(nil))
	f.runDelegation(t, id)
	if len(f.j3Inputs) != 1 {
		t.Fatalf("J3 calls = %d, want 1", len(f.j3Inputs))
	}
	sec, _ := j2SectionText(f.j3Inputs[0], labelTaskSection)
	requireAbsent(t, sec, handoffHeading)
}

// 委譲の段の検査し直しの 3 つの入口は、計画に書き足した一覧と突き合わせる。
func TestRecheckOfApprovedPlan_UsesThePlanList(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mut  func(m map[string]any)
		ok   bool
	}{
		{"一覧どおり", nil, true},
		{"一覧を持たない計画は検査しない", func(m map[string]any) {
			delete(m, "acceptance_criteria")
			delete(m, "deferred_criteria")
		}, true},
		{"一覧に無い識別", handoffPlan([]string{handoffID1, covUnknownID}, []string{handoffID2, handoffID3}), false},
		{"漏れ", handoffPlan([]string{handoffID1}, nil), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDelegateFixture(t)
			id := f.newInProgress(t, "t", "P1", handoffSpec("main-repo", "impl", tc.mut))
			cid, _ := parseChallengeID(id)
			rewriteSpecAfterApproval(t, f.s, id)
			in := f.input(f.cycle(t, 300), &id)

			_, err := f.s.loadDelegationContext(ctx0, in, f.detail(t, id).Challenge)
			if tc.ok != (err == nil) || (err != nil && !errors.Is(err, ErrValidation)) {
				t.Errorf("loadDelegationContext: err = %v, want ok=%v", err, tc.ok)
			}
			ones, ok, reason, err := f.s.loadPlanCandidates(ctx0, in, cid)
			if err != nil || ok != tc.ok || (tc.ok && len(ones) != 1) {
				t.Errorf("loadPlanCandidates: ones=%v ok=%v reason=%q err=%v, want ok=%v", ones, ok, reason, err, tc.ok)
			}
			_, ok, err = f.s.contractRepoWorkDir(ctx0, f.agent, f.conn, cid, "", "")
			if err != nil || ok != tc.ok {
				t.Errorf("contractRepoWorkDir: ok=%v err=%v, want ok=%v", ok, err, tc.ok)
			}
		})
	}
}

// 計画の登録で済みのタスクの受け持ちとして数えた基準は、検査し直しでも数える。
func TestRecheckOfApprovedPlan_CountsCriteriaCoveredByDoneTasks(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", handoffSpec("main-repo", "impl", handoffPlan([]string{handoffID1, handoffID2}, nil)))
	cid, _ := parseChallengeID(id)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, covers, created_at, updated_at)
			SELECT challenge_id, plan_version + 100, 'old', title, body, done_criteria, repo, operation, 'done', ?, created_at, updated_at FROM task WHERE challenge_id = ?`,
			`["`+handoffID3+`"]`, cid)
		return err
	})
	in := f.input(f.cycle(t, 300), &id)
	if _, err := f.s.loadDelegationContext(ctx0, in, f.detail(t, id).Challenge); err != nil {
		t.Errorf("loadDelegationContext: %v", err)
	}
	if _, ok, reason, err := f.s.loadPlanCandidates(ctx0, in, cid); err != nil || !ok {
		t.Errorf("loadPlanCandidates: ok=%v reason=%q err=%v", ok, reason, err)
	}
	if _, ok, err := f.s.contractRepoWorkDir(ctx0, f.agent, f.conn, cid, "", ""); err != nil || !ok {
		t.Errorf("contractRepoWorkDir: ok=%v err=%v", ok, err)
	}
}

// 人が登録した計画（構造化した出力が無い）の課題は、仕様の文書の場所を持っていても受入基準の検査をされない。
// 扱いは、場所を持たないときと同じになる。
func TestDelegationTargets_HandRegisteredPlanIsNotCheckedForCriteria(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", "")
	targets := func() (out delegationTargets) {
		readForTest(t, f.s, func(tx *sql.Tx) (err error) {
			out, err = selectDelegationTargetsFor(ctx0, tx, f.conn)
			return err
		})
		return out
	}
	before := targets()
	writeWorkspaceFile(t, f.s.workspace, criteriaDocPath, criteriaDoc)
	path := criteriaDocPath
	if _, err := f.s.EditChallenge(ctx0, ChannelCLI, id, EditInput{Spec: &path}); err != nil {
		t.Fatal(err)
	}
	if after := targets(); !reflect.DeepEqual(before, after) {
		t.Errorf("targets changed with a spec path: %+v -> %+v", before, after)
	}
}

// 完了の検証の入力には、節「受け持つ受入基準」を入れない。
func TestTaskVerify_InputHasNoCoveredCriteriaSection(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	list := planAcceptanceCriteria{Path: criteriaDocPath, Items: []acceptanceCriterion{{ID: handoffID2, Text: handoffCritSecond}}}
	f.sql(t, `UPDATE task SET covers = ?`, `["`+handoffID2+`"]`)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task_plan SET spec = json_set(spec, '$.acceptance_criteria', json(?))`, mustJSON(t, list))
		return err
	})
	var got []JudgmentLaunchInput
	f.j5.j5.onInvoke = func(in JudgmentLaunchInput) { got = append(got, in) }
	f.setVerdict("met")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("J5 calls = %d, want 1", len(got))
	}
	requireAbsent(t, allSectionsText(got[0].Sections), handoffHeading+":")
	requireAbsent(t, allSectionsText(got[0].Sections), "## "+handoffHeading+"\n")
}

// --- 足す形 ---

func expandCovers(covers ...string) []byte {
	return []byte(expandAddOutput(func(ts []map[string]any) { ts[0]["covers"] = covers }))
}

func TestExpand_CoversAreRecordedAndHandedOff(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	setApprovedPlanCriteria(t, f, criteriaDocPath, handoffCritFirst, handoffCritSecond, handoffCritThird)
	// 受け持たれていない基準（3 番目）が残っても足せる。
	res := f.expand(t, &expandRecorder{out: expandCovers(handoffID2, handoffID1)})
	if len(res.Items) != 1 || res.Items[0].Result != RunResultSucceeded {
		t.Fatalf("items = %+v", res.Items)
	}
	task := f.taskByKey(t, "c")
	if task == nil || strings.Join(task.Covers, ",") != handoffID2+","+handoffID1 {
		t.Fatalf("task c = %+v, want the covers of the output", task)
	}
	writeWorkspaceFile(t, f.s.workspace, criteriaDocPath, handoffRewritten)
	requireCoveredSection(t, renderTaskDoc(t, f.s, f.cid(), *task), "## "+handoffHeading+"\n\n")
}

func TestExpand_InvalidCovers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		listed bool
		covers []string
		want   string
	}{
		{"一覧に無い識別", true, []string{handoffID1, covUnknownID}, "unknown criterion"},
		{"一覧を持たない計画に識別", false, []string{handoffID1}, "unknown criterion"},
		{"1 つのタスクで重なる", true, []string{handoffID1, handoffID1}, "duplicate criterion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := expandDoneFixture(t)
			if tc.listed {
				setApprovedPlanCriteria(t, f, criteriaDocPath, handoffCritFirst)
			}
			res := f.expand(t, &expandRecorder{out: expandCovers(tc.covers...)})
			if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
				t.Fatalf("items = %+v, want invalid_output", res.Items)
			}
			if f.taskByKey(t, "c") != nil {
				t.Error("a task was added")
			}
			if n := f.count(t, `SELECT COUNT(*) FROM run WHERE challenge_id = ? AND error LIKE ?`, f.cid(), "%"+tc.want+"%"); n != 1 {
				t.Errorf("runs whose reason has %q = %d, want 1", tc.want, n)
			}
		})
	}
}

// overLimitHeldWithCovers は、基準を受け持つタスクを足す依頼がオーナー待ち（保留つき）の課題。
func overLimitHeldWithCovers(t *testing.T) *paFixture {
	t.Helper()
	f := expandDoneFixture(t)
	setApprovedPlanCriteria(t, f, criteriaDocPath, handoffCritFirst, handoffCritSecond)
	f.expand(t, &expandRecorder{out: []byte(expandAddOutput(func(ts []map[string]any) {
		ts[0]["budget_impl_usd"], ts[0]["budget_review_usd"] = 3.0, 2.0
		ts[0]["covers"] = []string{handoffID1}
	}))})
	if st := f.detail(t, f.id).Status; st != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", st)
	}
	return f
}

func TestHoldAnswerTaskAddition_RecheckUsesThePlanList(t *testing.T) {
	t.Parallel()
	f := overLimitHeldWithCovers(t)
	rewriteSpecAfterApproval(t, f.s, f.id)
	if _, err := f.answerHold(t, taskAddOptionAdd); err != nil {
		t.Fatal(err)
	}
	if task := f.taskByKey(t, "c"); task == nil || strings.Join(task.Covers, ",") != handoffID1 {
		t.Fatalf("task c = %+v", task)
	}
}

func TestHoldAnswerTaskAddition_RecheckRefusesACriterionOutsideThePlanList(t *testing.T) {
	t.Parallel()
	f := overLimitHeldWithCovers(t)
	list := planAcceptanceCriteria{Path: criteriaDocPath, Items: []acceptanceCriterion{{ID: handoffID2, Text: handoffCritSecond}}}
	coretest.Exec(t, f.s.Workspace(), `UPDATE task_plan SET spec = json_set(spec, '$.acceptance_criteria', json(?)) WHERE challenge_id = ?`, mustJSON(t, list), f.cid())
	if _, err := f.answerHold(t, taskAddOptionAdd); !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if h, _ := f.lastHold(t); !h.Open() || f.taskByKey(t, "c") != nil {
		t.Errorf("hold = %+v, want unchanged", h)
	}
}

// --- 計画の作り直し ---

const (
	replanCritDone  = "済みのタスクが受け持った基準"
	replanCritNewB  = "新しい計画の基準 1"
	replanCritNewC  = "新しい計画の基準 2"
	replanOldText   = "文を変える前の基準"
	replanNewerText = "文を変えた後の基準"
)

// replanWithDoneCovers は、済みのタスク a が基準を受け持つ、作り直し前の課題。
func replanWithDoneCovers(t *testing.T, doc ...string) *paFixture {
	t.Helper()
	f := newReplanJ2Fixture(t, nil)
	writeWorkspaceFile(t, f.s.workspace, criteriaDocPath, "## 受入基準\n- [ ] "+strings.Join(doc, "\n- [ ] ")+"\n")
	f.setSpec(t, f.id, criteriaDocPath)
	setTaskCovers(t, f.s, f.cid(), "a", mustJSON(t, []string{acceptanceCriterionID(replanCritDone), acceptanceCriterionID(replanOldText)}))
	return f
}

// replanOut は作り直しの出力。1 つ目のタスクが covers を受け持つ。
func replanOut(covers ...string) []byte {
	var m map[string]any
	if err := json.Unmarshal(paPlanOut(2, true), &m); err != nil {
		panic(err)
	}
	m["tasks"].([]any)[0].(map[string]any)["covers"] = covers
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func TestReplanJ2_CriteriaCoveredByDoneTasksAreNotMissing(t *testing.T) {
	t.Parallel()
	f := replanWithDoneCovers(t, replanCritDone, replanCritNewB, replanCritNewC)
	in, res := f.j2Input(t, replanOut(acceptanceCriterionID(replanCritNewB), acceptanceCriterionID(replanCritNewC)))
	if len(res.Items) != 1 || res.Items[0].Result != RunResultSucceeded || res.Items[0].Outcome != "plan" {
		t.Fatalf("items = %+v, want a registered plan", res.Items)
	}
	sec, _ := j2SectionText(in, DoneTasksSectionLabel)
	// 記録は 2 件だが、いまの区画にあるのは 1 件。
	requireContains(t, sec, "受け持ち済みの受入基準: 1 件")
}

func TestReplanJ2_ACriterionWhoseTextChangedIsMissing(t *testing.T) {
	t.Parallel()
	f := replanWithDoneCovers(t, replanCritDone, replanNewerText)
	_, res := f.j2Input(t, replanOut())
	if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("items = %+v, want invalid_output", res.Items)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE challenge_id = ? AND error LIKE '%criteria not covered: 1 of 2%'`, f.cid()); n != 1 {
		t.Errorf("runs with the missing reason = %d, want 1", n)
	}
}

func TestReplanJ2_NoDoneCountWithoutSpecPath(t *testing.T) {
	t.Parallel()
	f := newReplanJ2Fixture(t, nil)
	in, _ := f.j2Input(t, paPlanOut(2, true))
	sec, _ := j2SectionText(in, DoneTasksSectionLabel)
	requireAbsent(t, sec, "受け持ち済み")
}

// 承認の後に場所を別の文書へ直してから作り直すと、区画「受入基準」は直した後の文書から作る。
func TestReplanJ2_SectionComesFromTheEditedSpecPath(t *testing.T) {
	t.Parallel()
	f := replanWithDoneCovers(t, replanCritDone)
	writeWorkspaceFile(t, f.s.workspace, "docs/other.md", "## 受入基準\n- [ ] 別の文書の基準\n")
	other := "docs/other.md"
	if _, err := f.s.EditChallenge(ctx0, ChannelCLI, f.id, EditInput{Spec: &other}); err != nil {
		t.Fatal(err)
	}
	in, _ := f.j2Input(t, replanOut(acceptanceCriterionID("別の文書の基準")))
	sec, _ := j2SectionText(in, AcceptanceCriteriaSectionLabel)
	requireContains(t, sec, "仕様の文書: docs/other.md")
	requireContains(t, sec, "別の文書の基準")
	requireAbsent(t, sec, replanCritDone)
}

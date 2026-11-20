package core

import (
	"database/sql"
	"strings"
	"testing"
)

// このファイルは、計画の作り直しの J2 の入力（済みのタスク・課題の単位の確かめの指摘）と、済みのタスクがある課題の計画の検査（uses_integration が真）を、
// 実ストアと偽の判断の IF で検証する。

// replanFixture は、統合先を使う 2 個のタスク（a が済み・b が着手中）の計画を承認済みで持ち、保留への --replan の回答の後（分類済）の課題。
// holdMut が非 nil なら、回答の前に、開いている保留の行（問い・理由）をそれで書き換える。
func newReplanJ2Fixture(t *testing.T, holdMut func(f *paFixture, cid int64)) *paFixture {
	t.Helper()
	f := newPAFixture(t, 2, true)
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(f.id)
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusDone)
	setTaskStatusRaw(t, f.s, cid, "b", TaskStatusInProgress)
	if _, err := f.s.HoldChallenge(ctx0, ChannelCLI, f.id, HoldInput{Question: "範囲を変えたい"}); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if holdMut != nil {
		holdMut(f, cid)
	}
	pre, err := f.s.PrepareAnswer(ctx0, f.id, "足りない分を足す")
	if err != nil {
		t.Fatal(err)
	}
	if c, _, err := f.s.ExecuteAnswer(ctx0, AnswerRequest{ChallengeID: f.id, ExpectedVersion: pre.Version, Answer: "足りない分を足す", Replan: true}, relayedAttestation(t, f.id, "s", "q")); err != nil || c.Status != StatusClassified {
		t.Fatalf("answer --replan: %v %+v", err, c)
	}
	return f
}

func (f *paFixture) j2Input(t *testing.T, out []byte) (JudgmentLaunchInput, *J2AutoResult) {
	t.Helper()
	var got JudgmentLaunchInput
	inv := succeededInvoker(out)
	inv.onInvoke = func(in JudgmentLaunchInput) { got = in }
	res, err := f.run(t, inv, idPtr(f.id))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	return got, res
}

func j2SectionText(in JudgmentLaunchInput, label string) (string, bool) {
	for _, s := range in.Sections {
		if s.Label == label {
			return s.Content, true
		}
	}
	return "", false
}

// AC-209: done のタスクがある課題の、計画の作り直しの J2 の入力に、済みのタスクの題名（と完了条件）がある。済みでないタスクは載らない。
func TestReplanJ2_AC209_TheInputHasTheTitlesOfTheDoneTasks(t *testing.T) {
	t.Parallel()
	f := newReplanJ2Fixture(t, nil)
	in, _ := f.j2Input(t, paPlanOut(2, true))
	sec, ok := j2SectionText(in, DoneTasksSectionLabel)
	if !ok {
		t.Fatalf("J2 input lacks the section %q:\n%s", DoneTasksSectionLabel, allSectionsText(in.Sections))
	}
	if !strings.Contains(sec, "TASK-a") {
		t.Errorf("the section lacks the title of the done task: %q", sec)
	}
	if strings.Contains(sec, "TASK-b") {
		t.Errorf("the section carries the task that is not done: %q", sec)
	}
	if !strings.Contains(sec, "完了条件: ") {
		t.Errorf("the section lacks the done criteria: %q", sec)
	}
}

// 済みのタスクが無い課題の計画の作り直しの J2 の入力には、区画「済みのタスク」を入れない。
func TestReplanJ2_NoDoneTaskNoSection(t *testing.T) {
	t.Parallel()
	f := newReplanJ2Fixture(t, func(f *paFixture, cid int64) {
		setTaskStatusRaw(t, f.s, cid, "a", TaskStatusInProgress)
	})
	in, _ := f.j2Input(t, paPlanOut(2, true))
	if _, ok := j2SectionText(in, DoneTasksSectionLabel); ok {
		t.Errorf("J2 input has the section %q although no task is done", DoneTasksSectionLabel)
	}
}

// AC-210: 保留の reason が challenge_check_not_met の保留への --replan の回答の後の J2 の入力に、J5 の指摘がある。
func TestReplanJ2_AC210_TheInputHasTheFindingsOfTheChallengeCheck(t *testing.T) {
	t.Parallel()
	f := newReplanJ2Fixture(t, func(f *paFixture, cid int64) {
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE hold SET reason = 'challenge_check_not_met', question = ? WHERE challenge_id = ? AND answer IS NULL`,
				challengeCheckNotMetQuestion(&j5ValidatedOutput{Verdict: J5VerdictNotMet, Reason: "REASON-XYZ", Feedback: strPtr("FINDING-XYZ")}), cid)
			return err
		})
	})
	in, _ := f.j2Input(t, paPlanOut(2, true))
	sec, ok := j2SectionText(in, ChallengeCheckFindingsSectionLabel)
	if !ok {
		t.Fatalf("J2 input lacks the section %q:\n%s", ChallengeCheckFindingsSectionLabel, allSectionsText(in.Sections))
	}
	for _, want := range []string{"FINDING-XYZ", "REASON-XYZ"} {
		if !strings.Contains(sec, want) {
			t.Errorf("the section lacks %q: %q", want, sec)
		}
	}
}

// 理由が challenge_check_not_met でない保留への回答の後の J2 の入力には、区画「課題の単位の確かめの指摘」を入れない。
func TestReplanJ2_TheFindingsSectionIsOnlyForTheChallengeCheckHold(t *testing.T) {
	t.Parallel()
	f := newReplanJ2Fixture(t, nil)
	in, _ := f.j2Input(t, paPlanOut(2, true))
	if _, ok := j2SectionText(in, ChallengeCheckFindingsSectionLabel); ok {
		t.Errorf("J2 input has the section %q for an ordinary hold", ChallengeCheckFindingsSectionLabel)
	}
}

// AC-208: done のタスクがある課題の、uses_integration が偽の計画は不正になる（真の計画は登録される）。
func TestReplanJ2_AC208_APlanWithoutTheIntegrationIsInvalidWhileATaskIsDone(t *testing.T) {
	t.Parallel()
	f := newReplanJ2Fixture(t, nil)
	_, res := f.j2Input(t, paPlanOut(1, false)) // タスクが 1 個なら、偽でも見直し前は有効
	if len(res.Items) != 1 || res.Items[0].Outcome == "plan" {
		t.Fatalf("items = %+v, want the plan rejected as invalid", res.Items)
	}
	if got := f.detail(t, f.id).Status; got != StatusClassified {
		t.Errorf("status = %s, want classified (nothing was registered)", got)
	}
	_, res = f.j2Input(t, paPlanOut(1, true))
	if len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
		t.Fatalf("items = %+v, want the plan with the integration registered", res.Items)
	}
}

// 済みのタスクが無い課題では、タスクが 1 個で uses_integration が偽の計画は有効のまま（統合先を使わない課題の動きは変えない）。
func TestReplanJ2_APlanWithoutTheIntegrationStaysValidWhenNoTaskIsDone(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 1, false) // 作る側が、計画が登録されたこと（outcome が plan）を確かめる
	if got := f.detail(t, f.id).Status; got != StatusAwaitingPlanApproval {
		t.Errorf("status = %s, want awaiting_plan_approval", got)
	}
}

package core

// このファイルは、区切り S4c-3 の「回答する保留の指定（--hold）・保留での照合・回答で保留を閉じる遷移」を、実ストアで検証する
// （AC-49〜55・AC-61〜71・AC-75〜77・AC-80・AC-81・AC-130）。--relayed と本人確認の前の要約（AC-72〜74・78・79）は CLI の試験が持つ。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func (f *holdScopeFixture) holdQuestion(t *testing.T, hold string) string {
	t.Helper()
	if hold == "" {
		return ""
	}
	n, _ := parseHoldID(hold)
	return f.q(t, `SELECT question FROM hold WHERE id = ?`, n)
}

// twoHolds は、タスク a の契約の待ちの保留（H-1。オーナーへの問い）と、タスク b を止める保留（H-2）が開いている状態にする。
func newTwoHoldsFixture(t *testing.T, optsB HoldOptionsInput) *holdScopeFixture {
	t.Helper()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	optsB.Scope = HoldScopeTask
	if err := f.overlay(f.taskB, optsB); err != nil {
		t.Fatal(err)
	}
	return f
}

// answerHold は、保留 hold（空なら指定しない）を確かめてから回答する（CLI と同じ①→②）。
func (f *holdScopeFixture) answerHold(t *testing.T, hold, answer, option string) (*Challenge, error) {
	t.Helper()
	p, err := f.s.PrepareAnswerHold(context.Background(), f.id, hold, answer, option, false)
	if err != nil {
		return nil, err
	}
	c, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{
		ChallengeID: f.id, ExpectedVersion: p.Version, Answer: answer, Option: option,
		HoldID: p.HoldID, HoldQuestion: p.Question, HoldOptions: p.Options, HoldOptionRequired: p.OptionRequired,
	}, verifiedAttestationForTest(t, f.id))
	return c, err
}

func (f *holdScopeFixture) holdAnswer(t *testing.T, id string) string {
	t.Helper()
	n, _ := parseHoldID(id)
	return f.q(t, `SELECT COALESCE(answer, '') FROM hold WHERE id = ?`, n)
}

// AC-49・50・55・62・63・130: --hold で指定した保留だけに答える。答えた後も課題は人間対応待ちで、ほかの保留は開いたまま。両方に答えると着手中へ戻る。
func TestAnswerHold_AnswersTheSpecifiedHoldAndKeepsAwaitingHuman(t *testing.T) {
	t.Parallel()
	f := newTwoHoldsFixture(t, HoldOptionsInput{})
	c, err := f.answerHold(t, "H-1", "これで", "")
	if err != nil {
		t.Fatalf("answer H-1: %v", err)
	}
	if c.Status != StatusAwaitingHuman { // AC-49
		t.Errorf("status = %s, want awaiting_human", c.Status)
	}
	if got := f.holdAnswer(t, "H-1"); got != "これで" { // AC-62
		t.Errorf("H-1 answer = %q", got)
	}
	if got := f.holdAnswer(t, "H-2"); got != "" { // AC-50・63
		t.Errorf("H-2 answer = %q, want empty", got)
	}
	if got := f.openHolds(t); got != "1" {
		t.Errorf("open holds = %s, want 1", got)
	}
	// AC-130: 回答の作業ログに保留の ID が残る。
	if got := f.q(t, `SELECT json_extract(after, '$.hold_id') FROM activity WHERE entity = 'challenge' AND json_extract(after, '$.answer') = 'これで'`); got != "H-1" {
		t.Errorf("worklog hold_id = %q, want H-1", got)
	}
	c, err = f.answerHold(t, "H-2", "こちらも", "")
	if err != nil {
		t.Fatalf("answer H-2: %v", err)
	}
	if c.Status != StatusInProgress { // AC-55
		t.Errorf("status = %s, want in_progress", c.Status)
	}
}

// AC-61: 開いている保留が 1 つなら --hold を省ける。
func TestAnswerHold_SingleOpenHoldNeedsNoHoldFlag(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	if c, err := f.answerHold(t, "", "これで", ""); err != nil || c.Status != StatusInProgress {
		t.Fatalf("answer = %v, %v, want in_progress", c, err)
	}
}

// AC-64〜68: 開いている保留が 2 つで --hold を省くと、検証の誤りで断る。何も書かず、出力に保留の ID・タスクの題名・問いの先頭が出る。
func TestAnswerHold_TwoOpenHoldsWithoutHoldIsRefusedWithTheList(t *testing.T) {
	t.Parallel()
	f := newTwoHoldsFixture(t, HoldOptionsInput{})
	v := f.version(t)
	_, err := f.answerHold(t, "", "これで", "")
	if !errors.Is(err, ErrValidation) { // AC-64
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if f.holdAnswer(t, "H-1") != "" || f.holdAnswer(t, "H-2") != "" || f.version(t) != v || f.openHolds(t) != "2" { // AC-65
		t.Errorf("something was written")
	}
	msg := err.Error()
	for _, want := range []string{"H-1", "H-2", "タスク a", "タスク b", "どうしますか", "q"} { // AC-66・67・68
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	// ExecuteAnswer に保留を渡さない道（旧来の呼び出し）も、最新の保留へ黙って書かず断る。
	_, _, err = f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: v, Answer: "x"}, verifiedAttestationForTest(t, f.id))
	if !errors.Is(err, ErrValidation) {
		t.Errorf("ExecuteAnswer without hold = %v, want ErrValidation", err)
	}
}

// AC-69・70・71: 別の課題の保留・閉じた保留・H-<番号>の形でない値は、検証の誤りで断る。
func TestAnswerHold_RefusesHoldsThatAreNotOpenHoldsOfTheChallenge(t *testing.T) {
	t.Parallel()
	f := newTwoHoldsFixture(t, HoldOptionsInput{})
	other := f.newMulti(t, "other", "P2", false, multiTask{key: "c"})
	otherCID, _ := parseChallengeID(other)
	if _, err := f.s.transition(context.Background(), ChannelCLI, other, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		_, err := insertHoldWithOptions(ctx, tc, "別の課題の問い", HoldKindHuman, tc.current.Status, nil, HoldOptionsInput{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	otherHold := "H-" + f.q(t, `SELECT id FROM hold WHERE challenge_id = ?`, otherCID)
	if _, err := f.answerHold(t, "H-1", "済み", ""); err != nil {
		t.Fatal(err)
	}
	for name, hold := range map[string]string{"other challenge (AC-69)": otherHold, "closed (AC-70)": "H-1", "no such": "H-999", "bad form (AC-71)": "1", "lowercase": "h-2", "zero": "H-0", "suffix": "H-2x"} {
		_, err := f.answerHold(t, hold, "x", "")
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
	if got := f.holdAnswer(t, "H-2"); got != "" {
		t.Errorf("H-2 answer = %q, want empty", got)
	}
}

// AC-75: 確かめた後、書き込みの前にほかのタスクの保留が立って課題の版が進んでも、回答は成立する。
func TestAnswerHold_OtherHoldRaisedAfterPrepareDoesNotConflict(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	p, err := f.s.PrepareAnswerHold(context.Background(), f.id, "", "これで", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.overlay(f.taskB, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	c, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: p.Version, Answer: "これで",
		HoldID: p.HoldID, HoldQuestion: p.Question, HoldOptions: p.Options, HoldOptionRequired: p.OptionRequired}, verifiedAttestationForTest(t, f.id))
	if err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}
	if c.Status != StatusAwaitingHuman || f.holdAnswer(t, "H-1") != "これで" {
		t.Errorf("status = %s, H-1 answer = %q", c.Status, f.holdAnswer(t, "H-1"))
	}
}

// AC-76・77: 確かめた保留が閉じていた・問いが書き換わっていたら、コンフリクトで断る。
func TestAnswerHold_ClosedOrRewrittenHoldIsAConflict(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(f *holdScopeFixture, t *testing.T){
		"closed (AC-76)": func(f *holdScopeFixture, t *testing.T) {
			if _, err := f.answerHold(t, "H-1", "先に", ""); err != nil {
				t.Fatal(err)
			}
		},
		"question rewritten (AC-77)": func(f *holdScopeFixture, t *testing.T) {
			f.mustExec(t, `UPDATE hold SET question = '書き換わった問い' WHERE id = 1`)
		},
		"options rewritten": func(f *holdScopeFixture, t *testing.T) {
			f.mustExec(t, `UPDATE hold SET options = '[{"id":"z","label":"Z","description":""}]' WHERE id = 1`)
		},
	} {
		change := change
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newTwoHoldsFixture(t, HoldOptionsInput{})
			p, err := f.s.PrepareAnswerHold(context.Background(), f.id, "H-1", "これで", "", false)
			if err != nil {
				t.Fatal(err)
			}
			change(f, t)
			_, _, err = f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: p.Version, Answer: "これで",
				HoldID: p.HoldID, HoldQuestion: p.Question, HoldOptions: p.Options, HoldOptionRequired: p.OptionRequired}, verifiedAttestationForTest(t, f.id))
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("err = %v, want ErrConflict", err)
			}
			if f.holdAnswer(t, "H-2") != "" {
				t.Errorf("H-2 was written")
			}
		})
	}
}

func (f *holdScopeFixture) mustExec(t *testing.T, q string, args ...any) {
	t.Helper()
	if err := f.delegateFixture.exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// AC-80・81: 選択肢の識別は、指定した保留の選択肢で確かめる。別の保留にだけある識別は検証の誤り。
func TestAnswerHold_OptionIsCheckedAgainstTheSpecifiedHold(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	if err := f.overlay(f.taskA, HoldOptionsInput{Scope: HoldScopeTask, Options: []HoldOption{{ID: "y", Label: "Y 案"}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.overlay(f.taskB, HoldOptionsInput{Scope: HoldScopeTask, Options: []HoldOption{{ID: "x", Label: "X 案"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.answerHold(t, "H-1", "", "x"); !errors.Is(err, ErrValidation) { // AC-81
		t.Fatalf("option of the other hold: err = %v, want ErrValidation", err)
	}
	if f.openHolds(t) != "2" {
		t.Fatalf("something was written")
	}
	if _, err := f.answerHold(t, "H-2", "", "x"); err != nil { // AC-80
		t.Fatalf("own option: %v", err)
	}
	if got := f.q(t, `SELECT answer_option FROM hold WHERE id = 2`); got != "x" {
		t.Errorf("answer_option = %q, want x", got)
	}
}

// AC-52: タスク a の facts_unmet の保留に答えると、タスク a の数えが戻る（タスク b の保留は開いたまま）。
func TestAnswerHold_FactsUnmetCountIsResetWhileAnotherHoldIsOpen(t *testing.T) {
	t.Parallel()
	f := newTwoHoldsFixture(t, HoldOptionsInput{})
	f.mustExec(t, `UPDATE hold SET reason = 'facts_unmet' WHERE id = 1`)
	f.mustExec(t, `UPDATE contract_run SET facts_unmet_count = 2 WHERE task_id = ?`, f.taskA)
	f.mustExec(t, `UPDATE contract_run SET facts_unmet_count = 2 WHERE task_id = ?`, f.taskB)
	if _, err := f.answerHold(t, "H-1", "直した", ""); err != nil {
		t.Fatal(err)
	}
	if got := f.q(t, `SELECT facts_unmet_count FROM contract_run WHERE task_id = ?`, f.taskA); got != "0" {
		t.Errorf("task a count = %s, want 0", got)
	}
	if got := f.q(t, `SELECT facts_unmet_count FROM contract_run WHERE task_id = ?`, f.taskB); got != "2" {
		t.Errorf("task b count = %s, want 2 (untouched)", got)
	}
	if f.openHolds(t) != "1" || f.challengeStatus(t) != string(StatusAwaitingHuman) {
		t.Errorf("open holds = %s status = %s", f.openHolds(t), f.challengeStatus(t))
	}
}

// AC-53: タスク a の統合先の保留（委譲先の終わり）に答えると、タスク a は受け取り直しの状態（着手中）へ戻る（課題は人間対応待ちのまま）。
func TestAnswerHold_IntegrationTaskIsResumedWhileAnotherHoldIsOpen(t *testing.T) {
	t.Parallel()
	f := newTwoHoldsFixture(t, HoldOptionsInput{})
	f.mustExec(t, `UPDATE hold SET reason = 'delegate_ended' WHERE id = 1`)
	f.mustExec(t, `UPDATE contract_run SET ended_at = ?, state = 'succeeded' WHERE task_id = ?`, "2026-01-01T00:00:00.000Z", f.taskA)
	f.mustExec(t, `UPDATE task SET status = 'verifying' WHERE id = ?`, f.taskA)
	if _, err := f.answerHold(t, "H-1", "もう一度", ""); err != nil {
		t.Fatal(err)
	}
	if got := f.q(t, `SELECT status FROM task WHERE id = ?`, f.taskA); got != string(TaskStatusInProgress) {
		t.Errorf("task a status = %s, want in_progress", got)
	}
	if f.challengeStatus(t) != string(StatusAwaitingHuman) {
		t.Errorf("challenge status = %s, want awaiting_human", f.challengeStatus(t))
	}
}

// AC-54: タスク a の契約の待ちの保留に答えると、課題が着手中へ戻るのを待たず、次の回りで答えが委譲先へ返る（タスク b の保留は開いたまま）。
func TestAnswerHold_ContractWaitAnswerIsReturnedWhileAnotherHoldIsOpen(t *testing.T) {
	t.Parallel()
	f := newTwoHoldsFixture(t, HoldOptionsInput{})
	// 契約の run が、問いの待ち（waiting）を写した保留（H-1）に結び付いている状態にする。
	f.mustExec(t, `UPDATE contract_run SET state = 'waiting', wait_hold_id = 1 WHERE task_id = ?`, f.taskA)
	f.mustExec(t, `UPDATE contract_run SET state = 'waiting', wait_hold_id = 2 WHERE task_id = ?`, f.taskB)
	row := f.contractRunRowOf(t, f.taskA)
	if _, ok, err := f.s.loadContractAnswer(context.Background(), row, nil); err != nil || ok {
		t.Fatalf("before the answer: resume = %v, err = %v, want no resume", ok, err)
	}
	if _, err := f.answerHold(t, "H-1", "ANSWER-MARKER", ""); err != nil {
		t.Fatal(err)
	}
	if f.challengeStatus(t) != string(StatusAwaitingHuman) || f.openHolds(t) != "1" {
		t.Fatalf("status = %s open = %s", f.challengeStatus(t), f.openHolds(t))
	}
	// タスク b の契約の待ちには、まだ答えが無い（b の保留は開いたまま）。
	if _, ok, err := f.s.loadContractAnswer(context.Background(), f.contractRunRowOf(t, f.taskB), nil); err != nil || ok {
		t.Errorf("task b: resume = %v, err = %v, want no resume", ok, err)
	}
	got, ok, err := f.s.loadContractAnswer(context.Background(), row, nil)
	if err != nil || !ok || got != "ANSWER-MARKER" {
		t.Fatalf("task a: answer = %q resume = %v err = %v, want ANSWER-MARKER", got, ok, err)
	}
}

func (f *holdScopeFixture) contractRunRowOf(t *testing.T, task int64) *contractRunRow {
	t.Helper()
	var row *contractRunRow
	readForTest(t, f.s, func(tx *sql.Tx) error {
		rs, err := tx.Query(contractRunSelect+` WHERE task_id = ? AND ended_at IS NULL`, task)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		if rs.Next() {
			row, err = scanContractRun(rs)
		}
		return err
	})
	if row == nil {
		t.Fatalf("no open contract run for task %d", task)
	}
	return row
}

// AC-51: タスク a の保留に答えた後（タスク b の保留は開いたまま）、次の回りで、タスク a のレビュー待ちの受け取りが行われる。
// 答える前（タスク a の保留が開いている間）は、受け取らない。
func TestAnswerHold_ReviewIsReceivedAfterTheTaskHoldIsAnsweredWhileAnotherHoldIsOpen(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	taskA := f.count(t, `SELECT id FROM task`)
	cid, _ := parseChallengeID(f.cid)
	f.sql(t, `UPDATE contract_run SET task_id = ?`, taskA)
	// タスク b（別のタスク）を足し、b を止める保留を開く。a の保留（H-1）も開く。
	f.sql(t, `INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
		SELECT challenge_id, plan_version, 'b', 'タスク b', body, done_criteria, repo, operation, 'pending', created_at, updated_at FROM task WHERE id = ?`, taskA)
	taskB := f.count(t, `SELECT id FROM task WHERE key = 'b'`)
	for _, task := range []int64{taskA, taskB} {
		f.sql(t, `INSERT INTO hold (challenge_id, question, from_status, raised_at, kind, task_id, scope) VALUES (?, 'q', 'in_progress', '2026-01-01T00:00:00.000Z', 'human', ?, 'task')`, cid, task)
	}
	f.sql(t, `UPDATE challenge SET status = 'awaiting_human'`)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 0 {
		t.Fatalf("verifying tasks = %d while the task's own hold is open, want 0", n)
	}
	// タスク a の保留に答える（回答の書き込みの形）。
	f.sql(t, `UPDATE hold SET answer = 'ok', answered_at = '2026-01-01T00:00:01.000Z' WHERE task_id = ?`, taskA)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying' AND key <> 'b'`); n != 1 {
		t.Fatalf("verifying tasks (task a) = %d, want 1", n)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Errorf("challenge status = %s, want awaiting_human (task b hold is still open)", got)
	}
}

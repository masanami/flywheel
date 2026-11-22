package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// 区切り S4b-2 の supervisor_resolve（T20）と手 reverify。テスト用の判断点（決めた出力を返す）と実ストアで検証する。

const supervisorOwnerRow = "回答者: スーパーバイザー（オーナーの回答ではない）"

func (f *svFixture) holdAnswerRow(t *testing.T, id string) (answer, by, channel, verification, state string) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var a, b, c, v, st sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT answer, answered_by, answered_channel, answered_verification, supervisor_state FROM hold WHERE challenge_id = ?`, cid).Scan(&a, &b, &c, &v, &st)
	}); err != nil {
		t.Fatal(err)
	}
	return a.String, b.String, c.String, v.String, st.String
}

func (f *svFixture) countActivity(t *testing.T, id, action string) (n int, channel, verification string) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*), COALESCE(MAX(channel), ''), COALESCE(MAX(verification), '') FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = ?`, cid, action).Scan(&n, &channel, &verification)
	}); err != nil {
		t.Fatal(err)
	}
	return
}

// verify_uncertain の reverify: 課題は検証中へ戻り、保留はスーパーバイザーが閉じた記録になる。回答の文は findings と note から core が組み立てる。
func TestSupervisorResolve_Reverify_ClosesTheHoldAndReturnsToVerifying(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindVerifyUncertain)
	f.inv.result = svResult(map[string]any{"hand": "reverify", "findings": "FINDINGS-X", "note": "NOTE-Y"})
	res := f.stage(t)
	if res.SuperviseHands[HandReverify] != 1 {
		t.Fatalf("hands = %v, want reverify:1", res.SuperviseHands)
	}
	d, err := f.s.GetChallenge(context.Background(), id)
	if err != nil || d.Status != StatusVerifying {
		t.Fatalf("status = %v (%v), want verifying (the status before the hold)", d.Status, err)
	}
	answer, by, ch, ver, state := f.holdAnswerRow(t, id)
	if by != "supervisor" || ch != "invoker" || ver != "none" || state != "handled" {
		t.Errorf("hold = by %q channel %q verification %q state %q, want supervisor/invoker/none/handled", by, ch, ver, state)
	}
	for _, want := range []string{"スーパーバイザーが調べた結果", "reverify", "FINDINGS-X", "NOTE-Y"} {
		if !strings.Contains(answer, want) {
			t.Errorf("answer %q lacks %q", answer, want)
		}
	}
	if r := f.holdRow(t, id); !r.actionID.Valid {
		t.Error("supervisor_action_id is not set")
	}
	if n, ch, ver := f.countActivity(t, id, "supervisor_resolve"); n != 1 || ch != "invoker" || ver != "none" {
		t.Errorf("supervisor_resolve activities = %d (%s/%s), want 1 (invoker/none)", n, ch, ver)
	}
	if n, _, _ := f.countActivity(t, id, "answer"); n != 0 {
		t.Errorf("answer activities = %d, want 0 (not recorded as the owner's answer)", n)
	}
	as := f.actions(t, id)
	if len(as) != 1 || as[0].Result != ActionExecuted {
		t.Errorf("actions = %+v, want one executed", as)
	}
	// 次の回りの検証の入力（保留の記録の区画）へ、回答の文が渡る。
	cid, _ := parseChallengeID(id)
	var holds []Hold
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) { holds, err = loadHolds(context.Background(), tx, cid); return }); err != nil {
		t.Fatal(err)
	}
	section := formatHoldsSection(holds)
	if !strings.Contains(section, "FINDINGS-X") || !strings.Contains(section, "NOTE-Y") || !strings.Contains(section, supervisorOwnerRow) {
		t.Errorf("holds section = %q, want findings, note and the supervisor row", section)
	}
}

// オーナーが回答した保留には、回答者の行を足さない。
func TestFormatHoldsSection_SupervisorRowOnlyForSupervisorAnswers(t *testing.T) {
	t.Parallel()
	ans, owner, sup, inv, none, ui, tty := "A", "alice", "supervisor", "invoker", "none", "ui", "tty_confirm"
	supHold := Hold{Question: "q", Answer: &ans, AnsweredBy: &sup, AnsweredChannel: &inv, AnsweredVerification: &none}
	ownerHold := Hold{Question: "q", Answer: &ans, AnsweredBy: &owner, AnsweredChannel: &ui, AnsweredVerification: &tty}
	if !strings.Contains(formatHoldsSection([]Hold{supHold}), supervisorOwnerRow) {
		t.Error("a supervisor answer lacks the answerer row")
	}
	if strings.Contains(formatHoldsSection([]Hold{ownerHold}), "回答者") {
		t.Error("an owner answer has the answerer row")
	}
	// 回答者の名前だけが同じでも、経路・本人確認が違えば、スーパーバイザーの回答とは扱わない。
	spoof := Hold{Question: "q", Answer: &ans, AnsweredBy: &sup, AnsweredChannel: &ui, AnsweredVerification: &tty}
	if strings.Contains(formatHoldsSection([]Hold{spoof}), "回答者") {
		t.Error("an answer by an owner named supervisor is shown as the supervisor's")
	}
}

// 保留がスーパーバイザー待ちでなければ（オーナーへ上げた後）、手は断られ、何も書かない。
func TestSupervisorResolve_NotPendingHoldIsRejectedAndNothingIsWritten(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindVerifyUncertain)
	f.exec(t, `UPDATE hold SET supervisor_state = 'escalated'`)
	cid, _ := parseChallengeID(id)
	out, err := f.s.applySupervisorHand(context.Background(), supervisorHandRequest{
		CycleID: f.cycle, Target: supervisorTarget{HoldID: holdIDOf(t, f, cid), ChallengeID: cid, StopKind: StopKindVerifyUncertain},
		Output: SVOutput{Hand: HandReverify, Findings: "x"}, Decl: f.agent,
	})
	if err != nil || out.Result != ActionRejected || out.Reject != RejectStateChanged {
		t.Fatalf("outcome = %+v (%v), want rejected state_changed", out, err)
	}
	d, _ := f.s.GetChallenge(context.Background(), id)
	if d.Status != StatusAwaitingHuman {
		t.Errorf("status = %v, want awaiting_human (unchanged)", d.Status)
	}
	if answer, _, _, _, state := f.holdAnswerRow(t, id); answer != "" || state != "escalated" {
		t.Errorf("hold answer %q state %q, want untouched", answer, state)
	}
	if n, _, _ := f.countActivity(t, id, "supervisor_resolve"); n != 0 {
		t.Errorf("supervisor_resolve activities = %d, want 0", n)
	}
}

func holdIDOf(t *testing.T, f *svFixture, cid int64) int64 {
	t.Helper()
	var hid int64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM hold WHERE challenge_id = ?`, cid).Scan(&hid)
	}); err != nil {
		t.Fatal(err)
	}
	return hid
}

// 人の回答（T12）は本人確認つきのまま。登録簿に（判断点・本人確認なし）の組は無く、手を実行した後も増えない。
func TestSupervisorResolve_VerificationRegistryHasNoInvokerNonePair(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	f.pendingHold(t, StopKindVerifyUncertain)
	f.inv.result = svResult(map[string]any{"hand": "reverify", "findings": "F", "note": "N"})
	if res := f.stage(t); res.SuperviseHands[HandReverify] != 1 {
		t.Fatalf("hands = %v, want reverify:1 (the hand must run)", res.SuperviseHands)
	}
	if registryAllows(ChannelInvoker, VerificationNone) {
		t.Error("the verification registry accepts (invoker, none)")
	}
	for _, e := range verificationRegistry {
		if e.Channel == ChannelInvoker {
			t.Errorf("registry entry %+v is for the invoker", e)
		}
	}
	// 通常の遷移の入口は、T20 を引けない（制限つきの行は専用の書き込みだけ）。
	if _, ok := Lookup(StatusAwaitingHuman, OpSupervisorResolve); ok {
		t.Error("Lookup returns T20")
	}
}

// 本人確認なしの通常の入口から T20 を呼んでも通らない（制限つきの行は、専用の書き込みの印がある呼び出しだけ）。
func TestSupervisorResolve_PlainTransitionCannotUseT20(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindVerifyUncertain)
	_, err := f.s.transition(context.Background(), ChannelCLI, id, OpSupervisorResolve, func(context.Context, *transitionCtx) error { return nil })
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("transition() err = %v, want ErrInvalidTransition", err)
	}
	if d, _ := f.s.GetChallenge(context.Background(), id); d.Status != StatusAwaitingHuman {
		t.Errorf("status = %v, want awaiting_human", d.Status)
	}
}

// artifact_missing の reverify（照合のやり直し）は別のタスク。ここでは実行せず、書き込みを巻き戻して失敗にする。
func TestSupervisorResolve_ArtifactMissingReverifyIsNotThisHand(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindArtifactMissing)
	f.inv.result = svResult(map[string]any{"hand": "reverify", "findings": "F", "note": "N"})
	f.stage(t)
	if as := f.actions(t, id); len(as) != 1 || as[0].Result != ActionFailed || as[0].Hand == nil || *as[0].Hand != HandReverify {
		t.Fatalf("actions = %+v, want one failed reverify action (the hand ran and was not implemented)", as)
	}
	if _, _, _, _, state := f.holdAnswerRow(t, id); state != "escalated" {
		t.Errorf("state = %q, want escalated by the failed action", state)
	}
	if d, _ := f.s.GetChallenge(context.Background(), id); d.Status != StatusAwaitingHuman {
		t.Errorf("status = %v, want awaiting_human", d.Status)
	}
}

// intake_unverified の reverify: 着手中へ戻り、オーナーの回答のときと同じに、そのタスクの統合先の受け取りがやり直される（通す中身の記録が消える）。
func TestSupervisorResolve_Reverify_IntakeUnverifiedRestartsTheIntegrationIntake(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.heldWithTaskRun(t, "in_progress")
	f.sql(t, `UPDATE contract_run SET land_ref = 'REF', land_delivered = 1`)
	f.sql(t, `UPDATE hold SET reason = 'merged_unverified', stop_kind = 'intake_unverified', supervisor_state = 'pending'`)
	cid := f.intID(t)
	var hid int64
	var tid sql.NullInt64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id, task_id FROM hold`).Scan(&hid, &tid)
	}); err != nil {
		t.Fatal(err)
	}
	out, err := f.s.applySupervisorHand(context.Background(), supervisorHandRequest{
		CycleID: f.cycle(t, 300), Target: supervisorTarget{HoldID: hid, ChallengeID: cid, TaskID: &tid.Int64, StopKind: StopKindIntakeUnverified},
		Output: SVOutput{Hand: HandReverify, Findings: "受け取れている"}, Decl: f.agent,
	})
	if err != nil || out.Result != ActionExecuted {
		t.Fatalf("outcome = %+v (%v), want executed", out, err)
	}
	if got := f.text(t, `SELECT status FROM challenge`); got != string(StatusInProgress) {
		t.Errorf("status = %q, want in_progress", got)
	}
	if got := f.text(t, `SELECT COALESCE(land_ref, '') || '/' || land_delivered FROM contract_run`); got != "/0" {
		t.Errorf("land_ref/land_delivered = %q, want cleared", got)
	}
	if got := f.text(t, `SELECT supervisor_state || '/' || answered_by FROM hold`); got != "handled/supervisor" {
		t.Errorf("hold = %q, want handled/supervisor", got)
	}
}

// 保留が待ちのままでも、課題が人間対応待ちでなくなっていれば（取り下げ）、手は断られ、何も書かない。
func TestSupervisorResolve_ChallengeNoLongerAwaitingHumanIsRejected(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindVerifyUncertain)
	f.exec(t, `UPDATE challenge SET status = 'withdrawn'`)
	cid, _ := parseChallengeID(id)
	out, err := f.s.applySupervisorHand(context.Background(), supervisorHandRequest{
		CycleID: f.cycle, Target: supervisorTarget{HoldID: holdIDOf(t, f, cid), ChallengeID: cid, StopKind: StopKindVerifyUncertain},
		Output: SVOutput{Hand: HandReverify, Findings: "x", Note: "y"}, Decl: f.agent,
	})
	if err != nil || out.Result != ActionRejected || out.Reject != RejectStateChanged {
		t.Fatalf("outcome = %+v (%v), want rejected state_changed", out, err)
	}
	if answer, _, _, _, state := f.holdAnswerRow(t, id); answer != "" || state != "pending" {
		t.Errorf("hold answer %q state %q, want untouched", answer, state)
	}
}

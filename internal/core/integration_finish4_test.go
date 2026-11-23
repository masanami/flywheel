package core

import (
	"context"
	"errors"
	"testing"
)

// 仕上げ 4 項目 1: 計画を作り直す回答は、通す中身（land_ref）の有無では断らず、最後に読んだ状態が running の実行があるときだけ断る。

func (f *igLandFixture) replanAnswer(t *testing.T) (*Challenge, error) {
	t.Helper()
	prev, err := f.s.PrepareAnswer(context.Background(), f.cid, "NEW-SCOPE")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.cid, ExpectedVersion: prev.Version, Answer: "NEW-SCOPE", Replan: true},
		verifiedAttestationForTest(t, f.cid))
	return c, err
}

// 取りやめになったタスクの実行の取り消し（作り直した計画の承認の後の状態の確認）を行い、取り消しの呼び出しの数を返す。
func (f *igLandFixture) cancelAfterReplan(t *testing.T) int {
	t.Helper()
	f.sql(t, `UPDATE task SET status = 'cancelled'`)
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	f.pollLand(t)
	if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) == "" {
		t.Fatal("the contract run of the cancelled task is still open")
	}
	return len(f.client.cancels)
}

// 保留の理由が land_refused（「入れてよい」を返した後に、同じ中身のレビュー待ちが戻った）の保留に、計画を作り直す回答ができる。
// 実行の記録の状態はレビュー待ちなので、取りやめの取り消しは委譲先の中止を呼ぶ。
func TestFinish4_ReplanAnswerIsAcceptedForALandRefusedHold(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.commits[landContent] = igPassingChecks
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if got := f.lastHoldReason(t); got != string(holdReasonLandRefused) {
		t.Fatalf("precondition: reason = %q", got)
	}
	if f.ref(t, "land_ref") == "" {
		t.Fatal("precondition: the land content must remain")
	}
	c, err := f.replanAnswer(t)
	if err != nil || c.Status != StatusClassified {
		t.Fatalf("replan answer: %+v, err %v, want classified", c, err)
	}
	if n := f.cancelAfterReplan(t); n != 1 {
		t.Fatalf("cancels = %d, want 1 (the recorded state is waiting)", n)
	}
}

// 「入れてよい」を返し済みの実行が失敗で終わり、別の保留中のため開けたまま残した行がある課題の保留にも、計画を作り直す回答ができる。
// 実行の記録の状態は終わりなので、取りやめの取り消しは委譲先の中止を呼ばず終わった記録にする。
func TestFinish4_ReplanAnswerIsAcceptedForAnOpenStoppedRunAfterLand(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.sql(t, `INSERT INTO hold (challenge_id, question, reason, from_status, raised_at, task_id) VALUES (?, 'q', 'checks_failed', 'in_progress', '2026-11-20T00:00:00.000Z',
		(SELECT task_id FROM contract_run))`, f.intID(t))
	f.sql(t, `UPDATE challenge SET status = 'awaiting_human', version = version + 1`)
	f.checks.err = errors.New("boom")
	f.read(t, contractOut(ContractStateFailed, nil, func(st *ContractStatus) { st.Artifacts = landedArts }))
	if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) != "" || f.ref(t, "land_ref") == "" {
		t.Fatal("precondition: an open stopped run with the land content")
	}
	c, err := f.replanAnswer(t)
	if err != nil || c.Status != StatusClassified {
		t.Fatalf("replan answer: %+v, err %v, want classified", c, err)
	}
	if n := f.cancelAfterReplan(t); n != 0 {
		t.Fatalf("cancels = %d, want 0 (the recorded state is an end)", n)
	}
}

// 最後に読んだ状態が running の実行（「入れてよい」を返して委譲先が受け取ったもの）は、作業している実行として、引き続き断る。
func TestFinish4_ReplanAnswerIsRefusedWhileARunToldToLandIsRunning(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.heldWithTaskRun(t, "in_progress")
	f.sql(t, `UPDATE contract_run SET land_ref = ?, land_delivered = 1`, landContent)
	if _, err := f.replanAnswer(t); !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
}

// 「入れてよい」を返そうとして、届いたか分からない実行（通す中身があり、届いた記録が無く、最後に読んだ状態が終わりでない。resume が失敗した等）は、
// 委譲先が入れている最中かもしれないので断る。終わりを読んだ行と、届いた記録のあるレビュー待ち（land_refused の保留）は断らない（上の 2 試験）。
func TestFinish4_ReplanAnswerIsRefusedForARunWhoseLandDeliveryIsUnknown(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"waiting", "running"} {
		state := state
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newIgLandFixture(t)
			f.heldWithTaskRun(t, "in_progress")
			f.sql(t, `UPDATE contract_run SET state = ?, land_ref = ?, land_delivered = 0`, state, landContent)
			if _, err := f.replanAnswer(t); !errors.Is(err, ErrValidation) {
				t.Fatalf("err = %v, want ErrValidation", err)
			}
		})
	}
}

// 仕上げ 4 項目 2: 記録から作った読み（成果物に PR が無く、取り込み済みの成果物で補う）の成果物を、もう一度 landingArtifacts に通しても、
// 統合先の識別が二重に数えられない。
func TestFinish4_LandingArtifactsDoNotDoubleTheIntegrationRefsOfARecordedRead(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.succeed(t)
	f.sql(t, `DELETE FROM run_artifact WHERE kind = 'pr'`)
	ext := f.ref(t, "external_run_id")
	first, err := f.s.landingArtifacts(context.Background(), &ContractRunReport{ExternalRunID: ext}, f.intID(t))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.s.landingArtifacts(context.Background(), &ContractRunReport{ExternalRunID: ext, Artifacts: first}, f.intID(t))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := integrationArtifactRefs(first); len(b) != 1 {
		t.Fatalf("precondition: integration branches = %v, want 1", b)
	}
	_, c1 := integrationArtifactRefs(first)
	if b, c := integrationArtifactRefs(second); len(b) != 1 || len(c) != len(c1) || len(second) != len(first) {
		t.Fatalf("branches = %v, commits = %v, len %d -> %d, want the same artifacts as the first read", b, c, len(first), len(second))
	}
}

// 仕上げ 4 項目 3: 複数のリポジトリにまたがる課題は、検証中のタスクがある間は保留にならず（起動しないだけ）、無くなったときに保留になる。
func TestFinish4_MultiRepoChallengeIsHeldOnlyWhenNothingIsWorkingOrVerifying(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "repos2busy", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	cid, _ := parseChallengeID(id)
	if err := f.delegateFixture.exec(`UPDATE task SET repo = 'other-repo' WHERE challenge_id = ? AND key = 'b'`, cid); err != nil {
		t.Fatal(err)
	}
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusVerifying)
	f.runDelegationStage(t, nil)
	if len(f.client.starts) != 0 {
		t.Errorf("starts = %v, want none", f.client.starts)
	}
	if got := f.status(t, id); got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress while a task is verifying", got)
	}
	if got := f.holdReasons(t, id); len(got) != 0 {
		t.Fatalf("hold reasons = %v, want none while a task is verifying", got)
	}
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusDone)
	f.runDelegationStage(t, nil)
	if got := f.status(t, id); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human once nothing is working", got)
	}
	if got := f.holdReasons(t, id); len(got) != 1 || got[0] != "tasks_blocked" {
		t.Fatalf("hold reasons = %v, want [tasks_blocked]", got)
	}
}

// 仕上げ 4 項目 4: 開いたまま（ended_at が NULL）の停止の行は、保留にし損ねた停止の掃引の対象にしない（状態の確認が終わった記録にした後で拾う）。
func TestFinish4_SweepSkipsAnOpenStoppedRunAndTakesItOnceTheStatusCheckEndedIt(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.heldWithTaskRun(t, "in_progress")
	f.sql(t, `UPDATE challenge SET status = 'in_progress', version = version + 1`)
	// 着手中へ戻った課題に開いている保留は無い（本番では回答が閉じる）。ended_at より後に閉じるので、停止は保留の間に起きたままになる。
	f.sql(t, `UPDATE hold SET closed_at = '2026-11-23T00:00:00.000Z' WHERE answer IS NULL AND closed_at IS NULL`)
	f.sql(t, `UPDATE contract_run SET state = 'failed', ended_at = NULL, land_ref = NULL`)
	if n, err := f.s.HoldUnrecordedContractStops(context.Background()); err != nil || n != 0 {
		t.Fatalf("sweep held %d (%v) for an open stopped run, want 0", n, err)
	}
	f.sql(t, `UPDATE contract_run SET ended_at = '2026-11-21T00:00:00.000Z'`)
	if n, err := f.s.HoldUnrecordedContractStops(context.Background()); err != nil || n != 1 {
		t.Fatalf("sweep held %d (%v) for an ended stopped run, want 1", n, err)
	}
}

package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// このファイルは、統合先を使う課題のタスクの実行の成果物つきのレビュー待ち（区切り S3b）を、実ストア・偽の ContractClient で固定する。

const (
	integBranch  = "feat/integration-1"
	integBranch2 = "feat/integration-2"
)

var (
	integBranchArt  = ContractArtifact{Kind: "branch", Ref: integBranch, Role: "integration"}
	integCommitArt  = ContractArtifact{Kind: "commit", Ref: "aaaaaaaa", Role: "integration"}
	integArtsOK     = []ContractArtifact{reviewContentArtifact, integBranchArt, integCommitArt}
	integOptsWithLd = intakeOptsWithLand
)

func newIntegrationFixture(t *testing.T) *reviewFlowFixture {
	t.Helper()
	f := newIntakeFixture(t, reviewConnectorsJSON)
	f.sql(t, `UPDATE challenge SET uses_integration = 1`)
	return f
}

func (f *reviewFlowFixture) sql(t *testing.T, q string, args ...any) {
	t.Helper()
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *reviewFlowFixture) text(t *testing.T, q string) string {
	t.Helper()
	var v sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q).Scan(&v) }); err != nil {
		t.Fatal(err)
	}
	return v.String
}

func (f *reviewFlowFixture) assertIntegrationHold(t *testing.T) {
	t.Helper()
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s", got)
	}
	if got := f.lastHoldReason(t); got != string(holdReasonIntegrationRefInvalid) {
		t.Fatalf("reason = %q, question = %q", got, f.lastHoldQuestion(t))
	}
	wantLastHoldStop(t, f.s, stopPendingIntake) // AC-12: 統合先の識別が合わない保留は intake_unverified
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 0 {
		t.Fatalf("verifying tasks = %d", n)
	}
	if strings.Contains(f.lastHoldQuestion(t), string(holdReasonIntegrationRefInvalid)) {
		t.Fatalf("AC-91 question shows the internal value: %q", f.lastHoldQuestion(t))
	}
}

func TestIntegrationReview_DefaultBranchPRHolds(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	before := f.artifactRows(t)
	f.readOnce(t, intakeOut(integOptsWithLd, append([]ContractArtifact{{Kind: "pr", Ref: reviewPRURL}}, integArtsOK...), nil))
	f.assertIntegrationHold(t)                   // AC-77・AC-78・AC-91
	if got := f.artifactRows(t); got != before { // AC-88
		t.Fatalf("artifact rows %d -> %d", before, got)
	}
	if !strings.Contains(f.lastHoldQuestion(t), "1 個") {
		t.Fatalf("question = %q", f.lastHoldQuestion(t))
	}
}

func TestIntegrationReview_IdentityProblemsHold(t *testing.T) {
	t.Parallel()
	cases := map[string][]ContractArtifact{
		"AC-79 no branch":    {reviewContentArtifact, integCommitArt},
		"AC-80 two branches": {reviewContentArtifact, integBranchArt, {Kind: "branch", Ref: integBranch2, Role: "integration"}, integCommitArt},
		"AC-81 no commit":    {reviewContentArtifact, integBranchArt},
		"two commits":        {reviewContentArtifact, integBranchArt, integCommitArt, {Kind: "commit", Ref: "dddddddd", Role: "integration"}},
	}
	for name, arts := range cases {
		arts := arts
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationFixture(t)
			before := f.artifactRows(t)
			f.readOnce(t, intakeOut(integOptsWithLd, arts, nil))
			f.assertIntegrationHold(t)
			if got := f.artifactRows(t); got != before {
				t.Fatalf("AC-88 artifact rows %d -> %d", before, got)
			}
		})
	}
}

func TestIntegrationReview_BranchDifferentFromRecordedHolds(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.sql(t, `UPDATE challenge SET integration_ref = ?`, integBranch2)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	f.assertIntegrationHold(t) // AC-82
	q := f.lastHoldQuestion(t)
	if !strings.Contains(q, integBranch) || !strings.Contains(q, integBranch2) {
		t.Fatalf("question lacks both values: %q", q)
	}
}

func TestIntegrationReview_PassRecordsRefVerifiesTaskOnly(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	before := f.artifactRows(t)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	if got := f.text(t, `SELECT integration_ref FROM challenge`); got != integBranch { // AC-83
		t.Fatalf("integration_ref = %q", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 1 { // AC-84
		t.Fatalf("verifying tasks = %d", n)
	}
	if got := f.status(t, f.cid); got != StatusInProgress { // AC-85
		t.Fatalf("status = %s", got)
	}
	if n := f.openOperations(t); n != 0 { // AC-86
		t.Fatalf("operations = %d", n)
	}
	if got := f.artifactRows(t) - before; got != 3 { // AC-87
		t.Fatalf("artifact rows added = %d", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run_artifact WHERE role = 'integration'`); n != 2 {
		t.Fatalf("integration-role artifacts = %d", n)
	}
}

func TestIntegrationReview_MatchingRecordedRefPasses(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.sql(t, `UPDATE challenge SET integration_ref = ?`, integBranch)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 1 {
		t.Fatalf("verifying tasks = %d", n)
	}
}

// AC-89: options に land が無いレビュー待ちは、統合先を使う課題のタスクでも land_unavailable。
func TestIntegrationReview_WithoutLandIsLandUnavailable(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.readOnce(t, intakeOut([]string{"respond"}, integArtsOK, nil))
	if got := f.lastHoldReason(t); got != string(holdReasonLandUnavailable) {
		t.Fatalf("reason = %q", got)
	}
}

// AC-90: required_facts を満たさないレビュー待ちには、統合先を使う課題のタスクでも respond が返る。
func TestIntegrationReview_UnmetFactsResponds(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, requiredSelfReviewJSON)
	f.sql(t, `UPDATE challenge SET uses_integration = 1`)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, map[string]string{"self_review": "incomplete", "gate": "pass"}))
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d", len(f.client.resumes))
	}
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 0 {
		t.Fatalf("verifying tasks = %d", n)
	}
}

// AC-92・AC-93: 検証した中身を持つタスクの実行の、中身の変わったレビュー待ちは、先に確かめと取り込みを行う。
func TestIntegrationReview_ChangedContentAfterVerifiedIsImported(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	f.sql(t, `UPDATE contract_run SET verified_ref = 'deadbeef'`)
	before := f.artifactRows(t)
	changed := []ContractArtifact{{Kind: "commit", Ref: "bbbbbbbb"}, integBranchArt, {Kind: "commit", Ref: "cccccccc", Role: "integration"}}
	f.readOnce(t, intakeOut(integOptsWithLd, changed, nil))
	if got := f.artifactRows(t) - before; got != 3 { // AC-92
		t.Fatalf("artifact rows added = %d", got)
	}
}

func TestIntegrationReview_ChangedContentWithoutIntegrationIdentityHolds(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	f.sql(t, `UPDATE contract_run SET verified_ref = 'deadbeef'`)
	before := f.artifactRows(t)
	f.readOnce(t, intakeOut(integOptsWithLd, []ContractArtifact{{Kind: "commit", Ref: "bbbbbbbb"}}, nil))
	// AC-93（タスクは前の読みで検証中になっている）
	if got := f.lastHoldReason(t); got != string(holdReasonIntegrationRefInvalid) {
		t.Fatalf("reason = %q", got)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s", got)
	}
	if got := f.artifactRows(t); got != before {
		t.Fatalf("artifact rows %d -> %d", before, got)
	}
}

func TestIntegrationReview_UnchangedReadDoesNotImportAgain(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	before := f.artifactRows(t)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	if got := f.artifactRows(t); got != before {
		t.Fatalf("artifact rows %d -> %d", before, got)
	}
}

// AC-94〜AC-96: レビュー待ちを経ない成功は保留にして、課題を検証中にしない。
func TestIntegrationSucceededWithoutReviewHolds(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		merged bool
		reason holdReason
	}{
		"AC-94 not merged": {false, holdReasonDelegateEnded},
		"AC-95 merged":     {true, holdReasonMergedUnverified},
	} {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationFixture(t)
			f.sql(t, `UPDATE challenge SET integration_ref = ?`, integBranch)
			if tc.merged {
				f.setPR(3, "https://github.com/o/r/pull/3", "merged", integBranch)
			} else {
				f.setPR(3, "https://github.com/o/r/pull/3", "open", integBranch)
			}
			arts := []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/3"}, integBranchArt, integCommitArt}
			f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) { st.Artifacts = arts })}
			f.poll(t)
			if got := f.lastHoldReason(t); got != string(tc.reason) {
				t.Fatalf("reason = %q", got)
			}
			// AC-10・AC-11・AC-36: 成果物が統合先へ入らないまま終わった・検証を経ずに入っていた保留は intake_unverified
			wantLastHoldStop(t, f.s, stopPendingIntake)
			if got := f.status(t, f.cid); got == StatusVerifying { // AC-96
				t.Fatalf("status = %s", got)
			}
			if f.count(t, `SELECT COUNT(*) FROM hold WHERE task_id IS NOT NULL`) != 1 {
				t.Fatal("the hold is not tied to the task")
			}
		})
	}
}

func TestSubmitHeldContractSuccesses_SkipsIntegrationChallenges(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.sql(t, `UPDATE contract_run SET held_success = 1`)
	n, err := f.s.SubmitHeldContractSuccesses(context.Background(), nil)
	if err != nil || n != 0 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s", got)
	}
}

// AC-224: 取りやめのタスクの契約の実行のレビュー待ちは受け取らない。
func TestIntegrationReview_CancelledTaskIsNotReceived(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	before := f.artifactRows(t)
	f.sql(t, `UPDATE task SET status = 'cancelled'`)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'cancelled'`); n != 1 {
		t.Fatalf("cancelled tasks = %d", n)
	}
	if got := f.artifactRows(t); got != before {
		t.Fatalf("artifact rows %d -> %d", before, got)
	}
}

// AC-225: 統合先を使わない課題の動きは S3a のまま（反映の操作を作り、課題とタスクが検証中になる）。
func TestIntegrationReview_NonIntegrationChallengeKeepsS3aBehavior(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, reviewConnectorsJSON)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, nil))
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 1 {
		t.Fatalf("verifying tasks = %d", n)
	}
	if n := f.openOperations(t); n != 1 {
		t.Fatalf("operations = %d", n)
	}
}

// AC-15・AC-29: 保留中の課題では、レビュー待ちを読んでもタスクを検証中にしない。保留が解けた後の状態の確認で検証中にする。
func TestIntegrationReview_HeldChallengeDoesNotVerifyTaskUntilResolved(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.sql(t, `UPDATE challenge SET status = 'awaiting_human'`)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 0 {
		t.Fatalf("AC-15 verifying tasks = %d", n)
	}
	f.sql(t, `UPDATE challenge SET status = 'in_progress'`)
	f.readOnce(t, intakeOut(integOptsWithLd, integArtsOK, nil))
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 1 {
		t.Fatalf("AC-29 verifying tasks = %d", n)
	}
}

// 取りやめのタスクのレビュー待ちは、統合先を使わない課題でも受け取らない（課題は着手中のまま・操作も保留も作らない）。
func TestReceiveReview_CancelledTaskIsNotReceivedForNonIntegration(t *testing.T) {
	t.Parallel()
	f := newIntakeFixture(t, reviewConnectorsJSON)
	f.sql(t, `UPDATE task SET status = 'cancelled'`)
	f.readOnce(t, intakeOut(intakeOptsWithLand, intakeArtsOK, nil))
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s", got)
	}
	if n := f.openOperations(t); n != 0 {
		t.Fatalf("operations = %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'cancelled'`); n != 1 {
		t.Fatalf("cancelled tasks = %d", n)
	}
}

// 統合先を使う課題が保留の間に succeeded を読んでも、実行中と数え続ける印（held_success）を残さない。
func TestIntegrationSucceededWhileHeldLeavesNoHeldSuccessMark(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.sql(t, `UPDATE challenge SET status = 'awaiting_human'`)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) { st.Artifacts = integArtsOK })}
	f.poll(t)
	if n := f.count(t, `SELECT COUNT(*) FROM contract_run WHERE held_success = 1`); n != 0 {
		t.Fatalf("held_success marks = %d", n)
	}
}

// 取りやめのタスクの実行が succeeded になっても、保留にしない。
func TestIntegrationSucceededForCancelledTaskDoesNotHold(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.sql(t, `UPDATE task SET status = 'cancelled'`)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) { st.Artifacts = integArtsOK })}
	f.poll(t)
	if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != 0 {
		t.Fatalf("holds = %d", n)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s", got)
	}
}

// 統合先を特定できないとき、マージ済みの PR があっても merged_unverified にしない（delegate_ended。問いは断定しない）。
func TestIntegrationSucceededWithoutIdentifiableBranchIsDelegateEnded(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.setPR(3, "https://github.com/o/r/pull/3", "merged", "main")
	arts := []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/3"}}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) { st.Artifacts = arts })}
	f.poll(t)
	if got := f.lastHoldReason(t); got != string(holdReasonDelegateEnded) {
		t.Fatalf("reason = %q", got)
	}
	if strings.Contains(f.lastHoldQuestion(t), string(holdReasonDelegateEnded)) { // AC-237
		t.Fatalf("question names the reason value: %q", f.lastHoldQuestion(t))
	}
	if strings.Contains(f.lastHoldQuestion(t), "成果物は統合先へ入っていない。検証") {
		t.Fatalf("question asserts what was not checked: %q", f.lastHoldQuestion(t))
	}
}

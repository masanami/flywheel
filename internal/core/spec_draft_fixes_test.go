package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"
)

// このファイルは「一緒に直す点」（開始のコマンドの最中の取り下げ・済んだ記録の課題から仕様の文書の場所を外したとき）を
// 確かめる。テストの注記の AC の番号は、Issue #1380 の受け持ち（親 #1374 の受入基準の並び順）。

// addSpecRunWithoutResult は、結果の無い予算の枠 spec の実行の記録を置き、その内部整数 ID を返す。
func (f *seFixture) addSpecRunWithoutResult(t *testing.T, pid int64, heartbeat time.Time) int64 {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	n := f.seRun(t)
	f.insertRow(t, `UPDATE run SET result = NULL, ended_at = NULL, pid = ?, host = ?, heartbeat_at = ? WHERE id = ?`,
		pid, host, formatTimestamp(heartbeat), n)
	return n
}

// 開始のコマンドを呼んでいる最中の取り下げは、要約を読む ① でも、確認の後の書き込み ② でも conflict で終わる。
func TestSpecDraftWithdraw_DuringTheStartCommandIsAConflict(t *testing.T) { // AC-385・AC-386・AC-387
	t.Parallel()
	f := newSDSFixture(t)
	id := f.record(t, "starting", SpecDraft{})
	cid, _ := parseChallengeID(id)
	draft := specDraftPathFor(cid)
	// 開始の前に ① を済ませる（確認の入力を待つ間に開始が始まった形）。
	preview, err := f.s.PrepareWithdraw(context.Background(), id, "やめる")
	if err != nil {
		t.Fatal(err)
	}
	var (
		prepareErr, executeErr error
		before, after          *ChallengeDetail
		draftKept              bool
		called                 bool
	)
	f.client.onStart = func(ContractLaunchInput) {
		called = true
		writeWorkspaceFile(t, f.s.workspace, draft, "# 下書き\n")
		before = f.detail(t, id)
		_, prepareErr = f.s.PrepareWithdraw(context.Background(), id, "やめる")
		_, executeErr = f.s.ExecuteWithdraw(context.Background(), WithdrawRequest{
			ChallengeID: id, ExpectedVersion: preview.Version, Reason: "やめる",
		}, verifiedAttestationForTest(t, id))
		after = f.detail(t, id)
		draftKept = scExists(t, f.s.workspace+"/"+draft)
	}
	f.delegate(t, 100)

	if !called {
		t.Fatal("the start command was not called")
	}
	for name, err := range map[string]error{"prepare": prepareErr, "execute": executeErr} {
		if !errors.Is(err, ErrConflict) {
			t.Errorf("%s during start = %v, want ErrConflict", name, err)
		}
	}
	if after.Status != StatusClassified || after.Status != before.Status || after.Version != before.Version ||
		after.SpecDraft.State != before.SpecDraft.State {
		t.Errorf("challenge changed: %s v%d %s -> %s v%d %s", before.Status, before.Version, before.SpecDraft.State,
			after.Status, after.Version, after.SpecDraft.State)
	}
	if !draftKept {
		t.Error("the draft file must be kept")
	}
	scRequireExists(t, f.s, draft, true)
}

func TestSpecDraftWithdraw_AfterTheStartCommandReturnsCancelsTheRun(t *testing.T) { // AC-388
	t.Parallel()
	f := newSDSFixture(t)
	id := f.record(t, "started", SpecDraft{})
	cid, _ := parseChallengeID(id)
	draft := specDraftPathFor(cid)
	f.delegate(t, 100)
	f.requireRecord(t, id, SpecDraftStateRunning, 1)
	writeWorkspaceFile(t, f.s.workspace, draft, "# 下書き\n")
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)

	// CLI の取り下げと同じ順: ①、開いた契約の実行の取り消し、版の取り直し、②。
	if _, err := f.s.PrepareWithdraw(context.Background(), id, "やめる"); err != nil {
		t.Fatalf("PrepareWithdraw after start: %v", err)
	}
	n, err := f.s.CancelOpenContractRuns(context.Background(),
		ContractCancelInput{ChallengeID: id, AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client})
	if err != nil || n != 1 || len(f.client.cancels) != 1 {
		t.Fatalf("CancelOpenContractRuns = %d, %v, cancels = %v", n, err, f.client.cancels)
	}
	scWithdraw(t, f.s, id)
	scRequireExists(t, f.s, draft, false)
}

func TestSpecDraftWithdraw_StaleStartRunIsReapedAndWithdraws(t *testing.T) { // AC-389・AC-390
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateRunning)
	n := f.addSpecRunWithoutResult(t, deadPID(t), f.s.currentTime().Add(-defaultStaleAfter-time.Minute))
	scWithdraw(t, f.s, f.id)
	if result := f.runResult(t, n); result != string(RunResultInterrupted) {
		t.Errorf("the stale run result = %q, want interrupted", result)
	}
	scRequireExists(t, f.s, f.draft, false)
}

func TestSpecDraftWithdraw_FreshHeartbeatRunIsAConflict(t *testing.T) { // AC-391
	t.Parallel()
	f := newSEFixture(t, SpecDraftStateRunning)
	// プロセスが生きていなくても、心拍が新しければ最中とみなす。
	n := f.addSpecRunWithoutResult(t, deadPID(t), f.s.currentTime())
	before := f.show(t)
	if _, err := withdrawForTest(t, f.s, f.id, "やめる"); !errors.Is(err, ErrConflict) {
		t.Fatalf("withdraw = %v, want ErrConflict", err)
	}
	if after := f.show(t); after.Status != before.Status || after.Version != before.Version {
		t.Errorf("challenge changed: %s v%d -> %s v%d", before.Status, before.Version, after.Status, after.Version)
	}
	if result := f.runResult(t, n); result != "" {
		t.Error("the fresh run must stay open")
	}
	scRequireExists(t, f.s, f.draft, true)
}

// runResult は実行の記録の結果を返す（結果が無ければ空）。
func (f *seFixture) runResult(t *testing.T, n int64) string {
	t.Helper()
	var result string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT COALESCE(result, '') FROM run WHERE id = ?`, n).Scan(&result)
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

// newDoneSpecFixture は、記録が済みで、仕様の文書の場所が記録の下書きを指す分類済の課題を作る。
func newDoneSpecFixture(t *testing.T) *seFixture {
	t.Helper()
	f := newSEFixture(t, SpecDraftStateDone)
	sdSetSpec(t, f.s, f.id, f.draft, "")
	return f
}

func TestSpecDraftEdit_ClearingTheSpecOfADoneRecordRemovesTheDraft(t *testing.T) { // AC-392・AC-393・AC-394
	t.Parallel()
	f := newDoneSpecFixture(t)
	f.mustEditSpec(t, "")
	scRequireExists(t, f.s, f.draft, false)
	f.requireState(t, SpecDraftStateDone)
	if d := f.show(t); d.SpecPath != nil {
		t.Errorf("spec_path = %v, want null", *d.SpecPath)
	}
	if n := len(scFailures(t, f.s, f.id)); n != 0 {
		t.Errorf("cleanup failures = %d", n)
	}
}

func TestSpecDraftEdit_ClearingKeepsADraftAnotherChallengeUses(t *testing.T) { // AC-395
	t.Parallel()
	f := newDoneSpecFixture(t)
	other := createAndAdvance(t, f.s, StatusClassified)
	sdSetSpec(t, f.s, other.ID, f.draft, "")
	f.mustEditSpec(t, "")
	scRequireExists(t, f.s, f.draft, true)
	f.requireState(t, SpecDraftStateDone)
	if d := f.show(t); d.SpecPath != nil {
		t.Errorf("spec_path = %v, want null", *d.SpecPath)
	}
}

func TestSpecDraftEdit_ClearingWithoutARecordKeepsTheFile(t *testing.T) { // AC-396
	t.Parallel()
	f := newSEFixture(t, "")
	writeWorkspaceFile(t, f.s.workspace, f.draft, "# 下書き\n")
	sdSetSpec(t, f.s, f.id, f.draft, "")
	f.mustEditSpec(t, "")
	scRequireExists(t, f.s, f.draft, true)
	if d := f.show(t); d.SpecPath != nil {
		t.Errorf("spec_path = %v, want null", *d.SpecPath)
	}
	if n := len(scFailures(t, f.s, f.id)); n != 0 {
		t.Errorf("cleanup failures = %d", n)
	}
}

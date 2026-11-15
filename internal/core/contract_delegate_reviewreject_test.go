package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// 人が完了の承認を差し戻した（T15）後は、その理由が同じ契約の run へ resume（respond）で 1 回だけ届き、
// 次のレビュー待ちで再び検証中へ進む。
func TestPollContractRuns_HumanCompletionRejectionIsReturnedByResumeOnceAndTheNextReviewWaitVerifiesAgain(t *testing.T) {
	f := newReviewFlowFixture(t)
	f.toVerifying(t)
	time.Sleep(10 * time.Millisecond)
	n, _ := parseChallengeID(f.cid)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, reason, decided_at)
			VALUES (?, 'completion', 'rejected', 1, 'alice', 'cli', 'tty_confirm', 'HUMAN-REASON', ?)`, n, time.Now().UTC().Format(timestampLayout)); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE challenge SET status = ? WHERE id = ?`, string(StatusInProgress), n)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// 差し戻しを受けた接続ツールが同じレビュー待ちをすぐ返しても、契約の run は waiting・review のまま残る。
	f.client.resumeOut = reviewWaitOut()
	f.poll(t)
	if len(f.client.resumes) != 1 || len(f.client.resumes[0]) != 7 || f.client.resumes[0][3] != "--choice" || f.client.resumes[0][4] != "respond" {
		t.Fatalf("resumes = %v, want one respond", f.client.resumes)
	}
	if got := f.client.resumes[0][6]; !strings.Contains(got, "HUMAN-REASON") {
		t.Fatalf("note = %q, want the human reason", got)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress（同じレビュー待ちは取り込み直さない）", got)
	}
	// 二重に返さない（次の周は pendingReviewReply に入り、新しい差し戻しが無いので状態を読むだけ）。
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.poll(t)
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1", len(f.client.resumes))
	}
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.poll(t)
	f.client.queue = []ContractLaunchOutput{reviewWaitOut()}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
}

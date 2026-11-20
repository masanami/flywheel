package core

// このファイルは、統合先を使う課題の反映の段（区切り S3b の受入基準 AC-165・AC-188〜AC-191）を、実ストア・偽の契約のコマンド・偽の PR の取得で検証する。

import (
	"database/sql"
	"reflect"
	"testing"
)

const reflIntegRef = "feat/integration-1"

func (f *reflFixture) useIntegration(t *testing.T) {
	t.Helper()
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET uses_integration = 1, integration_ref = ? WHERE id = ?`, reflIntegRef, f.cid)
		return err
	})
}

// AC-190: 統合先を使う、完了の課題で、承認済みの反映の操作があり、終わっていない契約の実行が無いとき、反映の段が呼ぶ commands.reflect の
// {artifact_ref} は、challenge.integration_ref である（反映の操作の参照ではない）。
func TestReflectStage_AC190_IntegrationChallengeUsesTheIntegrationRefAsArtifactRef(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.useIntegration(t)
	f.started(t)
	want := []string{"tool", "reflect", "--artifact", reflIntegRef, "--challenge", formatChallengeID(f.cid)}
	if len(f.client.starts) != 1 || !reflect.DeepEqual(f.client.starts[0], want) {
		t.Fatalf("starts = %v, want %v", f.client.starts, want)
	}
}

// AC-191: 統合先を使わない課題では、そのときの {artifact_ref} は、S3a と同じに、反映の操作の参照である。
func TestReflectStage_AC191_ChallengeWithoutIntegrationUsesTheReleaseRef(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	if got := argAfter(t, f.client.starts[0], "--artifact"); got != relRef {
		t.Fatalf("{artifact_ref} = %q, want the release ref %q", got, relRef)
	}
}

// AC-188・AC-189: 完了と反映の承認の後、反映の段は、上げる依頼へ commands.resume を land で呼び（{approved_ref} は承認した中身）、
// 上げる依頼の succeeded を読み、成果物が反映先へ入っていれば、反映の操作は実行済み（結果 landed）になる。
func TestReflectStage_AC188_AC189_AnIntegrationChallengeLandsTheUpRequest(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.useIntegration(t)
	f.started(t)
	f.queue(reviewFor(relHead))
	f.one(t)
	want := []string{"tool", "resume", "ext-1", "--choice", "land", "--note", "", "--approved-ref", relHead}
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0], want) { // AC-188
		t.Fatalf("resumes = %v, want %v", f.client.resumes, want)
	}
	f.merged()
	f.queue(contractOut(ContractStateSucceeded, nil, nil))
	if item := f.one(t); item.Result != ExecutionResultLanded { // AC-189
		t.Fatalf("item = %+v, want landed", item)
	}
	f.wantOpState(t, "executed")
}

// AC-165: 完了でない課題の上げる依頼は、反映の段では読まれない。
func TestReflectStage_AC165_AnUpRequestOfAnUndoneChallengeIsNotReadByTheReleaseStage(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.useIntegration(t)
	f.started(t)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = 'verifying' WHERE id = ?`, f.cid)
		return err
	})
	f.exec(t)
	if len(f.client.statuses) != 0 || len(f.client.resumes) != 0 || len(f.client.cancels) != 0 {
		t.Fatalf("statuses %v resumes %v cancels %v, want the release stage to leave the up request to the delegation stage",
			f.client.statuses, f.client.resumes, f.client.cancels)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %v, want the request is not started again", f.client.starts)
	}
}

// AC-164・AC-165: 同じ上げる依頼（外部の実行 ext-1）について、課題が完了でない間は、委譲の段の状態の確認が commands.status を呼び、
// 反映の段は呼ばない。課題が完了なら（AC-126）反対に、反映の段が読み、委譲の段は読まない。
func TestReflectStage_AC164_AC165_TheSameRequestIsReadByTheDelegationStageOnlyWhileTheChallengeIsUndone(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.useIntegration(t)
	f.started(t)
	// 完了の課題: 委譲の段は読まない。
	f.poll(t)
	if len(f.client.statuses) != 0 {
		t.Fatalf("done challenge: statuses = %v, want none from the delegation stage", f.client.statuses)
	}
	for _, st := range []string{"in_progress", "verifying"} {
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE challenge SET status = ? WHERE id = ?`, st, f.cid)
			return err
		})
		before := len(f.client.statuses)
		f.exec(t) // 反映の段
		if got := len(f.client.statuses); got != before || len(f.client.resumes) != 0 || len(f.client.cancels) != 0 {
			t.Fatalf("%s: the release stage called status/resume/cancel: statuses %v resumes %v cancels %v", st, f.client.statuses, f.client.resumes, f.client.cancels)
		}
		f.poll(t)                                                                                      // 委譲の段
		if got := len(f.client.statuses); got != before+1 || f.client.statuses[before][2] != "ext-1" { // AC-164
			t.Fatalf("%s: statuses = %v, want one more commands.status of ext-1 from the delegation stage", st, f.client.statuses)
		}
	}
}

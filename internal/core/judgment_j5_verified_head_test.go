package core

import (
	"context"
	"database/sql"
	"testing"
)

// J5 の met を課題へ写すとき、起動の前に取得して入力にした head を、直前の委譲の run の
// kind=pr の成果物の行へ T8 と同じトランザクションで残す（M45H11。AC-23〜AC-25）。

const verifiedHeadQuery = `SELECT COALESCE(verified_head_sha, 'NULL') FROM run_artifact WHERE kind = 'pr'`

func (f *j5Fixture) setPRHead(head string) {
	pr := prChecks("open", checkDone)
	pr.HeadSHA = head
	f.checks.prs["o/r#1"] = pr
}

func TestVerifyAutoJ5_Met_RecordsTheHeadFetchedBeforeJ5(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.setPRHead("head-before-j5")
	// J5 の起動の最中に PR の head が進んでも、記録するのは起動の前に取得して入力にした値。
	f.j5.onInvoke = func(JudgmentLaunchInput) { f.setPRHead("head-after-j5") }
	if got := f.queryString(t, verifiedHeadQuery); got != "NULL" {
		t.Fatalf("precondition: verified_head_sha = %q, want NULL", got)
	}
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s", got)
	}
	if got := f.queryString(t, verifiedHeadQuery); got != "head-before-j5" {
		t.Errorf("verified_head_sha = %q, want head-before-j5", got)
	}
}

func TestVerifyAutoJ5_NotMetAndUncertain_DoNotRecordTheHead(t *testing.T) {
	cases := map[string]JudgmentLaunchOutput{
		"not_met":   j5Out("not_met", "fix", nil),
		"uncertain": j5Out("uncertain", nil, "q?"),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			f := newJ5Fixture(t, "impl")
			f.delegateToVerifying(t, "C-1")
			f.setPRHead("head-x")
			f.j5.result = out
			if _, err := f.verify(t, nil); err != nil {
				t.Fatal(err)
			}
			if got := f.queryString(t, verifiedHeadQuery); got != "NULL" {
				t.Errorf("verified_head_sha = %q, want NULL", got)
			}
		})
	}
}

// 実物の個数の不一致で保留に読み替えたときは書かない。
func TestVerifyAutoJ5_MetTurnedIntoAHoldDoesNotRecordTheHead(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.setPRHead("head-x")
	f.setExpected(t, "C-1", 2)
	f.j5.result = j5OutWithActual("met", actualItems(1))
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	if got := f.queryString(t, verifiedHeadQuery); got != "NULL" {
		t.Errorf("verified_head_sha = %q, want NULL", got)
	}
}

// 書くのは、J5 が参照した直前の委譲の run の kind=pr の成果物の行だけ。
func TestVerifyAutoJ5_MetRecordsHeadOnlyOnTheReferencedDelegationRun(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.setPRHead("head-y")
	// 別の run（別の課題の委譲）にも同じ PR の成果物の行を置く。この行は NULL のまま残る。
	other := f.insertOtherRunWithPRArtifact(t, j5PRURL)
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.queryString(t, `SELECT COALESCE(verified_head_sha, 'NULL') FROM run_artifact WHERE id = ?`, other); got != "NULL" {
		t.Errorf("the other run's row verified_head_sha = %q, want NULL", got)
	}
	if got := f.queryString(t, `SELECT COUNT(*) FROM run_artifact WHERE verified_head_sha IS NOT NULL`); got != "1" {
		t.Errorf("rows with a verified head = %s, want 1", got)
	}
	if got := f.queryString(t, `SELECT COUNT(*) FROM run_artifact WHERE verified_head_sha = 'head-y' AND kind = 'pr' AND ref = ?`, j5PRURL); got != "1" {
		t.Errorf("the referenced PR row was not updated: %s", got)
	}
}

// insertOtherRunWithPRArtifact は別の課題の委譲の run を 1 つ足し、その run に ref の PR の成果物の行を置いて、行の ID を返す。
func (f *j5Fixture) insertOtherRunWithPRArtifact(t *testing.T, ref string) int64 {
	t.Helper()
	ctx := context.Background()
	ch, err := f.s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "other", Description: "d", DoneCriteria: "d"})
	if err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(ch.ID)
	now := f.s.currentTime()
	var id int64
	if err := f.s.db.Write(ctx, func(tx *sql.Tx) error {
		run, err := insertRun(ctx, tx, cid, insertRunInput{Kind: runKindDelegate, ChallengeVersion: 1,
			SessionID: "55555555-5555-4555-8555-555555555555", PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now,
			MaxBudgetUSD: 1_000_000, BudgetBucket: budgetBucketImpl})
		if err != nil {
			return err
		}
		runID, _ := parseRunID(run.ID)
		res, err := tx.Exec(`INSERT INTO run_artifact (run_id, kind, ref, state, base, verified_at) VALUES (?, 'pr', ?, 'open', 'develop', ?)`,
			runID, ref, "2026-10-02T00:00:00.000Z")
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

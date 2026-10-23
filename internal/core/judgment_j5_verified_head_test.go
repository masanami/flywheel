package core

import (
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
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.queryString(t, `SELECT COUNT(*) FROM run_artifact WHERE verified_head_sha IS NOT NULL`); got != "1" {
		t.Errorf("rows with a verified head = %s, want 1", got)
	}
	if got := f.queryString(t, `SELECT COUNT(*) FROM run_artifact WHERE verified_head_sha = 'head-y' AND kind = 'pr' AND ref = ?`, j5PRURL); got != "1" {
		t.Errorf("the referenced PR row was not updated: %s", got)
	}
}

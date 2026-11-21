package core

// 区切り S3c（衝突の解消の確かめ）の試験の補強（#962）。本物の経路を通す形で、次を固定する。
// 保留への遷移・保留からの戻り（SQL で状態を書き換えない）／反映の承認の記録そのもの／中身の比較の口が失敗を返すとき。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// holdByTheRealPath は、課題を本物の経路（HoldChallenge）で保留にする（SQL で状態だけを書き換えると、保留の記録が無いまま状態だけが動く）。
func (f *reviewFlowFixture) holdByTheRealPath(t *testing.T) {
	t.Helper()
	fixedStoreActor(f.s, "alice")
	got, err := f.s.HoldChallenge(context.Background(), ChannelCLI, f.cid, HoldInput{Question: "why?"})
	if err != nil {
		t.Errorf("HoldChallenge: %v", err)
		return
	}
	if got.Status != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human", got.Status)
	}
}

// answerByTheRealPath は、保留中の課題への回答を本物の経路（PrepareAnswer・ExecuteAnswer）で記録し、保留に入る直前の状態へ戻す。
func (f *reviewFlowFixture) answerByTheRealPath(t *testing.T) {
	t.Helper()
	fixedStoreActor(f.s, "alice")
	prev, err := f.s.PrepareAnswer(context.Background(), f.cid, "because")
	if err != nil {
		t.Fatalf("PrepareAnswer: %v", err)
	}
	if _, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.cid, ExpectedVersion: prev.Version, Answer: "because"}, verifiedAttestationForTest(t, f.cid)); err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}
}

// 本物の経路で保留にすると、保留の記録（未回答の 1 件）が残り、課題の状態は保留へ動く。確かめの対象から外れ、回答で戻ると対象に戻る。
func TestJ5Fix_RealHoldLeavesAHoldRecordAndTheTargetComesBackAfterTheAnswer(t *testing.T) {
	t.Parallel()
	f := newFixIntegrationFixture(t)
	f.holdByTheRealPath(t)
	if n := f.count(t, `SELECT COUNT(*) FROM hold WHERE answer IS NULL`); n != 1 {
		t.Fatalf("open holds = %d, want 1", n)
	}
	if got := f.fixTargets(t); len(got) != 0 {
		t.Fatalf("fix targets = %+v, want none while held", got)
	}
	f.answerByTheRealPath(t)
	if f.status(t, f.cid) != StatusInProgress {
		t.Fatalf("challenge = %s, want in_progress after the answer", f.status(t, f.cid))
	}
	if got := f.fixTargets(t); len(got) != 1 {
		t.Fatalf("fix targets = %+v, want the target back after the answer", got)
	}
}

// approvalRecords は承認の記録そのもの（全行・全列。取り下げの列〔superseded_*〕を含む）を 1 つの文字列にして返す。操作の状態（approved）ではなく、承認の行を直接見る。
func (f *fixFixture) approvalRecords(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT * FROM approval ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for k := range vals {
				ptrs[k] = &vals[k]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			for k, c := range cols {
				fmt.Fprintf(&out, "%s=%v|", c, vals[k])
			}
			out.WriteString(";")
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// 反映先宛ての確かめは、どの結果でも、反映の承認の記録そのものを変えない（承認した中身 fixPassed の承認が 1 件、承認のまま残る）。
// 通した中身（fix_verified_ref）は別の列に書かれ、承認の行へは書かれない。
func TestReflectFixWriter_TheApprovalRecordItselfStaysUntouched(t *testing.T) {
	t.Parallel()
	for _, verdict := range []string{"met", "not_met", "uncertain"} {
		verdict := verdict
		t.Run(verdict, func(t *testing.T) {
			t.Parallel()
			f := newReflectWriterFixture(t)
			before := f.approvalRecords(t)
			if n := f.count(t, `SELECT COUNT(*) FROM approval WHERE kind = 'release' AND decision = 'approved' AND head_sha = '`+fixPassed+`'`); n != 1 || before == "" {
				t.Fatalf("approval records before = %q (live approvals of the passed content: %d), want exactly one", before, n)
			}
			f.setVerdict(verdict)
			f.verify(t)
			if got := f.approvalRecords(t); got != before {
				t.Fatalf("approval records changed:\n before = %s\n after  = %s", before, got)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM approval WHERE kind = 'release'`); n != 1 {
				t.Fatalf("release approvals = %d, want 1", n)
			}
		})
	}
}

// 中身の比較の口が失敗を返したとき（3 つの比較のどれでも・統合先宛てでも反映先宛てでも）、確かめは「満たした」を通さない:
// 通した記録（conflict_fix_passed）は残らず、通した中身は進まず、承認の記録も変わらない（比較は通した中身・宛先の最新・反映先の土台の 3 つ）。
func TestJ5Fix_AFailingComparisonNeverPassesTheContent(t *testing.T) {
	t.Parallel()
	for name, c := range truncFixCases(true) {
		base := integBranch
		if c.dest == j5FixDestReflect {
			base = "main"
		}
		for _, key := range [][2]string{{fixPassed, fixPending}, {"m1", fixPending}, {base, fixPending}} {
			c, key := c, key
			t.Run(name+"/"+key[0]+"→"+key[1], func(t *testing.T) {
				t.Parallel()
				f := c.mk(t)
				approvals := f.approvalRecords(t)
				f.cmp.errs[key] = errors.New("upstream compare failed")
				f.setVerdict("met")
				f.verify(t)
				if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'conflict_fix_passed'`); n != 0 {
					t.Fatalf("conflict_fix_passed = %d, want none", n)
				}
				if v := f.text(t, `SELECT IFNULL(fix_verified_ref, '') FROM contract_run`); v != "" {
					t.Fatalf("fix_verified_ref = %q, want it not advanced", v)
				}
				if c.dest == j5FixDestIntegration {
					if v := f.text(t, `SELECT IFNULL(verified_ref, '') FROM contract_run`); v != fixPassed {
						t.Fatalf("verified_ref = %q, want %q (not advanced to the pending content)", v, fixPassed)
					}
				}
				if got := f.approvalRecords(t); got != approvals {
					t.Fatalf("approval records changed: %s -> %s", approvals, got)
				}
			})
		}
	}
}

// 確かめ（J5）の入力のデータの区画の名前は「衝突の解消の差分」で、本文に直したファイルの差分が入る。この名前を固定するのはこの試験だけ。invoker の試験
// （TestLauncher_InvokeJudgment_J5FixCheckStdinCarriesTheDiffInsideItsDataSection）は、渡された区画の本文が実際の起動の標準入力の区画の内側に入ることを見る。
func TestJ5Fix_InputCarriesTheConflictFixDiffSection(t *testing.T) {
	t.Parallel()
	if labelFixDiffSection != "衝突の解消の差分" {
		t.Fatalf("labelFixDiffSection = %q", labelFixDiffSection)
	}
	f := newFixIntegrationFixture(t)
	f.setVerdict("met")
	f.verify(t)
	if d := f.sectionsOf(t)[labelFixDiffSection]; !strings.Contains(d, "a.go") || !strings.Contains(d, "+RESOLVED-LINE") {
		t.Fatalf("diff section = %q", d)
	}
}

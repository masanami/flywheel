package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは、課題が終わったときの仕様の下書きの削除（docs/features/request-to-plan.md の S2a）を確かめる。
// テストの名前の AC の番号は、Issue #1332 の受け持ち（親 #1329 の受入基準の並び順）。

// scChallenge は仕様の文書 spec（中身つき）を持つ、状態 status の課題を作る。
func scChallenge(t *testing.T, s *Store, status Status, spec string) string {
	t.Helper()
	c := createAndAdvance(t, s, status)
	writeWorkspaceFile(t, s.workspace, spec, "# 下書き\n")
	sdSetSpec(t, s, c.ID, spec, sdBase)
	return c.ID
}

func scStore(t *testing.T) *Store {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	return s
}

func scExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

func scRequireExists(t *testing.T, s *Store, spec string, want bool) {
	t.Helper()
	if got := scExists(t, filepath.Join(s.workspace, spec)); got != want {
		t.Errorf("draft %s exists = %v, want %v", spec, got, want)
	}
}

func scWithdraw(t *testing.T, s *Store, id string) {
	t.Helper()
	c, err := withdrawForTest(t, s, id, "やめる")
	if err != nil || c.Status != StatusWithdrawn {
		t.Fatalf("withdraw: %+v, %v", c, err)
	}
}

func scComplete(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := approvePlanForTest(t, s, id, ttyAtt(t, id)); err != nil {
		t.Fatalf("approve completion: %v", err)
	}
	d, err := s.GetChallenge(context.Background(), id)
	if err != nil || d.Status != StatusDone {
		t.Fatalf("detail = %+v, %v, want done", d, err)
	}
}

func scFailures(t *testing.T, s *Store, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, a := range activitiesFor(t, s, id) {
		if a.Action != "spec_draft_cleanup_failed" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(a.After, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestSpecDraftCleanup_Withdraw(t *testing.T) {
	t.Parallel()
	t.Run("AC-96・AC-109・AC-110 消して、記録は残す", func(t *testing.T) {
		t.Parallel()
		s := scStore(t)
		id := scChallenge(t, s, StatusClassified, sdPath)
		scWithdraw(t, s, id)
		scRequireExists(t, s, sdPath, false)
		d, err := s.GetChallenge(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if d.SpecPath == nil || *d.SpecPath != sdPath {
			t.Errorf("spec_path = %v", d.SpecPath)
		}
		if d.SpecBasePath == nil || *d.SpecBasePath != sdBase {
			t.Errorf("spec_base_path = %v", d.SpecBasePath)
		}
		if f := scFailures(t, s, id); len(f) != 0 {
			t.Errorf("failures = %v", f)
		}
	})
	t.Run("AC-104 ファイルがすでに無い", func(t *testing.T) {
		t.Parallel()
		s := scStore(t)
		id := scChallenge(t, s, StatusClassified, sdPath)
		if err := os.Remove(filepath.Join(s.workspace, sdPath)); err != nil {
			t.Fatal(err)
		}
		scWithdraw(t, s, id)
		if f := scFailures(t, s, id); len(f) != 0 {
			t.Errorf("failures = %v", f)
		}
	})
	t.Run("AC-111 下書きでない文書は消さない", func(t *testing.T) {
		t.Parallel()
		s := scStore(t)
		const spec = "docs/features/real.md"
		scWithdraw(t, s, scChallenge(t, s, StatusClassified, spec))
		scRequireExists(t, s, spec, true)
	})
}

func TestSpecDraftCleanup_SharedDraft(t *testing.T) { // AC-102・AC-103
	t.Parallel()
	for _, other := range []string{sdPath, "./.flywheel//specs/example.md"} {
		t.Run(other, func(t *testing.T) {
			t.Parallel()
			s := scStore(t)
			first := scChallenge(t, s, StatusClassified, sdPath)
			second := scChallenge(t, s, StatusAwaitingPlanApproval, other)
			scWithdraw(t, s, first)
			scRequireExists(t, s, sdPath, true)
			scWithdraw(t, s, second)
			scRequireExists(t, s, sdPath, false)
			if f := append(scFailures(t, s, first), scFailures(t, s, second)...); len(f) != 0 {
				t.Errorf("failures = %v", f)
			}
		})
	}
	t.Run("完了して反映を待つ課題が指すあいだは残す", func(t *testing.T) {
		t.Parallel()
		s := scStore(t)
		done := scChallenge(t, s, StatusDone, sdPath)
		cid, _ := parseChallengeID(done)
		coretest.InsertApprovedRelease(t, s.workspace, int(cid), relRef, relHead, execTS(0))
		scWithdraw(t, s, scChallenge(t, s, StatusClassified, sdPath))
		scRequireExists(t, s, sdPath, true)
	})
}

func TestSpecDraftCleanup_LinkOutsideTheDraftDir(t *testing.T) { // AC-105・AC-106
	t.Parallel()
	for _, tc := range []struct {
		name, spec, link, target string
	}{
		{"途中のリンク", ".flywheel/specs/link/example.md", ".flywheel/specs/link", "outside"},
		{"末尾のリンク", ".flywheel/specs/file.md", ".flywheel/specs/file.md", "outside/example.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := scStore(t)
			c := createAndAdvance(t, s, StatusClassified)
			writeWorkspaceFile(t, s.workspace, "outside/example.md", "keep")
			if err := os.MkdirAll(filepath.Join(s.workspace, ".flywheel/specs"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(s.workspace, tc.target), filepath.Join(s.workspace, tc.link)); err != nil {
				t.Fatal(err)
			}
			sdSetSpec(t, s, c.ID, tc.spec, "")
			scWithdraw(t, s, c.ID)
			scRequireExists(t, s, "outside/example.md", true)
			got := scFailures(t, s, c.ID)
			if len(got) != 1 || got[0]["path"] != tc.spec {
				t.Errorf("failures = %v, want one with the path", got)
			}
		})
	}
}

func TestSpecDraftCleanup_UnwritableDir(t *testing.T) { // AC-107・AC-108
	t.Parallel()
	s := scStore(t)
	id := scChallenge(t, s, StatusClassified, sdPath)
	dir := filepath.Join(s.workspace, ".flywheel/specs")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if probe, err := os.Create(filepath.Join(dir, "probe")); err == nil {
		_ = probe.Close()
		t.Skip("this user ignores directory permissions")
	}
	scWithdraw(t, s, id)
	scRequireExists(t, s, sdPath, true)
	got := scFailures(t, s, id)
	if len(got) != 1 || got[0]["path"] != sdPath {
		t.Fatalf("failures = %v, want one with the path", got)
	}
	if reason, _ := got[0]["reason"].(string); strings.TrimSpace(reason) == "" {
		t.Errorf("reason = %v", got[0])
	}
}

func TestSpecDraftCleanup_Completion(t *testing.T) {
	t.Parallel()
	t.Run("AC-97 未実行の操作が無い完了", func(t *testing.T) {
		t.Parallel()
		s := scStore(t)
		id := scChallenge(t, s, StatusAwaitingCompletionApproval, sdPath)
		scComplete(t, s, id)
		scRequireExists(t, s, sdPath, false)
	})
	t.Run("AC-98 未実行の操作がある完了", func(t *testing.T) {
		t.Parallel()
		s := scStore(t)
		id := scChallenge(t, s, StatusAwaitingCompletionApproval, sdPath)
		if _, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: id, Kind: string(OperationKindDelete), Summary: "消す"}); err != nil {
			t.Fatalf("CreateOperation: %v", err)
		}
		scComplete(t, s, id)
		scRequireExists(t, s, sdPath, true)
	})
}

func TestSpecDraftCleanup_ReleaseExecution(t *testing.T) {
	t.Parallel()
	merged := UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "x", HeadSHA: relHead, MergeCommitSHA: "mc"}
	fixture := func(t *testing.T) *relFixture {
		t.Helper()
		f := newRelFixture(t)
		writeWorkspaceFile(t, f.s.workspace, sdPath, "# 下書き\n")
		sdSetSpec(t, f.s, formatChallengeID(f.cid), sdPath, "")
		f.fake.pr = merged
		return f
	}
	t.Run("AC-99 最後の 1 件の実行で消す", func(t *testing.T) {
		t.Parallel()
		f := fixture(t)
		if item := f.run(t); item.Result != ExecutionResultAlreadyMerged {
			t.Fatalf("item = %+v", item)
		}
		scRequireExists(t, f.s, sdPath, false)
	})
	t.Run("AC-100 2 件のうち 1 件目では残す", func(t *testing.T) {
		t.Parallel()
		f := fixture(t)
		coretest.InsertApprovedRelease(t, f.s.workspace, int(f.cid), "https://github.com/o/r/pull/99", relHead, execTS(0))
		if item := f.run(t); item.Result != ExecutionResultAlreadyMerged {
			t.Fatalf("item = %+v", item)
		}
		scRequireExists(t, f.s, sdPath, true)
	})
	t.Run("AC-101 検証中へ戻した後も残る", func(t *testing.T) {
		t.Parallel()
		f := fixture(t)
		insertCompletionApprovalForTest(t, f.s, f.cid)
		res, err := reopenExecPath(context.Background(), f.s, ChannelCLI, ReopenForReleaseInput{
			OperationID: formatOperationID(f.opID), Reason: ReopenReasonNotMergeable,
		})
		if err != nil || !res.Reopened {
			t.Fatalf("reopen: %+v, %v", res, err)
		}
		scRequireExists(t, f.s, sdPath, true)
	})
}

// 実行されずに閉じた反映の操作も、まだ実行していない不可逆操作から外れる（Issue #1343）。
func TestSpecDraftCleanup_ReleaseClosedWithoutExecution(t *testing.T) {
	t.Parallel()
	const otherRef = "https://github.com/o/r/pull/99"
	// fixture は下書きを持つ完了した課題と、承認待ちの反映の操作 1 件。
	fixture := func(t *testing.T) *relFixture {
		t.Helper()
		f := newRelFixture(t)
		writeWorkspaceFile(t, f.s.workspace, sdPath, "# 下書き\n")
		sdSetSpec(t, f.s, formatChallengeID(f.cid), sdPath, "")
		f.setOpState(t, OperationStatePending)
		return f
	}
	for _, tc := range []struct {
		state  string
		reason ClosedReason
	}{
		{"closed", ClosedReasonPRClosed},
		{"merged", ClosedReasonMergedOutside},
	} {
		t.Run("最後の承認待ちの反映が閉じたら消す "+string(tc.reason), func(t *testing.T) {
			t.Parallel()
			f := fixture(t)
			f.fake.pr = UpstreamPullRequestChecks{State: tc.state, Base: "main", HeadSHA: relHead}
			res := f.runAll(t, nil)
			if len(res.Tidied) != 1 || res.Tidied[0].ClosedReason != tc.reason {
				t.Fatalf("tidied = %+v, want one %s", res.Tidied, tc.reason)
			}
			scRequireExists(t, f.s, sdPath, false)
		})
	}
	t.Run("ほかに承認待ちの反映が残るあいだは残す", func(t *testing.T) {
		t.Parallel()
		f := fixture(t)
		// 参照を PR の URL にしないので、片付けは 2 件目を読まず、承認待ちのまま残る。
		coretest.InsertReleaseOperation(t, f.s.workspace, int(f.cid), "not-a-pr", string(OperationStatePending))
		f.fake.pr = UpstreamPullRequestChecks{State: "closed", Base: "main", HeadSHA: relHead}
		if res := f.runAll(t, nil); len(res.Tidied) != 1 {
			t.Fatalf("tidied = %+v, want one", res.Tidied)
		}
		scRequireExists(t, f.s, sdPath, true)
	})
	t.Run("最後の承認待ちの操作を人が差し戻したら消す", func(t *testing.T) {
		t.Parallel()
		f := fixture(t)
		ctx := context.Background()
		id, reason := formatOperationID(f.opID), "入れない"
		prev, err := f.s.PrepareOperationRejection(ctx, id, reason)
		if err != nil {
			t.Fatalf("PrepareOperationRejection: %v", err)
		}
		if _, _, err := f.s.ExecuteOperationApproval(ctx, OperationApprovalRequest{
			OperationID: id, Decision: ApprovalDecisionRejected, Reason: &reason,
			ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion,
		}, verifiedAttestationForTest(t, id)); err != nil {
			t.Fatalf("ExecuteOperationApproval: %v", err)
		}
		scRequireExists(t, f.s, sdPath, false)
	})
	t.Run("契約の成果物から外れて閉じた最後の 1 件", func(t *testing.T) {
		t.Parallel()
		f := fixture(t)
		coretest.InsertReleaseOperation(t, f.s.workspace, int(f.cid), otherRef, string(OperationStatePending))
		runID := coretest.InsertRun(t, f.s.workspace, coretest.InsertRunInput{
			ChallengeID: int(f.cid), Kind: "delegate", ChallengeVersion: 1, SessionID: "33333333-3333-4333-8333-333333333333",
			PID: 1, Host: "h", HeartbeatAt: execTS(0), StartedAt: execTS(0), EndedAt: execTS(0), Result: "succeeded",
			MaxBudgetUSD: 1_000_000, BudgetBucket: "impl",
		})
		keepOther := map[string]bool{prKey("o/r", 99): true}
		if err := f.s.supersedeStaleReleases(context.Background(), f.cid, runID, keepOther); err != nil {
			t.Fatalf("supersedeStaleReleases: %v", err)
		}
		scRequireExists(t, f.s, sdPath, true)
		if err := f.s.supersedeStaleReleases(context.Background(), f.cid, runID, nil); err != nil {
			t.Fatalf("supersedeStaleReleases: %v", err)
		}
		scRequireExists(t, f.s, sdPath, false)
	})
}

func TestSpecDraftCleanup_KeptWhileTheChallengeGoesOn(t *testing.T) {
	t.Parallel()
	t.Run("AC-112 計画の承認", func(t *testing.T) {
		t.Parallel()
		f, id := landRegister(t, landDraftPath, "", j2OutputFor(false, landPlan(landTask{lands: true}, landTask{deps: []string{"a"}})))
		if err := approvePlanForTest(t, f.s, id, ttyAtt(t, id)); err != nil {
			t.Fatalf("approve: %v", err)
		}
		scRequireExists(t, f.s, landDraftPath, true)
	})
	t.Run("AC-113 保留", func(t *testing.T) {
		t.Parallel()
		s := scStore(t)
		id := scChallenge(t, s, StatusInProgress, sdPath)
		if c, err := s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "q"}); err != nil || c.Status != StatusAwaitingHuman {
			t.Fatalf("hold: %+v %v", c, err)
		}
		scRequireExists(t, s, sdPath, true)
	})
	t.Run("AC-114 検証中へ進む", func(t *testing.T) {
		t.Parallel()
		s := scStore(t)
		id := scChallenge(t, s, StatusInProgress, sdPath)
		if c, err := s.SubmitChallenge(context.Background(), ChannelCLI, id); err != nil || c.Status != StatusVerifying {
			t.Fatalf("submit: %+v %v", c, err)
		}
		scRequireExists(t, s, sdPath, true)
	})
}

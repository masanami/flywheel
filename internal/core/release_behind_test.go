package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// behindFake は fakeRelease に、base の取り込みの依頼（UpstreamPullRequestBranchUpdater）を足した偽の実装。
type behindFake struct {
	*fakeRelease
	updateErr error
	updates   []string
	// onUpdate が非 nil なら、取り込みの依頼の時点で呼ぶ（head を進める等）。
	onUpdate func()
}

func (b *behindFake) UpdatePullRequestBranch(_ context.Context, _ string, _ int, head string) error {
	b.updates = append(b.updates, head)
	if b.updateErr != nil {
		return b.updateErr
	}
	if b.onUpdate != nil {
		b.onUpdate()
	}
	return nil
}

func behindFixture(t *testing.T) (*relFixture, *behindFake) {
	t.Helper()
	f := newRelFixture(t)
	f.fake.pr.MergeableState = UpstreamMergeStateBehind
	f.fake.pr.Checks = []UpstreamCheck{{Name: "check", Status: "completed", Conclusion: "success"}}
	return f, &behindFake{fakeRelease: f.fake}
}

func (f *relFixture) runWith(t *testing.T, o ReleaseExecOptions) *ReleaseExecItem {
	t.Helper()
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, o)
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("Items = %+v, NotStarted = %+v, want 1 item", res.Items, res.NotStarted)
	}
	f.now = f.now.Add(time.Second)
	return &res.Items[0]
}

// 完了条件 (1): BEHIND の PR は、マージを試して merge_failed を重ねず、base の取り込みへ進み、待つ（数えない）。
// 上限（3）を超える回数を試しても limit_reached で取り消されず、承認が保たれる。
func TestRelease_BehindUpdatesBranchInsteadOfRepeatingMergeFailed(t *testing.T) {
	f, bf := behindFixture(t)
	f.fake.mergeErr = errors.New("Pull request is not up to date with the base branch")
	o := f.opts()
	o.Merger = bf
	for i := 0; i < f.limit+2; i++ {
		item := f.runWith(t, o)
		if item.Result != ExecutionResultWaitingChecks {
			t.Fatalf("run %d: result = %s, want waiting_checks", i, item.Result)
		}
		if item.ConsecutiveFailures != 0 {
			t.Fatalf("run %d: consecutive failures = %d, want 0", i, item.ConsecutiveFailures)
		}
	}
	if len(f.fake.mergeArgs) != 0 {
		t.Errorf("merge calls = %v, want none while BEHIND", f.fake.mergeArgs)
	}
	if len(bf.updates) != f.limit+2 || bf.updates[0] != relHead {
		t.Errorf("updates = %v, want one per run with the head %s", bf.updates, relHead)
	}
	if st := f.state(t); st.opState != "approved" {
		t.Errorf("state = %s, want approved", st.opState)
	}
}

func TestRelease_BehindUpdateFailureCounts(t *testing.T) {
	f, bf := behindFixture(t)
	bf.updateErr = errors.New("HTTP 422")
	o := f.opts()
	o.Merger = bf
	item := f.runWith(t, o)
	if item.Result != ExecutionResultMergeFailed || item.ConsecutiveFailures != 1 {
		t.Fatalf("result = %s (failures %d), want merge_failed counted", item.Result, item.ConsecutiveFailures)
	}
}

// 取り込みを依頼できない merger（更新の IF を持たない）では、従来どおりマージを試す。
func TestRelease_BehindWithoutUpdaterTriesMerge(t *testing.T) {
	f, _ := behindFixture(t)
	f.fake.mergeErr = errors.New("not up to date")
	item := f.run(t)
	if item.Result != ExecutionResultMergeFailed || len(f.fake.mergeArgs) != 1 {
		t.Fatalf("result = %s, merges = %d, want merge_failed after one merge attempt", item.Result, len(f.fake.mergeArgs))
	}
}

// BEHIND でも、チェックが失敗していれば取り込まない（checks_failed が先）。
func TestRelease_BehindWithFailedChecksDoesNotUpdate(t *testing.T) {
	f, bf := behindFixture(t)
	f.fake.pr.Checks = []UpstreamCheck{{Name: "e2e-real", Status: "completed", Conclusion: "failure"}}
	o := f.opts()
	o.Merger = bf
	if item := f.runWith(t, o); item.Result != ExecutionResultChecksFailed || len(bf.updates) != 0 {
		t.Fatalf("result = %s, updates = %v", item.Result, bf.updates)
	}
}

// 取り込み後の head（既定ブランチの取り込みだけ）は、すべてのチェックが成功していれば承認し直さずにマージする。
func movedHeadFixture(t *testing.T) (*relFixture, *fakeCompare) {
	t.Helper()
	f := newRelFixture(t)
	f.fake.pr.HeadSHA = "n1"
	f.fake.pr.Checks = []UpstreamCheck{
		{Name: "check", Status: "completed", Conclusion: "success"},
		{Name: "e2e-real", Status: "completed", Conclusion: "success"},
	}
	cmp := newFakeCompare()
	cmp.set(relHead, "n1", ahead(commit("m1", "main change", "m0"), commit("n1", "Merge branch 'main'", relHead, "m1")))
	cmp.set("main", "n1", ahead(commit(relHead, "fix", "p0"), commit("n1", "Merge branch 'main'", relHead, "m1")))
	cmp.set("m1", "n1", ahead(commit(relHead, "fix", "p0")))
	return f, cmp
}

func TestRelease_MovedHeadByMergeInMergesWithoutReapproval(t *testing.T) {
	f, cmp := movedHeadFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{State: "merged", Base: "main", HeadSHA: "n1", MergeCommitSHA: "mc"}
	o := f.opts()
	o.Compare = cmp
	item := f.runWith(t, o)
	if item.Result != ExecutionResultMerged {
		t.Fatalf("result = %s, want merged", item.Result)
	}
	if len(f.fake.mergeArgs) != 1 || f.fake.mergeArgs[0][2] != "n1" {
		t.Errorf("merge args = %v, want the merge pinned to the updated head n1", f.fake.mergeArgs)
	}
}

// 必須でないチェック（e2e-real）が失敗していれば、必須のチェックが成功でもマージしない。
func TestRelease_MovedHeadWithFailedNonRequiredCheckDoesNotMerge(t *testing.T) {
	f, cmp := movedHeadFixture(t)
	f.fake.pr.Checks[1].Conclusion = "failure"
	o := f.opts()
	o.Compare = cmp
	if item := f.runWith(t, o); item.Result != ExecutionResultChecksFailed || len(f.fake.mergeArgs) != 0 {
		t.Fatalf("result = %s, merges = %v, want checks_failed without merging", item.Result, f.fake.mergeArgs)
	}
}

// 取り込んだ直後でチェックがまだ付いていない head は、何も見ていないので待つ。
// 依頼がまだ無い間は、CI が走っていないのでチェックが 0 件でも待たず、取り込み後の head で依頼する（依頼が CI を走らせる）。
func TestRelease_MovedHeadWithoutChecksRequestsMergeOnTheNewHead(t *testing.T) {
	f, cmp := movedHeadFixture(t)
	f.fake.pr.Checks = nil
	o := f.opts()
	o.Compare = cmp
	item := f.runWith(t, o)
	if item.Result != ExecutionResultMergeRequested || len(f.fake.mergeArgs) != 1 || f.fake.mergeArgs[0][2] != f.fake.pr.HeadSHA {
		t.Fatalf("result = %s, merges = %v, want merge_requested on head %s", item.Result, f.fake.mergeArgs, f.fake.pr.HeadSHA)
	}
}

// 取り込み以外の変更が入った head、比較の手段が無い場合は従来どおり head_changed。
func TestRelease_MovedHeadNotMergeInOnlyIsHeadChanged(t *testing.T) {
	f, cmp := movedHeadFixture(t)
	cmp.set("main", "n1", ahead(commit(relHead, "fix", "p0"), commit("m1x", "feat", relHead), commit("n1", "Merge branch 'main'", relHead, "m1")))
	cmp.set(relHead, "n1", ahead(commit("m1x", "feat", relHead), commit("n1", "Merge branch 'main'", "m1x", "m1")))
	o := f.opts()
	o.Compare = cmp
	if item := f.runWith(t, o); item.Result != ExecutionResultHeadChanged {
		t.Fatalf("result = %s, want head_changed", item.Result)
	}
	f2, _ := movedHeadFixture(t)
	if item := f2.run(t); item.Result != ExecutionResultHeadChanged {
		t.Fatalf("without compare source: result = %s, want head_changed", item.Result)
	}
}

// 1 本マージした直後に mergeable が null でも、設定の回数まで読み直して次へ進む。
func TestRelease_MergeableNullIsRereadBeforeWaiting(t *testing.T) {
	f := newRelFixture(t)
	null := UpstreamPullRequestChecks{URL: relRef, State: "open", Base: "main", HeadSHA: relHead}
	f.fake.prSeq = []UpstreamPullRequestChecks{null, null, openPR(), {URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "mc"}}
	o := f.opts()
	o.MergeableRetries, o.MergeableInterval = 3, time.Nanosecond
	if item := f.runWith(t, o); item.Result != ExecutionResultMerged {
		t.Fatalf("result = %s, want merged after rereading mergeable", item.Result)
	}

	g := newRelFixture(t)
	g.fake.pr.Mergeable = nil
	o = g.opts()
	o.MergeableRetries, o.MergeableInterval = 2, time.Nanosecond
	if item := g.runWith(t, o); item.Result != ExecutionResultWaitingChecks || g.fake.getCalls != 3 {
		t.Fatalf("result = %s, reads = %d, want waiting_checks after bounded rereads", item.Result, g.fake.getCalls)
	}
}

// 取り込み後の head でマージを依頼して結果を読めなかった（merge_unconfirmed）後の試行は、承認なしのマージとは扱わず
// already_merged にする。
func TestRelease_MergedOnMovedHeadIsAlreadyMerged(t *testing.T) {
	f, cmp := movedHeadFixture(t)
	f.fake.pr = UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: "n1", MergeCommitSHA: "mc"}
	o := f.opts()
	o.Compare = cmp
	if item := f.runWith(t, o); item.Result != ExecutionResultAlreadyMerged {
		t.Fatalf("result = %s, want already_merged", item.Result)
	}
}

// 直したファイルがある取り込み（人が差分を見ていない）は、承認し直しが要る。
func TestRelease_MovedHeadWithConflictFixesIsHeadChanged(t *testing.T) {
	f, cmp := movedHeadFixture(t)
	cmp.set(relHead, "n1", withFiles(cmp.results[[2]string{relHead, "n1"}], UpstreamFile{Filename: "a.go", Patch: "@@ a"}))
	cmp.set("m1", "n1", withFiles(ahead(commit(relHead, "fix", "p0")), UpstreamFile{Filename: "a.go", Patch: "@@ a2"}))
	o := f.opts()
	o.Compare = cmp
	if item := f.runWith(t, o); item.Result != ExecutionResultHeadChanged || len(f.fake.mergeArgs) != 0 {
		t.Fatalf("result = %s, merges = %v, want head_changed", item.Result, f.fake.mergeArgs)
	}
}

// (c) 取り込みの依頼で衝突が出た（読み直した PR が mergeable=false）なら、merge_failed ではなく既存の衝突の
// 立て直し（not_mergeable。完了の課題は着手中へ戻す）へ回る。
func TestRelease_BehindUpdateConflictGoesToNotMergeableRecovery(t *testing.T) {
	f, bf := behindFixture(t)
	bf.updateErr = errors.New("HTTP 422: merge conflict between base and head")
	no := false
	conflicted := f.fake.pr
	conflicted.Mergeable = &no
	conflicted.MergeableState = "dirty"
	f.fake.prSeq = []UpstreamPullRequestChecks{f.fake.pr, conflicted}
	o := f.opts()
	o.Merger = bf
	item := f.runWith(t, o)
	if item.Result != ExecutionResultNotMergeable || !item.Reopened || item.ReopenReason != ReopenReasonNotMergeable {
		t.Fatalf("item = %+v, want not_mergeable routed to the conflict recovery", item)
	}
	if len(bf.updates) != 1 || len(f.fake.mergeArgs) != 0 {
		t.Errorf("updates = %v, merges = %v, want one update and no merge", bf.updates, f.fake.mergeArgs)
	}
}

// multiPRFake は PR の番号ごとに状態を持つ偽の upstream。どれか 1 本をマージすると、残りの開いた PR を BEHIND にする
// （既定ブランチが進んだ状態）。
type multiPRFake struct {
	prs     map[int]*UpstreamPullRequestChecks
	merges  []int
	updates []int
}

func prNumberFromURL(t *testing.T, u string) int {
	t.Helper()
	_, n, ok := parseReleasePullRequestRef(u)
	if !ok {
		t.Fatalf("bad PR url %q", u)
	}
	return n
}

func (m *multiPRFake) GetPullRequestChecks(_ context.Context, _ string, n int) (UpstreamPullRequestChecks, error) {
	return *m.prs[n], nil
}

func (m *multiPRFake) MergePullRequest(_ context.Context, prURL, _, _ string) error {
	_, n, _ := parseReleasePullRequestRef(prURL)
	m.merges = append(m.merges, n)
	m.prs[n].State = "merged"
	m.prs[n].MergeCommitSHA = "mc"
	for k, p := range m.prs {
		if k != n && p.State == "open" {
			p.MergeableState = UpstreamMergeStateBehind
		}
	}
	return nil
}

func (m *multiPRFake) UpdatePullRequestBranch(_ context.Context, _ string, n int, _ string) error {
	m.updates = append(m.updates, n)
	return nil
}

// (d) 同じ周に承認済みの PR が 2 本あり、1 本をマージすると残りが BEHIND になる。残りは取り込みだけ済ませて
// 数えない待ち（waiting_checks）へ戻り、同じ周ではマージを試さない（CI を待たない）。
func TestRelease_MultiplePRsBehindAfterFirstMergeOnlyUpdateAndWait(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	refs := []string{"https://github.com/o/r/pull/1", "https://github.com/o/r/pull/2"}
	m := &multiPRFake{prs: map[int]*UpstreamPullRequestChecks{}}
	for i, ref := range refs {
		c := createAndAdvance(t, s, StatusDone)
		head := "h" + string(rune('1'+i))
		coretest.InsertApprovedRelease(t, s.workspace, int(mustParseChallengeID(t, c.ID)), ref, head, execTS(0))
		m.prs[prNumberFromURL(t, ref)] = &UpstreamPullRequestChecks{
			URL: ref, State: "open", Base: "main", HeadSHA: head, Mergeable: truePtr(),
			Checks: []UpstreamCheck{{Name: "check", Status: "completed", Conclusion: "success"}},
		}
	}
	now := execTime(100)
	res, err := s.ExecuteReleases(context.Background(), nil, ReleaseExecOptions{
		Checks: m, Merger: m, Connectors: relConnectors(""),
		Agent: &AgentDeclaration{FailureLimit: 3}, Trigger: ReleaseExecTriggerOpExec,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %+v, want 2", res.Items)
	}
	var merged, waiting int
	for _, it := range res.Items {
		switch it.Result {
		case ExecutionResultMerged:
			merged++
		case ExecutionResultWaitingChecks:
			waiting++
			if it.ConsecutiveFailures != 0 {
				t.Errorf("waiting item counted: %+v", it)
			}
		default:
			t.Errorf("unexpected item %+v", it)
		}
	}
	if merged != 1 || waiting != 1 {
		t.Fatalf("merged = %d, waiting = %d, want 1 and 1 (items %+v)", merged, waiting, res.Items)
	}
	if len(m.merges) != 1 || len(m.updates) != 1 || m.merges[0] == m.updates[0] {
		t.Errorf("merges = %v, updates = %v, want one merge and one update of the other PR", m.merges, m.updates)
	}
}

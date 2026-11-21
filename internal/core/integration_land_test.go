package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
)

// このファイルは、統合先を使う課題のタスクの実行を「統合先へ入れる」まで進める状態の確認（区切り S3b）を、
// 実ストア・偽の ContractClient・偽の上流（チェック・比較・PR）で固定する。

// igLandChecks は UpstreamCheckSource と UpstreamCommitChecksSource の偽の実装。
type igLandChecks struct {
	// onCommitChecks は、コミットのチェックを読む呼び出しの間（読みの後・land を書く前）に差し込む処理。
	onCommitChecks func()
	mu             sync.Mutex
	commits        map[string][]UpstreamCheck
	prs            map[string]UpstreamPullRequestChecks
	err            error
	calls          []string
}

func newIgLandChecks() *igLandChecks {
	return &igLandChecks{commits: map[string][]UpstreamCheck{}, prs: map[string]UpstreamPullRequestChecks{}}
}

func (l *igLandChecks) GetCommitChecks(_ context.Context, _, sha string) ([]UpstreamCheck, error) {
	if l.onCommitChecks != nil {
		l.onCommitChecks()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, sha)
	if l.err != nil {
		return nil, l.err
	}
	return l.commits[sha], nil
}

func (l *igLandChecks) GetPullRequestChecks(_ context.Context, _ string, number int) (UpstreamPullRequestChecks, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return UpstreamPullRequestChecks{}, l.err
	}
	pr, ok := l.prs[releaseKey(number)]
	if !ok {
		return UpstreamPullRequestChecks{}, errors.New("no such pull request")
	}
	return pr, nil
}

const (
	igLandConnectorsJSON = `{
  "version": 1,
  "human_question_kinds": [],
  "connectors": [
    {"id": "tool", "form": "cli", "permission_mode": "default",
     "commands": {
       "start": ["tool", "start", "--challenge", "{challenge_id}"],
       "status": ["tool", "status", "{run_id}"],
       "resume": ["tool", "resume", "{run_id}", "--choice", "{choice}", "--note", "{note}", "--approved", "{approved_ref}"],
       "cancel": ["tool", "cancel", "{run_id}"]
     },
     "contract_version": 1,
     "operations": [{"id": "impl", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]}
  ],
  "repos": [
    {"name": "main-repo", "remote": "o/r", "default_branch": "main", "connector": "tool", "slots": {"provider": "clone", "paths": ["slot1"]}}
  ]
}`
	landPRURL   = "https://github.com/o/r/pull/7"
	landContent = "c0ffee1" // reviewContentArtifact の ref（検証した中身）
)

var landReviewArts = integArtsOK

type igLandFixture struct {
	*reviewFlowFixture
	checks  *igLandChecks
	compare *fakeCompare
	// onStatus は、状態の確認（commands.status）の呼び出しの間（読みの前）に差し込む処理。
	onStatus func()
}

// igHookClient は ContractClient の status の前に処理を差し込む。
type igHookClient struct {
	ContractClient
	before func()
}

func (c igHookClient) Status(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	if c.before != nil {
		c.before()
	}
	return c.ContractClient.Status(ctx, in)
}

// newIgLandFixture は、統合先を使う課題で、タスクが検証中、契約の実行が waiting（検証した中身は landContent）の状態を作る。
func newIgLandFixture(t *testing.T) *igLandFixture {
	t.Helper()
	f := newIntakeFixture(t, igLandConnectorsJSON)
	f.sql(t, `UPDATE challenge SET uses_integration = 1`)
	lf := &igLandFixture{reviewFlowFixture: f, checks: newIgLandChecks(), compare: newFakeCompare()}
	lf.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if n := f.count(t, `SELECT COUNT(*) FROM task WHERE status = 'verifying'`); n != 1 {
		t.Fatalf("precondition: verifying tasks = %d", n)
	}
	f.sql(t, `UPDATE contract_run SET verified_ref = ?`, landContent)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	return lf
}

func (f *igLandFixture) pollLand(t *testing.T) {
	t.Helper()
	if _, err := f.s.PollContractRuns(context.Background(), ContractPollInput{
		AgentDecl: f.agent, ConnDecl: f.conn, Client: igHookClient{ContractClient: f.client, before: f.onStatus}, CycleID: f.cycle(t, 300), Reconcile: f.branches,
		Checks: f.checks, Compare: f.compare,
	}); err != nil {
		t.Fatalf("PollContractRuns: %v", err)
	}
}

func (f *igLandFixture) read(t *testing.T, out ContractLaunchOutput) {
	t.Helper()
	f.client.queue = []ContractLaunchOutput{out}
	f.pollLand(t)
}

func (f *igLandFixture) ref(t *testing.T, col string) string {
	t.Helper()
	return f.text(t, `SELECT `+col+` FROM contract_run`)
}

func (f *igLandFixture) taskStatus(t *testing.T) string {
	t.Helper()
	return f.text(t, `SELECT status FROM task`)
}

func (f *igLandFixture) j5Calls() int { return len(f.j5.j5Inputs) }

// verifyAll は同じサイクルの後の段（検証の段）と同じに、J5 の対象を自動で選んで回す。
func (f *igLandFixture) verifyAll(t *testing.T) {
	t.Helper()
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 300), nil)); err != nil {
		t.Fatalf("VerifyAutoJ5: %v", err)
	}
}

var igPassingChecks = []UpstreamCheck{checkDone}

// AC-109〜AC-111: 検証した中身があり、レビュー待ちで、チェックがすべて通っているなら、land を {approved_ref} つきで返す。
func TestLandVerified_PassingChecksReturnLand(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.commits[landContent] = igPassingChecks
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1", len(f.client.resumes))
	}
	cmd := strings.Join(f.lastResume(t), " ")
	if !strings.Contains(cmd, "--choice land") || !strings.Contains(cmd, "--approved "+landContent) { // AC-109・AC-110
		t.Fatalf("resume command = %q", cmd)
	}
	if got := f.ref(t, "land_ref"); got != landContent { // AC-111
		t.Fatalf("land_ref = %q", got)
	}
	if got := f.count(t, `SELECT land_delivered FROM contract_run`); got != 1 {
		t.Fatalf("land_delivered = %d", got)
	}
	if f.count(t, `SELECT COUNT(*) FROM operation`) != 0 {
		t.Fatal("an operation was created")
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s", got)
	}
}

// AC-112: 検証した中身が無いなら、レビュー待ちでも land を返さない。
func TestLandVerified_WithoutVerifiedRefReturnsNoLand(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.sql(t, `UPDATE contract_run SET verified_ref = NULL`)
	f.checks.commits[landContent] = igPassingChecks
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %d, want 0", len(f.client.resumes))
	}
}

// AC-113・AC-114: 終わっていないチェックがあれば、何も返さず待つ（課題は着手中のまま）。
func TestLandVerified_PendingChecksWait(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.commits[landContent] = []UpstreamCheck{checkDone, checkRunning}
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %d, want 0", len(f.client.resumes))
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s", got)
	}
}

// 上流のチェックを読めなかったときも、何も返さず待つ（保留にしない）。
func TestLandVerified_UnreadableChecksWait(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.err = errors.New("boom")
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if len(f.client.resumes) != 0 || f.status(t, f.cid) != StatusInProgress {
		t.Fatalf("resumes = %d, status = %s", len(f.client.resumes), f.status(t, f.cid))
	}
}

// チェックが 0 件のとき（検証した中身そのもの）は、S3a と同じに通す。
func TestLandVerified_NoChecksPass(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1", len(f.client.resumes))
	}
}

// AC-115・AC-150: 失敗があれば、保留（checks_failed。タスクに結び付く）。
func TestLandVerified_FailedChecksHold(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.commits[landContent] = []UpstreamCheck{checkDone, checkFailed}
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s", got)
	}
	if got := f.lastHoldReason(t); got != string(holdReasonChecksFailed) {
		t.Fatalf("reason = %q", got)
	}
	if f.count(t, `SELECT COUNT(*) FROM hold WHERE task_id IS NOT NULL`) != 1 {
		t.Fatal("the hold is not tied to the task")
	}
	if strings.Contains(f.lastHoldQuestion(t), "checks_failed") {
		t.Fatalf("question shows the internal value: %q", f.lastHoldQuestion(t))
	}
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %d", len(f.client.resumes))
	}
}

// AC-116: land を返した後、running の間は resume を呼ばない。
func TestLandVerified_RunningAfterLandDoesNotResume(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.commits[landContent] = igPassingChecks
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	f.read(t, contractOut(ContractStateRunning, nil, nil))
	f.read(t, contractOut(ContractStateRunning, nil, nil))
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1", len(f.client.resumes))
	}
}

// AC-117・AC-118: land が届いた後に、同じ中身のレビュー待ちが戻ったら、保留（land_refused）。問いに委譲先の summary。
func TestLandVerified_SameContentAfterDeliveredIsRefused(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.commits[landContent] = igPassingChecks
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if got := f.lastHoldReason(t); got != string(holdReasonLandRefused) {
		t.Fatalf("reason = %q", got)
	}
	if strings.Contains(f.lastHoldQuestion(t), string(holdReasonLandRefused)) { // AC-237: 問いの文に理由の値の語は出ない
		t.Fatalf("question names the reason value: %q", f.lastHoldQuestion(t))
	}
	if !strings.Contains(f.lastHoldQuestion(t), "s-waiting") { // AC-118（偽の委譲先の summary）
		t.Fatalf("question lacks the summary: %q", f.lastHoldQuestion(t))
	}
	if f.count(t, `SELECT COUNT(*) FROM hold WHERE task_id IS NOT NULL`) != 1 { // AC-150
		t.Fatal("the hold is not tied to the task")
	}
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1 (no second land)", len(f.client.resumes))
	}
}

// AC-119: land が届いた記録が無く、同じ中身のレビュー待ちを読んだら、もう一度 land を返す。
func TestLandVerified_NoDeliveredRecordLandsAgain(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.commits[landContent] = igPassingChecks
	f.sql(t, `UPDATE contract_run SET land_ref = ?, land_delivered = 0`, landContent)
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1", len(f.client.resumes))
	}
	if f.status(t, f.cid) != StatusInProgress {
		t.Fatal("the challenge was held")
	}
}

// resume が失敗したときは、保留にせず、届いた記録も付けない（次の機会に同じ land を返し直す）。
func TestLandVerified_FailedResumeIsRetriedNotHeld(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.commits[landContent] = igPassingChecks
	f.client.resumeOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if f.count(t, `SELECT land_delivered FROM contract_run`) != 0 || f.status(t, f.cid) != StatusInProgress {
		t.Fatal("a failed resume was recorded as delivered or held the challenge")
	}
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if len(f.client.resumes) != 2 || f.count(t, `SELECT land_delivered FROM contract_run`) != 1 {
		t.Fatalf("resumes = %d", len(f.client.resumes))
	}
}

const newContent = "bbbbbbbb"

var movedArts = []ContractArtifact{{Kind: "commit", Ref: newContent}, integBranchArt, integCommitArt}

func (f *igLandFixture) setMergeInOnly() {
	f.compare.set(landContent, newContent, ahead(commit("m1", "main change", "m0"), commit(newContent, "Merge branch 'feat/integration-1'", landContent, "m1")))
	f.compare.set(integBranch, newContent, ahead(commit("p0", "feat", "m0"), commit(landContent, "fix", "p0"), commit(newContent, "Merge branch 'feat/integration-1'", landContent, "m1")))
	f.compare.set("m1", newContent, UpstreamComparison{Status: UpstreamCompareAhead, TotalCommits: 1, Commits: []UpstreamCommit{commit(landContent, "fix", "p0")}})
}

// AC-120〜AC-122: 中身が違い、取り込みだけなら、検証した中身を新しい中身に改め、J5 を呼ばずに land を返す。
func TestLandVerified_MergeInOnlyUpdatesVerifiedRefAndLands(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.setMergeInOnly()
	f.checks.commits[newContent] = igPassingChecks
	before := f.j5Calls()
	f.read(t, intakeOut(integOptsWithLd, movedArts, nil))
	if got := f.ref(t, "verified_ref"); got != newContent { // AC-120
		t.Fatalf("verified_ref = %q", got)
	}
	if f.j5Calls() != before || len(f.client.resumes) != 1 { // AC-121
		t.Fatalf("j5 calls %d -> %d, resumes = %d", before, f.j5Calls(), len(f.client.resumes))
	}
	if cmd := strings.Join(f.lastResume(t), " "); !strings.Contains(cmd, "--approved "+newContent) {
		t.Fatalf("resume command = %q", cmd)
	}
	if len(f.compare.calls) == 0 || f.compare.calls[0] != [2]string{landContent, newContent} { // AC-122
		t.Fatalf("compare calls = %v", f.compare.calls)
	}
	var toDest bool
	for _, c := range f.compare.calls {
		toDest = toDest || c == [2]string{integBranch, newContent}
	}
	if !toDest {
		t.Fatalf("the destination %q was not the base of a comparison: %v", integBranch, f.compare.calls)
	}
	if got := f.ref(t, "land_ref"); got != newContent {
		t.Fatalf("land_ref = %q", got)
	}
}

// AC-120・AC-121（待つ間）: 取り込みだけでも、新しい中身のチェックが終わっていない間は land を返さない。それでも検証した中身は
// その場で新しい中身になり、同じ周の後の検証の段（J5 の対象選び）が、検証し直さない（J5 を呼ばない）。チェックが通れば、次の読みで land を返す。
func TestLandVerified_MergeInOnlyWhileChecksPendingKeepsJ5OutAndLandsLater(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.setMergeInOnly()
	f.checks.commits[newContent] = []UpstreamCheck{checkRunning}
	before := f.j5Calls()
	f.read(t, intakeOut(integOptsWithLd, movedArts, nil))
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %d, want 0 while the checks are pending", len(f.client.resumes))
	}
	if got := f.ref(t, "verified_ref"); got != newContent {
		t.Fatalf("verified_ref = %q, want the new content", got)
	}
	if f.ref(t, "land_ref") != "" || f.count(t, `SELECT land_delivered FROM contract_run`) != 0 {
		t.Fatal("land_ref or land_delivered is still set")
	}
	f.verifyAll(t)
	if f.j5Calls() != before {
		t.Fatalf("J5 was called %d times for a merge-in-only move", f.j5Calls()-before)
	}
	f.checks.commits[newContent] = igPassingChecks
	f.read(t, intakeOut(integOptsWithLd, movedArts, nil))
	if len(f.client.resumes) != 1 || !strings.Contains(strings.Join(f.lastResume(t), " "), "--approved "+newContent) {
		t.Fatalf("resumes = %v", f.client.resumes)
	}
	if f.ref(t, "land_ref") != newContent {
		t.Fatalf("land_ref = %q", f.ref(t, "land_ref"))
	}
}

// 取り込みだけで、新しい中身のチェックが 0 件のときは、前の中身（成果物の履歴から引く）にチェックがあった間は待ち、J5 も呼ばない。
// 新しい中身にチェックが付いて通れば、land を返す。
func TestLandVerified_MergeInOnlyWithNoChecksWaitsAcrossReadsWithoutJ5(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.setMergeInOnly()
	f.checks.commits[landContent] = igPassingChecks
	before := f.j5Calls()
	for i := 0; i < 2; i++ {
		f.read(t, intakeOut(integOptsWithLd, movedArts, nil))
		f.verifyAll(t)
	}
	if len(f.client.resumes) != 0 || f.j5Calls() != before {
		t.Fatalf("resumes = %d, J5 calls = %d", len(f.client.resumes), f.j5Calls()-before)
	}
	if got := f.ref(t, "verified_ref"); got != newContent {
		t.Fatalf("verified_ref = %q", got)
	}
	f.checks.commits[newContent] = igPassingChecks
	f.read(t, intakeOut(integOptsWithLd, movedArts, nil))
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1", len(f.client.resumes))
	}
}

// 前の中身にもチェックが 0 件だったなら（チェックの無いリポジトリ）、取り込みだけの新しい中身は、チェックが 0 件でも通る。
func TestLandVerified_MergeInOnlyWithNoChecksOnEitherContentLands(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.setMergeInOnly()
	f.read(t, intakeOut(integOptsWithLd, movedArts, nil))
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1", len(f.client.resumes))
	}
}

// 検証した中身のレビュー待ちで、land が届いた記録の中身が検証した中身と違うとき（記録だけが残った）は、拒否と読まず、もう一度 land を返す。
func TestLandVerified_DeliveredRecordOfAnotherContentIsNotARefusal(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.commits[landContent] = igPassingChecks
	f.sql(t, `UPDATE contract_run SET land_ref = 'other', land_delivered = 1`)
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if f.count(t, `SELECT COUNT(*) FROM hold`) != 0 || len(f.client.resumes) != 1 {
		t.Fatalf("holds = %d, resumes = %d", f.count(t, `SELECT COUNT(*) FROM hold`), len(f.client.resumes))
	}
}

// AC-124・AC-125・AC-144（AC-25・AC-26）: 中身の変更がある・判定できないときは、検証した中身と land_ref を消す（確かめ待ちの中身は NULL のまま）。コンフリクトの解消ありは integration_land_fix_test.go。
func TestLandVerified_OtherMovesClearVerifiedAndLandRef(t *testing.T) {
	t.Parallel()
	cases := map[string]func(f *igLandFixture){
		"AC-124 content changed": func(f *igLandFixture) {
			f.compare.set(landContent, newContent, ahead(commit(newContent, "new work", landContent)))
			f.compare.set(integBranch, newContent, ahead(commit(landContent, "fix", "p0"), commit(newContent, "new work", landContent)))
		},
		"AC-125 undeterminable": func(*igLandFixture) {},
	}
	for name, setup := range cases {
		setup := setup
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIgLandFixture(t)
			f.sql(t, `UPDATE contract_run SET land_ref = ?, land_delivered = 1`, landContent)
			f.checks.commits[newContent] = igPassingChecks
			setup(f)
			f.read(t, intakeOut(integOptsWithLd, movedArts, nil))
			if got := f.ref(t, "verified_ref"); got != "" {
				t.Fatalf("verified_ref = %q", got)
			}
			if got := f.ref(t, "fix_pending_ref"); got != "" { // AC-25・AC-26
				t.Fatalf("fix_pending_ref = %q", got)
			}
			if got := f.ref(t, "land_ref"); got != "" { // AC-144
				t.Fatalf("land_ref = %q", got)
			}
			if f.count(t, `SELECT land_delivered FROM contract_run`) != 0 || len(f.client.resumes) != 0 {
				t.Fatal("land_delivered was kept or land was returned")
			}
			if f.taskStatus(t) != "verifying" {
				t.Fatalf("task = %s, want verifying (the next J5 verifies again)", f.taskStatus(t))
			}
		})
	}
}

// AC-26: 保留中の課題の、検証した中身があるレビュー待ちの実行へは、resume を呼ばない（状態の確認の前から保留）。
func TestLandVerified_HeldChallengeDoesNotResume(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.commits[landContent] = igPassingChecks
	f.sql(t, `UPDATE challenge SET status = 'awaiting_human'`)
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %d, want 0", len(f.client.resumes))
	}
}

// landVerifiedReview・sendLand の確かめ（着手中・検証中・検証した中身の一致）を、読みの途中の変化で通す。
// 状態の確認の呼び出しの間（読みの前に判断の入口を通った後）か、チェックを読む間（land を書く直前）に、状態を変える。
func TestLandVerified_GuardsAfterTheReadDoNotReturnLand(t *testing.T) {
	t.Parallel()
	const held = `UPDATE challenge SET status = 'awaiting_human'`
	cases := map[string]struct{ onStatus, onChecks string }{
		// 状態の確認の間に保留になる（受け取りは着手中でなければ何もせず、入れる側の着手中の確かめが止める）。
		"AC-26 held between the status read and the land step": {onStatus: held},
		"AC-26 held right before land is written":              {onChecks: held},
		"the task is not verifying":                            {onStatus: `UPDATE task SET status = 'cancelled'`},
		"the verified content changed right before land":       {onChecks: `UPDATE contract_run SET verified_ref = 'zzzzzzzz'`},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIgLandFixture(t)
			f.checks.commits[landContent] = igPassingChecks
			if c.onStatus != "" {
				f.onStatus = func() { f.sql(t, c.onStatus) }
			}
			if c.onChecks != "" {
				f.checks.onCommitChecks = func() { f.sql(t, c.onChecks) }
			}
			f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
			if len(f.client.resumes) != 0 {
				t.Fatalf("resumes = %d, want 0", len(f.client.resumes))
			}
			if got := f.ref(t, "land_ref"); got != "" {
				t.Fatalf("land_ref = %q, want empty", got)
			}
		})
	}
}

// 保留中の課題では、取り込みだけで動いた中身も、検証した中身を改めない（着手中の確かめが、状態の確認の間に保留になった課題でも効く）。
func TestLandVerified_HeldChallengeKeepsTheVerifiedContentOfAMovedReview(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.setMergeInOnly()
	f.checks.commits[newContent] = igPassingChecks
	f.onStatus = func() { f.sql(t, `UPDATE challenge SET status = 'awaiting_human'`) }
	f.read(t, intakeOut(integOptsWithLd, movedArts, nil))
	if got := f.ref(t, "verified_ref"); got != landContent {
		t.Fatalf("verified_ref = %q, want it kept while the challenge is held", got)
	}
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %d, want 0", len(f.client.resumes))
	}
}

var landedArts = []ContractArtifact{{Kind: "pr", Ref: landPRURL}, integBranchArt, {Kind: "commit", Ref: "dddddddd", Role: "integration"}}

// landAndSucceed は land を返してから、succeeded を読む（artifacts と PR の head・状態は呼び出し元が決める）。
func (f *igLandFixture) landed(t *testing.T) {
	t.Helper()
	f.checks.commits[landContent] = igPassingChecks
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	f.checks.prs["o/r#7"] = UpstreamPullRequestChecks{URL: landPRURL, State: "merged", Base: integBranch, HeadSHA: landContent}
	f.setPR(7, landPRURL, "merged", integBranch)
}

func (f *igLandFixture) succeed(t *testing.T) {
	t.Helper()
	f.read(t, contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) { st.Artifacts = landedArts }))
}

// AC-127〜AC-129: succeeded で、成果物が統合先へ入っていて、入った中身が land_ref と同じなら、タスクは done・終わった記録・統合先の中身の識別が成果物に残る。
func TestLandSucceeded_LandedSameContentCompletesTask(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.succeed(t)
	if f.taskStatus(t) != "done" { // AC-127
		t.Fatalf("task = %s", f.taskStatus(t))
	}
	if f.text(t, `SELECT ended_at FROM contract_run`) == "" { // AC-128
		t.Fatal("the contract run is not ended")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run_artifact WHERE kind = 'commit' AND ref = 'dddddddd' AND role = 'integration'`); n < 1 { // AC-129
		t.Fatalf("integration content artifacts = %d", n)
	}
	if f.count(t, `SELECT COUNT(*) FROM hold`) != 0 {
		t.Fatal("a hold was raised")
	}
}

// AC-27・AC-28: 保留中の課題でも、succeeded を読んで統合先へ入っていれば、タスクは done になり、課題は人間対応待ちのまま。
func TestLandSucceeded_WhileHeldCompletesTaskAndKeepsChallengeHeld(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.sql(t, `UPDATE challenge SET status = 'awaiting_human'`)
	f.succeed(t)
	if f.taskStatus(t) != "done" {
		t.Fatalf("task = %s", f.taskStatus(t))
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s", got)
	}
}

// AC-130・AC-131: 入っていなければ delegate_ended（タスクは done にしない）。
func TestLandSucceeded_NotLandedHoldsDelegateEnded(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.setPR(7, landPRURL, "open", integBranch)
	f.succeed(t)
	if got := f.lastHoldReason(t); got != string(holdReasonDelegateEnded) {
		t.Fatalf("reason = %q", got)
	}
	if f.taskStatus(t) == "done" {
		t.Fatal("the task is done")
	}
	if f.count(t, `SELECT COUNT(*) FROM hold WHERE task_id IS NOT NULL`) != 1 { // AC-150
		t.Fatal("the hold is not tied to the task")
	}
}

// AC-130: 成果物の PR が、統合先ではない別のブランチへマージ済みなら、統合先へは入っていない（delegate_ended）。
func TestLandSucceeded_MergedIntoAnotherBranchHoldsDelegateEnded(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.checks.prs["o/r#7"] = UpstreamPullRequestChecks{URL: landPRURL, State: "merged", Base: "main", HeadSHA: landContent}
	f.setPR(7, landPRURL, "merged", "main")
	f.succeed(t)
	if got := f.lastHoldReason(t); got != string(holdReasonDelegateEnded) {
		t.Fatalf("reason = %q", got)
	}
	if f.taskStatus(t) == "done" {
		t.Fatal("the task is done")
	}
}

// AC-132・AC-133: 入った中身が land_ref と違えば merged_unverified（タスクは done にしない）。
func TestLandSucceeded_OtherContentHoldsMergedUnverified(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.checks.prs["o/r#7"] = UpstreamPullRequestChecks{URL: landPRURL, State: "merged", Base: integBranch, HeadSHA: "other-head"}
	f.succeed(t)
	if got := f.lastHoldReason(t); got != string(holdReasonMergedUnverified) {
		t.Fatalf("reason = %q", got)
	}
	if strings.Contains(f.lastHoldQuestion(t), string(holdReasonMergedUnverified)) { // AC-237
		t.Fatalf("question names the reason value: %q", f.lastHoldQuestion(t))
	}
	if f.taskStatus(t) == "done" {
		t.Fatal("the task is done")
	}
	if f.count(t, `SELECT COUNT(*) FROM hold WHERE task_id IS NOT NULL`) != 1 { // AC-150
		t.Fatal("the hold is not tied to the task")
	}
}

// AC-134・AC-135: 上流から読めなければ何も変えず、次の状態の確認で読めれば done になる。
func TestLandSucceeded_UnreadableUpstreamRetriesNextRead(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.checks.err = errors.New("boom")
	f.succeed(t)
	if f.taskStatus(t) != "verifying" || f.status(t, f.cid) != StatusInProgress || f.count(t, `SELECT COUNT(*) FROM hold`) != 0 { // AC-134
		t.Fatalf("task = %s, status = %s", f.taskStatus(t), f.status(t, f.cid))
	}
	f.checks.err = nil
	f.succeed(t)
	if f.taskStatus(t) != "done" { // AC-135
		t.Fatalf("task = %s", f.taskStatus(t))
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run_artifact WHERE kind = 'commit' AND ref = 'dddddddd' AND role = 'integration'
		AND run_id = (SELECT MAX(id) FROM run WHERE kind = 'contract')`); n != 1 { // AC-129（done にした読みの run に残る）
		t.Fatalf("integration content artifacts of the last read = %d", n)
	}
}

// 入っていない・入った中身が違うときの保留と、契約の実行の終わった記録は、同じトランザクションで書く（終わりの記録だけが失敗して、
// 実行が開いたまま課題が保留になることが無い）。終わりの記録の書き込みを失敗させると、保留も残らず、次の読みでやり直せる。
func TestLandSucceeded_HoldAndEndedRecordAreWrittenTogether(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.setPR(7, landPRURL, "open", integBranch)
	f.sql(t, `CREATE TRIGGER fail_end BEFORE UPDATE OF ended_at ON contract_run WHEN NEW.ended_at IS NOT NULL BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) { st.Artifacts = landedArts })}
	_, _ = f.s.PollContractRuns(context.Background(), ContractPollInput{
		AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client, CycleID: f.cycle(t, 300), Reconcile: f.branches, Checks: f.checks, Compare: f.compare,
	})
	if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != 0 {
		t.Fatalf("holds = %d, want 0 (the ended record failed, so the hold must not remain)", n)
	}
	if f.status(t, f.cid) != StatusInProgress {
		t.Fatalf("status = %s", f.status(t, f.cid))
	}
	f.sql(t, `DROP TRIGGER fail_end`)
	f.succeed(t)
	if got := f.lastHoldReason(t); got != string(holdReasonDelegateEnded) {
		t.Fatalf("reason = %q", got)
	}
	if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) == "" {
		t.Fatal("the contract run is not ended")
	}
}

// AC-134: 成果物の PR を上流から読めない（PR の取得の失敗）ときも、何も変えず（保留にもしない）、次の状態の確認でやり直す。
func TestLandSucceeded_UnreadablePullRequestRetriesNextRead(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.branches.pullErrs["o/r#7"] = errors.New("boom")
	f.succeed(t)
	if f.taskStatus(t) != "verifying" || f.status(t, f.cid) != StatusInProgress || f.count(t, `SELECT COUNT(*) FROM hold`) != 0 {
		t.Fatalf("task = %s, status = %s", f.taskStatus(t), f.status(t, f.cid))
	}
	delete(f.branches.pullErrs, "o/r#7")
	f.succeed(t)
	if f.taskStatus(t) != "done" { // AC-135
		t.Fatalf("task = %s", f.taskStatus(t))
	}
}

// AC-136・AC-145: land を返した後に failed・cancelled を読んだら、検証した中身と land_ref を消す。
func TestLandAfter_FailedAndCancelledClearRefs(t *testing.T) {
	t.Parallel()
	for _, state := range []ContractState{ContractStateFailed, ContractStateCancelled} {
		state := state
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			f := newIgLandFixture(t)
			f.checks.commits[landContent] = igPassingChecks
			f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
			if f.ref(t, "land_ref") != landContent {
				t.Fatalf("precondition: land was not returned (land_ref = %q)", f.ref(t, "land_ref"))
			}
			f.read(t, contractOut(state, nil, nil))
			if f.ref(t, "verified_ref") != "" || f.ref(t, "land_ref") != "" {
				t.Fatalf("verified_ref = %q, land_ref = %q", f.ref(t, "verified_ref"), f.ref(t, "land_ref"))
			}
		})
	}
}

func (f *igLandFixture) answer(t *testing.T, text string) {
	t.Helper()
	prev, err := f.s.PrepareAnswer(context.Background(), f.cid, text)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.cid, ExpectedVersion: prev.Version, Answer: text},
		verifiedAttestationForTest(t, f.cid)); err != nil {
		t.Fatal(err)
	}
}

// AC-137・AC-138・AC-149: land_refused の保留への回答で、そのタスクの契約の実行の land_ref は NULL・land_delivered は偽、
// verified_ref は残る。ほかのタスクの契約の実行は変えない。
func TestLandAnswer_LandRefusedClearsLandRefOfThatTaskOnly(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.commits[landContent] = igPassingChecks
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	addTaskRowForTest(t, f.s, f.intID(t), 1, "t2", "main-repo", "impl")
	f.sql(t, `INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd, started_at, observed_at, task_id, kind, land_ref, land_delivered)
		SELECT challenge_id, connector_id, 'ext-other', start_run_id, 'running', summary, 0, max_budget_usd, started_at, observed_at, (SELECT MAX(id) FROM task), 'task', 'other-land', 1 FROM contract_run LIMIT 1`)
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil)) // 同じ中身が戻る
	if got := f.lastHoldReason(t); got != string(holdReasonLandRefused) {
		t.Fatalf("reason = %q", got)
	}
	f.answer(t, "直した")
	if got := f.text(t, `SELECT COALESCE(land_ref, '') FROM contract_run WHERE external_run_id = 'ext-1'`); got != "" { // AC-137
		t.Fatalf("land_ref = %q", got)
	}
	if f.count(t, `SELECT land_delivered FROM contract_run WHERE external_run_id = 'ext-1'`) != 0 {
		t.Fatal("land_delivered is still true")
	}
	if got := f.text(t, `SELECT verified_ref FROM contract_run WHERE external_run_id = 'ext-1'`); got != landContent { // AC-138
		t.Fatalf("verified_ref = %q", got)
	}
	if got := f.text(t, `SELECT land_ref FROM contract_run WHERE external_run_id = 'ext-other'`); got != "other-land" { // AC-149
		t.Fatalf("the other task's land_ref = %q", got)
	}
}

// AC-146: checks_failed の保留への回答で、land_ref は NULL になる。
func TestLandAnswer_ChecksFailedClearsLandRef(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.checks.commits[landContent] = []UpstreamCheck{checkFailed}
	f.sql(t, `UPDATE contract_run SET land_ref = ?`, landContent)
	f.read(t, intakeOut(integOptsWithLd, landReviewArts, nil))
	if got := f.lastHoldReason(t); got != string(holdReasonChecksFailed) {
		t.Fatalf("reason = %q", got)
	}
	f.answer(t, "チェックを直した")
	if got := f.ref(t, "land_ref"); got != "" {
		t.Fatalf("land_ref = %q", got)
	}
	if got := f.ref(t, "verified_ref"); got != landContent {
		t.Fatalf("verified_ref = %q", got)
	}
}

// AC-147・AC-148: delegate_ended・merged_unverified の保留への回答で、契約の実行が終わっていれば、タスクは in_progress になる。
func TestLandAnswer_EndedHoldsReturnTaskToInProgress(t *testing.T) {
	t.Parallel()
	for name, setup := range map[string]func(f *igLandFixture){
		"AC-147 delegate_ended": func(f *igLandFixture) { f.setPR(7, landPRURL, "open", integBranch) },
		"AC-148 merged_unverified": func(f *igLandFixture) {
			f.checks.prs["o/r#7"] = UpstreamPullRequestChecks{URL: landPRURL, State: "merged", Base: integBranch, HeadSHA: "other-head"}
		},
	} {
		setup := setup
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIgLandFixture(t)
			f.landed(t)
			setup(f)
			f.succeed(t)
			if f.status(t, f.cid) != StatusAwaitingHuman {
				t.Fatalf("status = %s", f.status(t, f.cid))
			}
			f.answer(t, "確かめた")
			if f.taskStatus(t) != "in_progress" {
				t.Fatalf("task = %s", f.taskStatus(t))
			}
		})
	}
}

// AC-139: 統合先を使う課題のタスクの実行は、反映の段が読む実行（commands.status を呼ぶ相手）から外れる。使わない課題の実行は読む（AC-227 の土台）。
func TestReleaseStage_DoesNotLoadIntegrationTaskRuns(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	cid := f.intID(t)
	load := func() *releaseRun {
		var run *releaseRun
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			var err error
			run, err = loadOpenRunInTx(context.Background(), tx, cid)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return run
	}
	if run := load(); run != nil {
		t.Fatalf("the release stage loaded the integration task run %s", run.row.ExternalRunID)
	}
	f.sql(t, `UPDATE challenge SET uses_integration = 0`)
	if run := load(); run == nil {
		t.Fatal("the release stage does not load the task run of a challenge without an integration branch")
	}
}

// AC-139・AC-227: 反映の段（ExecuteReleases）は、統合先を使う課題のタスクの実行を読まない（commands.status も resume も呼ばない）。
// 使わない課題では、同じ操作が S3a と同じにタスクの実行を読んで land を返す（TestReleaseLand_ReviewWaitWithLandOptionIsAnsweredWithLandAndTheApprovedRef）。
func TestReleaseStage_ExecuteReleasesDoesNotCallStatusForIntegrationTaskRuns(t *testing.T) {
	t.Parallel()
	for name, integ := range map[string]int{"without an integration branch": 0, "with an integration branch": 1} {
		integ := integ
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newLandFixture(t)
			f.sql(t, `UPDATE challenge SET uses_integration = ?`, integ)
			f.queueReview()
			statusesBefore := len(f.client.statuses)
			f.release(t)
			calls := len(f.client.statuses) - statusesBefore + len(f.client.resumes)
			if integ == 1 && calls != 0 {
				t.Fatalf("the release stage called the contract %d times for an integration task run", calls)
			}
			if integ == 0 && (len(f.client.statuses)-statusesBefore != 1 || len(f.client.resumes) != 1) {
				t.Fatalf("status calls = %d, resumes = %d, want 1 and 1 (S3a)", len(f.client.statuses)-statusesBefore, len(f.client.resumes))
			}
		})
	}
}

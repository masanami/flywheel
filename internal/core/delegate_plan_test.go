package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// このファイルは委譲の段の実行計画（候補ごとの並行な実行・枠の許可・予算ガード。#106）を、
// 実ストア・偽の判断／委譲の IF・偽の SlotGit で検証する（AC-279・297〜335・347〜351・366・369 の core 側）。
// 同じリポジトリの候補は直列化せず、枠に空きがあれば並行に起動する。

const planConnectorsJSON = `{
  "version": 1,
  "human_question_kinds": [],
  "connectors": [
    {"id": "pc", "form": "plugin", "permission_mode": "acceptEdits",
     "operations": [{"id": "impl", "invocation": "/h:impl {challenge_id}", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]},
    {"id": "plain", "form": "plugin", "permission_mode": "acceptEdits",
     "operations": [{"id": "impl", "invocation": "/h:impl {challenge_id}", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]}
  ],
  "repos": [
    {"name": "multi", "remote": "o/multi", "default_branch": "main", "connector": "pc", "slots": {"provider": "clone", "paths": ["m1", "m2", "m3"]}},
    {"name": "other", "remote": "o/other", "default_branch": "main", "connector": "pc", "slots": {"provider": "clone", "paths": ["o1", "o2"]}},
    {"name": "solo", "remote": "o/solo", "default_branch": "main", "connector": "pc", "slots": {"provider": "clone", "paths": ["s1"]}},
    {"name": "plain", "remote": "o/plain", "default_branch": "main", "connector": "plain", "slots": {"provider": "clone", "paths": ["p1", "p2"]}},
    {"name": "wt", "remote": "o/wt", "default_branch": "main", "connector": "pc", "slots": {"provider": "worktree", "base": "wt-base", "count": 2}},
    {"name": "excl", "remote": "o/excl", "default_branch": "main", "connector": "pc", "slots": {"provider": "clone", "paths": ["e1", "e2", "e3"]}}
  ]
}`

type eventLog struct {
	mu sync.Mutex
	ev []string
}

func (l *eventLog) add(e string) {
	l.mu.Lock()
	l.ev = append(l.ev, e)
	l.mu.Unlock()
}

type planFixture struct {
	*delegateFixture
	events *eventLog
	// maxUnfinished は偽の委譲の起動の時点で見えた、終了していない委譲の run の数の最大値。
	maxUnfinished atomic.Int64
	nextIssue     int
	// holdBlock・holdStarted は、次の 1 回の委譲の起動を止める（holdRunning が置く）。
	holdMu      sync.Mutex
	holdBlock   chan struct{}
	holdStarted chan struct{}
	// overlapWant は、重なりを期待するテストが置く同時の委譲の数（0 なら期待しない）。overlapReached は
	// 終了していない委譲の run がその数に達したときに閉じる（待ち合わせ。固定の時間の Sleep ではない）。
	overlapWant    int
	overlapReached chan struct{}
	overlapOnce    sync.Once
	// overlapWaited は、重なりが成り立った時点で取った waitedSignal（重なった委譲がそろって同じ知らせで終わる）。
	overlapWaited <-chan struct{}
	// holdForWaiter は、上限を守ること（重ならないこと）を見るテストが置く数。起動の順で最初の
	// holdForWaiter 件の偽の委譲は、その委譲の起動の後に別の候補が枠待ちに入るまで終わらない。
	// 上限を破る実装は、その間に次の委譲を起動して maxUnfinished が上限を超える。
	holdForWaiter int
	started       atomic.Int64
	// waitedMu・waited: 候補が枠待ちに入るたびに、Store の delegationWaited が waited を閉じて作り直す
	// （待っている偽の委譲のすべてへ知らせる）。
	waitedMu sync.Mutex
	waited   chan struct{}
}

// notifyWaited は、候補が枠待ちに入ったことを、それまでに waited を受け取った偽の委譲のすべてへ知らせる。
func (f *planFixture) notifyWaited() {
	f.waitedMu.Lock()
	defer f.waitedMu.Unlock()
	close(f.waited)
	f.waited = make(chan struct{})
}

// waitedSignal は、この時点より後に候補が枠待ちに入ると閉じるチャネルを返す。
func (f *planFixture) waitedSignal() <-chan struct{} {
	f.waitedMu.Lock()
	defer f.waitedMu.Unlock()
	return f.waited
}

// holdOverlap は、起動した委譲の run（n は終了していない委譲の run の数）を、待ち合わせが成り立つまで
// 終了させずに置く。固定の時間は待たない（10 秒は待ち合わせが成り立たないときの安全弁で、成功の経路では通らない）。
//   - 重なりを期待するテスト（overlapWant>0）は、終了していない委譲の run がその数に達するまで待つ。
//   - 上限を見るテスト（holdForWaiter>0）は、起動の順で最初の holdForWaiter 件が、その起動の後（重なりを
//     期待するテストでは、重なりが成り立った後）に別の候補が枠待ちに入るまで待つ（待つ候補は、上限が埋まっている間は再評価のたびに枠待ちに入り直すので、取りこぼさない）。
//     それより後の件は、待つ候補が残っていないので待たずに終わる。
func (f *planFixture) holdOverlap(n int64) {
	waited := f.waitedSignal()
	order := f.started.Add(1)
	if f.overlapWant > 0 {
		if n >= int64(f.overlapWant) {
			f.overlapOnce.Do(func() {
				f.overlapWaited = f.waitedSignal()
				close(f.overlapReached)
			})
		}
		select {
		case <-f.overlapReached:
		case <-time.After(10 * time.Second):
			return
		}
		// 重なった委譲は、そろって終了していない間に枠待ちに入った知らせで終わる（先に終わった 1 件が
		// 枠を空けて、後の件の待ち合わせの相手を消さないため）。
		waited = f.overlapWaited
	}
	if order <= int64(f.holdForWaiter) {
		select {
		case <-waited:
		case <-time.After(10 * time.Second):
		}
	}
}

func newPlanFixture(t *testing.T) *planFixture {
	t.Helper()
	f := &planFixture{delegateFixture: newDelegateFixture(t), events: &eventLog{}, nextIssue: 100, overlapReached: make(chan struct{}), waited: make(chan struct{})}
	f.s.delegationWait = 5 * time.Millisecond
	f.s.delegationWaited = f.notifyWaited
	conn, err := parseConnectorsDeclaration([]byte(planConnectorsJSON))
	if err != nil {
		t.Fatalf("parseConnectorsDeclaration: %v", err)
	}
	if err := conn.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	f.conn = conn
	for _, r := range conn.Repos {
		paths := r.Slots.Paths
		for _, rel := range paths {
			f.git.states[filepath.Join(f.s.Workspace(), rel)] = SlotTreeState{Exists: true, PointerOK: true, OriginURL: "https://github.com/" + r.Remote + ".git"}
		}
	}
	f.git.provisioned = SlotTreeState{Exists: true, PointerOK: true, OriginURL: "https://github.com/o/wt.git"}
	f.j3.onInvoke = nil
	f.deleg.onInvoke = func(in DelegateLaunchInput) {
		f.holdMu.Lock()
		block, started := f.holdBlock, f.holdStarted
		f.holdBlock, f.holdStarted = nil, nil
		f.holdMu.Unlock()
		if started != nil {
			started <- struct{}{}
		}
		if block != nil {
			<-block
		}
		f.events.add(fmt.Sprintf("delegate %d", in.SourceIssueNumber))
		var n int64
		_ = f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COUNT(*) FROM run WHERE kind = 'delegate' AND result IS NULL`).Scan(&n)
		})
		for {
			cur := f.maxUnfinished.Load()
			if n <= cur || f.maxUnfinished.CompareAndSwap(cur, n) {
				break
			}
		}
		f.holdOverlap(n)
	}
	return f
}

// candidate は repo の着手中の課題（取り込み元の Issue 番号 issue を持つ。0 なら対応無し）を作る。
func (f *planFixture) candidate(t *testing.T, repo, priority string, issue int) string {
	t.Helper()
	f.nextIssue++
	id := f.newInProgress(t, fmt.Sprintf("c%d", f.nextIssue), priority, planSpec(func(m map[string]any) { m["repo"] = repo }))
	if issue == 0 {
		return id
	}
	key := fmt.Sprintf("o/%s#%d", repo, issue)
	in := validBindingInput(key)
	th := simpleThread("body")
	in.UpstreamUpdatedAt = th.Issue.UpdatedAt
	if _, err := createSourceBindingForTest(f.s, id, time.Now(), in); err != nil {
		t.Fatal(err)
	}
	th.Issue.ExternalKey, th.Issue.Repo, th.Issue.Number, th.Issue.State = key, "o/"+repo, issue, "open"
	f.upstream.threads[key] = th
	return id
}

func (f *planFixture) in(cycleID string, id *string) DelegateInput {
	in := f.input(cycleID, id)
	return in
}

func (f *planFixture) runPlan(t *testing.T) (*DelegateResult, error) {
	t.Helper()
	return f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 300), nil))
}

func (f *planFixture) mustRunPlan(t *testing.T) *DelegateResult {
	t.Helper()
	res, err := f.runPlan(t)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	return res
}

func (f *planFixture) setSlotState(t *testing.T, base string, st slotState) {
	t.Helper()
	slots, err := f.s.ListSlots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, sl := range slots {
		if filepath.Base(sl.Path) == base {
			id, _ := parseSlotID(sl.ID)
			if err := f.s.mutateSlots(context.Background(), func(tx *sql.Tx) error {
				_, err := updateSlotState(context.Background(), tx, id, st, nil, "x")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("slot %s not found", base)
}

func (f *planFixture) ensure(t *testing.T, repo string) {
	t.Helper()
	r, _ := f.conn.findConnectorRepo(repo)
	if err := f.s.EnsureSlots(context.Background(), *r); err != nil {
		t.Fatal(err)
	}
}

func launchedIssues(f *planFixture) []int {
	var out []int
	for _, l := range f.deleg.launched() {
		out = append(out, l.SourceIssueNumber)
	}
	return out
}

// holdRunning は課題 id の委譲を別の周で起動したまま止める（終了していない委譲の run を作る）。
// 返る release を呼ぶと止めた委譲が終わる。
func (f *planFixture) holdRunning(t *testing.T, id string) (release func()) {
	t.Helper()
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	f.holdMu.Lock()
	f.holdBlock, f.holdStarted = block, started
	f.holdMu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 300), &id))
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the held delegation did not start")
	}
	var once sync.Once
	rel := func() {
		once.Do(func() {
			close(block)
			if err := <-done; err != nil {
				t.Errorf("held delegation: %v", err)
			}
		})
	}
	t.Cleanup(rel)
	return rel
}

// --- 枠待ち・優先度の順・上限（AC-297〜300） ---

func TestPlan_RateLimitedStopsTheWaitingCandidate(t *testing.T) {
	t.Parallel() // 共有の状態（パッケージ変数の書き換え・t.Setenv）が無い。Store ごとの actor・待ちの間隔を使う（flywheel#578）
	f := newPlanFixture(t)
	f.agent.MaxParallelRuns = 1
	f.holdForWaiter = 1 // 1 件目は、2 件目が枠待ちに入るまで終わらない
	f.candidate(t, "multi", "P0", 11)
	b := f.candidate(t, "multi", "P1", 12)
	f.deleg.result = JudgmentLaunchOutput{Result: RunResultErrored, RateLimited: true}
	res := f.mustRunPlan(t)
	if len(res.Items) != 1 || len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != b || res.NotStarted[0].Reason != NotStartedRateLimited {
		t.Errorf("result = %+v", res)
	}
}

func TestPlan_TwoCandidatesOfTheSameRepoRunConcurrently(t *testing.T) {
	t.Parallel() // 共有の状態（パッケージ変数の書き換え・t.Setenv）が無い。Store ごとの actor・待ちの間隔を使う（flywheel#578）
	f := newPlanFixture(t)
	f.overlapWant = 2 // 2 つの委譲が重なるまで待ち合わせる
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	f.mustRunPlan(t)
	if got := f.maxUnfinished.Load(); got != 2 {
		t.Errorf("max unfinished delegate runs = %d, want 2", got)
	}
}

func TestPlan_MaxParallelRunsOne_SecondCandidateWaitsForTheFirst(t *testing.T) {
	t.Parallel() // 共有の状態（パッケージ変数の書き換え・t.Setenv）が無い。Store ごとの actor・待ちの間隔を使う（flywheel#578）
	f := newPlanFixture(t)
	f.agent.MaxParallelRuns = 1
	f.holdForWaiter = 1 // 1 件目は、2 件目が枠待ちに入るまで終わらない
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	res := f.mustRunPlan(t)
	if len(res.Items) != 2 || len(res.NotStarted) != 0 {
		t.Fatalf("result = %+v, want both launched in the same cycle", res)
	}
	if got := f.maxUnfinished.Load(); got != 1 {
		t.Errorf("max unfinished = %d, want 1", got)
	}
	runs := f.delegateRuns(t)
	if runs[1].StartedAt.Before(*runs[0].EndedAt) {
		t.Error("the second group started before the first run ended")
	}
}

func TestPlan_GlobalLimitAcrossRepos(t *testing.T) {
	t.Parallel() // 共有の状態（パッケージ変数の書き換え・t.Setenv）が無い。Store ごとの actor・待ちの間隔を使う（flywheel#578）
	f := newPlanFixture(t)
	f.overlapWant = 2   // 2 つの委譲が重なるまで待ち合わせる
	f.holdForWaiter = 2 // 重なった 2 件は、3 件目が枠待ちに入るまで終わらない
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	f.candidate(t, "other", "P1", 21)
	res := f.mustRunPlan(t)
	if len(res.Items) != 3 {
		t.Fatalf("items = %+v", res.Items)
	}
	if got := f.maxUnfinished.Load(); got > 2 || got < 2 {
		t.Errorf("max unfinished = %d, want exactly 2 (max_parallel_runs)", got)
	}
}

func TestPlan_UsableSlotsLimitTheRepo(t *testing.T) {
	t.Parallel() // 共有の状態（パッケージ変数の書き換え・t.Setenv）が無い。Store ごとの actor・待ちの間隔を使う（flywheel#578）
	f := newPlanFixture(t)
	f.holdForWaiter = 1 // 1 件目は、2 件目が枠待ちに入るまで終わらない
	f.ensure(t, "multi")
	f.setSlotState(t, "m2", slotStateNeedsAttention)
	f.setSlotState(t, "m3", slotStateNeedsAttention)
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	res := f.mustRunPlan(t)
	if len(res.Items) != 2 {
		t.Fatalf("items = %+v, want both launched one after the other", res.Items)
	}
	if got := f.maxUnfinished.Load(); got != 1 {
		t.Errorf("max unfinished = %d, want 1", got)
	}
}

func TestPlan_NoUsableSlot_NotStartedSlotUnavailable_WithoutWaiting(t *testing.T) {
	t.Parallel() // 共有の状態（パッケージ変数の書き換え・t.Setenv）が無い。Store ごとの actor・待ちの間隔を使う（flywheel#578）
	f := newPlanFixture(t)
	f.ensure(t, "solo")
	f.setSlotState(t, "s1", slotStateNeedsAttention)
	id := f.candidate(t, "solo", "P1", 11)
	res := f.mustRunPlan(t)
	if len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != id || res.NotStarted[0].Reason != NotStartedSlotUnavailable {
		t.Errorf("not started = %+v", res.NotStarted)
	}
}

func TestPlan_UnprovisionedWorktreeSlotsCount(t *testing.T) {
	t.Parallel() // 共有の状態（パッケージ変数の書き換え・t.Setenv）が無い。Store ごとの actor・待ちの間隔を使う（flywheel#578）
	f := newPlanFixture(t)
	f.overlapWant = 2 // 2 つの委譲が重なるまで待ち合わせる
	f.candidate(t, "wt", "P1", 11)
	f.candidate(t, "wt", "P1", 12)
	res := f.mustRunPlan(t)
	if len(res.Items) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if got := f.maxUnfinished.Load(); got != 2 {
		t.Errorf("max unfinished = %d, want 2 (count 2, not provisioned yet)", got)
	}
}

func TestPlan_WaitingCandidatesStartInPriorityOrder(t *testing.T) {
	t.Parallel() // 共有の状態（パッケージ変数の書き換え・t.Setenv）が無い。Store ごとの actor・待ちの間隔を使う（flywheel#578）
	f := newPlanFixture(t)
	f.agent.MaxParallelRuns = 1
	f.holdForWaiter = 2 // 先の 2 件は、後の候補が枠待ちに入るまで終わらない
	f.candidate(t, "multi", "P2", 11)
	f.candidate(t, "other", "P0", 21)
	f.candidate(t, "multi", "P1", 12)
	f.mustRunPlan(t)
	if got, want := launchedIssues(f), []int{21, 12, 11}; !reflect.DeepEqual(got, want) {
		t.Errorf("launch order = %v, want %v", got, want)
	}
}

func TestPlan_CycleBudgetAndRunBudget_AfterWaiting(t *testing.T) {
	t.Parallel() // 共有の状態（パッケージ変数の書き換え・t.Setenv）が無い。Store ごとの actor・待ちの間隔を使う（flywheel#578）
	f := newPlanFixture(t)
	f.agent.MaxParallelRuns = 1
	f.holdForWaiter = 1 // 1 件目は、2 件目が枠待ちに入るまで終わらない
	f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	// 最初の起動（J3 の 3 ＋ 実装枠 50 ＋ レビュー対応枠 30）だけが入る周の上限。2 件目は、1 件目の費用
	// （J3 の 3 ＋ 委譲の 2.5）が数えられた後では入らない。
	res, err := f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 88), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != b || res.NotStarted[0].Reason != NotStartedCycleBudget {
		t.Errorf("result = %+v", res)
	}
}

// --- 予算・記録（AC-334・335・347〜351） ---

// 実装枠の残りが起動の最小額に満たない候補は run_budget で起動しない（J3 の費用を払う前）。
func TestPlan_RunBudget_NotStarted(t *testing.T) {
	t.Parallel() // 共有の状態（パッケージ変数の書き換え・t.Setenv）が無い。Store ごとの actor・待ちの間隔を使う（flywheel#578）
	f := newPlanFixture(t)
	f.agent.MaxParallelRuns = 1
	f.candidate(t, "multi", "P1", 11)
	id := f.newInProgress(t, "lowbudget", "P2", planSpec(func(m map[string]any) { m["repo"] = "multi"; m["budget_impl_usd"] = 0.5 }))
	res := f.mustRunPlan(t)
	var got NotStartedReason
	for _, ns := range res.NotStarted {
		if ns.ChallengeID == id {
			got = ns.Reason
		}
	}
	if got != NotStartedRunBudget {
		t.Errorf("not started = %+v, want %s run_budget", res.NotStarted, id)
	}
}

// 使えるスロットが 1 本のリポジトリ（worktree）で、同じリポジトリの 2 課題を別々の呼び出しで起動すると、
// 後の呼び出しは slot_unavailable で終わる（待たない。AC-1120）。
func TestPlan_ExplicitID_SingleSlot_SlotUnavailable(t *testing.T) {
	t.Parallel() // 共有の状態（パッケージ変数の書き換え・t.Setenv）が無い。Store ごとの actor・待ちの間隔を使う（flywheel#578）
	f := newPlanFixture(t)
	f.ensure(t, "wt")
	slots, _ := f.s.ListSlots(context.Background())
	f.setSlotState(t, filepath.Base(slots[len(slots)-1].Path), slotStateNeedsAttention) // 使えるスロットは 1 本
	running := f.candidate(t, "wt", "P1", 5)
	other := f.candidate(t, "wt", "P1", 6)
	release := f.holdRunning(t, running)
	defer release()
	_, err := f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 300), &other))
	if !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("err = %v, want ErrSlotUnavailable", err)
	}
}

// 契約の run（form: cli）の許可は、待ちの列の優先度・ID の昇順を守る: 前にいる claude の課題が今すぐ許可できる分の枠は先に譲り、
// 譲ってもなお枠が残るなら許可する（契約は待たないので、余裕があるのに譲るだけで拒否しない）。
func TestAdmitContract_YieldsOnlyTheSlotsEarlierWaitersTake(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	conn := &ConnectorsDeclaration{
		Connectors: []Connector{{ID: "pc", Form: ConnectorFormPlugin}, {ID: "cl", Form: ConnectorFormCLI}},
		Repos: []ConnectorRepo{
			{Name: "multi", Connector: "pc", Slots: ConnectorRepoSlots{Provider: "clone", Paths: []string{"m1", "m2"}}},
			{Name: "clirepo", Connector: "cl"},
		},
	}
	claudeRepo, cliRepo := conn.Repos[0], conn.Repos[1]
	cases := []struct {
		name         string
		max          int
		claudeRank   int
		wantAdmitted bool
	}{
		{"last slot, earlier claude waiter", 1, 1, false},
		{"spare slot after the earlier claude waiter", 2, 1, true},
		{"contract waiter is first", 1, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sch := newDelegationScheduler(f.s, DelegateInput{AgentDecl: &AgentDeclaration{MaxParallelRuns: tc.max}, ConnDecl: conn})
			claudeW := sch.newWaiter(claudeRepo, planCandidate{Rank: tc.claudeRank, Seq: 1})
			cliW := sch.newWaiter(cliRepo, planCandidate{Rank: 2, Seq: 2})
			sch.enqueue(claudeW)
			sch.enqueue(cliW)
			adm, err := (&delegateRun{sch: sch, w: cliW}).admitContract(context.Background())
			if tc.wantAdmitted {
				if err != nil {
					t.Fatalf("err = %v, want admitted", err)
				}
				adm.finish()
			} else if !errors.Is(err, ErrSlotUnavailable) {
				t.Fatalf("err = %v, want ErrSlotUnavailable", err)
			}
		})
	}
}

// 同じリポジトリの契約（form: cli）の候補が 1 回の委譲の段で並ぶとき、許可した候補は待ちの列から外れる。
// 外れていないと、許可済みで start の終わっていない候補が「前にいて今すぐ許可できる候補」として数えられ、
// 実行中の数との二重の数えになって、枠に空きがあるのに後ろの候補が slot_unavailable で見送られる
// （出どころ: core の見直しの区切り S1・全体のレビュー）。
func TestAdmitContract_AdmittedCandidateLeavesTheQueue_NoDoubleCount(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	conn := &ConnectorsDeclaration{
		Connectors: []Connector{{ID: "cl", Form: ConnectorFormCLI}},
		Repos:      []ConnectorRepo{{Name: "clirepo", Connector: "cl"}},
	}
	sch := newDelegationScheduler(f.s, DelegateInput{AgentDecl: &AgentDeclaration{MaxParallelRuns: 8}, ConnDecl: conn})
	sch.pre = 5 // 実行中（記録前の許可済み）の起動が 5 件
	var waiters []*schedWaiter
	for i := 1; i <= 3; i++ {
		w := sch.newWaiter(conn.Repos[0], planCandidate{Rank: 1, Seq: int64(i)})
		sch.enqueue(w)
		waiters = append(waiters, w)
	}
	for i, w := range waiters {
		adm, err := (&delegateRun{sch: sch, w: w}).admitContract(context.Background())
		if err != nil {
			t.Fatalf("候補 %d 件目: err = %v, want 許可（上限 8・実行中 5+%d）", i+1, err, i)
		}
		if w.queued {
			t.Errorf("候補 %d 件目: 許可した後も待ちの列に残っている", i+1)
		}
		_ = adm
	}
	// executePlan の goroutine の defer による外し直しが重なっても安全であること。
	for _, w := range waiters {
		sch.dequeue(w)
	}
	// 上限いっぱい（5+3=8）なら、4 件目は見送る。
	extra := sch.newWaiter(conn.Repos[0], planCandidate{Rank: 1, Seq: 4})
	sch.enqueue(extra)
	if _, err := (&delegateRun{sch: sch, w: extra}).admitContract(context.Background()); !errors.Is(err, ErrSlotUnavailable) {
		t.Errorf("4 件目: err = %v, want ErrSlotUnavailable", err)
	}
}

// countRuns は run の表から where に当たる行の数を返す。
func (f *planFixture) countRuns(t *testing.T, where string) int {
	t.Helper()
	var n int
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM run WHERE ` + where).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// 同じリポジトリの着手中の課題 2 件は、記録した前提が無く、枠に空きがあれば、1 回の委譲の段で両方起動する。
// 予測も J9 も起動しない。
func TestPlan_SameRepoCandidates_BothLaunchInOnePass_WithoutPredictionOrJ9(t *testing.T) {
	t.Parallel()
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	res := f.mustRunPlan(t)
	if len(res.Items) != 2 || res.Items[0].ChallengeID != a || res.Items[1].ChallengeID != b || len(res.NotStarted) != 0 {
		t.Fatalf("result = items %+v, not started %+v; want both launched", res.Items, res.NotStarted)
	}
	if got := f.countRuns(t, `kind = 'predict'`); got != 0 {
		t.Errorf("predict runs = %d, want 0", got)
	}
	if got := f.countRuns(t, `judgment = 'J9'`); got != 0 {
		t.Errorf("J9 runs = %d, want 0", got)
	}
}

// 記録した前提が終わっていない課題は、委譲の段で起動しない（前提の無い課題は起動する）。
func TestPlan_UnmetPrerequisite_IsNotLaunched(t *testing.T) {
	t.Parallel()
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	if _, err := f.s.AddDependencies(context.Background(), ChannelCLI, b, []string{a}); err != nil {
		t.Fatal(err)
	}
	res := f.mustRunPlan(t)
	if len(res.Items) != 1 || res.Items[0].ChallengeID != a {
		t.Fatalf("items = %+v, want only %s", res.Items, a)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != b || res.NotStarted[0].Reason != NotStartedDependency {
		t.Fatalf("not started = %+v, want %s dependency", res.NotStarted, b)
	}
}

// 同じリポジトリに走っている委譲があっても、ID を指定した run は、枠に空きがあれば起動する。
func TestPlan_ExplicitID_LaunchesWhileSameRepoHasARunningDelegation(t *testing.T) {
	t.Parallel()
	f := newPlanFixture(t)
	running := f.candidate(t, "multi", "P1", 11)
	other := f.candidate(t, "multi", "P1", 12)
	release := f.holdRunning(t, running)
	defer release()
	res, err := f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 300), &other))
	if err != nil {
		t.Fatalf("RunDelegation(%s): %v", other, err)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != other || len(res.NotStarted) != 0 {
		t.Fatalf("result = items %+v, not started %+v; want %s launched", res.Items, res.NotStarted, other)
	}
}

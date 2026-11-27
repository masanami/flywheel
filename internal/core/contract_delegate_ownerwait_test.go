package core

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// このファイルは、オーナーの回答待ち・レビュー待ちの契約の run を同時委譲数に数えない扱い（AC-50〜AC-55。core の見直しの区切り S1〔docs/features/core-redesign.md。番号は masanami/flywheel#732 の受入基準の並び順〕）を、
// 委譲の段（RunDelegation）を実際に通して検証する。実ストア・偽の ContractClient。

// ownerWaitConnectorsJSON は、別のリポジトリ（other-repo）を足した宣言。2 つの課題を別々のリポジトリへ向け、
// 起動を止めるものを同時委譲数（max_parallel_runs）だけにする。
var ownerWaitConnectorsJSON = strings.Replace(reviewConnectorsJSON, `{"name": "main-repo"`,
	`{"name": "other-repo", "remote": "o/s", "default_branch": "main", "connector": "tool", "slots": {"provider": "clone", "paths": ["slot-s"]}},
    {"name": "main-repo"`, 1)

// perRunClient は run ごとに実行 ID（ext-1, ext-2, …）と状態を持つ偽の ContractClient。
// status は実行 ID ごとの状態を返し、resume はその run を running へ戻す。
type perRunClient struct {
	n       int
	state   map[string]ContractLaunchOutput
	starts  [][]string
	resumes [][]string
}

func newPerRunClient() *perRunClient { return &perRunClient{state: map[string]ContractLaunchOutput{}} }

func (c *perRunClient) set(runID string, out ContractLaunchOutput) {
	out.Status.RunID = runID
	c.state[runID] = out
}

func (c *perRunClient) CheckFlow(_ context.Context, in ContractLaunchInput) (FlowCheckOutput, error) {
	return passingFlowCheck(in), nil
}

func (c *perRunClient) Start(_ context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	c.starts = append(c.starts, in.Command)
	c.n++
	id := fmt.Sprintf("ext-%d", c.n)
	c.set(id, contractOut(ContractStateRunning, nil, nil))
	return c.state[id], nil
}

func (c *perRunClient) Status(_ context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	return c.state[in.Command[len(in.Command)-1]], nil
}

func (c *perRunClient) Resume(_ context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	c.resumes = append(c.resumes, in.Command)
	c.set(in.Command[2], contractOut(ContractStateRunning, nil, nil))
	return c.state[in.Command[2]], nil
}

func (c *perRunClient) Cancel(_ context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	return c.state[in.Command[2]], nil
}

// ownerWaitFixture は、同時委譲数の上限を持つ 2 リポジトリの宣言と、run ごとの偽のクライアント。
// first は main-repo の課題（フィクスチャが作る着手中の課題）。
type ownerWaitFixture struct {
	*contractFixture
	pc    *perRunClient
	first string
}

func newOwnerWaitFixture(t *testing.T, maxParallel int) *ownerWaitFixture {
	t.Helper()
	cf := newContractFixtureWith(t, ownerWaitConnectorsJSON)
	cf.agent.MaxParallelRuns = maxParallel
	// この試験が確かめるのはオーナーの回答待ちの扱い。回答の待ちをスーパーバイザーが先に受けると、判断の呼び出し（f.j3）に数えが混ざるので切る。
	cf.agent.Supervisor = false
	return &ownerWaitFixture{contractFixture: cf, pc: newPerRunClient(), first: cf.cid}
}

// stage は委譲の段（RunDelegation。対象は自動で選ぶ）を 1 回通す。
func (f *ownerWaitFixture) stage(t *testing.T) *DelegateResult {
	t.Helper()
	in := f.input(f.cycle(t, 300), nil)
	in.Contract = f.pc
	res, err := f.s.RunDelegation(context.Background(), in)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	return res
}

// startFirst は first の契約の run（ext-1）を委譲の段で起動する。
func (f *ownerWaitFixture) startFirst(t *testing.T) {
	t.Helper()
	f.stage(t)
	if want := [][]string{{"tool", "start", "--challenge", f.first}}; !reflect.DeepEqual(f.pc.starts, want) {
		t.Fatalf("starts = %v, want %v", f.pc.starts, want)
	}
}

// holdFirst は first の run（ext-1）をオーナーの回答待ち（run は waiting・課題は保留）にする。
func (f *ownerWaitFixture) holdFirst(t *testing.T) {
	t.Helper()
	f.pc.set("ext-1", waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "human", Text: "どうする"}, nil))
	f.stage(t)
	f.assertRun(t, f.first, "waiting", StatusAwaitingHuman)
}

// answerFirst は first の保留へ、回答の口（PrepareAnswer・ExecuteAnswer。flywheel answer が呼ぶもの）で回答する。
func (f *ownerWaitFixture) answerFirst(t *testing.T) {
	t.Helper()
	p, err := f.s.PrepareAnswer(context.Background(), f.first, "ANSWER-MARKER")
	if err != nil {
		t.Fatalf("PrepareAnswer: %v", err)
	}
	if _, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.first, ExpectedVersion: p.Version, Answer: "ANSWER-MARKER"},
		verifiedAttestationForTest(t, f.first)); err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}
	if got := f.status(t, f.first); got != StatusInProgress {
		t.Fatalf("status = %s after the answer, want in_progress", got)
	}
}

// newOther は other-repo の着手中の課題を作る。
func (f *ownerWaitFixture) newOther(t *testing.T) string {
	t.Helper()
	return f.newInProgress(t, "T2", "P2", planSpec(func(m map[string]any) { m["repo"] = "other-repo" }))
}

// assertRun は課題の開いた契約の run の状態と、課題の状態を確かめる。
func (f *ownerWaitFixture) assertRun(t *testing.T, id, wantState string, wantStatus Status) {
	t.Helper()
	n, _ := parseChallengeID(id)
	var state string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT state FROM contract_run WHERE challenge_id = ? AND ended_at IS NULL`, n).Scan(&state)
	}); err != nil {
		t.Fatalf("read the open contract run of %s: %v", id, err)
	}
	if got := f.status(t, id); state != wantState || got != wantStatus {
		t.Fatalf("%s: contract run = %s, challenge = %s, want %s / %s", id, state, got, wantState, wantStatus)
	}
}

// startedFor は、課題 id の start が呼ばれた回数。
func (f *ownerWaitFixture) startedFor(id string) int {
	n := 0
	for _, s := range f.pc.starts {
		if s[len(s)-1] == id {
			n++
		}
	}
	return n
}

func notStartedReason(res *DelegateResult, id string) (NotStartedReason, bool) {
	for _, ns := range res.NotStarted {
		if ns.ChallengeID == id {
			return ns.Reason, true
		}
	}
	return "", false
}

// 同時委譲数の上限 1 で、別の課題の契約の run が 1 本あるとき、委譲の段が別の課題を起動するかどうか（AC-50〜AC-53）。
func TestRunDelegation_OwnerAndReviewWaitsDoNotFillMaxParallelRuns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// prepare は first の run を、基準が言う状態にする（起動は済んでいる）。
		prepare   func(t *testing.T, f *ownerWaitFixture)
		wantStart bool
	}{
		{"AC-50 オーナーの回答待ち（run は waiting・課題は保留）のとき、別の課題の委譲が起動する", func(t *testing.T, f *ownerWaitFixture) {
			f.holdFirst(t)
		}, true},
		{"AC-51 レビュー待ち（求めている操作は review・課題は検証中）のとき、別の課題の委譲が起動する", func(t *testing.T, f *ownerWaitFixture) {
			f.pc.set("ext-1", reviewWaitOut())
			f.stage(t)
			f.assertRun(t, f.first, "waiting", StatusVerifying)
		}, true},
		{"AC-52 課題が着手中で run が動いているとき、別の課題の委譲は起動しない", func(t *testing.T, f *ownerWaitFixture) {
			f.assertRun(t, f.first, "running", StatusInProgress)
		}, false},
		{"AC-53 回答を受けて課題が着手中へ戻ったとき、別の課題の委譲は起動しない", func(t *testing.T, f *ownerWaitFixture) {
			f.holdFirst(t)
			f.answerFirst(t)
			// 回答の直後（再開のコマンドを送る前。run は waiting のまま・課題は着手中）から数える。
			f.assertRun(t, f.first, "waiting", StatusInProgress)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newOwnerWaitFixture(t, 1)
			f.startFirst(t)
			tc.prepare(t, f)
			other := f.newOther(t)
			res := f.stage(t)
			reason, notStarted := notStartedReason(res, other)
			if tc.wantStart {
				if f.startedFor(other) != 1 || notStarted {
					t.Fatalf("starts = %v, not_started = %+v, want %s started", f.pc.starts, res.NotStarted, other)
				}
				f.assertRun(t, other, "running", StatusInProgress)
				return
			}
			if f.startedFor(other) != 0 || !notStarted || reason != NotStartedSlotUnavailable {
				t.Fatalf("starts = %v, not_started = %+v, want %s not started with %s", f.pc.starts, res.NotStarted, other, NotStartedSlotUnavailable)
			}
		})
	}
}

// AC-54: 同時委譲数の上限 1 で動いている契約の run が 1 本あるときに、別の課題の回答待ちの run へ回答すると、
// 次の委譲の段がその run へ再開のコマンド（宣言の resume）を送る（上限で止まらない）。
func TestRunDelegation_AnswerToAnOwnerWaitIsResumedEvenWhenMaxParallelRunsIsFull(t *testing.T) {
	t.Parallel()
	f := newOwnerWaitFixture(t, 1)
	f.startFirst(t)
	f.holdFirst(t)
	other := f.newOther(t)
	f.stage(t) // first が回答待ちの間に、other の run（ext-2）が起動して上限 1 を埋める。
	f.assertRun(t, other, "running", StatusInProgress)
	f.answerFirst(t)
	if len(f.pc.resumes) != 0 {
		t.Fatalf("resumes = %v, want none before the next delegation stage", f.pc.resumes)
	}
	f.stage(t)
	if want := [][]string{{"tool", "resume", "ext-1", "--choice", "ANSWER-MARKER", "--note", ""}}; !reflect.DeepEqual(f.pc.resumes, want) {
		t.Fatalf("resumes = %v, want %v", f.pc.resumes, want)
	}
	f.assertRun(t, f.first, "running", StatusInProgress)
	f.assertRun(t, other, "running", StatusInProgress)
	if len(f.pc.starts) != 2 {
		t.Fatalf("starts = %v, want no new start", f.pc.starts)
	}
}

// AC-55: 回答を受けて着手中へ戻り、契約の run がまだ終わっていない課題へ、新しい委譲を起動しない。
// 上限は 2 にして、止めているのが同時委譲数ではなく課題ごとの「実行中か」の判定であることを確かめる。
func TestRunDelegation_AnsweredChallengeWithAnOpenContractRunIsNotDelegatedAgain(t *testing.T) {
	t.Parallel()
	f := newOwnerWaitFixture(t, 2)
	f.startFirst(t)
	f.holdFirst(t)
	// 保留の間も起動しない。
	f.stage(t)
	f.answerFirst(t)
	// 回答の後の段（resume を送る段）と、その次の段（run は running）のどちらでも起動しない。
	for i := 0; i < 2; i++ {
		f.stage(t)
		f.assertRun(t, f.first, "running", StatusInProgress)
	}
	if len(f.pc.resumes) != 1 {
		t.Fatalf("resumes = %v, want exactly one", f.pc.resumes)
	}
	if len(f.pc.starts) != 1 || len(f.delegateRuns(t)) != 0 || f.j3Calls() != 0 {
		t.Fatalf("starts = %v, delegate runs = %d, j3 calls = %d, want no new delegation for %s", f.pc.starts, len(f.delegateRuns(t)), f.j3Calls(), f.first)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM contract_run`); n != 1 {
		t.Fatalf("contract runs = %d, want 1", n)
	}
}

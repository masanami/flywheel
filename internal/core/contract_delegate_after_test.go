package core

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

// このファイルは form: cli の接続ツールの委譲の後半（判断待ちの写し・resume・予算の上限での中止・検証中への移り変わり・
// 計画の委譲と周からの状態の確認・成果物の見え方）を、実ストア・偽の ContractClient で検証する。

func waitingOut(ra *ContractRequestedAction, cost *float64) ContractLaunchOutput {
	return contractOut(ContractStateWaiting, cost, func(st *ContractStatus) { st.RequestedAction = ra })
}

// decider と kind による写し分け: observe は読み直し、それ以外（human・parent・any の answer・approve）はオーナーの保留。
func TestPollContractRuns_WaitingIsMappedByKindAndDecider(t *testing.T) {
	cases := []struct {
		kind, decider string
		wantHold      bool
	}{
		{"approve", "human", true}, {"answer", "human", true},
		{"answer", "parent", true}, {"approve", "parent", true}, // J4 ができるまではオーナーの保留
		{"approve", "any", true}, {"answer", "any", true}, // 仕様に写し方の無い any は安全側の人の承認
		{"observe", "human", false}, {"observe", "parent", false}, {"observe", "any", false},
	}
	for _, c := range cases {
		t.Run(c.kind+"/"+c.decider, func(t *testing.T) {
			f := newContractFixture(t)
			if _, err := f.start(t); err != nil {
				t.Fatal(err)
			}
			f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: c.kind, Decider: c.decider, Text: "TEXT-MARKER"}, nil)}
			reps := f.poll(t)
			if len(reps) != 1 || reps[0].Held != c.wantHold {
				t.Fatalf("report = %+v, want held=%v", reps, c.wantHold)
			}
			want, wantHolds := StatusInProgress, int64(0)
			if c.wantHold {
				want, wantHolds = StatusAwaitingHuman, 1
			}
			if got := f.status(t, f.cid); got != want {
				t.Fatalf("status = %s, want %s", got, want)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != wantHolds {
				t.Fatalf("holds = %d, want %d", n, wantHolds)
			}
		})
	}
}

// requested_action の選択肢は、保留の問いに選択肢として並ぶ。
func TestPollContractRuns_WaitingHoldShowsTheOptions(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "parent", Text: "どちらにするか", Options: []string{"OPT-A", "OPT-B"}}, nil)}
	f.poll(t)
	var q string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT question FROM hold`).Scan(&q) }); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"どちらにするか", "選択肢:", "1. OPT-A", "2. OPT-B"} {
		if !strings.Contains(q, w) {
			t.Fatalf("question = %q, want it to contain %q", q, w)
		}
	}
}

// 保留への答えは commands.resume で返り、同じ答えを二重に返さない。
func TestPollContractRuns_AnswerIsReturnedByResumeOnce(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "approve", Decider: "human", Text: "承認して"}, nil)}
	f.poll(t)
	// 答える前の周は resume を呼ばない。
	f.poll(t)
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want none before the answer", f.client.resumes)
	}
	f.answerLatestHold(t, f.cid, "ANSWER-MARKER")
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	reps := f.poll(t)
	if len(reps) != 1 || reps[0].State != ContractStateRunning {
		t.Fatalf("report = %+v, want running after resume（resume はすぐ戻る）", reps)
	}
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0], []string{"tool", "resume", "ext-1", "--answer", "ANSWER-MARKER"}) {
		t.Fatalf("resumes = %v, want one resume for ext-1 carrying the answer", f.client.resumes)
	}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, nil)}
	f.poll(t)
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1（答えは二重に返さない）", len(f.client.resumes))
	}
}

// resume の引数の {answer} に答えが入る（引数の要素の数は変わらない）。
func TestFillContractArgs_AnswerIsSubstitutedLast(t *testing.T) {
	got, err := fillContractArgs([]string{"t", "resume", "{run_id}", "--answer={answer}"}, "C-1", nil, "ext-9", "a {run_id} b")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"t", "resume", "ext-9", "--answer=a {run_id} b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

// 合計を上回ったら cancel で中止して保留にする。同額では中止しない。
func TestPollContractRuns_OverBudgetCancelsAndHolds(t *testing.T) {
	f := newContractFixture(t) // run の上限額 5 USD
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, float64Ptr(5), nil)}
	f.poll(t)
	if len(f.client.cancels) != 0 {
		t.Fatalf("cancels = %v, want none at exactly the budget", f.client.cancels)
	}
	f.client.cancelOut = contractOut(ContractStateCancelled, float64Ptr(5.5), nil)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, float64Ptr(5.5), nil)}
	reps := f.poll(t)
	if len(f.client.cancels) != 1 || !reflect.DeepEqual(f.client.cancels[0], []string{"tool", "cancel", "ext-1"}) {
		t.Fatalf("cancels = %v, want one cancel for ext-1", f.client.cancels)
	}
	if len(reps) != 1 || !reps[0].Held || f.status(t, f.cid) != StatusAwaitingHuman {
		t.Fatalf("report = %+v status = %s, want held", reps, f.status(t, f.cid))
	}
	var q string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT question FROM hold`).Scan(&q) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, "上回った") {
		t.Fatalf("question = %q, want it to say the budget was exceeded", q)
	}
	if state, _, _, ended := f.contractRow(t); state != "cancelled" || !ended {
		t.Fatalf("contract_run state=%s ended=%v, want cancelled and ended", state, ended)
	}
}

// 中止が失敗したときも保留にし、費用が増え続けうることを問いに書く。自動ではやり直さない。
func TestPollContractRuns_CancelFailureHoldsAndIsNotRetried(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "exit 1"}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, float64Ptr(6), nil)}
	reps := f.poll(t)
	if len(reps) != 1 || !reps[0].Held || f.status(t, f.cid) != StatusAwaitingHuman {
		t.Fatalf("report = %+v, want held after the failed cancel", reps)
	}
	var q string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT question FROM hold`).Scan(&q) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, "増え続け") || !strings.Contains(q, "exit 1") {
		t.Fatalf("question = %q, want the failed cancel and the growing cost", q)
	}
	f.poll(t)
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %d, want 1（自動ではやり直さない）", len(f.client.cancels))
	}
}

// succeeded で課題は着手中から検証中へ進み、成果物が J5 から見える。
func TestPollContractRuns_SucceededMovesTheChallengeToVerifyingAndJ5SeesTheArtifacts(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) {
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/9"}}
	})}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	vc, err := f.s.loadJ5Context(context.Background(), f.detail(t, f.cid).Challenge)
	if err != nil {
		t.Fatalf("loadJ5Context: %v", err)
	}
	if vc.LastDelegate == nil || vc.LastDelegate.Kind != runKindContract || len(vc.Artifacts) != 1 || vc.Artifacts[0].Ref != "https://github.com/o/r/pull/9" {
		t.Fatalf("J5 context: last=%+v artifacts=%+v, want the contract run's PR", vc.LastDelegate, vc.Artifacts)
	}
}

// 委譲の run と契約の run のどちらから成果物を取っても、J5 の入力は同じになる。
func TestLoadJ5Context_SameForDelegateAndContractRuns(t *testing.T) {
	ctx := context.Background()
	f := newContractFixture(t)
	other := f.newInProgress(t, "D", "P2", planSpec(nil))
	const pr = "https://github.com/o/r/pull/7"
	cid, _ := parseChallengeID(f.cid)
	oid, _ := parseChallengeID(other)
	insert := func(challenge int64, kind runKind) {
		err := f.s.db.Write(ctx, func(tx *sql.Tx) error {
			now := time.Now()
			row, err := insertRun(ctx, tx, challenge, insertRunInput{Kind: kind, ChallengeVersion: 1, SessionID: "11111111-1111-4111-8111-11111111111" + string(kind[0]),
				PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1, BudgetBucket: budgetBucketImpl})
			if err != nil {
				return err
			}
			id, _ := parseRunID(row.ID)
			verified := now
			if _, err := insertRunArtifact(ctx, tx, id, insertRunArtifactInput{Kind: artifactKindPR, Ref: pr, VerifiedAt: &verified}); err != nil {
				return err
			}
			_, err = updateRunEnd(ctx, tx, id, updateRunEndInput{EndedAt: now, Result: RunResultSucceeded})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(cid, runKindContract)
	insert(oid, runKindDelegate)
	load := func(id string) *j5Context {
		vc, err := f.s.loadJ5Context(ctx, f.detail(t, id).Challenge)
		if err != nil {
			t.Fatalf("loadJ5Context(%s): %v", id, err)
		}
		return vc
	}
	a, b := load(f.cid), load(other)
	if a.LastDelegate == nil || b.LastDelegate == nil || len(a.Artifacts) != 1 || len(b.Artifacts) != 1 || a.Artifacts[0].Ref != b.Artifacts[0].Ref || a.Artifacts[0].Kind != b.Artifacts[0].Kind {
		t.Fatalf("contract: %+v %+v / delegate: %+v %+v, want the same artifacts", a.LastDelegate, a.Artifacts, b.LastDelegate, b.Artifacts)
	}
}

// cli 形態の操作の計画で委譲するとき、claude の子も J3 も起動せず、宣言の start を呼んで実行 ID を記録し、その周の委譲を終える。
func TestRunDelegation_CLIPlanStartsTheContractWithoutAChild(t *testing.T) {
	f := newContractFixture(t)
	in := f.input(f.cycle(t, 300), idPtr(f.cid))
	in.Contract = f.client
	res, err := f.s.RunDelegation(context.Background(), in)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Result != RunResultSucceeded {
		t.Fatalf("items = %+v, want one succeeded item", res.Items)
	}
	if want := []string{"tool", "start", "--challenge", f.cid}; len(f.client.starts) != 1 || !reflect.DeepEqual(f.client.starts[0], want) {
		t.Fatalf("starts = %v, want %v", f.client.starts, want)
	}
	if f.j3Calls() != 0 || len(f.delegateRuns(t)) != 0 {
		t.Fatalf("j3 calls = %d, delegate runs = %d, want no child and no J3", f.j3Calls(), len(f.delegateRuns(t)))
	}
	if state, _, _, ended := f.contractRow(t); state != "running" || ended {
		t.Fatalf("contract_run state=%s ended=%v, want running", state, ended)
	}
	var maxBudget int64
	_ = f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT max_budget_usd FROM contract_run`).Scan(&maxBudget)
	})
	if maxBudget <= 0 {
		t.Fatalf("max_budget_usd = %d, want the sum of the impl and review frames", maxBudget)
	}
}

// 周（RunDelegation）は、終わっていない契約の run を持つ課題について状態を読む。
func TestRunDelegation_PollsUnfinishedContractRuns(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, nil)}
	in := f.input(f.cycle(t, 300), nil)
	in.Contract = f.client
	if _, err := f.s.RunDelegation(context.Background(), in); err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	if len(f.client.statuses) != 1 {
		t.Fatalf("statuses = %d, want 1（周が PollContractRuns を呼ぶ）", len(f.client.statuses))
	}
}

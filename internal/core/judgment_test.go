package core

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// fakeJudgmentInvoker is the core-level "偽の判断のIF" (§invoker の共通の規則
// 「判断点の起動は…core の判断のIFを呼ぶテスト用の経路で検証する」). It never
// touches a store or spawns a real process.
type fakeJudgmentInvoker struct {
	availableErr error
	result       JudgmentLaunchOutput
	invokeErr    error
	// block, if non-nil, is read from before InvokeJudgment returns (used to
	// hold the call open while a test inspects concurrent behaviour).
	block chan struct{}
	// invoked is closed once InvokeJudgment starts (signals step① committed).
	invoked  chan struct{}
	onInvoke func(in JudgmentLaunchInput)
}

func (f *fakeJudgmentInvoker) Available(_ context.Context) error { return f.availableErr }

func (f *fakeJudgmentInvoker) InvokeJudgment(_ context.Context, in JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	if f.onInvoke != nil {
		f.onInvoke(in)
	}
	if f.invoked != nil {
		close(f.invoked)
	}
	if f.block != nil {
		<-f.block
	}
	return f.result, f.invokeErr
}

func createChallengeForJudgmentTest(t *testing.T, s *Store) string {
	t.Helper()
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "judgment test"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	return ch.ID
}

func float64Ptr(f float64) *float64 { return &f }

// --- Available()（invoker_unavailable相当）は何も記録・変更しない ---

func TestRunJudgment_AvailableError_ChangesNothing(t *testing.T) {
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)

	before, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge before: %v", err)
	}

	wantErr := errors.New("claude not found")
	inv := &fakeJudgmentInvoker{availableErr: wantErr}
	_, err = s.RunJudgment(context.Background(), RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunJudgment error = %v, want to wrap %v", err, wantErr)
	}

	after, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge after: %v", err)
	}
	if after.Version != before.Version {
		t.Errorf("challenge version changed: before=%d after=%d", before.Version, after.Version)
	}

	runs, err := s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("ListRuns = %d runs, want 0 (Available() error must record nothing)", len(runs))
	}
}

// --- ①: run は起動より前にsession_idとともに記録される ---

func TestRunJudgment_RecordsRunBeforeInvoking(t *testing.T) {
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)

	invoked := make(chan struct{})
	block := make(chan struct{})
	inv := &fakeJudgmentInvoker{
		invoked: invoked, block: block,
		result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{"ok":true}`)},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.RunJudgment(context.Background(), RunJudgmentInput{
			ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv,
		})
	}()

	<-invoked // InvokeJudgment が呼ばれた時点＝①のトランザクションはコミット済み

	runs, err := s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id, OpenOnly: true})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns(--open) = %d runs while invoking, want 1", len(runs))
	}
	if runs[0].SessionID == "" {
		t.Error("recorded run has no session_id")
	}
	if runs[0].Result != "" {
		t.Errorf("recorded run has a result before ending: %q", runs[0].Result)
	}

	close(block)
	<-done
}

// --- ②: 子を待つ間は書き込みロックを持たない ---

func TestRunJudgment_DoesNotHoldWriteLockWhileWaiting(t *testing.T) {
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)

	invoked := make(chan struct{})
	block := make(chan struct{})
	inv := &fakeJudgmentInvoker{
		invoked: invoked, block: block,
		result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)},
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.RunJudgment(context.Background(), RunJudgmentInput{
			ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv,
		})
		done <- err
	}()
	<-invoked

	s2, err := OpenWorkspace(s.Workspace())
	if err != nil {
		t.Fatalf("OpenWorkspace (second handle): %v", err)
	}
	defer func() { _ = s2.Close() }()

	writeDone := make(chan error, 1)
	go func() {
		_, err := s2.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "concurrent"})
		writeDone <- err
	}()

	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("concurrent CreateChallenge failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent write did not complete quickly; RunJudgment may be holding the write lock while waiting for the invoker")
	}

	close(block)
	if err := <-done; err != nil {
		t.Fatalf("RunJudgment: %v", err)
	}
}

// --- heartbeat の更新 ---

func TestRunJudgment_UpdatesHeartbeatWhileWaiting(t *testing.T) {
	s := newStoreForTest(t)
	s.heartbeatInterval = 30 * time.Millisecond
	id := createChallengeForJudgmentTest(t, s)

	invoked := make(chan struct{})
	block := make(chan struct{})
	var runDir string
	inv := &fakeJudgmentInvoker{
		invoked: invoked, block: block,
		onInvoke: func(in JudgmentLaunchInput) { runDir = in.RunDir },
		result:   JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.RunJudgment(context.Background(), RunJudgmentInput{
			ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv,
		})
	}()
	<-invoked

	runID, ok := parseRunID(filepath.Base(runDir))
	if !ok {
		t.Fatalf("could not parse run id from RunDir %q", runDir)
	}

	time.Sleep(120 * time.Millisecond)
	row1, err := loadRunForTest(t, s, runID)
	if err != nil {
		t.Fatalf("load run (1): %v", err)
	}

	time.Sleep(120 * time.Millisecond)
	row2, err := loadRunForTest(t, s, runID)
	if err != nil {
		t.Fatalf("load run (2): %v", err)
	}
	if !row2.HeartbeatAt.After(row1.HeartbeatAt) {
		t.Errorf("heartbeat did not advance: row1=%v row2=%v", row1.HeartbeatAt, row2.HeartbeatAt)
	}

	close(block)
	<-done
}

// --- ErrRunInProgress ---

func TestRunJudgment_SecondConcurrentCallIsRunInProgress(t *testing.T) {
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)

	invoked := make(chan struct{})
	block := make(chan struct{})
	inv := &fakeJudgmentInvoker{
		invoked: invoked, block: block,
		result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.RunJudgment(context.Background(), RunJudgmentInput{
			ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv,
		})
	}()
	<-invoked

	inv2 := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)}}
	_, err := s.RunJudgment(context.Background(), RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ2, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv2,
	})
	if !errors.Is(err, ErrRunInProgress) {
		t.Errorf("second concurrent RunJudgment error = %v, want ErrRunInProgress", err)
	}

	close(block)
	<-done
}

// --- 結果の記録・session_idの不一致 ---

func TestRunJudgment_RecordsSessionIDMismatch(t *testing.T) {
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)

	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{
		Result:            RunResultSucceeded,
		SessionIDReturned: "22222222-2222-2222-2222-222222222222",
		StructuredOutput:  []byte(`{"ok":true}`),
	}}
	res, err := s.RunJudgment(context.Background(), RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv,
	})
	if err != nil {
		t.Fatalf("RunJudgment: %v", err)
	}
	if !res.SessionIDMismatch {
		t.Error("SessionIDMismatch = false, want true")
	}
	if res.SessionID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("SessionID = %q, want the returned value", res.SessionID)
	}

	runs, err := s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].SessionID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("stored run session_id = %+v, want the returned value recorded", runs)
	}
}

// --- 結果が succeeded 以外は課題の状態・版を変えない ---

func TestRunJudgment_NonSucceededDoesNotChangeChallengeVersion(t *testing.T) {
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)
	before, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge before: %v", err)
	}

	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}}
	res, err := s.RunJudgment(context.Background(), RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv,
	})
	if err != nil {
		t.Fatalf("RunJudgment: %v", err)
	}
	if res.Result != RunResultErrored {
		t.Fatalf("Result = %v, want errored", res.Result)
	}

	after, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge after: %v", err)
	}
	if after.Version != before.Version || after.Status != before.Status {
		t.Errorf("challenge changed: before=%+v after=%+v", before.Challenge, after.Challenge)
	}
}

// --- 費用の記録（computeRunCost の直接検証と RunJudgment 経由の検証） ---

func TestComputeRunCost_NewSessionReported(t *testing.T) {
	cost, source := computeRunCost(RunResultSucceeded, float64Ptr(0.5), false, nil, 100_000_000)
	if cost != 500_000 || source != CostSourceReported {
		t.Errorf("cost=%d source=%v, want 500000/reported", cost, source)
	}
}

func TestComputeRunCost_NoReportedCostIsUnknown(t *testing.T) {
	cost, source := computeRunCost(RunResultSucceeded, nil, false, nil, 1_000_000)
	if cost != 1_000_000 || source != CostSourceUnknown {
		t.Errorf("cost=%d source=%v, want 1000000/unknown", cost, source)
	}
}

func TestComputeRunCost_TimedOutMalformedInterruptedAreUnknown(t *testing.T) {
	for _, r := range []RunResult{RunResultTimedOut, RunResultMalformed, RunResultInterrupted} {
		cost, source := computeRunCost(r, float64Ptr(0.9), false, nil, 2_000_000)
		if cost != 2_000_000 || source != CostSourceUnknown {
			t.Errorf("result=%v: cost=%d source=%v, want 2000000/unknown", r, cost, source)
		}
	}
}

func TestComputeRunCost_LaunchFailedIsZero(t *testing.T) {
	cost, source := computeRunCost(RunResultLaunchFailed, nil, false, nil, 5_000_000)
	if cost != 0 {
		t.Errorf("cost = %d, want 0", cost)
	}
	if source != "" {
		t.Errorf("source = %q, want empty (no cost source recorded)", source)
	}
}

// AC: 直前のrunが0.2→0.35を報告する場合に0.15になる（出所はdelta）。
func TestComputeRunCost_ResumeDeltaIsPreviousSubtractedFromCurrent(t *testing.T) {
	prev := int64(200_000) // $0.20
	cost, source := computeRunCost(RunResultSucceeded, float64Ptr(0.35), true, &prev, 100_000_000)
	if cost != 150_000 || source != CostSourceDelta {
		t.Errorf("cost=%d source=%v, want 150000/delta", cost, source)
	}
}

func TestComputeRunCost_NegativeDeltaFallsBackToMaxBudgetUnknown(t *testing.T) {
	prev := int64(500_000)
	cost, source := computeRunCost(RunResultSucceeded, float64Ptr(0.1), true, &prev, 3_000_000)
	if cost != 3_000_000 || source != CostSourceUnknown {
		t.Errorf("cost=%d source=%v, want 3000000/unknown", cost, source)
	}
}

// self-review 指摘（round1, code-reviewer CONFIRMED）: resume なのに直前の
// run の報告（total_cost_usd）が無いケースは、「新しいセッション」と同じ
// reported 扱いにしてはならない（§費用の記録「直前のrunの報告が無い…ときは
// …unknownにする」）。
func TestComputeRunCost_ResumeWithNoPreviousReportIsUnknown(t *testing.T) {
	cost, source := computeRunCost(RunResultSucceeded, float64Ptr(0.3), true, nil, 5_000_000)
	if cost != 5_000_000 || source != CostSourceUnknown {
		t.Errorf("cost=%d source=%v, want 5000000/unknown (resume with no previous report must fail closed)", cost, source)
	}
}

// RunJudgment 経由で --resume（ResumeFromRunID）の費用の差分計算を検証する。
func TestRunJudgment_ResumeComputesDeltaCostFromPreviousRun(t *testing.T) {
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)

	// self-review 指摘（round3, code-reviewer CONFIRMED）を固定する:
	// RunJudgment が JudgmentInvoker へ渡す JudgmentLaunchInput の
	// IsResume・SessionID を、onInvoke で捕まえて直接検証する（これまでは
	// core.RunJudgmentResultの値だけを見ており、invoker境界を越える直前の
	// 入力〈launchIn〉自体は一度も検証していなかった。IsResume:isResumeの
	// 配線が落ちても、この検証が無いと気付けなかった）。
	var gotIn1, gotIn2 JudgmentLaunchInput
	inv1 := &fakeJudgmentInvoker{
		onInvoke: func(in JudgmentLaunchInput) { gotIn1 = in },
		result:   JudgmentLaunchOutput{Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(0.2), StructuredOutput: []byte(`{}`)},
	}
	res1, err := s.RunJudgment(context.Background(), RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 5, Stdin: []byte("x"), Invoker: inv1,
	})
	if err != nil {
		t.Fatalf("RunJudgment (1): %v", err)
	}
	if res1.CostSource != CostSourceReported || res1.CostUSD != 0.2 {
		t.Fatalf("first run cost=%v source=%v, want 0.2/reported", res1.CostUSD, res1.CostSource)
	}
	if gotIn1.IsResume {
		t.Error("first run: JudgmentLaunchInput.IsResume = true, want false (new session)")
	}
	if gotIn1.SessionID != res1.SessionID {
		t.Errorf("first run: JudgmentLaunchInput.SessionID = %q, want the recorded session id %q", gotIn1.SessionID, res1.SessionID)
	}

	runID1 := res1.RunID
	// self-review 指摘（round1, code-reviewer/design-reviewer 双方が
	// CONFIRMED）を固定する: invoker が実際に使った session_id
	// （res1.SessionID。--resume で claude へ渡した値）をそのまま返り値の
	// session_id として返すとき、run に記録される session_id はその値と
	// 一致し、誤った session_id_mismatch を立てない。
	var gotIn2Set bool
	inv2 := &fakeJudgmentInvoker{
		onInvoke: func(in JudgmentLaunchInput) { gotIn2, gotIn2Set = in, true },
		result: JudgmentLaunchOutput{
			Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(0.35), StructuredOutput: []byte(`{}`),
			SessionIDReturned: res1.SessionID,
		},
	}
	res2, err := s.RunJudgment(context.Background(), RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 5, Stdin: []byte("x"), Invoker: inv2,
		ResumeFromRunID: &runID1,
	})
	if err != nil {
		t.Fatalf("RunJudgment (2, resume): %v", err)
	}
	if res2.CostSource != CostSourceDelta {
		t.Fatalf("resumed run cost source = %v, want delta", res2.CostSource)
	}
	if diff := res2.CostUSD - 0.15; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("resumed run cost = %v, want 0.15", res2.CostUSD)
	}
	if res2.SessionID != res1.SessionID {
		t.Errorf("resumed run session_id = %q, want the resumed session %q", res2.SessionID, res1.SessionID)
	}
	if res2.SessionIDMismatch {
		t.Error("resumed run reports a session_id mismatch even though the invoker echoed back the same resumed session id")
	}
	if !gotIn2Set {
		t.Fatal("invoker was never invoked for the resumed run")
	}
	if !gotIn2.IsResume {
		t.Error("resumed run: JudgmentLaunchInput.IsResume = false, want true")
	}
	if gotIn2.SessionID != res1.SessionID {
		t.Errorf("resumed run: JudgmentLaunchInput.SessionID = %q, want the resumed session id %q (not a freshly generated one)", gotIn2.SessionID, res1.SessionID)
	}
}

// self-review 指摘（round1, code-reviewer PLAUSIBLE）を固定する: --resume は
// 同じ課題の run だけを再開先にできる。別の課題の run を指定すると
// ErrValidation になり、何も記録しない。
func TestRunJudgment_ResumeFromAnotherChallengeIsRejected(t *testing.T) {
	s := newStoreForTest(t)
	id1 := createChallengeForJudgmentTest(t, s)
	id2 := createChallengeForJudgmentTest(t, s)

	inv1 := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{
		Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(0.2), StructuredOutput: []byte(`{}`),
	}}
	res1, err := s.RunJudgment(context.Background(), RunJudgmentInput{
		ChallengeID: id1, Judgment: JudgmentJ1, MaxBudgetUSD: 5, Stdin: []byte("x"), Invoker: inv1,
	})
	if err != nil {
		t.Fatalf("RunJudgment (challenge 1): %v", err)
	}

	inv2 := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)}}
	_, err = s.RunJudgment(context.Background(), RunJudgmentInput{
		ChallengeID: id2, Judgment: JudgmentJ1, MaxBudgetUSD: 5, Stdin: []byte("x"), Invoker: inv2,
		ResumeFromRunID: &res1.RunID,
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("resuming from another challenge's run: err = %v, want ErrValidation", err)
	}

	runs, err := s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id2})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("ListRuns(challenge 2) = %+v, want no run recorded (rejected before ①)", runs)
	}
}

// --- 中断した run の回収（ReapInterruptedRuns） ---

// deadPID starts and waits for a trivial child process, returning its (now
// free) pid — a value guaranteed not to be alive.
func deadPID(t *testing.T) int64 {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start throwaway process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait throwaway process: %v", err)
	}
	return int64(pid)
}

func TestReapInterruptedRuns_StaleHeartbeatAndDeadProcessBecomesInterrupted(t *testing.T) {
	s := newStoreForTest(t)
	s.staleAfter = 100 * time.Millisecond
	id := createChallengeForJudgmentTest(t, s)
	cid, ok := parseChallengeID(id)
	if !ok {
		t.Fatal("parseChallengeID")
	}

	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}

	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	row, err := insertRunForTest(t, s, cid, insertRunInput{
		Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
		SessionID: "11111111-1111-1111-1111-111111111111",
		PID:       deadPID(t), Host: host,
		HeartbeatAt: base, StartedAt: base, MaxBudgetUSD: 500_000, BudgetBucket: budgetBucketJudgment,
	})
	if err != nil {
		t.Fatalf("insertRunForTest: %v", err)
	}

	s.now = func() time.Time { return base.Add(200 * time.Millisecond) }
	if err := s.ReapInterruptedRuns(context.Background()); err != nil {
		t.Fatalf("ReapInterruptedRuns: %v", err)
	}

	runID, ok := parseRunID(row.ID)
	if !ok {
		t.Fatal("parseRunID")
	}
	got, err := loadRunForTest(t, s, runID)
	if err != nil {
		t.Fatalf("loadRunForTest: %v", err)
	}
	if got.Result != RunResultInterrupted {
		t.Errorf("Result = %v, want interrupted", got.Result)
	}
	if got.CostUSD == nil || *got.CostUSD != 500_000 {
		t.Errorf("CostUSD = %v, want 500000 (the max budget)", got.CostUSD)
	}
}

func TestReapInterruptedRuns_LiveProcessIsNotClosedEvenIfHeartbeatIsStale(t *testing.T) {
	s := newStoreForTest(t)
	s.staleAfter = 100 * time.Millisecond
	id := createChallengeForJudgmentTest(t, s)
	cid, ok := parseChallengeID(id)
	if !ok {
		t.Fatal("parseChallengeID")
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}

	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	row, err := insertRunForTest(t, s, cid, insertRunInput{
		Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
		SessionID: "11111111-1111-1111-1111-111111111111",
		PID:       int64(os.Getpid()), Host: host,
		HeartbeatAt: base, StartedAt: base, MaxBudgetUSD: 500_000, BudgetBucket: budgetBucketJudgment,
	})
	if err != nil {
		t.Fatalf("insertRunForTest: %v", err)
	}

	s.now = func() time.Time { return base.Add(200 * time.Millisecond) }
	if err := s.ReapInterruptedRuns(context.Background()); err != nil {
		t.Fatalf("ReapInterruptedRuns: %v", err)
	}

	runID, ok := parseRunID(row.ID)
	if !ok {
		t.Fatal("parseRunID")
	}
	got, err := loadRunForTest(t, s, runID)
	if err != nil {
		t.Fatalf("loadRunForTest: %v", err)
	}
	if got.Result != "" {
		t.Errorf("Result = %v, want still open (\"\") because the process is alive", got.Result)
	}
}

func TestListRuns_ReapsInterruptedRunsFirst(t *testing.T) {
	s := newStoreForTest(t)
	s.staleAfter = 50 * time.Millisecond
	id := createChallengeForJudgmentTest(t, s)
	cid, _ := parseChallengeID(id)
	host, _ := os.Hostname()

	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	_, err := insertRunForTest(t, s, cid, insertRunInput{
		Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
		SessionID: "11111111-1111-1111-1111-111111111111",
		PID:       deadPID(t), Host: host,
		HeartbeatAt: base, StartedAt: base, MaxBudgetUSD: 500_000, BudgetBucket: budgetBucketJudgment,
	})
	if err != nil {
		t.Fatalf("insertRunForTest: %v", err)
	}

	s.now = func() time.Time { return base.Add(500 * time.Millisecond) }
	runs, err := s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Result != RunResultInterrupted {
		t.Fatalf("ListRuns = %+v, want a single interrupted run", runs)
	}
}

// 既定の閾値（300 秒）の境界を固定する: s.staleAfter を差し替えず、heartbeat が
// 301 秒古い run は回収され、299 秒古い run は回収されない。
func TestReapInterruptedRuns_DefaultThresholdIs300Seconds(t *testing.T) {
	for _, tc := range []struct {
		age  time.Duration
		want RunResult
	}{
		{age: 301 * time.Second, want: RunResultInterrupted},
		{age: 299 * time.Second, want: ""},
	} {
		s := newStoreForTest(t)
		id := createChallengeForJudgmentTest(t, s)
		cid, _ := parseChallengeID(id)
		host, err := os.Hostname()
		if err != nil {
			t.Fatalf("os.Hostname: %v", err)
		}
		base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
		s.now = func() time.Time { return base }
		row, err := insertRunForTest(t, s, cid, insertRunInput{
			Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
			SessionID: "11111111-1111-1111-1111-111111111111",
			PID:       deadPID(t), Host: host,
			HeartbeatAt: base, StartedAt: base, MaxBudgetUSD: 500_000, BudgetBucket: budgetBucketJudgment,
		})
		if err != nil {
			t.Fatalf("insertRunForTest: %v", err)
		}
		s.now = func() time.Time { return base.Add(tc.age) }
		if err := s.ReapInterruptedRuns(context.Background()); err != nil {
			t.Fatalf("ReapInterruptedRuns: %v", err)
		}
		runID, _ := parseRunID(row.ID)
		got, err := loadRunForTest(t, s, runID)
		if err != nil {
			t.Fatalf("loadRunForTest: %v", err)
		}
		if got.Result != tc.want {
			t.Errorf("age %v: Result = %q, want %q", tc.age, got.Result, tc.want)
		}
	}
}

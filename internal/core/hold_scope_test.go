package core

// このファイルは、区切り S4c-3 の土台（保留の止める範囲・保留を課題に重ねる遷移・保留を閉じるときの遷移先・保留の ID・
// 「このタスクを進めてよいか」の述語・ほかの保留を閉じる共通の関数）を、実ストアで検証する。

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
)

// holdScopeFixture は、統合先を使う、タスク a・b を持つ着手中の課題と、a・b の契約の実行。
type holdScopeFixture struct {
	*contractFixture
	id         string
	cid        int64
	taskA      int64
	taskB      int64
	runA, runB *ContractRunReport
}

func newHoldScopeFixture(t *testing.T) *holdScopeFixture {
	t.Helper()
	return newHoldScopeFixtureStarted(t, true)
}

// newHoldScopeFixtureStarted の start が偽なら、委譲の段を回さない（契約の実行は無い。取り下げの試験が使う）。
func newHoldScopeFixtureStarted(t *testing.T, start bool) *holdScopeFixture {
	t.Helper()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "two", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	if start {
		f.runDelegationStage(t, nil)
	}
	cid, _ := parseChallengeID(id)
	hf := &holdScopeFixture{contractFixture: f, id: id, cid: cid,
		taskA: mustTaskInt(f.taskID(t, id, "a")), taskB: mustTaskInt(f.taskID(t, id, "b"))}
	if start {
		hf.runA = hf.reportOf(t, hf.taskA)
		hf.runB = hf.reportOf(t, hf.taskB)
	}
	return hf
}

func (f *holdScopeFixture) reportOf(t *testing.T, task int64) *ContractRunReport {
	t.Helper()
	runInt, err := strconv.ParseInt(f.delegateFixture.queryString(t, `SELECT id FROM run WHERE kind = 'contract' AND task_id = ? ORDER BY id LIMIT 1`, task), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	ext := f.delegateFixture.queryString(t, `SELECT external_run_id FROM contract_run WHERE task_id = ?`, task)
	return &ContractRunReport{RunID: formatRunID(runInt), ChallengeID: f.id, ExternalRunID: ext}
}

// waitHold は、タスクの契約の run が、オーナーへの問いの待ちを返したことを写す（着手中の課題にだけ立つ）。
func (f *holdScopeFixture) waitHold(t *testing.T, rep *ContractRunReport) {
	t.Helper()
	held, err := f.s.holdContractWait(context.Background(), rep, "どうしますか")
	if err != nil || !held {
		t.Fatalf("holdContractWait = %v, %v, want held", held, err)
	}
}

// overlay は、人間対応待ちの課題へ、保留を作る共通の所を直接呼ぶ（呼び出し元の状態の確認は通らない）。
func (f *holdScopeFixture) overlay(task int64, opts HoldOptionsInput) error {
	_, err := f.s.transition(context.Background(), ChannelCLI, f.id, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		tc.taskID = &task
		_, err := insertHoldWithOptions(ctx, tc, "q", HoldKindHuman, tc.current.Status, tc.taskID, opts)
		return err
	})
	return err
}

func (f *holdScopeFixture) q(t *testing.T, q string, args ...any) string {
	t.Helper()
	return f.delegateFixture.queryString(t, q, args...)
}

func (f *holdScopeFixture) openHolds(t *testing.T) string {
	return f.q(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL`, f.cid)
}

func (f *holdScopeFixture) challengeStatus(t *testing.T) string {
	return f.q(t, `SELECT status FROM challenge WHERE id = ?`, f.cid)
}

func (f *holdScopeFixture) version(t *testing.T) int {
	n, _ := strconv.Atoi(f.q(t, `SELECT version FROM challenge WHERE id = ?`, f.cid))
	return n
}

func TestHoldScope_FormatAndParseHoldID(t *testing.T) {
	t.Parallel()
	if got := formatHoldID(7); got != "H-7" {
		t.Errorf("formatHoldID(7) = %q, want H-7", got)
	}
	if n, ok := parseHoldID("H-12"); !ok || n != 12 {
		t.Errorf("parseHoldID(H-12) = %d, %v", n, ok)
	}
	for _, bad := range []string{"", "H-", "H-0", "H-01", "h-1", "C-1", "1", "H-1x"} {
		if _, ok := parseHoldID(bad); ok {
			t.Errorf("parseHoldID(%q) ok = true, want false", bad)
		}
	}
}

// 着手中の課題で、タスク a に結び付く保留が立つと、その保留の scope は task（作業ログに、保留の ID と止める範囲が残る）。
func TestHoldScope_IntegrationTaskWaitHoldIsTaskScoped(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	if got := f.q(t, `SELECT scope FROM hold ORDER BY id DESC LIMIT 1`); got != "task" {
		t.Errorf("hold.scope = %q, want task", got)
	}
	if got := f.q(t, `SELECT json_extract(after, '$.hold_scope') || ' ' || json_extract(after, '$.hold_id') FROM activity WHERE action = 'hold' ORDER BY id DESC LIMIT 1`); got != "task H-1" {
		t.Errorf("worklog hold_scope/hold_id = %q, want %q", got, "task H-1")
	}
}

// 統合先を使わない課題（タスクが 1 個）の、タスクに結び付く保留の scope は NULL。
func TestHoldScope_NonIntegrationTaskHoldIsChallengeScoped(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "one", "P1", false, multiTask{key: "a"})
	f.runDelegationStage(t, nil)
	cid, _ := parseChallengeID(id)
	hf := &holdScopeFixture{contractFixture: f, id: id, cid: cid, taskA: mustTaskInt(f.taskID(t, id, "a"))}
	hf.waitHold(t, hf.reportOf(t, hf.taskA))
	if got := hf.q(t, `SELECT COALESCE(scope, 'NULL') || '/' || COALESCE(task_id, 'NULL') FROM hold ORDER BY id DESC LIMIT 1`); got != "NULL/"+strconv.FormatInt(hf.taskA, 10) {
		t.Errorf("scope/task_id = %q, want NULL/<task a>", got)
	}
	if got := hf.q(t, `SELECT json_extract(after, '$.hold_scope') FROM activity WHERE action = 'hold' ORDER BY id DESC LIMIT 1`); got != "challenge" {
		t.Errorf("worklog hold_scope = %q, want challenge", got)
	}
}

// タスクに結び付かない保留の scope は NULL。タスクに結び付かないのに task を渡すと、ErrValidation で何も書かない。
func TestHoldScope_TaskScopeNeedsATask(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusInProgress)
	_, err := s.transition(context.Background(), ChannelCLI, c.ID, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		_, err := insertHoldWithOptions(ctx, tc, "q", HoldKindHuman, tc.current.Status, nil, HoldOptionsInput{Scope: HoldScopeTask})
		return err
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "q"}); err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(c.ID)
	var scope sql.NullString
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT scope FROM hold WHERE challenge_id = ?`, cid).Scan(&scope)
	}); err != nil || scope.Valid {
		t.Errorf("scope = %v, err = %v, want NULL", scope, err)
	}
}

// タスクを止める保留が開いている間に、別のタスクを止める保留を重ねると、2 つ目が立つ。from_status は着手中、課題の状態は
// 人間対応待ちのまま、版は進む。タスク a の保留の scope は task のまま。
func TestHoldScope_OverlayTaskHold(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	v := f.version(t)
	if err := f.overlay(f.taskB, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatalf("overlay: %v", err)
	}
	if got := f.openHolds(t); got != "2" {
		t.Errorf("open holds = %s, want 2", got)
	}
	if got := f.q(t, `SELECT from_status FROM hold ORDER BY id DESC LIMIT 1`); got != string(StatusInProgress) {
		t.Errorf("from_status = %q, want in_progress", got)
	}
	if got := f.challengeStatus(t); got != string(StatusAwaitingHuman) {
		t.Errorf("status = %q, want awaiting_human", got)
	}
	if got := f.version(t); got != v+1 {
		t.Errorf("version = %d, want %d", got, v+1)
	}
	if got := f.q(t, `SELECT scope FROM hold WHERE task_id = ? AND answer IS NULL`, f.taskA); got != "task" {
		t.Errorf("task a hold scope = %q, want task", got)
	}
}

// 重なりは、行を作る前に遷移の誤りで断る（索引の違反を返さない）: 同じタスクに開いている保留がある・課題を止める保留を作ろうとする。
func TestHoldScope_OverlayIsRefusedWithInvalidTransition(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	v := f.version(t)
	cases := []struct {
		name string
		task int64
		opts HoldOptionsInput
	}{
		{"same task already has an open hold", f.taskA, HoldOptionsInput{Scope: HoldScopeTask}},
		{"challenge-scoped hold over task holds", f.taskB, HoldOptionsInput{}},
	}
	for _, c := range cases {
		if err := f.overlay(c.task, c.opts); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s: error = %v, want ErrInvalidTransition", c.name, err)
		}
	}
	if got := f.openHolds(t); got != "1" {
		t.Errorf("open holds = %s, want 1", got)
	}
	if got := f.q(t, `SELECT COUNT(*) FROM hold WHERE scope IS NULL AND challenge_id = ?`, f.cid); got != "0" {
		t.Errorf("rows with scope NULL = %s, want 0", got)
	}
	if got := f.version(t); got != v {
		t.Errorf("version = %d, want %d (unchanged)", got, v)
	}
	// 公開の保留の入口（課題を止める保留）も同じ遷移の誤り。
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, f.id, HoldInput{Question: "q"}); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("HoldChallenge over task holds: error = %v, want ErrInvalidTransition", err)
	}
}

// 課題を止める保留が開いている間は、タスクを止める保留を重ねられない。
func TestHoldScope_TaskHoldOverChallengeHoldIsRefused(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, f.id, HoldInput{Question: "課題を止める"}); err != nil {
		t.Fatal(err)
	}
	if err := f.overlay(f.taskB, HoldOptionsInput{Scope: HoldScopeTask}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v, want ErrInvalidTransition", err)
	}
	if got := f.openHolds(t); got != "1" {
		t.Errorf("open holds = %s, want 1", got)
	}
}

// 述語: 着手中は進めてよい。人間対応待ちは、開いている保留がどれもタスクを止める保留で、そのタスクに保留が無いときだけ進めてよい。
func TestHoldScope_TaskMayProceedPredicate(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	may := func(task int64) bool {
		var ok bool
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
			ok, err = taskMayProceed(context.Background(), tx, f.cid, task)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !may(f.taskA) || !may(f.taskB) {
		t.Error("in_progress: want both tasks may proceed")
	}
	f.waitHold(t, f.runA)
	if may(f.taskA) {
		t.Error("task a has an open hold: want false")
	}
	if !may(f.taskB) {
		t.Error("only task a is held: task b may proceed")
	}
	if err := f.overlay(f.taskB, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	if may(f.taskA) || may(f.taskB) {
		t.Error("both tasks have open holds: want false")
	}
}

func TestHoldScope_TaskMayProceedIsFalseUnderAChallengeHold(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, f.id, HoldInput{Question: "課題を止める"}); err != nil {
		t.Fatal(err)
	}
	var ok bool
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		ok, err = taskMayProceed(context.Background(), tx, f.cid, f.taskB)
		return err
	}); err != nil || ok {
		t.Fatalf("taskMayProceed = %v, %v, want false", ok, err)
	}
}

// 保留が 2 つ開いている課題で、片方に答えると、課題の状態は人間対応待ちのまま（版は進む）。もう片方は開いたまま。
// 両方に答えると、保留の戻り先（着手中）へ戻る。
func TestHoldScope_AnswerWithRemainingHoldKeepsAwaitingHuman(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	if err := f.overlay(f.taskB, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	answer := func() *Challenge {
		t.Helper()
		att := verifiedAttestationForTest(t, f.id)
		c, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: f.version(t), Answer: "これで"}, att)
		if err != nil {
			t.Fatalf("ExecuteAnswer: %v", err)
		}
		return c
	}
	v := f.version(t)
	c := answer()
	if c.Status != StatusAwaitingHuman || c.Version != v+1 {
		t.Errorf("after first answer: status = %s version = %d, want awaiting_human %d", c.Status, c.Version, v+1)
	}
	if got := f.openHolds(t); got != "1" {
		t.Errorf("open holds = %s, want 1", got)
	}
	if c := answer(); c.Status != StatusInProgress {
		t.Errorf("after second answer: status = %s, want in_progress", c.Status)
	}
	if got := f.openHolds(t); got != "0" {
		t.Errorf("open holds = %s, want 0", got)
	}
}

// 保留が 1 つだけ開いている課題の回答は、いまと同じに保留の戻り先へ戻る。
func TestHoldScope_AnswerWithOneHoldReturnsToFromStatus(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	att := verifiedAttestationForTest(t, f.id)
	c, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: f.version(t), Answer: "これで"}, att)
	if err != nil || c.Status != StatusInProgress {
		t.Fatalf("ExecuteAnswer = %v, %v, want in_progress", c, err)
	}
}

// 取り下げは、開いている保留を、止める範囲によらず全部閉じる。
func TestHoldScope_WithdrawClosesEveryOpenHold(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixtureStarted(t, false)
	if err := f.overlay(f.taskA, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	if err := f.overlay(f.taskB, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ExecuteWithdraw(context.Background(), WithdrawRequest{ChallengeID: f.id, ExpectedVersion: f.version(t), Reason: "やめる"}, verifiedAttestationForTest(t, f.id)); err != nil {
		t.Fatalf("ExecuteWithdraw: %v", err)
	}
	if got := f.openHolds(t); got != "0" {
		t.Errorf("open holds = %s, want 0", got)
	}
	if got := f.q(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ? AND closed_at IS NOT NULL`, f.cid); got != "2" {
		t.Errorf("closed holds = %s, want 2", got)
	}
}

// ほかの開いている保留を回答なしで閉じる共通の関数は、指定した保留以外を閉じ、閉じた保留の ID と理由を作業ログに残す。
func TestHoldScope_CloseOtherOpenHolds(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	if err := f.overlay(f.taskB, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	keep, _ := strconv.ParseInt(f.q(t, `SELECT id FROM hold WHERE task_id = ?`, f.taskB), 10, 64)
	var closed []string
	if _, err := f.s.transition(context.Background(), ChannelCLI, f.id, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		var err error
		closed, err = closeOtherOpenHolds(ctx, tc, keep, "計画を作り直すため")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(closed) != 1 || closed[0] != "H-1" {
		t.Fatalf("closed = %v, want [H-1]", closed)
	}
	if got := f.openHolds(t); got != "1" {
		t.Errorf("open holds = %s, want 1", got)
	}
	if got := f.q(t, `SELECT answer IS NULL FROM hold WHERE id = 1`); got != "1" {
		t.Errorf("closed hold must keep answer NULL")
	}
	if got := f.q(t, `SELECT json_extract(after, '$.closed_holds_reason') || json_extract(after, '$.closed_holds[0]') FROM activity WHERE action = 'hold' ORDER BY id DESC LIMIT 1`); got != "計画を作り直すためH-1" {
		t.Errorf("worklog = %q", got)
	}
}

// 同時に走らせる数の数え方は、タスクを止める保留が 2 つ開いている間も変わらない（保留の数を見ない）。
func TestHoldScope_ActiveDelegationCountIgnoresOpenTaskHolds(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	count := func() int {
		var n int
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
			n, err = activeDelegationCount(context.Background(), tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	f.waitHold(t, f.runA)
	before := count()
	if err := f.overlay(f.taskB, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	if got := f.openHolds(t); got != "2" {
		t.Fatalf("open holds = %s, want 2", got)
	}
	if after := count(); after != before {
		t.Errorf("active delegation count = %d with 2 task holds, want %d (same as with 1)", after, before)
	}
}

// スーパーバイザーが閉じる（T20）: ほかの開いている保留が残っていれば、課題の状態は人間対応待ちのまま（版は進む）。残っていなければ、
// 保留の戻り先（着手中）へ戻る。
func TestHoldScope_SupervisorResolveWithRemainingHoldKeepsAwaitingHuman(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	if err := f.overlay(f.taskB, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	resolve := func(hold int64) {
		t.Helper()
		if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`UPDATE hold SET supervisor_state = 'pending' WHERE id = ?`, hold); err != nil {
				return err
			}
			res, err := tx.Exec(`INSERT INTO cycle (trigger, budget_usd, started_at) VALUES ('manual', 0, '2026-10-24T00:00:00.000Z')`)
			if err != nil {
				return err
			}
			cyc, _ := res.LastInsertId()
			res, err = tx.Exec(`INSERT INTO supervisor_action (cycle_id, challenge_id, stop_kind, hand, findings, note, basis, result, created_at)
				VALUES (?, ?, 'rework_limit', 'retry', 'f', '', '[]', 'executed', '2026-10-24T00:00:00.000Z')`, cyc, f.cid)
			if err != nil {
				return err
			}
			act, _ := res.LastInsertId()
			return f.s.resolveSupervisorHold(context.Background(), supervisorHandInput{
				ChallengeID: f.cid, HoldID: hold, ActionID: act, Tx: tx,
				Output: SVOutput{Hand: HandRetry, Findings: "f"},
			}, false)
		}); err != nil {
			t.Fatalf("resolveSupervisorHold: %v", err)
		}
	}
	v := f.version(t)
	resolve(1)
	if got := f.challengeStatus(t); got != string(StatusAwaitingHuman) {
		t.Errorf("status = %q, want awaiting_human while another hold is open", got)
	}
	if got := f.version(t); got != v+1 {
		t.Errorf("version = %d, want %d", got, v+1)
	}
	if got := f.openHolds(t); got != "1" {
		t.Errorf("open holds = %s, want 1", got)
	}
	resolve(2)
	if got := f.challengeStatus(t); got != string(StatusInProgress) {
		t.Errorf("status = %q, want in_progress after the last hold is closed", got)
	}
}

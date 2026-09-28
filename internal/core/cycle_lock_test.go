package core

// このファイルは #83（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §サイクルの排他）の受入基準（AC-146〜150: 生きている保持者は locked・
// stale なロックは次の cycle が回収・cycle の終了後は排他が解放される・
// 保持者が自分でない排他を解放しようとしても残る・個別の操作は排他を待たない）
// を core のテストで検証する。CLI（`cycle` の終了コード・偽の `gh`・
// 偽の `claude` の呼び出し）は #86 の範囲。

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"
)

// loadCycleForTest は id の周を読む（無ければ nil, nil）。run_store_test.go の
// 他の loadXForTest ヘルパーと同じ形。
func loadCycleForTest(t *testing.T, s *Store, id int64) (*cycleRow, error) {
	t.Helper()
	var row *cycleRow
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		r, err := loadCycleByID(context.Background(), tx, id)
		if err != nil {
			return err
		}
		row = r
		return nil
	})
	return row, err
}

// countCyclesForTest は cycle 表の行数を返す（AC-146「cycle 行が増えない」の検証）。
func countCyclesForTest(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM cycle`).Scan(&n)
	}); err != nil {
		t.Fatalf("countCyclesForTest: %v", err)
	}
	return n
}

// --- AC-146: 生きている保持者（自プロセスの pid・現ホスト）がいれば
// ErrLocked で終わり、cycle 行が増えない ---

func TestBeginCycle_Exclusive_LiveHolderIsLocked(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)

	first, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true})
	if err != nil {
		t.Fatalf("BeginCycle(1st): %v", err)
	}
	before := countCyclesForTest(t, s)

	_, err = s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("BeginCycle(2nd) error = %v, want ErrLocked", err)
	}

	after := countCyclesForTest(t, s)
	if after != before {
		t.Errorf("cycle row count changed on ErrLocked: before=%d after=%d", before, after)
	}

	lock, err := loadLockForTest(t, s, cycleLockName)
	if err != nil {
		t.Fatalf("loadLockForTest: %v", err)
	}
	if lock == nil || lock.Holder != first.ID {
		t.Errorf("lock = %+v, want holder = %s", lock, first.ID)
	}
}

// --- stale なロックの回収: heartbeat が閾値より古く、保持者のプロセスが
// 生きていなければ、次の BeginCycle(Exclusive) が回収して取得する。古い周は
// interrupted で閉じられる ---

func TestBeginCycle_Exclusive_ReclaimsStaleLockAndClosesOldCycle(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	s.lockStaleAfter = 100 * time.Millisecond
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	oldCyc, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "old", StartedAt: base, BudgetUSD: 300_000_000})
	if err != nil {
		t.Fatalf("insertCycleForTest: %v", err)
	}
	oldCycleIDInt := mustParseCycleIDForTest(t, oldCyc.ID)
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	if _, err := insertLockForTest(t, s, insertLockInput{
		Name: cycleLockName, Holder: oldCycleIDInt, PID: deadPID(t), Host: host, AcquiredAt: base, HeartbeatAt: base,
	}); err != nil {
		t.Fatalf("insertLockForTest: %v", err)
	}

	s.now = func() time.Time { return base.Add(200 * time.Millisecond) }
	newCyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true})
	if err != nil {
		t.Fatalf("BeginCycle: %v, want success (stale lock reclaimed)", err)
	}
	if newCyc.ID == oldCyc.ID {
		t.Fatalf("BeginCycle returned the old cycle id %s, want a new one", newCyc.ID)
	}

	closedOld, err := loadCycleForTest(t, s, oldCycleIDInt)
	if err != nil {
		t.Fatalf("loadCycleForTest(old): %v", err)
	}
	if closedOld == nil || closedOld.Result != cycleResultInterrupted || closedOld.EndedAt == nil {
		t.Errorf("old cycle = %+v, want result=interrupted with ended_at set", closedOld)
	}

	lock, err := loadLockForTest(t, s, cycleLockName)
	if err != nil {
		t.Fatalf("loadLockForTest: %v", err)
	}
	if lock == nil || lock.Holder != newCyc.ID {
		t.Errorf("lock = %+v, want holder = %s (the reclaiming cycle)", lock, newCyc.ID)
	}
}

// self-review 指摘（round1, code-reviewer CONFIRMED・design-reviewer
// CONFIRMED）: 古い周の保持者プロセスが死んでいるとき、そのプロセスが残した
// 「終了していない run」も同じだけ heartbeat が古い。BeginCycle(Exclusive) が
// 回収の前に ReapInterruptedRuns を呼ばないと、その run の費用が old cycle の
// spent_usd に反映されないまま周が閉じてしまう（二度と埋まらない）。
func TestBeginCycle_Exclusive_ReclaimReapsOrphanedRunBeforeClosingOldCycle(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	s.lockStaleAfter = 100 * time.Millisecond
	s.staleAfter = 100 * time.Millisecond // run の heartbeat の回収閾値も同様に短く
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	oldCyc, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "old", StartedAt: base, BudgetUSD: 300_000_000})
	if err != nil {
		t.Fatalf("insertCycleForTest: %v", err)
	}
	oldCycleIDInt := mustParseCycleIDForTest(t, oldCyc.ID)
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	deadHolderPID := deadPID(t)
	if _, err := insertLockForTest(t, s, insertLockInput{
		Name: cycleLockName, Holder: oldCycleIDInt, PID: deadHolderPID, Host: host, AcquiredAt: base, HeartbeatAt: base,
	}); err != nil {
		t.Fatalf("insertLockForTest: %v", err)
	}

	// old cycle の保持者プロセスが残した、終了していない run（同じ死んだ pid・
	// 同じ古い heartbeat）。
	challengeID := createChallengeForJudgmentTest(t, s)
	cid, ok := parseChallengeID(challengeID)
	if !ok {
		t.Fatal("parseChallengeID")
	}
	if _, err := insertRunForTest(t, s, cid, insertRunInput{
		CycleID: &oldCycleIDInt, Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
		SessionID: "11111111-1111-1111-1111-111111111111",
		PID:       deadHolderPID, Host: host,
		HeartbeatAt: base, StartedAt: base, MaxBudgetUSD: 750_000, BudgetBucket: budgetBucketJudgment,
	}); err != nil {
		t.Fatalf("insertRunForTest: %v", err)
	}

	s.now = func() time.Time { return base.Add(200 * time.Millisecond) }
	if _, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); err != nil {
		t.Fatalf("BeginCycle: %v, want success (stale lock reclaimed)", err)
	}

	closedOld, err := loadCycleForTest(t, s, oldCycleIDInt)
	if err != nil {
		t.Fatalf("loadCycleForTest(old): %v", err)
	}
	if closedOld == nil || closedOld.Result != cycleResultInterrupted {
		t.Fatalf("old cycle = %+v, want result=interrupted", closedOld)
	}
	if closedOld.SpentUSD != 750_000 {
		t.Errorf("old cycle SpentUSD = %d, want 750000 (the orphaned run must be reaped and counted before the cycle closes)", closedOld.SpentUSD)
	}
}

// self-review 指摘（round2, code-reviewer CONFIRMED）: run の heartbeat
// （invokeWithHeartbeat）とロックの heartbeat（KeepCycleLock）は、同じ
// 死んだプロセスが別々の60秒ティッカーで更新するため、run の heartbeat が
// ロックの heartbeat よりわずかに新しい（＝s.staleAfter〈run単体の閾値〉には
// まだ達していない）窓が生じうる。この窓では BeginCycle が呼ぶ外側の
// ReapInterruptedRuns（run自身の heartbeat の古さだけを見る）はその run を
// 回収しないが、ロックの保持者（pid・host）は既に死亡確認済みなので、
// reclaimStaleLockOrErrLocked は run の heartbeat の古さに関わらず
// closeDeadHolderRuns でその run を閉じ、old cycle の spent_usd に費用を
// 反映する。
func TestBeginCycle_Exclusive_ReclaimClosesDeadHolderRunEvenIfItsOwnHeartbeatIsNotYetStale(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	s.lockStaleAfter = 100 * time.Millisecond
	// s.staleAfter は既定（300秒）のまま: run 単体の heartbeat の古さでは
	// 到底 stale と判定されない状況を作る。
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	oldCyc, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "old", StartedAt: base, BudgetUSD: 300_000_000})
	if err != nil {
		t.Fatalf("insertCycleForTest: %v", err)
	}
	oldCycleIDInt := mustParseCycleIDForTest(t, oldCyc.ID)
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	deadHolderPID := deadPID(t)
	if _, err := insertLockForTest(t, s, insertLockInput{
		Name: cycleLockName, Holder: oldCycleIDInt, PID: deadHolderPID, Host: host, AcquiredAt: base, HeartbeatAt: base,
	}); err != nil {
		t.Fatalf("insertLockForTest: %v", err)
	}

	// run の heartbeat はロックの heartbeat より新しい（base+50ms）が、
	// s.staleAfter（既定300秒）からすればまったく古くない。
	challengeID := createChallengeForJudgmentTest(t, s)
	cid, ok := parseChallengeID(challengeID)
	if !ok {
		t.Fatal("parseChallengeID")
	}
	runHeartbeat := base.Add(50 * time.Millisecond)
	if _, err := insertRunForTest(t, s, cid, insertRunInput{
		CycleID: &oldCycleIDInt, Kind: runKindJudgment, Judgment: judgmentJ1, ChallengeVersion: 1,
		SessionID: "11111111-1111-1111-1111-111111111111",
		PID:       deadHolderPID, Host: host,
		HeartbeatAt: runHeartbeat, StartedAt: base, MaxBudgetUSD: 400_000, BudgetBucket: budgetBucketJudgment,
	}); err != nil {
		t.Fatalf("insertRunForTest: %v", err)
	}

	// 200ms後: ロックは stale（>100ms）だが、run 単体は s.staleAfter(300秒)には
	// 遠く及ばない（150ms古いだけ）。
	s.now = func() time.Time { return base.Add(200 * time.Millisecond) }
	if _, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); err != nil {
		t.Fatalf("BeginCycle: %v, want success (stale lock reclaimed)", err)
	}

	closedOld, err := loadCycleForTest(t, s, oldCycleIDInt)
	if err != nil {
		t.Fatalf("loadCycleForTest(old): %v", err)
	}
	if closedOld == nil || closedOld.Result != cycleResultInterrupted {
		t.Fatalf("old cycle = %+v, want result=interrupted", closedOld)
	}
	if closedOld.SpentUSD != 400_000 {
		t.Errorf("old cycle SpentUSD = %d, want 400000 (the dead holder's run must be closed even though its own heartbeat was not yet past s.staleAfter)", closedOld.SpentUSD)
	}
}

// self-review 指摘（round1, code-reviewer CONFIRMED）: stale の条件は
// 「heartbeat が閾値より古い」と「保持者のプロセスが生きていない」の両方を
// 満たすこと。heartbeat が古くてもプロセスが生きていれば回収しない
// （§サイクルの排他「生きている保持者がいれば…locked」）。
func TestBeginCycle_Exclusive_LiveProcessKeepsLockEvenWithStaleHeartbeat(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	s.lockStaleAfter = 100 * time.Millisecond
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	oldCyc, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "old", StartedAt: base, BudgetUSD: 300_000_000})
	if err != nil {
		t.Fatalf("insertCycleForTest: %v", err)
	}
	oldCycleIDInt := mustParseCycleIDForTest(t, oldCyc.ID)
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	if _, err := insertLockForTest(t, s, insertLockInput{
		Name: cycleLockName, Holder: oldCycleIDInt, PID: int64(os.Getpid()), Host: host, AcquiredAt: base, HeartbeatAt: base,
	}); err != nil {
		t.Fatalf("insertLockForTest: %v", err)
	}

	// heartbeat は閾値よりずっと古いが、PID は自プロセス（生きている）。
	s.now = func() time.Time { return base.Add(200 * time.Millisecond) }
	if _, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); !errors.Is(err, ErrLocked) {
		t.Errorf("BeginCycle(stale heartbeat, live pid) = %v, want ErrLocked (process is alive)", err)
	}
}

// self-review 指摘（round1, code-reviewer CONFIRMED）: 保持者が別ホストなら
// isRunProcessAlive は「確かめようがない＝生きている扱い」とする（M3 S1は
// 単一ホスト運用が前提。judgment.go の isRunProcessAlive のコメント参照）。
// ロックの回収でも同じ規則が適用されることを確かめる。
func TestBeginCycle_Exclusive_DifferentHostIsTreatedAsAlive(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	s.lockStaleAfter = 100 * time.Millisecond
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	oldCyc, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "old", StartedAt: base, BudgetUSD: 300_000_000})
	if err != nil {
		t.Fatalf("insertCycleForTest: %v", err)
	}
	oldCycleIDInt := mustParseCycleIDForTest(t, oldCyc.ID)
	if _, err := insertLockForTest(t, s, insertLockInput{
		Name: cycleLockName, Holder: oldCycleIDInt, PID: deadPID(t), Host: "some-other-host", AcquiredAt: base, HeartbeatAt: base,
	}); err != nil {
		t.Fatalf("insertLockForTest: %v", err)
	}

	s.now = func() time.Time { return base.Add(200 * time.Millisecond) }
	if _, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); !errors.Is(err, ErrLocked) {
		t.Errorf("BeginCycle(different host holder) = %v, want ErrLocked (cannot confirm a different host's process is dead)", err)
	}
}

// 境界: heartbeat が既定の閾値（300秒）ちょうど・それ未満古いだけでは回収しない
// （保持者のプロセスが生きていなくても）。s.lockStaleAfter は差し替えず既定値を使う。
func TestBeginCycle_Exclusive_BoundaryAt300SecondsIsNotStale(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	oldCyc, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "old", StartedAt: base, BudgetUSD: 300_000_000})
	if err != nil {
		t.Fatalf("insertCycleForTest: %v", err)
	}
	oldCycleIDInt := mustParseCycleIDForTest(t, oldCyc.ID)
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	if _, err := insertLockForTest(t, s, insertLockInput{
		Name: cycleLockName, Holder: oldCycleIDInt, PID: deadPID(t), Host: host, AcquiredAt: base, HeartbeatAt: base,
	}); err != nil {
		t.Fatalf("insertLockForTest: %v", err)
	}

	// ちょうど300秒古い: 回収しない（stale の条件は「300秒より古く」＝ > 300s）。
	s.now = func() time.Time { return base.Add(300 * time.Second) }
	if _, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); !errors.Is(err, ErrLocked) {
		t.Errorf("BeginCycle at exactly 300s stale = %v, want ErrLocked (not reclaimed yet)", err)
	}

	// 301秒古い: 回収する。
	s.now = func() time.Time { return base.Add(301 * time.Second) }
	if _, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); err != nil {
		t.Errorf("BeginCycle at 301s stale = %v, want success (reclaimed)", err)
	}
}

// --- AC: cycle の終了後、サイクルの排他が解放されている（続けて実行した
// cycle が locked にならない） ---

func TestEndCycle_ReleasesLockWhenItIsTheHolder(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	cyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	if _, err := s.EndCycle(ctx, cyc.ID, CycleResultCompleted); err != nil {
		t.Fatalf("EndCycle: %v", err)
	}

	lock, err := loadLockForTest(t, s, cycleLockName)
	if err != nil {
		t.Fatalf("loadLockForTest: %v", err)
	}
	if lock != nil {
		t.Errorf("lock = %+v, want nil (released by EndCycle)", lock)
	}

	if _, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); err != nil {
		t.Errorf("BeginCycle after EndCycle = %v, want success (not locked)", err)
	}
}

// --- AC: 保持者が自分でない排他を解放しようとしても、排他は残る ---

func TestEndCycle_DoesNotReleaseLockHeldByAnotherCycle(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	holder, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true})
	if err != nil {
		t.Fatalf("BeginCycle(holder): %v", err)
	}
	// Exclusive=false: ロックには触れず、別の cycle 行だけを作る
	// （§サイクルの排他「個別の操作はサイクルのロックを取らない」）。
	other, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "classify --auto", BudgetUSD: 1, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle(other, non-exclusive): %v", err)
	}

	if _, err := s.EndCycle(ctx, other.ID, CycleResultCompleted); err != nil {
		t.Fatalf("EndCycle(other): %v", err)
	}

	lock, err := loadLockForTest(t, s, cycleLockName)
	if err != nil {
		t.Fatalf("loadLockForTest: %v", err)
	}
	if lock == nil || lock.Holder != holder.ID {
		t.Errorf("lock = %+v, want it to remain held by %s", lock, holder.ID)
	}
}

func TestReleaseCycleLock_DoesNotReleaseWhenNotHolder(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	holder, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true})
	if err != nil {
		t.Fatalf("BeginCycle(holder): %v", err)
	}
	other, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "classify --auto", BudgetUSD: 1, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle(other): %v", err)
	}

	if err := s.ReleaseCycleLock(ctx, other.ID); err != nil {
		t.Fatalf("ReleaseCycleLock(other): %v", err)
	}
	lock, err := loadLockForTest(t, s, cycleLockName)
	if err != nil {
		t.Fatalf("loadLockForTest: %v", err)
	}
	if lock == nil || lock.Holder != holder.ID {
		t.Fatalf("lock = %+v, want it to remain held by %s after releasing a non-holder", lock, holder.ID)
	}

	if err := s.ReleaseCycleLock(ctx, holder.ID); err != nil {
		t.Fatalf("ReleaseCycleLock(holder): %v", err)
	}
	lock, err = loadLockForTest(t, s, cycleLockName)
	if err != nil {
		t.Fatalf("loadLockForTest: %v", err)
	}
	if lock != nil {
		t.Errorf("lock = %+v, want nil after releasing the actual holder", lock)
	}
}

// --- AC: cycle の実行中も、ID を指定した個別の操作は排他を待たずに実行できる ---

func TestBeginCycle_NonExclusive_DoesNotWaitForExclusiveLock(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	if _, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); err != nil {
		t.Fatalf("BeginCycle(exclusive): %v", err)
	}

	// 生きている保持者がいる間でも、Exclusive=false は ErrLocked にならない。
	other, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "classify --auto", BudgetUSD: 1, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle(non-exclusive) = %v, want success while the exclusive lock is held", err)
	}
	if other.Result != "" {
		t.Errorf("other.Result = %v, want unset (not ended)", other.Result)
	}
}

// --- HeartbeatCycleLock / KeepCycleLock: 補助 API の基本的な振る舞い ---

func TestHeartbeatCycleLock_UpdatesHeartbeatWhenHolder(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	cyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}

	later := base.Add(30 * time.Second)
	s.now = func() time.Time { return later }
	if err := s.HeartbeatCycleLock(ctx, cyc.ID); err != nil {
		t.Fatalf("HeartbeatCycleLock: %v", err)
	}
	lock, err := loadLockForTest(t, s, cycleLockName)
	if err != nil {
		t.Fatalf("loadLockForTest: %v", err)
	}
	if lock == nil || !lock.HeartbeatAt.Equal(later) {
		t.Errorf("lock.HeartbeatAt = %v, want %v", lock, later)
	}
}

func TestHeartbeatCycleLock_ReturnsErrLockLostWhenNotHolder(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	if _, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	other, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "classify --auto", BudgetUSD: 1, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle(other): %v", err)
	}
	if err := s.HeartbeatCycleLock(ctx, other.ID); !errors.Is(err, ErrLockLost) {
		t.Errorf("HeartbeatCycleLock(non-holder) = %v, want ErrLockLost", err)
	}
}

func TestKeepCycleLock_TicksHeartbeatUntilStopped(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	s.lockHeartbeatInterval = 10 * time.Millisecond
	cyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	before, err := loadLockForTest(t, s, cycleLockName)
	if err != nil {
		t.Fatalf("loadLockForTest: %v", err)
	}

	stop := s.KeepCycleLock(ctx, cyc.ID)
	// self-review 指摘（round1, code-reviewer CONFIRMED）: t.Fatal はこの
	// goroutine を即座に終了させるため、stop() を呼ばないまま関数を抜けると
	// KeepCycleLock の goroutine が t.TempDir() のストアへ書き込み続けたまま
	// テストの後始末（ディレクトリ削除）と競合しうる。t.Cleanup で確実に
	// 止める（stop は複数回呼んでも安全なので、下の明示的な stop() と共存できる）。
	t.Cleanup(stop)

	deadline := time.Now().Add(2 * time.Second)
	for {
		after, err := loadLockForTest(t, s, cycleLockName)
		if err != nil {
			t.Fatalf("loadLockForTest: %v", err)
		}
		if after != nil && after.HeartbeatAt.After(before.HeartbeatAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("KeepCycleLock did not tick a heartbeat update within the deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// self-review 指摘（round1, code-reviewer CONFIRMED・round2,
	// code-reviewer CONFIRMED〈baseline を stop() の前に読むと、stop() 呼び出し
	// 前後で飛び込んだ heartbeat の書き込みと競合してテストが flaky になる〉）:
	// stop() が goroutine の終了を待たずに返ると、stop() 後にもう1回
	// heartbeat が打たれる競合状態をこのテストでは検出できない。stop() は
	// goroutine の終了を待ってから返る（KeepCycleLock の実装）ので、まず
	// stop() を呼び切ってから heartbeat_at を読み、その後何周期待っても
	// 変わらないことを確かめる（baseline を stop() の後に読むことで、
	// stop() 呼び出し中に飛び込む最後の1回の heartbeat を baseline へ含め、
	// flaky さを排除する）。
	stop()
	stop() // 複数回呼んでも安全であることの検証

	afterStop, err := loadLockForTest(t, s, cycleLockName)
	if err != nil {
		t.Fatalf("loadLockForTest: %v", err)
	}
	time.Sleep(5 * s.lockHeartbeatInterval)
	afterWait, err := loadLockForTest(t, s, cycleLockName)
	if err != nil {
		t.Fatalf("loadLockForTest: %v", err)
	}
	if afterStop != nil && afterWait != nil && !afterWait.HeartbeatAt.Equal(afterStop.HeartbeatAt) {
		t.Errorf("heartbeat_at kept advancing after stop(): %v -> %v", afterStop.HeartbeatAt, afterWait.HeartbeatAt)
	}
}

// self-review 指摘（round2, code-reviewer CONFIRMED〈低優先度だが低コストで
// 検証できるため追加〉）: KeepCycleLock の goroutine は ctx がキャンセルされても
// 終了することを、stop() を呼ばずに確かめる（stop を呼ばずに放置しても
// 永久には heartbeat を打ち続けない）。
// self-review 指摘（round3, code-reviewer CONFIRMED）: 「cancel 後に
// heartbeat_at が進まない」ことだけを見るテストは、ctx.Done() の分岐を
// 削っても（HeartbeatCycleLock が context.Canceled な ctx で BeginTx に
// 失敗し続けるだけになり、heartbeat_at はやはり進まないため）区別できず、
// 実際にレビュアーが分岐を削る変異でテストを緑のまま通した。stop() を
// 呼ばずに goroutine の終了そのもの（keepCycleLockWithStoppedSignal が
// 返す stopped channel の close）を観測する形に書き直す。
func TestKeepCycleLock_ExitsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newStoreForTest(t)
	s.lockHeartbeatInterval = 10 * time.Millisecond
	cyc, err := s.BeginCycle(context.Background(), BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}

	stop, stopped := s.keepCycleLockWithStoppedSignal(ctx, cyc.ID)
	t.Cleanup(stop)

	select {
	case <-stopped:
		t.Fatal("goroutine exited before ctx was canceled")
	case <-time.After(30 * time.Millisecond):
	}

	cancel()
	// stop() を呼ばない: ctx のキャンセルだけで goroutine 自身が終了し、
	// stopped が close されることを直接確かめる。
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("KeepCycleLock's goroutine did not exit within the deadline after ctx was canceled (without calling stop())")
	}
}

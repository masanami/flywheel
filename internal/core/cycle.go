package core

// このファイルは #83（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §予算ガード・§一括の操作（サイクル）§サイクルの排他・§クリティカル設計決定 1・4）
// が足す、周（cycle）の開始・終了・サイクルの排他（lock）の公開 API を持つ。
//
// 【決定 2026-09-28 親】（#83）: `--auto` の個別の操作（`classify --auto` 等）も
// `cycle` 表に 1 行を作る（trigger は呼び出し元が渡す操作名。例:
// "classify --auto"）。サイクルの排他ロックを取るのは `flywheel cycle`
// （BeginCycleInput.Exclusive=true）だけで、個別の操作はロックを取らない
// （BeginCycleInput.Exclusive=false）。どちらも cycle 行を作ることで、
// 予約額・既消費額の評価（§予算ガード）が同じ規則（cycle_id で束ねる）に乗る。
//
// run の予算評価そのもの（RunJudgmentInput.CycleID・①のトランザクションでの
// 評価）は judgment.go に置く（RunJudgment 自体の一部であり、二重の書き込み
// トランザクションを避けるため）。このファイルは、周の開始・終了・
// サイクルの排他ロックの読み書きだけを持つ。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// cycleLockName はサイクルの排他ロック（lock 表）の名前。1 つしか無い
// （§サイクルの排他「ストアの中の名前つきのロック」）。
const cycleLockName = "cycle"

// defaultLockStaleAfter は、サイクルの排他ロックの保持者の heartbeat が
// これより古く、かつ保持者のプロセスがそのホストで生きていなければ stale
// として回収する閾値（§サイクルの排他「保持者の heartbeat が300秒より
// 古く…」）。judgment.go の defaultStaleAfter（run の heartbeat の回収）とは
// 別の閾値として持つ（テストが独立に差し替えられるように。値は同じ300秒）。
const defaultLockStaleAfter = 300 * time.Second

// defaultLockHeartbeatInterval は、排他ロックを保持している周が heartbeat を
// 更新する既定の間隔（§サイクルの排他「60秒ごとにheartbeatを更新する」）。
const defaultLockHeartbeatInterval = 60 * time.Second

// ErrLocked は、生きている別の周がサイクルの排他ロックを保持していることを
// 表す（§IF / API エラーコードの追加「locked」）。
var ErrLocked = errors.New("core: another cycle holds the exclusive lock")

// ErrBudgetExceeded は、周の上限額（既消費額＋予約額＋評価額）を超える、または計画の版の
// 実装枠の残りが 1 USD に満たないため run を起動できないことを表す（§IF / API エラーコードの追加
// 「budget_exceeded」・§予算ガード）。
var ErrBudgetExceeded = errors.New("core: this run would exceed the cycle budget or the plan version's implementation budget")

// ErrLockLost は、サイクルの排他ロックの保持者が別の周に替わっている
// （stale回収で奪われた等）ことを表す。HeartbeatCycleLock が、渡した周が
// 現在の保持者でないときに返す。
var ErrLockLost = errors.New("core: this cycle no longer holds the exclusive lock")

// Cycle は BeginCycle・EndCycle が返す、周 1 件の表示用の形
// （§IF / API「cycle の JSON 出力」の `cycle` の形の元になる値。USD への
// 変換はここで行う）。
type Cycle struct {
	ID        string
	Trigger   string
	StartedAt time.Time
	EndedAt   *time.Time
	Result    CycleResult
	BudgetUSD float64
	SpentUSD  float64
}

// CycleResult は cycle.result の閉集合の公開エイリアス（run_store.go の
// cycleResult をそのまま再輸出する。judgment.go の RunResult と同じ形）。
type CycleResult = cycleResult

// CycleResult の3値。
const (
	CycleResultCompleted   CycleResult = cycleResultCompleted
	CycleResultAborted     CycleResult = cycleResultAborted
	CycleResultInterrupted CycleResult = cycleResultInterrupted
)

func toPublicCycle(c *cycleRow) *Cycle {
	pub := &Cycle{
		ID:        c.ID,
		Trigger:   c.Trigger,
		StartedAt: c.StartedAt,
		Result:    c.Result,
		BudgetUSD: microsToUSD(c.BudgetUSD),
		SpentUSD:  microsToUSD(c.SpentUSD),
	}
	if c.EndedAt != nil {
		t := *c.EndedAt
		pub.EndedAt = &t
	}
	return pub
}

// BeginCycleInput は BeginCycle の入力。
type BeginCycleInput struct {
	// Trigger はこの周の起動の契機（"cycle" は "cron"/"manual" 等、
	// --auto の個別の操作は呼び出し元がその操作名（例: "classify --auto"）を
	// 渡す【決定 2026-09-28 親】。空文字は ErrValidation。
	Trigger string
	// BudgetUSD はこの周の上限額（USD）。0以下は ErrValidation。
	BudgetUSD float64
	// Exclusive が true なら、サイクルの排他ロックを取る（flywheel cycle が
	// 使う）。false なら、ロックには触れずに cycle 行だけを作る（--auto の
	// 個別の操作が使う。§サイクルの排他「個別の操作はサイクルのロックを
	// 取らない」）。
	Exclusive bool
}

// BeginCycle は新しい周を記録する（§一括の操作（サイクル）・§予算ガード・
// §サイクルの排他）。
//
// Exclusive=true のときは、1 つの書き込みトランザクションで:
//
//  1. サイクルの排他ロックを読む
//  2. 生きている保持者がいれば、何も作らず ErrLocked で終わる（cycle 行も
//     作らない）
//  3. 保持者が stale（heartbeat が閾値より古く、かつそのホストでプロセスが
//     生きていない）なら、古いロックを消して回収する（古い周がまだ
//     終了していなければ、result=interrupted・ended_at=now で閉じる）
//  4. 新しい cycle 行を作り、その周を保持者としてロックを作る
//
// を行う。Exclusive=false のときは、ロックに一切触れず cycle 行だけを作る
// （§サイクルの排他「個別の操作はサイクルのロックを取らない」。
// 同じ課題に対する二重の起動はストアの一意制約〈run の部分一意索引〉で
// 防ぐ、という既存の規則をそのまま使う）。
func (s *Store) BeginCycle(ctx context.Context, in BeginCycleInput) (*Cycle, error) {
	if in.Trigger == "" || in.BudgetUSD <= 0 {
		return nil, ErrValidation
	}

	// self-review 指摘（round1, code-reviewer CONFIRMED・design-reviewer
	// CONFIRMED）: stale なロックを回収するとき、古い周の保持者が残した
	// 「終了していない run」（プロセスが死んでいる以上、その run の heartbeat も
	// 同じだけ古い）を先に interrupted へ回収しておかないと、
	// closeCycleAsEnded が計算する spent_usd がその run の費用を含まないまま
	// 周が閉じてしまい、二度と埋まらない。judgment.go の RunJudgment・
	// runs_query.go の ListRuns と同じく、①のトランザクションを開く前に
	// ReapInterruptedRuns を呼んでおく（トランザクションの外＝短い書き込みの
	// 積み重ねで、①の中から呼ぶとロックを二重に取ろうとして自己ブロックする）。
	if err := s.ReapInterruptedRuns(ctx); err != nil {
		return nil, err
	}

	host, _ := os.Hostname()
	pid := int64(os.Getpid())
	now := s.currentTime()
	budgetMicros := usdToMicros(in.BudgetUSD)

	var result *cycleRow
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if in.Exclusive {
			if err := s.reclaimStaleLockOrErrLocked(ctx, tx, now); err != nil {
				return err
			}
		}

		row, err := insertCycle(ctx, tx, insertCycleInput{Trigger: in.Trigger, StartedAt: now, BudgetUSD: budgetMicros})
		if err != nil {
			return err
		}
		result = row

		if in.Exclusive {
			cycleIDInt, ok := parseCycleID(row.ID)
			if !ok {
				return fmt.Errorf("core: BeginCycle: malformed cycle id %q", row.ID)
			}
			if _, err := insertLock(ctx, tx, insertLockInput{
				Name: cycleLockName, Holder: cycleIDInt, PID: pid, Host: host, AcquiredAt: now, HeartbeatAt: now,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, classifyReadWriteErr(err)
	}
	return toPublicCycle(result), nil
}

// reclaimStaleLockOrErrLocked は、サイクルの排他ロックが無ければ何もしない
// （呼び出し元が新しく取得してよい）。ロックがあり、生きている保持者なら
// ErrLocked を返す。stale なら、古いロックを消し（古い周が終了していなければ
// interrupted で閉じてから）、呼び出し元が新しいロックを作れるようにする。
func (s *Store) reclaimStaleLockOrErrLocked(ctx context.Context, tx *sql.Tx, now time.Time) error {
	lock, err := loadLockByName(ctx, tx, cycleLockName)
	if err != nil {
		return err
	}
	if lock == nil {
		return nil
	}

	threshold := s.lockStaleAfter
	if threshold <= 0 {
		threshold = defaultLockStaleAfter
	}
	stale := now.Sub(lock.HeartbeatAt) > threshold && !isRunProcessAlive(lock.Host, lock.PID)
	if !stale {
		return ErrLocked
	}

	holderID, ok := parseCycleID(lock.Holder)
	if !ok {
		return fmt.Errorf("core: reclaim stale lock: malformed holder %q", lock.Holder)
	}
	oldCycle, err := loadCycleByID(ctx, tx, holderID)
	if err != nil {
		return err
	}
	if oldCycle != nil && oldCycle.EndedAt == nil {
		// self-review 指摘（round2, code-reviewer CONFIRMED）: 外側の
		// BeginCycle が呼ぶ s.ReapInterruptedRuns（トランザクションの外）は、
		// run 自身の heartbeat が defaultStaleAfter より古いものしか回収しない。
		// run の heartbeat（invokeWithHeartbeat）とロックの heartbeat
		// （KeepCycleLock）はどちらも既定60秒間隔の別々のティッカーで、同じ
		// 死んだプロセスが両方を更新するため、run の heartbeat がロックの
		// heartbeat よりわずかに新しい（＝run 単体ではまだ stale 未満の）窓が
		// 生じうる。ここではロックの保持者（pid・host）が死んでいることを
		// 既に確認済みなので、run 自身の heartbeat の古さに関わらず、
		// その pid・host の終了していない run を直接 interrupted で閉じる
		// （ReapInterruptedRuns より狭い条件で確実に閉じる）。
		if err := closeDeadHolderRuns(ctx, tx, holderID, lock.PID, lock.Host, now); err != nil {
			return err
		}
		if err := closeCycleAsEnded(ctx, tx, holderID, now, cycleResultInterrupted); err != nil {
			return err
		}
	}
	return deleteLock(ctx, tx, cycleLockName)
}

// closeDeadHolderRuns は cycleID に属する「終了していない run」のうち、
// pid・host が一致するもの（＝ちょうど回収したロックの保持者と同じ、
// 死亡が確認済みのプロセス）を interrupted で閉じる（費用は渡した上限額、
// 出所 unknown。§費用の記録「結果のJSONを得られなかった…ときは…
// max-budget-usdの額を費用とし、出所をunknownにする」と同じ規則）。
func closeDeadHolderRuns(ctx context.Context, tx *sql.Tx, cycleID, pid int64, host string, now time.Time) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, max_budget_usd FROM run WHERE cycle_id = ? AND ended_at IS NULL AND pid = ? AND host = ?`,
		cycleID, pid, host,
	)
	if err != nil {
		return err
	}
	type orphan struct {
		id           int64
		maxBudgetUSD int64
	}
	var orphans []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.id, &o.maxBudgetUSD); err != nil {
			_ = rows.Close()
			return err
		}
		orphans = append(orphans, o)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, o := range orphans {
		cost := o.maxBudgetUSD
		if _, err := updateRunEnd(ctx, tx, o.id, updateRunEndInput{
			EndedAt: now, Result: runResultInterrupted, CostUSD: &cost, CostSource: costSourceUnknown,
		}); err != nil {
			return err
		}
	}
	return nil
}

// EndCycle は id の周を終える（§一括の操作（サイクル）「排他を…解放する」・
// §予算ガード「周の既消費額は、その周に終了した run の費用の合計」）:
//
//   - spent_usd を、その周の終了した run の cost_usd の合計へ更新する
//   - ended_at・result を記録する
//   - その周がサイクルの排他ロックの保持者であれば解放する（保持者が
//     この周でなければロックには触れない。§サイクルの排他「取得に成功した
//     ロックだけを解放する」）
//
// id が周の ID の形式でない・存在しなければ ErrNotFound。result が閉集合の
// 外なら ErrValidation。
func (s *Store) EndCycle(ctx context.Context, cycleID string, result CycleResult) (*Cycle, error) {
	id, ok := parseCycleID(cycleID)
	if !ok {
		return nil, ErrNotFound
	}
	if !result.valid() {
		return nil, ErrValidation
	}
	now := s.currentTime()

	var row *cycleRow
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		cyc, err := loadCycleByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if cyc == nil {
			return ErrNotFound
		}
		if err := closeCycleAsEnded(ctx, tx, id, now, result); err != nil {
			return err
		}
		updated, err := loadCycleByID(ctx, tx, id)
		if err != nil {
			return err
		}
		row = updated
		return releaseCycleLockIfHolder(ctx, tx, formatCycleID(id))
	})
	if err != nil {
		return nil, classifyReadWriteErr(err)
	}
	return toPublicCycle(row), nil
}

// closeCycleAsEnded は id の周の spent_usd を（その時点で終了している run の
// cost_usd の合計へ）再計算してから、ended_at・result を記録する。
func closeCycleAsEnded(ctx context.Context, tx *sql.Tx, id int64, endedAt time.Time, result cycleResult) error {
	spent, err := cycleSpentMicros(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cycle SET spent_usd = ? WHERE id = ?`, spent, id); err != nil {
		return err
	}
	_, err = updateCycleEnd(ctx, tx, id, updateCycleEndInput{EndedAt: endedAt, Result: result})
	return err
}

// releaseCycleLockIfHolder は、サイクルの排他ロックの保持者が
// holderCycleIDDisplay（"Y-<n>" の形）であれば解放する。保持者が違う・
// ロックが無ければ何もしない（§サイクルの排他「保持者が自分でなければ
// 解放しない」）。
func releaseCycleLockIfHolder(ctx context.Context, tx *sql.Tx, holderCycleIDDisplay string) error {
	lock, err := loadLockByName(ctx, tx, cycleLockName)
	if err != nil {
		return err
	}
	if lock == nil || lock.Holder != holderCycleIDDisplay {
		return nil
	}
	return deleteLock(ctx, tx, cycleLockName)
}

// ReleaseCycleLock は cycleID がサイクルの排他ロックの保持者であれば解放する
// （保持者が違えば何もしない。EndCycle が呼ぶのと同じ規則を、周を終えずに
// 単独で呼びたい場合のために公開する）。
func (s *Store) ReleaseCycleLock(ctx context.Context, cycleID string) error {
	id, ok := parseCycleID(cycleID)
	if !ok {
		return ErrNotFound
	}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		return releaseCycleLockIfHolder(ctx, tx, formatCycleID(id))
	})
	return classifyReadWriteErr(err)
}

// HeartbeatCycleLock は cycleID がサイクルの排他ロックの保持者であれば
// heartbeat_at を更新する（§サイクルの排他「ロックを保持している cycle は
// 60秒ごとに heartbeat を更新する」）。保持者が違う・ロックが無ければ
// ErrLockLost を返し、何も変えない。
func (s *Store) HeartbeatCycleLock(ctx context.Context, cycleID string) error {
	id, ok := parseCycleID(cycleID)
	if !ok {
		return ErrNotFound
	}
	now := s.currentTime()
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		lock, err := loadLockByName(ctx, tx, cycleLockName)
		if err != nil {
			return err
		}
		if lock == nil || lock.Holder != formatCycleID(id) {
			return ErrLockLost
		}
		_, err = updateLockHeartbeat(ctx, tx, cycleLockName, now)
		return err
	})
	if err != nil {
		return classifyReadWriteErr(err)
	}
	return nil
}

// KeepCycleLock は、返り値の stop が呼ばれるまで、既定 60 秒
// （s.lockHeartbeatInterval で差し替え可能。テスト専用）ごとに
// HeartbeatCycleLock(ctx, cycleID) を呼び続ける goroutine を起動する
// （§サイクルの排他「60秒ごとにheartbeatを更新する」の巡回を担う補助。
// `cycle` の各段〈ingest→classify→plan→…〉を実行する呼び出し元が、
// BeginCycle(Exclusive) の直後に呼び、段の実行が終わったら stop することを
// 想定する）。heartbeat の更新の失敗はベストエフォート（次の
// BeginCycle(Exclusive) の stale 回収が最終的な安全網になる。judgment.go の
// invokeWithHeartbeat と同じ設計）。
//
// self-review 指摘（round1, code-reviewer PLAUSIBLE・design-reviewer
// PLAUSIBLE）: ctx がキャンセルされても goroutine が終わらず heartbeat を
// 打ち続ける経路と、stop がgoroutineの終了を待たずに返る経路（EndCycle・
// Store.Close と競合しうる）があった。ctx.Done() でも終了し、stop は
// goroutine の終了を待ってから返す（invokeWithHeartbeat は呼び出し元が
// 子プロセスの終了を待つ間だけ動くため、この種の競合を持たない。
// KeepCycleLock は呼び出し元が明示的に stop するまで動き続けるため、
// stop 後に他の書き込み〈EndCycle 等〉と重ならないことを保証する必要がある）。
// stop は複数回呼んでも安全。
func (s *Store) KeepCycleLock(ctx context.Context, cycleID string) (stop func()) {
	stop, _ = s.keepCycleLockWithStoppedSignal(ctx, cycleID)
	return stop
}

// keepCycleLockWithStoppedSignal は KeepCycleLock の実体。stopped は
// goroutine が終了した時点で close される channel で、テストが
// stop() を呼ばずに「ctx のキャンセルだけで goroutine が実際に終了したか」を
// 直接観測するために公開する（self-review 指摘 round3, code-reviewer
// CONFIRMED: heartbeat_at が進まないことだけを見るテストは、ctx.Done() の
// 分岐を削っても〈goroutine が回り続けて HeartbeatCycleLock が
// context.Canceled で失敗し続けるだけになるので〉heartbeat_at は同様に
// 進まず、区別できなかった。goroutine の終了そのものを見るテストに
// できるよう、内部実装を分離する）。
func (s *Store) keepCycleLockWithStoppedSignal(ctx context.Context, cycleID string) (stop func(), stopped <-chan struct{}) {
	interval := s.lockHeartbeatInterval
	if interval <= 0 {
		interval = defaultLockHeartbeatInterval
	}
	done := make(chan struct{})
	stoppedCh := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(stoppedCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.HeartbeatCycleLock(ctx, cycleID)
			}
		}
	}()
	return func() {
		once.Do(func() { close(done) })
		<-stoppedCh
	}, stoppedCh
}

// --- 予算ガード（§予算ガード）の評価に使う、周の既消費額・予約額 ---

// checkCycleBudget は、周 cycleIDDisplay（"Y-<n>"）に評価額 maxBudgetUSD の run を
// 起動できるかを、読み取りだけで確かめる（§予算ガードの評価式「既消費額 ＋ 予約額 ＋
// 評価額 ＞ 周の上限額なら起動しない」。RunJudgment の①と同じ式）。起動できなければ
// ErrBudgetExceeded。周が存在しない・既に終了していれば ErrValidation。起動の前に
// 高価な前処理（上流の取得）をする判断点が、上限を使い切った周でその前処理を
// 無駄に行わないための事前検査であり、最終の判定は RunJudgment の①が行う
// （この検査と①の間に他の run が起動しても、①が拒否する）。
func (s *Store) checkCycleBudget(ctx context.Context, cycleIDDisplay string, maxBudgetUSD float64) error {
	cyid, ok := parseCycleID(cycleIDDisplay)
	if !ok {
		return ErrValidation
	}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return evalCycleBudgetTx(ctx, tx, cyid, usdToMicros(maxBudgetUSD))
	})
	return classifyReadWriteErr(err)
}

// evalCycleBudgetTx は §予算ガードの評価式「既消費額 ＋ 予約額 ＋ 評価額 ＞ 周の上限額
// なら起動しない」を、周 cyid（内部整数 ID）について tx の中で評価する
// （RunJudgment の①〈書き込みトランザクション〉と checkCycleBudget〈読み取り〉が共有する
// 唯一の実装。評価額 maxBudgetMicros は USD の 100 万分の 1 の整数）。周が存在しない・
// 既に終了していれば ErrValidation、式が真なら ErrBudgetExceeded（等号は起動してよい）。
func evalCycleBudgetTx(ctx context.Context, tx *sql.Tx, cyid, maxBudgetMicros int64) error {
	cyc, err := loadCycleByID(ctx, tx, cyid)
	if err != nil {
		return err
	}
	if cyc == nil || cyc.EndedAt != nil {
		return ErrValidation
	}
	spent, err := cycleSpentMicros(ctx, tx, cyid)
	if err != nil {
		return err
	}
	reserved, err := cycleReservedMicros(ctx, tx, cyid)
	if err != nil {
		return err
	}
	if spent+reserved+maxBudgetMicros > cyc.BudgetUSD {
		return ErrBudgetExceeded
	}
	return nil
}

// cycleSpentMicros は cycleID の周に属する「終了した run」（ended_at IS NOT
// NULL）の cost_usd の合計を、USD の100万分の1単位の整数で返す（§予算ガード
// 「周の既消費額は、その周に終了した run の費用の合計」）。
func cycleSpentMicros(ctx context.Context, tx *sql.Tx, cycleID int64) (int64, error) {
	var v int64
	err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost_usd), 0) FROM run WHERE cycle_id = ? AND ended_at IS NOT NULL`, cycleID,
	).Scan(&v)
	return v, err
}

// cycleReservedMicros は cycleID の周に属する「終了していない run」
// （ended_at IS NULL）に渡した max_budget_usd の合計を、USD の100万分の1単位の
// 整数で返す（§予算ガード「その周の終了していない run に渡した上限額の合計
// （予約額）」）。
func cycleReservedMicros(ctx context.Context, tx *sql.Tx, cycleID int64) (int64, error) {
	var v int64
	err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(max_budget_usd), 0) FROM run WHERE cycle_id = ? AND ended_at IS NULL`, cycleID,
	).Scan(&v)
	return v, err
}

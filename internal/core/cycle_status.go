package core

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CycleState はサイクルの実行の状態（UI の「1 周回す」の表示用）。
type CycleState string

// CycleState の 3 値。
const (
	CycleStateIdle    CycleState = "idle"
	CycleStateRunning CycleState = "running"
	CycleStateStale   CycleState = "stale"
)

// CycleStatus は ReadCycleStatus の結果。
type CycleStatus struct {
	State CycleState
	// LockHeld はサイクルの排他ロックの行があること（heartbeat の新旧によらない）。
	LockHeld bool
	// LastStartedAt は個別の操作の周を除いた周のうち ID が最大のものの開始時刻。
	// LastEndedAt・LastResult は個別の操作の周を除いた終了した周のうち ID が最大のものの値。
	// 該当が無ければ nil。
	LastStartedAt *time.Time
	LastEndedAt   *time.Time
	LastResult    *CycleResult
	// LastEndReason は LastEndedAt の周の終わった理由（過去の行・理由の無い周は nil）。
	LastEndReason *CycleEndReason
	// Running は走っているサイクル（State が running のとき、排他を持つ周）の様子。走っていなければ nil。
	Running *RunningCycle
}

// WatchState はサイクルが見ている実行の様子（表示の区分。core が決める）。
type WatchState string

// WatchState の 3 値。working は同時に走らせる数に数える実行、waiting_owner はオーナーの回答か承認を待つ実行
// （課題が保留・完了の承認待ち）、verifying はそのどちらでもない終わっていない実行。
const (
	WatchStateWorking      WatchState = "working"
	WatchStateVerifying    WatchState = "verifying"
	WatchStateWaitingOwner WatchState = "waiting_owner"
)

// WatchStateValues は WatchState の閉集合の全値を返す。
func WatchStateValues() []WatchState {
	return []WatchState{WatchStateWorking, WatchStateVerifying, WatchStateWaitingOwner}
}

// WatchedRun はサイクルが見ている、終わっていない実行 1 件。
type WatchedRun struct {
	ChallengeID  string
	TaskID       string // タスクに結び付かない実行では空。
	Title        string // タスクの題名（タスクに結び付かない実行では課題の題名）。
	State        WatchState
	RunStartedAt time.Time
	SpentUSD     float64
}

// RunningCycle は走っているサイクルの様子。
type RunningCycle struct {
	StartedAt       time.Time
	Trigger         string
	Rounds          int
	ActiveRuns      int
	MaxParallelRuns int
	SpentUSD        float64
	BudgetUSD       float64
	Watching        []WatchedRun
	StopRequested   bool
}

// notIndividualOpFilter は個別の操作（run・classify --auto・plan --auto・verify --auto・improve・learn・op exec）の周を
// 除く条件。トリガーの値は internal/cli の各コマンドの Trigger に固定されている。手動の improve・learn・
// op exec も個別の操作として除く。
const notIndividualOpFilter = `trigger NOT IN ('run', 'classify --auto', 'plan --auto', 'verify --auto', 'improve', 'learn', '` + ReleaseExecTriggerOpExec + `')`

// ReadCycleStatus はサイクルの排他ロックと周の記録を読んで状態を導く。読むだけで、
// 古いロックの回収・周の終了はしない（回収は次の cycle の BeginCycle が行う）。
//
// state はロックだけで決める。heartbeat_at が閾値（300 秒。BeginCycle の回収と同じ値）以内なら
// running、より古ければ stale（保持者のプロセスの生死は見ない）、ロックが無ければ idle
// （終了していない周の有無は数えない）。
func (s *Store) ReadCycleStatus(ctx context.Context) (*CycleStatus, error) {
	now := s.currentTime()
	threshold := s.lockStaleThreshold()
	out := &CycleStatus{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		lock, err := loadLockByName(ctx, tx, cycleLockName)
		if err != nil {
			return err
		}
		switch {
		case lock != nil:
			out.LockHeld = true
			if now.Sub(lock.HeartbeatAt) <= threshold {
				out.State = CycleStateRunning
			} else {
				out.State = CycleStateStale
			}
		default:
			out.State = CycleStateIdle
		}

		// ID は整数の主キー（表示は "Y-<n>"）なので、最大は整数で比べる。
		latest, err := scanOptionalCycle(tx.QueryRowContext(ctx, cycleSelectColumns+` WHERE `+notIndividualOpFilter+` ORDER BY id DESC LIMIT 1`))
		if err != nil {
			return err
		}
		if latest != nil {
			t := latest.StartedAt
			out.LastStartedAt = &t
		}
		ended, err := scanOptionalCycle(tx.QueryRowContext(ctx, cycleSelectColumns+` WHERE ended_at IS NOT NULL AND `+notIndividualOpFilter+` ORDER BY id DESC LIMIT 1`))
		if err != nil {
			return err
		}
		if ended != nil {
			e := *ended.EndedAt
			r := ended.Result
			out.LastEndedAt, out.LastResult = &e, &r
			if ended.EndReason != "" {
				er := CycleEndReason(ended.EndReason)
				out.LastEndReason = &er
			}
		}
		if out.State == CycleStateRunning {
			// 同時に走らせる数の上限は宣言の値（宣言が無ければ既定）。走っているときだけ読む
			// （宣言が不備でも、state・last_* は読める）。
			decl, derr := LoadAgentDeclaration(s.workspace)
			if derr != nil {
				return derr
			}
			out.Running, err = readRunningCycle(ctx, tx, lock.Holder, decl.MaxParallelRuns)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, classifyReadWriteErr(err)
	}
	return out, nil
}

func scanOptionalCycle(row interface{ Scan(dest ...any) error }) (*cycleRow, error) {
	c, err := scanCycleRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// readRunningCycle は排他を持つ周 holder（"Y-<n>"）の様子を読む。周の記録が無ければ nil。
func readRunningCycle(ctx context.Context, tx *sql.Tx, holder string, maxParallel int) (*RunningCycle, error) {
	id, ok := parseCycleID(holder)
	if !ok {
		return nil, nil
	}
	c, err := scanOptionalCycle(tx.QueryRowContext(ctx, cycleSelectColumns+` WHERE id = ?`, id))
	if err != nil || c == nil {
		return nil, err
	}
	var stop sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT stop_requested_at FROM cycle WHERE id = ?`, id).Scan(&stop); err != nil {
		return nil, err
	}
	watching, err := readWatchedRuns(ctx, tx)
	if err != nil {
		return nil, err
	}
	// 作業中の件数は、同時に走らせる数の判定（スケジューラ）と同じ数え方の 1 か所に任せる。
	active, err := activeDelegationCount(ctx, tx)
	if err != nil {
		return nil, err
	}
	// 使った額は、その周の終わっている run の費用の合計（周の記録の spent_usd は、周が終わるときにだけ書かれる）。
	spent, err := cycleSpentMicros(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return &RunningCycle{
		StartedAt: c.StartedAt, Trigger: c.Trigger, Rounds: c.Rounds, ActiveRuns: active, MaxParallelRuns: maxParallel,
		SpentUSD: microsToUSD(spent), BudgetUSD: microsToUSD(c.BudgetUSD), Watching: watching,
		StopRequested: stop.Valid,
	}, nil
}

// readWatchedRuns は終わっていない実行（委譲の run・終わっていない契約の run）を、課題とタスクの組ごとに 1 件、
// 実行の始まった順に返す。working は activeDelegationCount の数え方と同じ条件（同時に走らせる数に数える実行）。
func readWatchedRuns(ctx context.Context, tx *sql.Tx) ([]WatchedRun, error) {
	const q = `
SELECT challenge_id, task_id, title, started_at, cost, working, owner_wait FROM (
  SELECT r.challenge_id AS challenge_id, r.task_id AS task_id, COALESCE(t.title, c.title) AS title, r.started_at AS started_at,
         COALESCE(r.cost_usd, 0) AS cost, 1 AS working, 0 AS owner_wait, r.id AS seq
    FROM run r JOIN challenge c ON c.id = r.challenge_id LEFT JOIN task t ON t.id = r.task_id
   WHERE r.kind = 'delegate' AND r.result IS NULL
  UNION ALL
  SELECT k.challenge_id, k.task_id, COALESCE(t.title, c.title), k.started_at, k.cost_usd,
         CASE WHEN NOT ` + contractRunReviewWaitWhere + ` AND NOT ` + ownerWaitContractRunWhere + ` AND NOT ` + endRecordedContractRunWhere + ` THEN 1 ELSE 0 END,
         CASE WHEN c.status IN ('awaiting_human', 'awaiting_completion_approval') THEN 1 ELSE 0 END, k.id
    FROM contract_run k JOIN challenge c ON c.id = k.challenge_id LEFT JOIN task t ON t.id = k.task_id
   WHERE k.ended_at IS NULL
  UNION ALL
  SELECT r.challenge_id, r.task_id, COALESCE(t.title, c.title), r.started_at, COALESCE(r.cost_usd, 0), 1, 0, r.id
    FROM run r JOIN challenge c ON c.id = r.challenge_id LEFT JOIN task t ON t.id = r.task_id
   WHERE r.kind = 'contract' AND r.result IS NULL
) ORDER BY started_at, seq`
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []WatchedRun{}
	seen := map[[2]int64]int{}
	for rows.Next() {
		var (
			cid                   int64
			taskID                sql.NullInt64
			title, startedAt      string
			cost                  int64
			working, ownerWaiting int
		)
		if err := rows.Scan(&cid, &taskID, &title, &startedAt, &cost, &working, &ownerWaiting); err != nil {
			return nil, err
		}
		started, err := parseTimestamp(startedAt)
		if err != nil {
			return nil, err
		}
		w := WatchedRun{ChallengeID: formatChallengeID(cid), Title: title, RunStartedAt: started, SpentUSD: microsToUSD(cost)}
		if taskID.Valid {
			w.TaskID = formatTaskID(taskID.Int64)
		}
		switch {
		case working == 1:
			w.State = WatchStateWorking
		case ownerWaiting == 1:
			w.State = WatchStateWaitingOwner
		default:
			w.State = WatchStateVerifying
		}
		// 課題とタスクの組ごとに 1 件（契約の実行は同時に走らせる数の数え方と同じく組ごとに 1 と見る。件数 active_runs は数え方の 1 か所の値を使う）。
		key := [2]int64{cid, taskID.Int64}
		if i, dup := seen[key]; dup {
			if out[i].State != WatchStateWorking && w.State == WatchStateWorking {
				out[i].State = WatchStateWorking
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, w)
	}
	return out, rows.Err()
}

// CycleStopResult は RequestCycleStop の結果。
type CycleStopResult struct {
	// Requested は走っているサイクルの記録へ止める指示（stop_requested_at）を書いたこと。
	// 走っているサイクルが無い（排他が無い・heartbeat が回収の時間を過ぎている）ときは false で、何も書かない。
	Requested bool
	// CycleID は止める指示を書いたサイクル（Requested が true のとき）。
	CycleID string
}

// RequestCycleStop は、排他を持っているサイクル（heartbeat が回収の時間以内）の記録の stop_requested_at に時刻を書く。
// 走っているサイクルが無ければ何も書かずに Requested=false を返す（エラーにしない）。すでに止める指示があれば時刻を
// 書き換えない。委譲先の実行は取り消さない。
func (s *Store) RequestCycleStop(ctx context.Context) (*CycleStopResult, error) {
	now := s.currentTime()
	threshold := s.lockStaleThreshold()
	out := &CycleStopResult{}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		lock, err := loadLockByName(ctx, tx, cycleLockName)
		if err != nil || lock == nil || now.Sub(lock.HeartbeatAt) > threshold {
			return err
		}
		id, ok := parseCycleID(lock.Holder)
		if !ok {
			return nil
		}
		res, err := tx.ExecContext(ctx, `UPDATE cycle SET stop_requested_at = COALESCE(stop_requested_at, ?) WHERE id = ? AND ended_at IS NULL`, formatTimestamp(now), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out.Requested, out.CycleID = true, formatCycleID(id)
		}
		return nil
	})
	if err != nil {
		return nil, classifyReadWriteErr(err)
	}
	return out, nil
}

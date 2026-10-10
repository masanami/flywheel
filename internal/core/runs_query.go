package core

// このファイルは #81（docs/features/m3-invoker-delegation.md §観測
// 「flywheel runs [<C-ID>] [--open]」・§IF / API「runs」）が足す、run の一覧の
// 読み取り専用 API（Store.ListRuns）を持つ。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Run は ListRuns が返す、run 1 件の表示用の形（§IF / API「runs」）。
// Judgment は委譲（kind=delegate）では ""。Result・CostUSD・CostSource・
// EndedAt は終了していない run では未設定（Result・CostSource は ""、
// CostUSD・EndedAt は nil）。CycleBudgetUSD は CycleID が nil（#83 決定
// 2026-09-28 親: 周の外で記録された run。例えば #83 より前の記録）なら nil、
// そうでなければその周（cycle）の上限額（USD）。
type Run struct {
	ID             string
	Kind           RunKind
	Judgment       JudgmentPoint
	ChallengeID    string
	CycleID        *string
	CycleBudgetUSD *float64
	SessionID      string
	Result         RunResult
	RateLimited    bool
	CostUSD        *float64
	CostSource     CostSource
	MaxBudgetUSD   float64
	StartedAt      time.Time
	EndedAt        *time.Time
}

// RunListOptions は ListRuns の絞り込み条件。
type RunListOptions struct {
	// ChallengeID が非nilなら、その課題（"C-<n>"）の run だけに絞る
	// （形式不正・存在しない課題は ErrNotFound）。
	ChallengeID *string
	// OpenOnly が true なら、終了していない run だけに絞る。
	OpenOnly bool
	// Limit が正なら、新しい方から最大その件数だけ返す（`show` の runs は 20 件。
	// 0 以下は件数を絞らない）。
	Limit int
}

// showRunsLimit は `show` の runs の最大件数（§観測「show は、課題の run の一覧
// （新しい順・最大 20 件）…を示す」）。
const showRunsLimit = 20

// ListRuns は run を新しい順（id 降順）で返す（§観測「flywheel runs」）。
// 呼び出しのたびに ReapInterruptedRuns を先に行う（§invoker の共通の規則
// 「次にストアを開いたflywheelのコマンドがそのrunをinterruptedで終了させる」の
// 具体化）。
func (s *Store) ListRuns(ctx context.Context, opt RunListOptions) ([]Run, error) {
	if err := s.ReapInterruptedRuns(ctx); err != nil {
		return nil, err
	}
	return s.ListRunsWithoutReap(ctx, opt)
}

// ListRunsWithoutReap は ListRuns と同じ絞り込み・並び・形で run を返すが、
// 中断した run を回収しない（ReapInterruptedRuns を呼ばない）ので、run・activity・
// lock のどの行も変えない。閲覧だけの経路（server。M4P12）が使う。
func (s *Store) ListRunsWithoutReap(ctx context.Context, opt RunListOptions) ([]Run, error) {
	var cid *int64
	if opt.ChallengeID != nil {
		id, ok := parseChallengeID(*opt.ChallengeID)
		if !ok {
			return nil, ErrNotFound
		}
		cid = &id
	}

	var result []Run
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		if cid != nil {
			if _, err := loadChallenge(ctx, tx, *cid); err != nil {
				return err
			}
		}

		query := runSelectColumns
		var conds []string
		var args []any
		if cid != nil {
			conds = append(conds, "challenge_id = ?")
			args = append(args, *cid)
		}
		if opt.OpenOnly {
			conds = append(conds, "result IS NULL")
		}
		if len(conds) > 0 {
			query += " WHERE " + strings.Join(conds, " AND ")
		}
		query += " ORDER BY id DESC"
		if opt.Limit > 0 {
			query += fmt.Sprintf(" LIMIT %d", opt.Limit)
		}

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		var rawRows []*runRow
		for rows.Next() {
			r, err := scanRunRow(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			rawRows = append(rawRows, r)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}

		budgets, err := loadCycleBudgetsForRuns(ctx, tx, rawRows)
		if err != nil {
			return err
		}
		for _, r := range rawRows {
			result = append(result, toPublicRun(r, budgets))
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if result == nil {
		result = []Run{}
	}
	return result, nil
}

func toPublicRun(r *runRow, cycleBudgetsMicros map[int64]int64) Run {
	pub := Run{
		ID:           r.ID,
		Kind:         r.Kind,
		Judgment:     r.Judgment,
		ChallengeID:  r.ChallengeID,
		CycleID:      r.CycleID,
		SessionID:    r.SessionID,
		Result:       r.Result,
		RateLimited:  r.RateLimited,
		CostSource:   r.CostSource,
		MaxBudgetUSD: microsToUSD(r.MaxBudgetUSD),
		StartedAt:    r.StartedAt,
		EndedAt:      r.EndedAt,
	}
	if r.CostUSD != nil {
		v := microsToUSD(*r.CostUSD)
		pub.CostUSD = &v
	}
	if r.CycleID != nil {
		if cid, ok := parseCycleID(*r.CycleID); ok {
			if budgetMicros, ok := cycleBudgetsMicros[cid]; ok {
				v := microsToUSD(budgetMicros)
				pub.CycleBudgetUSD = &v
			}
		}
	}
	return pub
}

// loadCycleBudgetsForRuns は rows が参照する（重複を除く）周ごとの
// budget_usd（USDの100万分の1単位）を1回の問い合わせで返す
// （#83・§IF / API「runs」の cycle_budget_usd。決定 2026-09-28 親: `--auto`の
// 個別の操作もcycle表に1行を作るため、runのcycle_idはNULL可のまま――NULLは
// 周の外で記録されたrunを表す）。
func loadCycleBudgetsForRuns(ctx context.Context, tx *sql.Tx, rows []*runRow) (map[int64]int64, error) {
	budgets := map[int64]int64{}
	seen := map[int64]bool{}
	var ids []int64
	for _, r := range rows {
		if r.CycleID == nil {
			continue
		}
		cid, ok := parseCycleID(*r.CycleID)
		if !ok || seen[cid] {
			continue
		}
		seen[cid] = true
		ids = append(ids, cid)
	}
	if len(ids) == 0 {
		return budgets, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := "SELECT id, budget_usd FROM cycle WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	rowsResult, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rowsResult.Close() }()
	for rowsResult.Next() {
		var id, budget int64
		if err := rowsResult.Scan(&id, &budget); err != nil {
			return nil, err
		}
		budgets[id] = budget
	}
	return budgets, rowsResult.Err()
}

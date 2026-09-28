package core

// このファイルは #81（docs/features/m3-invoker-delegation.md §観測
// 「flywheel runs [<C-ID>] [--open]」・§IF / API「runs」）が足す、run の一覧の
// 読み取り専用 API（Store.ListRuns）を持つ。

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Run は ListRuns が返す、run 1 件の表示用の形（§IF / API「runs」）。
// Judgment は委譲（kind=delegate）では ""。Result・CostUSD・CostSource・
// EndedAt は終了していない run では未設定（Result・CostSource は ""、
// CostUSD・EndedAt は nil）。
type Run struct {
	ID           string
	Kind         RunKind
	Judgment     JudgmentPoint
	ChallengeID  string
	CycleID      *string
	SessionID    string
	Result       RunResult
	RateLimited  bool
	CostUSD      *float64
	CostSource   CostSource
	MaxBudgetUSD float64
	StartedAt    time.Time
	EndedAt      *time.Time
}

// RunListOptions は ListRuns の絞り込み条件。
type RunListOptions struct {
	// ChallengeID が非nilなら、その課題（"C-<n>"）の run だけに絞る
	// （形式不正・存在しない課題は ErrNotFound）。
	ChallengeID *string
	// OpenOnly が true なら、終了していない run だけに絞る。
	OpenOnly bool
}

// ListRuns は run を新しい順（id 降順）で返す（§観測「flywheel runs」）。
// 呼び出しのたびに ReapInterruptedRuns を先に行う（§invoker の共通の規則
// 「次にストアを開いたflywheelのコマンドがそのrunをinterruptedで終了させる」の
// 具体化）。
func (s *Store) ListRuns(ctx context.Context, opt RunListOptions) ([]Run, error) {
	if err := s.ReapInterruptedRuns(ctx); err != nil {
		return nil, err
	}

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

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanRunRow(rows)
			if err != nil {
				return err
			}
			result = append(result, toPublicRun(r))
		}
		return rows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if result == nil {
		result = []Run{}
	}
	return result, nil
}

func toPublicRun(r *runRow) Run {
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
	return pub
}

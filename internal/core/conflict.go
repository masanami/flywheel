package core

// このファイルは衝突の予測（docs/features/ui-relation-graph.md §衝突（core））の結果の記録を持つ。
// 同時に委譲しない（順に走らせる）と決めた課題の組を challenge_conflict へ置き、関係グラフの API が
// 衝突の辺（kind=conflict）として返し、ボードの詳細が「衝突のため後回し」の理由として読む。
//
// 前提の依存（challenge_dependency）とは別の表で、依存の評価（dependency.go）・委譲の対象の選び方・
// 起動の判定は衝突の行を読まない（衝突は着手の門にしない。直列化は従来どおり serial_group.go が決める）。

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// conflictRec は記録する組 1 つ。Directed なら Challenge が後回しにした課題で With が先に委譲した課題
// （後回し → 先に委譲）。向きが無いとき（まだどちらも委譲していない）は Challenge < With。
type conflictRec struct {
	Challenge int64
	With      int64
	Directed  bool
}

// normalized は向きの無い組の challenge_id を ID の小さい側にそろえる（向きのある組はそのまま）。
func (r conflictRec) normalized() conflictRec {
	if !r.Directed && r.Challenge > r.With {
		r.Challenge, r.With = r.With, r.Challenge
	}
	return r
}

func int64Args(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// replaceConflictsTx は scope（今回の予測の対象にした課題。候補と実行中の課題）の課題どうしの組を、
// recs で置き換える: 両端が scope にある既存の行をすべて消し、recs を入れる。片方でも scope に無い
// 課題との組は触らない（今回は予測していないので、新しい情報が無い）。cycleID・runID は予測した周と
// 予測の口を呼んだ run（無ければ nil）。
func replaceConflictsTx(ctx context.Context, tx *sql.Tx, scope []int64, recs []conflictRec, cycleID, runID *int64, at string) error {
	if len(scope) > 0 {
		marks, args := placeholders(len(scope)), int64Args(scope)
		q := fmt.Sprintf(`DELETE FROM challenge_conflict WHERE challenge_id IN (%s) AND conflicts_with_id IN (%s)`, marks, marks)
		if _, err := tx.ExecContext(ctx, q, append(append([]any{}, args...), args...)...); err != nil {
			return err
		}
	}
	seen := map[[2]int64]bool{}
	for _, r := range recs {
		r = r.normalized()
		lo, hi := r.Challenge, r.With
		if lo > hi {
			lo, hi = hi, lo
		}
		if r.Challenge == r.With || seen[[2]int64{lo, hi}] {
			continue
		}
		seen[[2]int64{lo, hi}] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO challenge_conflict (challenge_id, conflicts_with_id, directed, cycle_id, run_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			r.Challenge, r.With, boolInt(r.Directed), cycleID, runID, at); err != nil {
			return err
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// orientConflictsTx は課題 delegated の委譲の run を記録するトランザクションの中で、その課題との
// 向きの無い組を「相手（後回し）→ delegated（先に委譲）」の向きにする。すでに向きのある組は変えない。
func orientConflictsTx(ctx context.Context, tx *sql.Tx, delegated int64) error {
	// 起動した課題はもう後回しではないので、後回しの側として持つ向きのある組は外す（待っている理由に残さない）。
	if _, err := tx.ExecContext(ctx, `DELETE FROM challenge_conflict WHERE directed = 1 AND challenge_id = ?`, delegated); err != nil {
		return err
	}
	type row struct {
		a, b       int64
		cycle, run sql.NullInt64
		createdAt  string
		other      int64
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT challenge_id, conflicts_with_id, cycle_id, run_id, created_at FROM challenge_conflict
		 WHERE directed = 0 AND (challenge_id = ? OR conflicts_with_id = ?)`, delegated, delegated)
	if err != nil {
		return err
	}
	var undirected []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.a, &r.b, &r.cycle, &r.run, &r.createdAt); err != nil {
			_ = rows.Close()
			return err
		}
		r.other = r.a
		if r.a == delegated {
			r.other = r.b
		}
		undirected = append(undirected, r)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range undirected {
		if _, err := tx.ExecContext(ctx, `DELETE FROM challenge_conflict WHERE challenge_id = ? AND conflicts_with_id = ?`, r.a, r.b); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO challenge_conflict (challenge_id, conflicts_with_id, directed, cycle_id, run_id, created_at) VALUES (?, ?, 1, ?, ?, ?)`,
			r.other, delegated, r.cycle, r.run, r.createdAt); err != nil {
			return err
		}
	}
	return nil
}

// activeConflict は両端とも終わっていない（完了・取り下げ以外の）課題の衝突の行 1 つ。
type activeConflict struct {
	Challenge int64
	With      int64
	Directed  bool
	CreatedAt string
}

// loadActiveConflictsTx は challenge_conflict の行のうち、両端の課題が完了でも取り下げでもないものを返す
// （片方が終わった衝突は返さない。行は残す）。(challenge_id, conflicts_with_id) の数の昇順。
func loadActiveConflictsTx(ctx context.Context, tx *sql.Tx) ([]activeConflict, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT cc.challenge_id, cc.conflicts_with_id, cc.directed, cc.created_at
		 FROM challenge_conflict cc
		 JOIN challenge a ON a.id = cc.challenge_id
		 JOIN challenge b ON b.id = cc.conflicts_with_id
		 WHERE a.status NOT IN (?, ?) AND b.status NOT IN (?, ?)`,
		string(StatusDone), string(StatusWithdrawn), string(StatusDone), string(StatusWithdrawn))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []activeConflict
	for rows.Next() {
		var c activeConflict
		var directed int
		if err := rows.Scan(&c.Challenge, &c.With, &directed, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Directed = directed == 1
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Challenge != out[j].Challenge {
			return out[i].Challenge < out[j].Challenge
		}
		return out[i].With < out[j].With
	})
	return out, nil
}

// recordConflicts は今回の予測（pred.Output が非 nil）の結果を、その予測の対象の課題どうしの組として
// 置き換える。予測が得られなかったとき（Output が nil。prediction_failed など）は何もしない（既存の行を
// 残し、新しい行も足さない）。
//
//   - 記録する組は、直列化グループを作る組（shared_files・dependency・unknown_pair のいずれかの理由があるもの）。
//     間接的に同じグループに入っただけの組（推移）は足さない（予測が見た組だけを記録する）。
//   - 実行中の課題との組は、候補を後回しにした向きで記録する。候補どうしの組は、まだどちらも委譲して
//     いないので向きを持たない（委譲の起動で orientConflictsTx が向きを付ける）。実行中どうしの組は、
//     後回しにした課題が無いので記録しない。
func (s *Store) recordConflicts(ctx context.Context, cycleID string, predictionRunID int64, running []planRunning, cands []planCandidate, out *ConflictPredictionOutput) error {
	if out == nil {
		return nil
	}
	idOf := map[int]int64{}
	isRunning := map[int]bool{}
	var scope []int64
	for _, r := range running {
		if r.Issue > 0 {
			idOf[r.Issue] = r.Seq
			isRunning[r.Issue] = true
			scope = append(scope, r.Seq)
		}
	}
	for _, c := range cands {
		if c.Issue > 0 {
			idOf[c.Issue] = c.Seq
			scope = append(scope, c.Seq)
		}
	}
	var recs []conflictRec
	for _, e := range predictedEdges(out.Pairs, func(n int) bool { _, ok := idOf[n]; return ok }) {
		ca, cb := idOf[e.a], idOf[e.b]
		switch ra, rb := isRunning[e.a], isRunning[e.b]; {
		case ra && rb:
		case ra:
			recs = append(recs, conflictRec{Challenge: cb, With: ca, Directed: true})
		case rb:
			recs = append(recs, conflictRec{Challenge: ca, With: cb, Directed: true})
		default:
			recs = append(recs, conflictRec{Challenge: ca, With: cb})
		}
	}
	var cyc *int64
	if n, ok := parseCycleID(cycleID); ok {
		cyc = &n
	}
	var run *int64
	if predictionRunID > 0 {
		run = &predictionRunID
	}
	at := formatTimestamp(s.currentTime())
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		return replaceConflictsTx(ctx, tx, scope, recs, cyc, run, at)
	})
	return classifyReadWriteErr(err)
}

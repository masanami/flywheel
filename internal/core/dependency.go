package core

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// このファイルは課題どうしの依存（challenge_dependency。M4.5 S2・M45H12）の登録と評価を持つ。
// 状態機械は変えず、「依存待ち」の状態も足さない。依存は委譲の起動の判定にだけ使う。

// AddDependencies は id の課題に依存先 dependsOn を足す。検査の順は 自己参照
// （ErrSelfDependency）→ 存在（ErrNotFound。依存元・依存先のどちらも）→ 終端の状態
// （ErrTerminalState。依存元が完了）→ 循環（ErrDependencyCycle）で、最初に当たったもので
// 何も書かずに終わる（fail-closed）。既にある依存は何もしない。足す依存が 1 つも無ければ
// 版も作業ログも変えない。足すものがあれば、1 トランザクションで版を 1 だけ上げ、作業ログに
// entity=challenge・action=dep_add を残す。
func (s *Store) AddDependencies(ctx context.Context, ch Channel, id string, dependsOn []string) (*Challenge, error) {
	return s.changeDependencies(ctx, ch, id, dependsOn, true)
}

// RemoveDependencies は id の課題から依存先 dependsOn を外す。検査の順は AddDependencies と同じ
// （循環の検査は無い）。無い依存は何もしない。
func (s *Store) RemoveDependencies(ctx context.Context, ch Channel, id string, dependsOn []string) (*Challenge, error) {
	return s.changeDependencies(ctx, ch, id, dependsOn, false)
}

// parseDependencyTargets は依存元 id と依存先を解釈し、検査の順の 1 番目（自己参照）を行う。
// 形式不正の ID は存在しない ID と同じ（ErrNotFound）だが、自己参照の判定が先。依存先は重複を除き昇順にする。
func parseDependencyTargets(id string, dependsOn []string) (cid int64, targets []int64, err error) {
	if len(dependsOn) == 0 {
		return 0, nil, ErrValidation
	}
	cid, srcOK := parseChallengeID(id)
	seen := map[int64]bool{}
	malformed := !srcOK
	for _, raw := range dependsOn {
		t, ok := parseChallengeID(raw)
		if !ok {
			malformed = true
			continue
		}
		if srcOK && t == cid {
			return 0, nil, ErrSelfDependency
		}
		if !seen[t] {
			seen[t] = true
			targets = append(targets, t)
		}
	}
	if malformed {
		return 0, nil, ErrNotFound
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })
	return cid, targets, nil
}

func (s *Store) changeDependencies(ctx context.Context, ch Channel, id string, dependsOn []string, add bool) (*Challenge, error) {
	cid, targets, err := parseDependencyTargets(id, dependsOn)
	if err != nil {
		return nil, err
	}
	action := ActivityActionDepRm
	if add {
		action = ActivityActionDepAdd
	}

	var result *Challenge
	err = s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		for _, t := range targets {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM challenge WHERE id = ?`, t).Scan(&one); err != nil {
				if err == sql.ErrNoRows {
					return ErrNotFound
				}
				return err
			}
		}
		if IsTerminal(Table, StatusVocabulary, current.Status) {
			return ErrTerminalState
		}

		existing, err := loadDependencyEdges(ctx, tx)
		if err != nil {
			return err
		}
		var changed []int64
		for _, t := range targets {
			has := existing[cid][t]
			if add != has {
				changed = append(changed, t)
			}
		}
		if add {
			// 新しい辺 cid→t は、t から cid へ既存の辺で届くと輪になる。新しい辺はどれも cid から出るので、
			// 輪が新しい辺を 2 つ通ることは無く、既存の辺だけで足りる。
			for _, t := range changed {
				if reachable(existing, t, cid) {
					return ErrDependencyCycle
				}
			}
		}
		if len(changed) == 0 {
			result = current
			return nil
		}

		nowStr := formatTimestamp(rec.at)
		now, err := parseTimestamp(nowStr)
		if err != nil {
			return err
		}
		for _, t := range changed {
			if add {
				_, err = tx.ExecContext(ctx, `INSERT INTO challenge_dependency (challenge_id, depends_on_id, created_at) VALUES (?, ?, ?)`, cid, t, nowStr)
			} else {
				_, err = tx.ExecContext(ctx, `DELETE FROM challenge_dependency WHERE challenge_id = ? AND depends_on_id = ?`, cid, t)
			}
			if err != nil {
				return err
			}
		}
		newVersion := current.Version + 1
		res, err := tx.ExecContext(ctx,
			`UPDATE challenge SET version = ?, updated_at = ? WHERE id = ? AND version = ?`,
			newVersion, nowStr, cid, current.Version)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("core: change dependencies of %s: expected to update 1 row, updated %d", id, affected)
		}
		ids := make([]string, 0, len(changed))
		for _, t := range changed {
			ids = append(ids, formatChallengeID(t))
		}
		if err := rec.record("challenge", cid, action, nil, map[string]any{"depends_on": ids, "version": newVersion}); err != nil {
			return err
		}
		updated := *current
		updated.Version = newVersion
		updated.UpdatedAt = now
		result = &updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// loadDependencyEdges はストアのすべての依存の辺（依存元→依存先）を、課題の状態によらず読む。
func loadDependencyEdges(ctx context.Context, tx *sql.Tx) (map[int64]map[int64]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT challenge_id, depends_on_id FROM challenge_dependency`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	edges := map[int64]map[int64]bool{}
	for rows.Next() {
		var from, to int64
		if err := rows.Scan(&from, &to); err != nil {
			return nil, err
		}
		if edges[from] == nil {
			edges[from] = map[int64]bool{}
		}
		edges[from][to] = true
	}
	return edges, rows.Err()
}

// reachable は辺 edges をたどって from から to へ（from == to なら 0 歩で）届くか。
func reachable(edges map[int64]map[int64]bool, from, to int64) bool {
	seen := map[int64]bool{}
	stack := []int64{from}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == to {
			return true
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		for next := range edges[n] {
			stack = append(stack, next)
		}
	}
	return false
}

// dependencyStatus は 1 つの依存先と、それが満たされているか。
type dependencyStatus struct {
	DependsOn int64
	Met       bool
}

// dependencyEvaluation は依存の評価の結果。Dependencies は依存元の課題ごとの依存先
// （依存先の内部 ID の昇順）、Cyclic は循環に含まれる課題の集合。
type dependencyEvaluation struct {
	Dependencies map[int64][]dependencyStatus
	Cyclic       map[int64]bool
}

// evaluateDependencies は依存の評価を返す core の内部関数（委譲の起動の判定・show・status が使う）。
// 依存が満たされるとは、依存先が完了で、かつその課題の release に pending・approved のものが無いこと。
// 循環に含まれる依存先は、完了で release が済んでいても満たされていないと数える。
func evaluateDependencies(ctx context.Context, tx *sql.Tx) (*dependencyEvaluation, error) {
	edges, err := loadDependencyEdges(ctx, tx)
	if err != nil {
		return nil, err
	}
	eval := &dependencyEvaluation{Dependencies: map[int64][]dependencyStatus{}, Cyclic: map[int64]bool{}}
	for from := range edges {
		for to := range edges[from] {
			if reachable(edges, to, from) {
				eval.Cyclic[from] = true
				break
			}
		}
	}

	done := map[int64]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT id, status FROM challenge`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			_ = rows.Close()
			return nil, err
		}
		done[id] = Status(status) == StatusDone
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	openRelease := map[int64]bool{}
	rows, err = tx.QueryContext(ctx, `SELECT DISTINCT challenge_id FROM operation WHERE kind = 'release' AND state IN ('pending', 'approved')`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		openRelease[id] = true
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for from, tos := range edges {
		list := make([]dependencyStatus, 0, len(tos))
		for to := range tos {
			list = append(list, dependencyStatus{DependsOn: to, Met: done[to] && !openRelease[to] && !eval.Cyclic[to]})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].DependsOn < list[j].DependsOn })
		eval.Dependencies[from] = list
	}
	return eval, nil
}

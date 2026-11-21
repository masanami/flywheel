package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// 通した衝突の解消の宛先（ConflictFixPassed.Target）。
const (
	ConflictFixTargetIntegration = "integration"
	ConflictFixTargetRelease     = "release"
)

// ConflictFixPassed は、衝突の解消の確かめが「満たした」として通した記録 1 件（作業ログの action = conflict_fix_passed から読む）。
// 統合先宛てなら TaskID を持ち OperationID は nil、反映先宛てなら OperationID を持ち TaskID は nil。
type ConflictFixPassed struct {
	// At は作業ログの記録の時刻。
	At             time.Time
	Target         string
	TaskID         *string
	OperationID    *string
	FromRef, ToRef string
	Files          []string
	RunID          string
}

// loadConflictFixes は課題の通した衝突の解消を、古い順（作業ログの id 昇順）で返す。無ければ空のスライス（nil でない）。
// 課題のタスク（entity=task）と、課題の反映の操作（entity=operation）の記録だけを読む。
func loadConflictFixes(ctx context.Context, tx *sql.Tx, challengeID int64) ([]ConflictFixPassed, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.at, a.entity, a.entity_id, a.before, a.after FROM activity a
		WHERE a.action = ? AND (
			(a.entity = 'task' AND a.entity_id IN (SELECT id FROM task WHERE challenge_id = ?))
			OR (a.entity = 'operation' AND a.entity_id IN (SELECT id FROM operation WHERE challenge_id = ?)))
		ORDER BY a.id ASC`, ActivityActionConflictFixPassed, challengeID, challengeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []ConflictFixPassed{}
	for rows.Next() {
		var (
			atStr, entity string
			entityID      int64
			before, after sql.NullString
		)
		if err := rows.Scan(&atStr, &entity, &entityID, &before, &after); err != nil {
			return nil, err
		}
		at, err := parseTimestamp(atStr)
		if err != nil {
			return nil, err
		}
		var b struct {
			Ref string `json:"ref"`
		}
		var a struct {
			Ref   string          `json:"ref"`
			Files []string        `json:"files"`
			RunID json.RawMessage `json:"run_id"`
		}
		if before.Valid {
			if err := json.Unmarshal([]byte(before.String), &b); err != nil {
				return nil, fmt.Errorf("conflict_fix_passed before: %w", err)
			}
		}
		if after.Valid {
			if err := json.Unmarshal([]byte(after.String), &a); err != nil {
				return nil, fmt.Errorf("conflict_fix_passed after: %w", err)
			}
		}
		if a.Files == nil {
			a.Files = []string{}
		}
		f := ConflictFixPassed{At: at, FromRef: b.Ref, ToRef: a.Ref, Files: a.Files, RunID: decodeFixRunID(a.RunID)}
		id := formatEntityID(entity, entityID)
		if entity == "task" {
			f.Target = ConflictFixTargetIntegration
			f.TaskID = &id
		} else {
			f.Target = ConflictFixTargetRelease
			f.OperationID = &id
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// fillRunFixPending は runs のうち、確かめ待ちの中身を持つ契約の実行を始めた run（contract_run.start_run_id）の FixPendingRef を埋める。
// 確かめ待ちの中身が無い run は nil のまま。
func fillRunFixPending(ctx context.Context, tx *sql.Tx, challengeID int64, runs []Run) error {
	rows, err := tx.QueryContext(ctx, `SELECT start_run_id, fix_pending_ref FROM contract_run
		WHERE challenge_id = ? AND fix_pending_ref IS NOT NULL AND fix_pending_ref <> ''`, challengeID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	pending := map[string]string{}
	for rows.Next() {
		var startRun int64
		var ref string
		if err := rows.Scan(&startRun, &ref); err != nil {
			return err
		}
		pending[formatRunID(startRun)] = ref
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range runs {
		if ref, ok := pending[runs[i].ID]; ok {
			v := ref
			runs[i].FixPendingRef = &v
		}
	}
	return nil
}

// decodeFixRunID は作業ログの after.run_id（確かめの実行の ID）を "R-<n>" の形で返す。文字列ならそのまま、数（内部の整数 ID）なら
// "R-<n>" にする。読めない・無いときは空文字（1 件の記録の形の揺れで課題の詳細全体を読めなくしない）。
func decodeFixRunID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return formatRunID(n)
	}
	return ""
}

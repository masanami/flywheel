package core

// このファイルは、閲覧だけの経路（internal/server。docs/features/m4-ui-server.md
// §アーキテクチャ決定）のために core が公開する読み取り専用の API を持つ。
// どれもストアへ書き込まない（回収もしない）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

// ResolveWorkspaceDir は M1 の探索規則（--workspace → FLYWHEEL_WORKSPACE →
// カレントディレクトリから上位へ）でワークスペースのディレクトリだけを解決する。
// ストアを開かず、ストアが無くてもパスを返す。明示された基点はそのまま返す。
// 明示が無く、上位にもストアが見つからなければカレントディレクトリを返す。
func ResolveWorkspaceDir(workspaceFlag string) (string, error) {
	base, explicit, err := resolveBaseDirExplicit(workspaceFlag)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrStoreError, err)
	}
	if explicit {
		return base, nil
	}
	if dir, ok := findWorkspace(base); ok {
		return dir, nil
	}
	return base, nil
}

// SchemaVersion は開いているストアの現在のスキーマ版（PRAGMA user_version）を
// 読み直して返す。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	})
	if err = classifyReadWriteErr(err); err != nil {
		return 0, err
	}
	return v, nil
}

// LatestSchemaVersion はこのバイナリが知るスキーマ版の最大値を返す。SchemaVersion が
// これより大きければ、開いた後にストアが新しい版へ上げられている。
func LatestSchemaVersion() (int, error) {
	migrations, err := store.Migrations()
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrStoreError, err)
	}
	latest := 0
	for _, m := range migrations {
		if m.Version > latest {
			latest = m.Version
		}
	}
	return latest, nil
}

// UnansweredHold は未回答の保留 1 件（課題の情報つき）。RunID は "R-<n>" か nil。
type UnansweredHold struct {
	ChallengeID string
	Title       string
	Question    string
	RaisedAt    time.Time
	FromStatus  Status
	RunID       *string
}

// ListUnansweredHolds は未回答の保留を raised_at の古い順（同時刻は課題の ID の
// 昇順）で返す。回答済みの保留は含めない。
func (s *Store) ListUnansweredHolds(ctx context.Context) ([]UnansweredHold, error) {
	out := []UnansweredHold{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT h.challenge_id, c.title, h.question, h.raised_at, h.from_status, h.run_id
			 FROM hold h JOIN challenge c ON c.id = h.challenge_id
			 WHERE h.answer IS NULL
			 ORDER BY h.raised_at, h.challenge_id, h.id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				cid        int64
				raisedAt   string
				fromStatus string
				runID      sql.NullInt64
				h          UnansweredHold
			)
			if err := rows.Scan(&cid, &h.Title, &h.Question, &raisedAt, &fromStatus, &runID); err != nil {
				return err
			}
			t, err := parseTimestamp(raisedAt)
			if err != nil {
				return err
			}
			h.ChallengeID = formatChallengeID(cid)
			h.RaisedAt = t
			h.FromStatus = Status(fromStatus)
			if runID.Valid {
				v := formatRunID(runID.Int64)
				h.RunID = &v
			}
			out = append(out, h)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

// RunReport は委譲の run の報告のうち、人間が読む 2 つ。無ければ空の配列。
type RunReport struct {
	RunID       string
	Assumptions []string
	Unverified  []string
}

// ReadRunReport は run の報告（.flywheel/runs/<ID>/stdout.json の
// structured_output の assumptions と unverified）を読む。run の ID の形
// （R-<正の整数>）が違えばファイルを読まずに ErrValidation、ストアに run が
// 無ければ ErrNotFound。ファイル・キーが無い、または読み取れない形なら空の配列。
func (s *Store) ReadRunReport(ctx context.Context, runID string) (*RunReport, error) {
	n, ok := parseRunID(runID)
	if !ok {
		return nil, ErrValidation
	}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		r, err := loadRunByID(ctx, tx, n)
		if err != nil {
			return err
		}
		if r == nil {
			return ErrNotFound
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}

	report := &RunReport{RunID: runID, Assumptions: []string{}, Unverified: []string{}}
	data, err := os.ReadFile(filepath.Join(runDirPath(s.workspace, runID), "stdout.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return report, nil
		}
		return nil, fmt.Errorf("%w: read run report: %w", ErrStoreError, err)
	}
	var doc struct {
		StructuredOutput map[string]json.RawMessage `json:"structured_output"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return report, nil
	}
	if v, ok := decodeStrings(doc.StructuredOutput["assumptions"]); ok {
		report.Assumptions = v
	}
	if v, ok := decodeStrings(doc.StructuredOutput["unverified"]); ok {
		report.Unverified = v
	}
	return report, nil
}

func decodeStrings(raw json.RawMessage) ([]string, bool) {
	var v []string
	if raw == nil || json.Unmarshal(raw, &v) != nil || v == nil {
		return nil, false
	}
	return v, true
}

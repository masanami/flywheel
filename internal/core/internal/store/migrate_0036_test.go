package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 区切り S4b-3 のスキーマ（版 36）。対処の記録の表に、子の問いと答え（questions。JSON。無ければ NULL）を 1 列だけ足す。

const migrationChildQuestionVersion = 36

func TestChildQuestionMigration(t *testing.T) {
	t.Run("fresh_store_has_a_nullable_questions_column", func(t *testing.T) {
		db := openFresh(t)
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationChildQuestionVersion {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationChildQuestionVersion)
		}
		action := tableColumnInfo(t, db, "supervisor_action")
		if nn, ok := action["questions"]; !ok || nn {
			t.Errorf("supervisor_action.questions: present=%v notnull=%v; want present and nullable", ok, nn)
		}
		if len(action) != 16 {
			t.Errorf("supervisor_action has %d columns, want 16 (the 15 of S4b-2 and questions)", len(action))
		}
	})
	t.Run("upgrade_from_35_keeps_rows_with_NULL_questions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flywheel.db")
		old, err := Open(path, migrationsUpTo(t, migrationChildQuestionVersion-1))
		if err != nil {
			t.Fatal(err)
		}
		insertChallenge(t, old, "a")
		if err := old.Write(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO cycle (trigger, started_at, budget_usd) VALUES ('manual', '2026-01-01T00:00:00Z', 1)`); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO supervisor_action (cycle_id, challenge_id, stop_kind, findings, note, basis, result, created_at)
				VALUES (1, 1, 'facts_unmet', 'f', '', '[]', 'failed', '2026-01-01T00:00:00Z')`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := old.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := Open(path, prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			var q sql.NullString
			if err := tx.QueryRow(`SELECT questions FROM supervisor_action`).Scan(&q); err != nil {
				return err
			}
			if q.Valid {
				t.Errorf("old supervisor_action row: questions = %v, want NULL", q)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

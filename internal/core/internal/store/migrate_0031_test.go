package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 区切り S4d-1 のスキーマ（版 31）。対話の欄の会話（chat_session）と発言（chat_message）の 2 表を足す。

const migrationChatVersion = 31

func TestChatMigration(t *testing.T) {
	write := func(db *DB, f func(tx *sql.Tx) error) error { return db.Write(context.Background(), f) }
	insertSession := func(tx *sql.Tx, closed any) error {
		_, err := tx.Exec(`INSERT INTO chat_session (engine_session, started_at, closed_at) VALUES ('11111111-1111-4111-8111-111111111111', '2026-11-22T00:00:00.000Z', ?)`, closed)
		return err
	}

	t.Run("fresh_store_has_both_tables_at_the_latest_version", func(t *testing.T) {
		db := openFresh(t)
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationChatVersion {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationChatVersion)
		}
		for table, cols := range map[string][]string{
			"chat_session": {"id", "engine_session", "engine_started", "started_at", "closed_at"},
			"chat_message": {"id", "session_id", "role", "body", "state", "cost_usd", "viewing_challenge_id", "viewing_task_id", "viewing_title", "activity", "created_at", "ended_at"},
		} {
			info := tableColumnInfo(t, db, table)
			for _, c := range cols {
				if _, ok := info[c]; !ok {
					t.Errorf("%s.%s is missing", table, c)
				}
			}
			if len(info) != len(cols) {
				t.Errorf("%s has %d columns, want %d", table, len(info), len(cols))
			}
		}
	})
	t.Run("defaults_are_engine_started_0_cost_0_activity_empty_array", func(t *testing.T) {
		db := openFresh(t)
		if err := write(db, func(tx *sql.Tx) error {
			if err := insertSession(tx, nil); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO chat_message (session_id, role, body, state, created_at) VALUES (1, 'owner', 'こんにちは', 'done', '2026-11-22T00:00:00.000Z')`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			var started, cost int
			var activity string
			var ended, vc sql.NullString
			if err := tx.QueryRow(`SELECT s.engine_started, m.cost_usd, m.activity, m.ended_at, m.viewing_challenge_id FROM chat_session s, chat_message m`).Scan(&started, &cost, &activity, &ended, &vc); err != nil {
				return err
			}
			if started != 0 || cost != 0 || activity != "[]" || ended.Valid || vc.Valid {
				t.Errorf("defaults = started %d cost %d activity %q ended %v viewing %v", started, cost, activity, ended, vc)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a_second_open_session_is_refused_but_a_closed_one_is_not", func(t *testing.T) {
		db := openFresh(t)
		if err := write(db, func(tx *sql.Tx) error { return insertSession(tx, nil) }); err != nil {
			t.Fatal(err)
		}
		if err := write(db, func(tx *sql.Tx) error { return insertSession(tx, nil) }); err == nil {
			t.Fatal("a second open chat_session was accepted; want a unique-index violation")
		}
		if err := write(db, func(tx *sql.Tx) error { return insertSession(tx, "2026-11-22T01:00:00.000Z") }); err != nil {
			t.Errorf("inserting a closed session next to an open one: %v", err)
		}
		if err := write(db, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE chat_session SET closed_at = '2026-11-22T02:00:00.000Z' WHERE closed_at IS NULL`)
			if err != nil {
				return err
			}
			return insertSession(tx, nil)
		}); err != nil {
			t.Errorf("opening a new session after closing the old one: %v", err)
		}
	})
	t.Run("upgrade_from_30_keeps_rows_and_adds_both_empty_tables", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flywheel.db")
		old, err := Open(path, migrationsUpTo(t, 30))
		if err != nil {
			t.Fatal(err)
		}
		insertChallenge(t, old, "a")
		if err := old.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := Open(path, prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationChatVersion {
			t.Errorf("user_version after upgrading from 30 = %d, want the latest %d", got, latestProdVersion(t))
		}
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			var ch, s, m int
			if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM challenge), (SELECT COUNT(*) FROM chat_session), (SELECT COUNT(*) FROM chat_message)`).Scan(&ch, &s, &m); err != nil {
				return err
			}
			if ch != 1 || s != 0 || m != 0 {
				t.Errorf("rows after upgrade: challenge %d chat_session %d chat_message %d; want 1, 0, 0", ch, s, m)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

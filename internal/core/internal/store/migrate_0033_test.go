package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 区切り S4b-1 の 1 のスキーマ（版 33）。hold に options・answer_option・option_required を足す。

const migrationHoldOptionsVersion = 33

func TestHoldOptionsMigration(t *testing.T) {
	t.Run("fresh_store_has_the_columns", func(t *testing.T) {
		db := openFresh(t)
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationHoldOptionsVersion {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationHoldOptionsVersion)
		}
		info := tableColumnInfo(t, db, "hold")
		for _, col := range []string{"options", "answer_option"} {
			if nn, ok := info[col]; !ok || nn {
				t.Errorf("hold.%s: present=%v notnull=%v; want present and nullable", col, ok, nn)
			}
		}
		if nn, ok := info["option_required"]; !ok || !nn {
			t.Errorf("hold.option_required: present=%v notnull=%v; want present and NOT NULL", ok, nn)
		}
	})
	t.Run("upgrade_from_32_keeps_rows_with_NULL_options_and_required_0", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flywheel.db")
		old, err := Open(path, migrationsUpTo(t, 32))
		if err != nil {
			t.Fatal(err)
		}
		insertChallenge(t, old, "a")
		if err := old.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (1, 'q', 'classified', '2026-01-01T00:00:00Z')`)
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
			var q string
			var opts, ans sql.NullString
			var req int
			if err := tx.QueryRow(`SELECT question, options, answer_option, option_required FROM hold`).Scan(&q, &opts, &ans, &req); err != nil {
				return err
			}
			if q != "q" || opts.Valid || ans.Valid || req != 0 {
				t.Errorf("old hold row: q=%q options=%v answer_option=%v option_required=%d; want q, NULL, NULL, 0", q, opts, ans, req)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

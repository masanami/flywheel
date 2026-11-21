package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 区切り S4a のスキーマ（版 31）。cycle に end_reason・rounds・stop_requested_at を足す（AC-182〜AC-187・AC-73）。

const migrationS4aVersion = 31

func TestS4aMigration(t *testing.T) {
	t.Run("fresh_store_is_at_version_31_or_later_and_the_migration_is_the_only_one_at_31", func(t *testing.T) {
		db := openFresh(t)
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationS4aVersion {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationS4aVersion)
		}
		n := 0
		for _, m := range prodMigrations(t) {
			if m.Version == migrationS4aVersion {
				n++
			}
		}
		if n != 1 {
			t.Errorf("migrations with version 31 = %d, want 1", n)
		}
	})
	t.Run("added_columns_have_the_declared_nullability_and_default", func(t *testing.T) {
		db := openFresh(t)
		info := tableColumnInfo(t, db, "cycle")
		if nn, ok := info["end_reason"]; !ok || nn {
			t.Errorf("cycle.end_reason: present=%v notnull=%v; want present and nullable", ok, nn)
		}
		if nn, ok := info["stop_requested_at"]; !ok || nn {
			t.Errorf("cycle.stop_requested_at: present=%v notnull=%v; want present and nullable", ok, nn)
		}
		if _, ok := info["rounds"]; !ok {
			t.Errorf("cycle.rounds is missing")
		}
		if got := columnDefault(t, db, "cycle", "rounds"); !got.Valid || got.String != "0" {
			t.Errorf("cycle.rounds default = %v, want 0", got)
		}
	})
	t.Run("upgrade_from_30_keeps_the_rows_and_leaves_end_reason_NULL", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flywheel.db")
		old, err := Open(path, migrationsUpTo(t, 30))
		if err != nil {
			t.Fatal(err)
		}
		if err := old.Write(context.Background(), func(tx *sql.Tx) error {
			for i := 0; i < 2; i++ {
				if _, err := tx.Exec(`INSERT INTO cycle (trigger, started_at, budget_usd) VALUES ('manual', '2026-11-01T00:00:00.000Z', 1)`); err != nil {
					return err
				}
			}
			return nil
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
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationS4aVersion {
			t.Errorf("user_version after upgrading from 30 = %d, want the latest %d", got, latestProdVersion(t))
		}
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			var n, nullReason, zeroRounds, nullStop int
			if err := tx.QueryRow(`SELECT COUNT(*), SUM(end_reason IS NULL), SUM(rounds = 0), SUM(stop_requested_at IS NULL) FROM cycle`).Scan(&n, &nullReason, &zeroRounds, &nullStop); err != nil {
				return err
			}
			if n != 2 || nullReason != 2 || zeroRounds != 2 || nullStop != 2 {
				t.Errorf("cycle rows=%d end_reason NULL=%d rounds=0:%d stop NULL=%d; want 2 each", n, nullReason, zeroRounds, nullStop)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 区切り S3c のスキーマ（版 30）。contract_run に衝突の解消の確かめの 3 列を足す。

const migrationS3cVersion = 30

func TestS3cMigration(t *testing.T) {
	write := func(db *DB, f func(tx *sql.Tx) error) error { return db.Write(context.Background(), f) }
	t.Run("fresh_store_is_at_the_latest_version_30_or_later", func(t *testing.T) {
		db := openFresh(t)
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationS3cVersion {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationS3cVersion)
		}
	})
	t.Run("added_columns_are_nullable_text_without_default", func(t *testing.T) {
		db := openFresh(t)
		for _, col := range []string{"fix_pending_ref", "fix_verified_ref", "fix_rejected_ref"} {
			if nn, ok := tableColumnInfo(t, db, "contract_run")[col]; !ok || nn {
				t.Errorf("contract_run.%s: present=%v notnull=%v; want present and nullable", col, ok, nn)
			}
			if got := columnDefault(t, db, "contract_run", col); got.Valid {
				t.Errorf("contract_run.%s default = %v, want none", col, got)
			}
		}
	})
	t.Run("upgrade_from_29_keeps_rows_and_leaves_the_three_columns_NULL", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flywheel.db")
		old, err := Open(path, migrationsUpTo(t, 29))
		if err != nil {
			t.Fatal(err)
		}
		insertChallenge(t, old, "a")
		if err := write(old, func(tx *sql.Tx) error { return insertContractRunRow(tx, "e1", nil) }); err != nil {
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
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationS3cVersion {
			t.Errorf("user_version after upgrading from 29 = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationS3cVersion)
		}
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			var ext string
			var nulls int
			if err := tx.QueryRow(`SELECT external_run_id, (fix_pending_ref IS NULL) + (fix_verified_ref IS NULL) + (fix_rejected_ref IS NULL) FROM contract_run`).Scan(&ext, &nulls); err != nil {
				return err
			}
			if ext != "e1" || nulls != 3 {
				t.Errorf("old contract_run row: external_run_id=%q NULL columns=%d; want e1, 3", ext, nulls)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

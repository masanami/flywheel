package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 10 のストアを開くと 0011 が当たり、既存の行の値は challenge.origin を除いて変わらず、
// 前からある課題の origin は source_binding の有無で ingest／manual になり、
// 前からある run の flywheel_version は NULL、空の occurrence ができて版は 11 になる。
func TestOpen_AppliesLearningMigrationToAVersion10Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")

	var upTo10 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 10 {
			upTo10 = append(upTo10, m)
		}
	}
	old, err := Open(path, upTo10)
	if err != nil {
		t.Fatalf("Open(v10) error = %v", err)
	}
	if got := userVersion(t, old); got != 10 {
		t.Fatalf("user_version = %d, want 10", got)
	}
	insertChallenge(t, old, "manual-one")
	insertChallenge(t, old, "ingested-one")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO source_binding (challenge_id, source_id, external_key, url, fingerprint, upstream_state, policy_state, comments_count, upstream_updated_at, read_comments_count, read_upstream_updated_at, created_at, updated_at)
			VALUES (2, 's', 'o/r#1', 'https://github.com/o/r/issues/1', 'f', 'open', 'in_policy', 0, '2026-10-24T00:00:00.000Z', 0, '2026-10-24T00:00:00.000Z', '2026-10-24T00:00:00.000Z', '2026-10-24T00:00:00.000Z')`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO run (kind, judgment, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket)
			VALUES ('judgment', 'j1', 1, 1, 'sess', 1, 'h', '2026-10-24T00:00:00.000Z', '2026-10-24T00:00:00.000Z', 0, 'b')`)
		return err
	}); err != nil {
		t.Fatalf("seed v10 rows: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open(latest) error = %v", err)
	}
	defer func() { _ = db.Close() }()
	if got := userVersion(t, db); got != 11 {
		t.Fatalf("user_version = %d, want 11", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, title, status, version, reporter, origin, followup_of FROM challenge ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		want := []struct{ title, origin string }{{"manual-one", "manual"}, {"ingested-one", "ingest"}}
		i := 0
		for rows.Next() {
			var id int
			var title, status, reporter, origin string
			var version int
			var followupOf sql.NullString
			if err := rows.Scan(&id, &title, &status, &version, &reporter, &origin, &followupOf); err != nil {
				return err
			}
			if i >= len(want) {
				t.Fatalf("unexpected extra challenge %d", id)
			}
			if title != want[i].title || status != "unclassified" || version != 1 || reporter != "alice" {
				t.Errorf("challenge %d changed: title=%q status=%q version=%d reporter=%q", id, title, status, version, reporter)
			}
			if origin != want[i].origin {
				t.Errorf("challenge %d origin = %q, want %q", id, origin, want[i].origin)
			}
			if followupOf.Valid {
				t.Errorf("challenge %d followup_of = %q, want NULL", id, followupOf.String)
			}
			i++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if i != len(want) {
			t.Errorf("challenge rows = %d, want %d", i, len(want))
		}
		var fv sql.NullString
		var sess string
		if err := tx.QueryRow(`SELECT session_id, flywheel_version FROM run`).Scan(&sess, &fv); err != nil {
			return err
		}
		if sess != "sess" || fv.Valid {
			t.Errorf("run changed: session_id=%q flywheel_version=%v, want sess/NULL", sess, fv)
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM occurrence`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("occurrence rows = %d, want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
}

// occurrence の参照は承認・保留・run・実行の試行・作業ログのちょうど 1 つ。
func TestOccurrence_RequiresExactlyOneReference(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "flywheel.db"), prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	insertChallenge(t, db, "t")
	ins := func(tx *sql.Tx, refCols, refVals string) error {
		_, err := tx.Exec(`INSERT INTO occurrence (challenge_id, kind, ` + refCols + `outcome, source, created_at)
			VALUES (1, 'plan_rejected', ` + refVals + `'bad', 'core', '2026-10-24T00:00:00.000Z')`)
		return err
	}
	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (1, 'q', 'planning', '2026-10-24T00:00:00.000Z')`); err != nil {
			t.Fatal(err)
		}
		if err := ins(tx, "", ""); err == nil {
			t.Error("no reference accepted")
		}
		if _, err := tx.Exec(`INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action) VALUES ('2026-10-24T00:00:00.000Z', 'a', 'cli', 'none', 'challenge', 1, 'x')`); err != nil {
			t.Fatal(err)
		}
		if err := ins(tx, "hold_id, activity_id, ", "1, 1, "); err == nil {
			t.Error("two references accepted")
		}
		if err := ins(tx, "hold_id, ", "1, "); err != nil {
			t.Errorf("one reference rejected: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

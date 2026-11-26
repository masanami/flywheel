package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// このファイルのテスト名の AC-<n> は、core の見直しの区切り S2a の課題（masanami/flywheel#750。
// docs/features/core-redesign.md の受入基準の並び順）の受入基準の番号。同じファイルの別の仕様の番号とは無関係。

const migrationTaskVersion = 26

func openFresh(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "flywheel.db"), prodMigrations(t))
	if err != nil {
		t.Fatalf("Open(fresh): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tableColumnInfo(t *testing.T, db *DB, table string) map[string]bool {
	t.Helper()
	cols := map[string]bool{} // 列名 → NOT NULL か
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT name, "notnull" FROM pragma_table_info(?)`, table)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var name string
			var nn int
			if err := rows.Scan(&name, &nn); err != nil {
				return err
			}
			cols[name] = nn == 1
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	return cols
}

// openV25WithRows はマイグレーション 0025 までを当てたストア（版 25）に、課題 2 件・run 1 件を入れて閉じ、パスを返す。
func openV25WithRows(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flywheel.db")
	old, err := Open(path, migrationsUpTo(t, 25))
	if err != nil {
		t.Fatalf("Open(v25): %v", err)
	}
	if got := userVersion(t, old); got != 25 {
		t.Fatalf("user_version = %d, want 25", got)
	}
	insertChallenge(t, old, "a")
	insertChallenge(t, old, "b")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO run (kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket)
			VALUES ('delegate', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', 1, 'impl')`)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTaskMigration(t *testing.T) {
	t.Run("AC-1_fresh_store_is_at_version_26_or_later_and_at_the_latest", func(t *testing.T) {
		db := openFresh(t)
		got := userVersion(t, db)
		if got != latestProdVersion(t) {
			t.Errorf("user_version = %d, want the latest %d", got, latestProdVersion(t))
		}
		// 版 26 のマイグレーションが含まれる（次の区切りで版が上がっても、この検査は 26 以上で成り立つ）。
		if got < migrationTaskVersion {
			t.Errorf("user_version = %d, want >= %d", got, migrationTaskVersion)
		}
		found := false
		for _, m := range prodMigrations(t) {
			if m.Version == migrationTaskVersion {
				found = true
			}
		}
		if !found {
			t.Errorf("no migration of version %d", migrationTaskVersion)
		}
	})
	t.Run("AC-2_version_25_store_becomes_version_26_or_later", func(t *testing.T) {
		db, err := Open(openV25WithRows(t), prodMigrations(t))
		if err != nil {
			t.Fatalf("Open(latest): %v", err)
		}
		defer func() { _ = db.Close() }()
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationTaskVersion {
			t.Errorf("user_version = %d, want latest %d (>= %d)", got, latestProdVersion(t), migrationTaskVersion)
		}
		if !tableExists(t, db, "task") {
			t.Error("task table not created by the upgrade")
		}
	})
	t.Run("AC-3_AC-4_upgrade_keeps_challenge_and_run_row_counts", func(t *testing.T) {
		path := openV25WithRows(t)
		old, err := Open(path, migrationsUpTo(t, 25))
		if err != nil {
			t.Fatal(err)
		}
		challengesBefore, runsBefore := countRows(t, old, "challenge"), countRows(t, old, "run")
		snapshotBefore := challengeRowsSnapshot(t, old)
		if err := old.Close(); err != nil {
			t.Fatal(err)
		}
		if challengesBefore != 2 || runsBefore != 1 {
			t.Fatalf("seeded rows = challenge %d, run %d; want 2, 1", challengesBefore, runsBefore)
		}
		db, err := Open(path, prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if got := countRows(t, db, "challenge"); got != challengesBefore {
			t.Errorf("challenge rows = %d, want %d (AC-3)", got, challengesBefore)
		}
		if got := countRows(t, db, "run"); got != runsBefore {
			t.Errorf("run rows = %d, want %d (AC-4)", got, runsBefore)
		}
		after := challengeRowsSnapshot(t, db)
		for i := range snapshotBefore {
			if after[i] != snapshotBefore[i] {
				t.Errorf("challenge row %d changed: %q -> %q", i, snapshotBefore[i], after[i])
			}
		}
	})
	t.Run("AC-5_existing_run_row_has_NULL_task_id_after_upgrade", func(t *testing.T) {
		db, err := Open(openV25WithRows(t), prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		var n, nulls int
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COUNT(*), COALESCE(SUM(task_id IS NULL), 0) FROM run`).Scan(&n, &nulls)
		}); err != nil {
			t.Fatal(err)
		}
		if n != 1 || nulls != 1 {
			t.Errorf("run rows = %d, with NULL task_id = %d; want 1, 1", n, nulls)
		}
		// 過去の課題の uses_integration も NULL のまま。
		var ui sql.NullInt64
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT uses_integration FROM challenge WHERE id = 1`).Scan(&ui)
		}); err != nil || ui.Valid {
			t.Errorf("uses_integration = %v, err = %v; want NULL", ui, err)
		}
	})
	t.Run("AC-6_fresh_store_has_task_table_with_required_columns", func(t *testing.T) {
		db := openFresh(t)
		if !tableExists(t, db, "task") {
			t.Fatal("task table not found")
		}
		cols := tableColumnInfo(t, db, "task")
		required := []string{"id", "challenge_id", "plan_version", "title", "body", "done_criteria", "repo", "operation", "status", "created_at", "updated_at", "expand_after", "lands_spec"}
		nullable := []string{"key", "non_goals", "source_key", "budget_impl_micros", "budget_review_micros", "addition_id", "planned_flow", "flow_reason", "covers"} // budget_* は版 29、addition_id・expand_after は版 34、planned_flow・flow_reason は版 40、covers は版 42 で足す
		// lands_spec は版 43 で足す（NOT NULL・既定 0）。
		// verify_not_met_streak は版 35 で足す（NOT NULL・既定 0）。
		if len(cols) != len(required)+len(nullable)+1 {
			t.Errorf("task columns = %v, want %d columns", cols, len(required)+len(nullable)+1)
		}
		for _, c := range required {
			// 主キー（id）は INTEGER PRIMARY KEY で notnull が 0 になりうるので、id 以外を検査する。
			if nn, ok := cols[c]; !ok || (c != "id" && !nn) {
				t.Errorf("task.%s: present=%v notnull=%v; want present and NOT NULL", c, ok, nn)
			}
		}
		for _, c := range nullable {
			if nn, ok := cols[c]; !ok || nn {
				t.Errorf("task.%s: present=%v notnull=%v; want present and nullable", c, ok, nn)
			}
		}
	})
	t.Run("AC-7_fresh_store_has_task_dependency_table", func(t *testing.T) {
		db := openFresh(t)
		if !tableExists(t, db, "task_dependency") {
			t.Fatal("task_dependency table not found")
		}
		cols := tableColumnInfo(t, db, "task_dependency")
		if len(cols) != 2 || !cols["task_id"] || !cols["depends_on_id"] {
			t.Errorf("task_dependency columns = %v, want task_id and depends_on_id (NOT NULL)", cols)
		}
		// 2 つの組が主キー: 同じ組は 2 つ入らない。
		seedTasks(t, db, [][2]any{{"a", 1}, {"b", 1}})
		exec := func() error {
			return db.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO task_dependency (task_id, depends_on_id) VALUES (2, 1)`)
				return err
			})
		}
		if err := exec(); err != nil {
			t.Fatalf("first dependency: %v", err)
		}
		if err := exec(); err == nil || !IsInvalidData(err) {
			t.Errorf("duplicate dependency error = %v, want a primary key violation", err)
		}
	})
	t.Run("AC-8_fresh_store_has_plan_answer_table", func(t *testing.T) {
		db := openFresh(t)
		if !tableExists(t, db, "plan_answer") {
			t.Fatal("plan_answer table not found")
		}
		cols := tableColumnInfo(t, db, "plan_answer")
		required := []string{"challenge_id", "plan_version", "question_number", "recommended", "actor", "channel", "verification", "answered_at"}
		nullable := []string{"choice", "text", "relay_session", "relay_quote"}
		for _, c := range required {
			if nn, ok := cols[c]; !ok || !nn {
				t.Errorf("plan_answer.%s: present=%v notnull=%v; want present and NOT NULL", c, ok, nn)
			}
		}
		for _, c := range nullable {
			if nn, ok := cols[c]; !ok || nn {
				t.Errorf("plan_answer.%s: present=%v notnull=%v; want present and nullable", c, ok, nn)
			}
		}
		if _, ok := cols["id"]; !ok || len(cols) != len(required)+len(nullable)+1 {
			t.Errorf("plan_answer columns = %v", cols)
		}
	})
	t.Run("task_id_and_uses_integration_columns_exist_and_are_nullable", func(t *testing.T) {
		db := openFresh(t)
		for _, table := range []string{"run", "contract_run", "hold"} {
			if nn, ok := tableColumnInfo(t, db, table)["task_id"]; !ok || nn {
				t.Errorf("%s.task_id: present=%v notnull=%v; want present and nullable", table, ok, nn)
			}
		}
		if nn, ok := tableColumnInfo(t, db, "challenge")["uses_integration"]; !ok || nn {
			t.Errorf("challenge.uses_integration: present=%v notnull=%v; want present and nullable", ok, nn)
		}
	})
	t.Run("existing_active_run_indexes_are_unchanged_by_0026", func(t *testing.T) {
		indexSQL := func(db *DB, name string) string {
			var got string
			if err := db.Read(context.Background(), func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&got)
			}); err != nil {
				t.Fatalf("index %s: %v", name, err)
			}
			return got
		}
		old, err := Open(filepath.Join(t.TempDir(), "old.db"), migrationsUpTo(t, 25))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = old.Close() }()
		db, err := Open(filepath.Join(t.TempDir(), "v28.db"), migrationsUpTo(t, 28)) // 版 29 が索引を作り直すので、26〜28 では変わらないことを見る
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		for _, name := range []string{"idx_run_active_challenge", "idx_contract_run_active_challenge"} {
			if before, after := indexSQL(old, name), indexSQL(db, name); before != after {
				t.Errorf("index %s changed:\n before: %s\n after:  %s", name, before, after)
			}
		}
	})
}

// seedTasks は課題 1 件と、(key, plan_version) のタスクを入れる。key が nil なら NULL。
func seedTasks(t *testing.T, db *DB, rows [][2]any) {
	t.Helper()
	if countRows(t, db, "challenge") == 0 {
		insertChallenge(t, db, "c")
	}
	for _, r := range rows {
		if err := insertTaskRow(db, 1, r[0], r[1]); err != nil {
			t.Fatalf("seed task %v: %v", r, err)
		}
	}
}

func insertTaskRow(db *DB, challengeID int64, key, planVersion any) error {
	return db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
			VALUES (?, ?, ?, 't', 'b', 'd', 'o/r', 'pull_request', 'pending', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')`,
			challengeID, planVersion, key)
		return err
	})
}

func TestTaskKeyUniqueness(t *testing.T) {
	t.Run("AC-9_same_challenge_plan_version_and_key_cannot_be_inserted_twice", func(t *testing.T) {
		db := openFresh(t)
		insertChallenge(t, db, "c")
		if err := insertTaskRow(db, 1, "a", 1); err != nil {
			t.Fatal(err)
		}
		err := insertTaskRow(db, 1, "a", 1)
		if err == nil || !IsUniqueViolation(err) {
			t.Fatalf("second insert error = %v, want a unique violation", err)
		}
		if n := countRows(t, db, "task"); n != 1 {
			t.Errorf("task rows = %d, want 1", n)
		}
	})
	t.Run("AC-9_same_key_is_allowed_in_another_plan_version_or_challenge", func(t *testing.T) {
		db := openFresh(t)
		insertChallenge(t, db, "c1")
		insertChallenge(t, db, "c2")
		for _, r := range []struct {
			cid int64
			pv  int
		}{{1, 1}, {1, 2}, {2, 1}} {
			if err := insertTaskRow(db, r.cid, "a", r.pv); err != nil {
				t.Errorf("insert (challenge %d, plan %d, key a): %v", r.cid, r.pv, err)
			}
		}
	})
	t.Run("AC-9_rows_with_NULL_key_do_not_collide", func(t *testing.T) {
		db := openFresh(t)
		insertChallenge(t, db, "c")
		for i := 0; i < 3; i++ {
			if err := insertTaskRow(db, 1, nil, 1); err != nil {
				t.Fatalf("NULL key insert %d: %v", i, err)
			}
		}
		if n := countRows(t, db, "task"); n != 3 {
			t.Errorf("task rows = %d, want 3", n)
		}
	})
}

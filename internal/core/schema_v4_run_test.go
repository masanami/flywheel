package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

// このファイルは #78（親要件チケット #77 §クリティカル設計決定 1・
// docs/features/m3-invoker-delegation.md）が足すマイグレーション 0004 の
// 受入基準 AC-165〜AC-168 を検証する: 版 3 のストア（0004 の前）を開いたときの
// 版・既存データの保持・新しい列の既定値、および課題ごとの run の排他。

// v3OnlyMigrationsForTest は store.Migrations()（0004 を足した後は版 1〜4 を
// 返す）から版 1〜3 だけを取り出す。「版 3 のストア」（0004 の前・0003 の後）の
// フィクスチャを作るためのテスト専用ヘルパー（v2OnlyMigrationsForTest
// [schema_v3_observation_test.go]・v1OnlyMigrationsForTest
// [source_binding_test.go] と同じ考え方）。
func v3OnlyMigrationsForTest(t *testing.T) []store.Migration {
	t.Helper()
	all, err := store.Migrations()
	if err != nil {
		t.Fatalf("store.Migrations(): %v", err)
	}
	var v3 []store.Migration
	for _, m := range all {
		if m.Version <= 3 {
			v3 = append(v3, m)
		}
	}
	if len(v3) != 3 {
		t.Fatalf("v3OnlyMigrationsForTest: found %d migrations with Version <= 3, want 3", len(v3))
	}
	return v3
}

// schemaVersion3FixtureRows は openSchemaVersion3Fixture が版 3 のストアへ
// 書き込む固定の内容（AC-166 の照合に使う）。
const (
	fixtureTS = "2026-09-21T00:00:00.000Z"
)

// openSchemaVersion3Fixture は、版 3 のストア（0004 の前）に、課題・計画・
// 不可逆操作・承認・保留・作業ログ・対応の記録（source_binding）を各 1 件
// 作り、OpenWorkspace で 0004 まで適用してから返す。
func openSchemaVersion3Fixture(t *testing.T) (dir string, s *Store) {
	t.Helper()
	dir = t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, flywheelDirName))
	dbPath := storeDBPath(dir)

	legacy, err := store.Open(dbPath, v3OnlyMigrationsForTest(t))
	if err != nil {
		t.Fatalf("store.Open(v3) error = %v", err)
	}
	if err := legacy.Write(context.Background(), func(tx *sql.Tx) error {
		stmts := []struct {
			q    string
			args []any
		}{
			{`INSERT INTO challenge (id, title, description, done_criteria, urgency, priority, status, version, reporter, created_at, updated_at)
			  VALUES (1, 'legacy title', 'legacy desc', 'legacy done', 'high', 'P1', 'in_progress', 2, 'alice', ?, ?)`,
				[]any{fixtureTS, fixtureTS}},
			{`INSERT INTO task_plan (challenge_id, version, body, created_at) VALUES (1, 1, 'plan body', ?)`,
				[]any{fixtureTS}},
			{`INSERT INTO operation (id, challenge_id, kind, summary, ref, state, version, created_at)
			  VALUES (1, 1, 'release', 'do the release', 'ref-1', 'pending', 1, ?)`,
				[]any{fixtureTS}},
			{`INSERT INTO approval (id, challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, reason, decided_at)
			  VALUES (1, 1, 1, 'release', 'approved', 1, 'alice', 'cli', 'tty_confirm', NULL, ?)`,
				[]any{fixtureTS}},
			{`INSERT INTO hold (id, challenge_id, question, from_status, raised_at, answer, answered_at, answered_by)
			  VALUES (1, 1, 'need info?', 'in_progress', ?, NULL, NULL, NULL)`,
				[]any{fixtureTS}},
			{`INSERT INTO activity (id, at, actor, channel, verification, entity, entity_id, action, before, after)
			  VALUES (1, ?, 'alice', 'cli', 'none', 'challenge', 1, 'create', NULL, '{"status":"unclassified"}')`,
				[]any{fixtureTS}},
			{`INSERT INTO source_binding (challenge_id, source_id, external_key, url, fingerprint, upstream_state, policy_state, comments_count, upstream_updated_at, read_comments_count, read_upstream_updated_at, created_at, updated_at)
			  VALUES (1, 's', 'o/r#1', 'https://example.invalid/o/r/1', '2:abc', 'open', 'in_policy', 5, ?, 5, ?, ?, ?)`,
				[]any{fixtureTS, fixtureTS, fixtureTS, fixtureTS}},
		}
		for _, st := range stmts {
			if _, err := tx.Exec(st.q, st.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("legacy fixture write error = %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	s, err = OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return dir, s
}

func TestOpenWorkspace_UpgradesSchemaVersion3StoreToVersion4(t *testing.T) {
	_, s := openSchemaVersion3Fixture(t)
	ctx := context.Background()

	// AC-165: 版が 4 になる（0005 を足した後は OpenWorkspace が最新版の 5 まで
	// 適用する。版 5 への移行は schema_v5_slot_test.go が検証する）。
	if got := schemaVersionForTest(t, s); got != 5 {
		t.Fatalf("schema version = %d, want 5 (AC-165 + AC-370)", got)
	}

	// AC-166: 既存の課題・計画・承認・保留・作業ログ・対応の記録が件数と
	// 内容で保持される。
	detail, err := s.GetChallenge(ctx, "C-1")
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.Title != "legacy title" || detail.Version != 2 || detail.Status != "in_progress" {
		t.Fatalf("GetChallenge() = %+v, want Title=legacy title Version=2 Status=in_progress (AC-166)", detail)
	}
	if detail.SourceBinding == nil || detail.SourceBinding.ExternalKey != "o/r#1" || detail.SourceBinding.CommentsCount != 5 {
		t.Fatalf("GetChallenge().SourceBinding = %+v, want ExternalKey=o/r#1 CommentsCount=5 (AC-166)", detail.SourceBinding)
	}
	if len(detail.Approvals) != 1 || detail.Approvals[0].Decision != ApprovalDecisionApproved {
		t.Fatalf("Approvals = %+v, want 1 approval {Decision:approved} (AC-166)", detail.Approvals)
	}

	plans := loadTaskPlanRowsForTest(t, s, 1)
	if len(plans) != 1 || plans[0].Body != "plan body" || plans[0].Version != 1 {
		t.Fatalf("task_plan rows = %+v, want 1 row {Version:1 Body:plan body} (AC-166)", plans)
	}

	ops, err := s.GetChallenge(ctx, "C-1")
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if len(ops.Operations) != 1 || ops.Operations[0].Summary != "do the release" {
		t.Fatalf("Operations = %+v, want 1 operation 'do the release' (AC-166)", ops.Operations)
	}

	holds := loadHoldRowsForTest(t, s, 1)
	if len(holds) != 1 || holds[0].Question != "need info?" {
		t.Fatalf("hold rows = %+v, want 1 row {Question:need info?} (AC-166)", holds)
	}

	activities, err := s.ListActivities(ctx, stringPtr("C-1"))
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	if len(activities) != 1 || activities[0].Action != "create" {
		t.Fatalf("activities = %+v, want 1 activity {Action:create} (AC-166)", activities)
	}

	// AC-167: 既存の計画の spec は NULL。あわせて hold.run_id・activity.run_id
	// も NULL のまま増える。
	if plans[0].Spec != nil {
		t.Fatalf("task_plan.spec = %v, want nil (AC-167)", plans[0].Spec)
	}
	if holds[0].RunID != nil {
		t.Fatalf("hold.run_id = %v, want nil (AC-167)", holds[0].RunID)
	}
	// Activity（公開の形）は run_id を持たない（#78 は公開 API を変えない）ので、
	// 生の列を直接読む。
	if got := loadActivityRunIDForTest(t, s, 1); got != nil {
		t.Fatalf("activity.run_id = %v, want nil (AC-167)", got)
	}
}

// AC-168 はストア層（run_store.go）の insertRun が守る（run_store_test.go の
// TestInsertRun_RejectsSecondActiveRunForSameChallenge・
// TestInsertRun_AllowsActiveRunForDifferentChallenge・
// TestInsertRun_AllowsNewActiveRunAfterFirstEnded）。ここでは、0003 から
// 0004 へ実際に昇格したストア（このファイルのフィクスチャ）でも同じ排他が
// 効くことを確かめる。
func TestOpenWorkspace_Version4Store_EnforcesActiveRunExclusivityAfterUpgrade(t *testing.T) {
	_, s := openSchemaVersion3Fixture(t)

	first, err := insertRunForTest(t, s, 1, validInsertRunInput())
	if err != nil {
		t.Fatalf("insertRun(1回目) error = %v", err)
	}
	if _, err := insertRunForTest(t, s, 1, validInsertRunInput()); !errors.Is(err, errActiveRunExists) {
		t.Fatalf("insertRun(2回目・同じ課題) error = %v, want errActiveRunExists (AC-168)", err)
	}

	if _, err := updateRunEndForTest(t, s, mustParseRunIDForTest(t, first.ID), updateRunEndInput{
		EndedAt: time.Now(), Result: runResultSucceeded,
	}); err != nil {
		t.Fatalf("updateRunEnd() error = %v", err)
	}
	if _, err := insertRunForTest(t, s, 1, validInsertRunInput()); err != nil {
		t.Fatalf("insertRun(1回目終了後の2回目) error = %v, want nil (AC-168)", err)
	}
}

// --- テスト用ヘルパー ---

type taskPlanRowForTest struct {
	ChallengeID int64
	Version     int64
	Body        string
	Spec        *string
}

func loadTaskPlanRowsForTest(t *testing.T, s *Store, challengeID int64) []taskPlanRowForTest {
	t.Helper()
	var rows []taskPlanRowForTest
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		r, err := tx.Query(`SELECT challenge_id, version, body, spec FROM task_plan WHERE challenge_id = ? ORDER BY version`, challengeID)
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		for r.Next() {
			var row taskPlanRowForTest
			var spec sql.NullString
			if err := r.Scan(&row.ChallengeID, &row.Version, &row.Body, &spec); err != nil {
				return err
			}
			if spec.Valid {
				v := spec.String
				row.Spec = &v
			}
			rows = append(rows, row)
		}
		return r.Err()
	})
	if err != nil {
		t.Fatalf("loadTaskPlanRowsForTest() error = %v", err)
	}
	return rows
}

type holdRowForTest struct {
	ID       int64
	Question string
	RunID    *int64
}

func loadHoldRowsForTest(t *testing.T, s *Store, challengeID int64) []holdRowForTest {
	t.Helper()
	var rows []holdRowForTest
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		r, err := tx.Query(`SELECT id, question, run_id FROM hold WHERE challenge_id = ? ORDER BY id`, challengeID)
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		for r.Next() {
			var row holdRowForTest
			var runID sql.NullInt64
			if err := r.Scan(&row.ID, &row.Question, &runID); err != nil {
				return err
			}
			if runID.Valid {
				v := runID.Int64
				row.RunID = &v
			}
			rows = append(rows, row)
		}
		return r.Err()
	})
	if err != nil {
		t.Fatalf("loadHoldRowsForTest() error = %v", err)
	}
	return rows
}

func loadActivityRunIDForTest(t *testing.T, s *Store, activityID int64) *int64 {
	t.Helper()
	var result *int64
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var runID sql.NullInt64
		if err := tx.QueryRow(`SELECT run_id FROM activity WHERE id = ?`, activityID).Scan(&runID); err != nil {
			return err
		}
		if runID.Valid {
			v := runID.Int64
			result = &v
		}
		return nil
	})
	if err != nil {
		t.Fatalf("loadActivityRunIDForTest() error = %v", err)
	}
	return result
}

func pragmaTableInfoColumnsForTest(t *testing.T, s *Store, table string) []string {
	t.Helper()
	var cols []string
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		r, err := tx.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		for r.Next() {
			var cid int
			var name, colType string
			var notNull int
			var dflt sql.NullString
			var pk int
			if err := r.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
				return err
			}
			cols = append(cols, name)
		}
		return r.Err()
	})
	if err != nil {
		t.Fatalf("pragmaTableInfoColumnsForTest(%s) error = %v", table, err)
	}
	return cols
}

func tableExistsForTest(t *testing.T, s *Store, table string) bool {
	t.Helper()
	var exists bool
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var name string
		err := tx.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err == sql.ErrNoRows {
			exists = false
			return nil
		}
		if err != nil {
			return err
		}
		exists = true
		return nil
	})
	if err != nil {
		t.Fatalf("tableExistsForTest(%s) error = %v", table, err)
	}
	return exists
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

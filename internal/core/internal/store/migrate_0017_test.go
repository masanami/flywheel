package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 16 のストアを開くと 0017 が当たり、課題に「いまの完了確認待ちが親が直接した完了（T17）から来たか」の
// 列が足される。既存の行は、完了確認待ちで、作業ログの最後（complete_direct と verify_met のうち）が complete_direct の
// 課題だけが真（従来の作業ログからの判定と結果が変わらない）。それ以外はすべて偽。版は 17 になる。
func TestOpen_BackfillsCompletionDirectFromTheActivityLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")
	var upTo16 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 16 {
			upTo16 = append(upTo16, m)
		}
	}
	old, err := Open(path, upTo16)
	if err != nil {
		t.Fatalf("Open(v16) error = %v", err)
	}
	if got := userVersion(t, old); got != 16 {
		t.Fatalf("user_version = %d, want 16", got)
	}
	// 課題 1〜6 を、状態と作業ログの組み合わせを変えて作る。
	type seed struct {
		status  string
		actions []string // entity=challenge の作業ログの action を古い順に
		want    int
	}
	seeds := []seed{
		{"awaiting_completion_approval", []string{"complete_direct"}, 1},               // T17 だけ
		{"awaiting_completion_approval", []string{"verify_met"}, 0},                    // T8 だけ
		{"awaiting_completion_approval", []string{"complete_direct", "verify_met"}, 0}, // 古い T17 のあと T8 で入り直した
		{"awaiting_completion_approval", []string{"verify_met", "reject", "complete_direct"}, 1},
		{"in_progress", []string{"complete_direct", "reject"}, 0}, // 完了確認待ちにいない
		{"done", []string{"complete_direct", "approve"}, 0},       // 完了
		{"awaiting_completion_approval", nil, 0},                  // 作業ログが無い
	}
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		for i, s := range seeds {
			if _, err := tx.Exec(`INSERT INTO challenge (title, status, reporter, created_at, updated_at) VALUES (?, ?, 'alice', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')`,
				"c", s.status); err != nil {
				return err
			}
			for _, a := range s.actions {
				if _, err := tx.Exec(`INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action) VALUES ('2026-11-01T00:00:00.000Z', 'alice', 'cli', 'none', 'challenge', ?, ?)`,
					i+1, a); err != nil {
					return err
				}
			}
		}
		// 別の entity の同じ ID の行は数えない。
		_, err := tx.Exec(`INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action) VALUES ('2026-11-01T00:00:00.000Z', 'alice', 'cli', 'none', 'operation', 2, 'complete_direct')`)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open(latest) error = %v", err)
	}
	defer func() { _ = db.Close() }()
	if got := userVersion(t, db); got != 20 {
		t.Fatalf("user_version = %d, want 20", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		for i, s := range seeds {
			var got int
			if err := tx.QueryRow(`SELECT completion_direct FROM challenge WHERE id = ?`, i+1).Scan(&got); err != nil {
				return err
			}
			if got != s.want {
				t.Errorf("challenge %d (status %s, log %v): completion_direct = %d, want %d", i+1, s.status, s.actions, got, s.want)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
}

// 新しい課題の行は、列を指定しなければ偽（0）になる（既存の挿入の経路が列を知らなくても壊れない）。
func TestChallenge_CompletionDirectDefaultsToFalse(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "flywheel.db"), prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	insertChallenge(t, db, "a")
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var got int
		if err := tx.QueryRow(`SELECT completion_direct FROM challenge WHERE id = 1`).Scan(&got); err != nil {
			return err
		}
		if got != 0 {
			t.Errorf("completion_direct = %d, want 0", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

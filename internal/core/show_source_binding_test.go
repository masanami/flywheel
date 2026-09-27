package core

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

// このファイルは #56 の AC-103 を検証する: スキーマ版 1（M1）のストアを開いた
// 後、既存の課題（対応を持たない）の show（GetChallenge）の source_binding は
// null（nil）である。v1OnlyMigrationsForTest・mustMkdirAll・storeDBPath・
// flywheelDirName は internal/core/source_binding_test.go・workspace_test.go・
// store.go が既に定義している同じパッケージのヘルパー・定数を再利用する。

func TestGetChallenge_SchemaVersion1Challenge_HasNilSourceBindingAfterUpgrade(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, flywheelDirName))
	dbPath := storeDBPath(dir)
	const ts = "2026-09-26T00:00:00.000Z"

	legacy, err := store.Open(dbPath, v1OnlyMigrationsForTest(t))
	if err != nil {
		t.Fatalf("store.Open(v1) error = %v", err)
	}
	if err := legacy.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO challenge (id, title, status, reporter, created_at, updated_at) VALUES (1, 'legacy', 'unclassified', 'alice', ?, ?)`,
			ts, ts,
		)
		return err
	}); err != nil {
		t.Fatalf("legacy fixture write error = %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	s, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace() error = %v", err)
	}
	defer func() { _ = s.Close() }()

	// 0003（#72）を足した後の最新版は 3（AC-99 の文言と同じ改訂）。
	if got := schemaVersionForTest(t, s); got != 3 {
		t.Fatalf("schema version = %d, want 3", got)
	}

	detail, err := s.GetChallenge(context.Background(), "C-1")
	if err != nil {
		t.Fatalf("GetChallenge(C-1) error = %v", err)
	}
	if detail.SourceBinding != nil {
		t.Fatalf("SourceBinding = %+v, want nil (AC-103: スキーマ版1から上げたストアの既存の課題は対応を持たない)", detail.SourceBinding)
	}
}

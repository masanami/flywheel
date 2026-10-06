package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

// このファイルは #72（親要件チケット #51 §クリティカル設計決定 1・
// §上流の更新の観測と既読）の受入基準 AC-151〜AC-159 を検証する: 0003 の
// 適用前に作られた対応（スキーマ版 2 のストア）を開いたときの既定値と、
// その対応が初めて観測値を得るときの扱い。

// v2OnlyMigrationsForTest は store.Migrations()（0003 を足した後は版 1・2・3 を
// 返す）から版 1・2 だけを取り出す。「版 2 のストア」（0003 の前・0002 の後）の
// フィクスチャを作るためのテスト専用ヘルパー（v1OnlyMigrationsForTest と同じ
// 考え方。source_binding_test.go）。
func v2OnlyMigrationsForTest(t *testing.T) []store.Migration {
	t.Helper()
	all, err := store.Migrations()
	if err != nil {
		t.Fatalf("store.Migrations(): %v", err)
	}
	var v2 []store.Migration
	for _, m := range all {
		if m.Version <= 2 {
			v2 = append(v2, m)
		}
	}
	if len(v2) != 2 {
		t.Fatalf("v2OnlyMigrationsForTest: found %d migrations with Version <= 2, want 2", len(v2))
	}
	return v2
}

// openSchemaVersion2FixtureWithBinding は、版 2 のストア（0003 の前）に課題
// C-1 と、観測値の列を持たない（＝既定値になる）対応を 1 件作り、
// OpenWorkspace で 0003 まで適用してから返す。
func openSchemaVersion2FixtureWithBinding(t *testing.T, externalKey string) (dir string, s *Store) {
	t.Helper()
	dir = t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, flywheelDirName))
	dbPath := storeDBPath(dir)
	const ts = "2026-09-21T00:00:00.000Z"

	legacy, err := store.Open(dbPath, v2OnlyMigrationsForTest(t))
	if err != nil {
		t.Fatalf("store.Open(v2) error = %v", err)
	}
	if err := legacy.Write(context.Background(), func(tx *sql.Tx) error {
		stmts := []string{
			`INSERT INTO challenge (id, title, status, reporter, created_at, updated_at)
			 VALUES (1, 'legacy bound', 'unclassified', 'alice', '` + ts + `', '` + ts + `')`,
			`INSERT INTO source_binding (challenge_id, source_id, external_key, url, fingerprint, upstream_state, policy_state, created_at, updated_at)
			 VALUES (1, 's', '` + externalKey + `', 'https://example.invalid/` + externalKey + `', '2:abc', 'open', 'in_policy', '` + ts + `', '` + ts + `')`,
		}
		for _, q := range stmts {
			if _, err := tx.Exec(q); err != nil {
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

func TestOpenWorkspace_UpgradesSchemaVersion2StoreToVersion3WithDefaults(t *testing.T) {
	_, s := openSchemaVersion2FixtureWithBinding(t, "o/r#1")

	// AC-151: 版が 3 になる（その後 0004 が足され最新版は 4 になったが、この
	// フィクスチャは v2OnlyMigrationsForTest で v3 相当まで作るための前提で
	// あり、v2 → v3 の観測値のテストの主眼は変わらない。OpenWorkspace は
	// 最新版まで適用するため実際の版は 4 になる。AC-165 は #78 が検証する）。
	if got := schemaVersionForTest(t, s); got != 6 {
		t.Fatalf("schema version = %d, want 6 (AC-151 + AC-165 + AC-370)", got)
	}

	detail, err := s.GetChallenge(context.Background(), "C-1")
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	sb := detail.SourceBinding
	if sb == nil {
		t.Fatalf("SourceBinding = nil, want non-nil")
	}
	// AC-152: comments_count は 0。
	if sb.CommentsCount != 0 {
		t.Errorf("CommentsCount = %d, want 0 (AC-152)", sb.CommentsCount)
	}
	// AC-153: read_comments_count は 0。
	if sb.ReadCommentsCount != 0 {
		t.Errorf("ReadCommentsCount = %d, want 0 (AC-153)", sb.ReadCommentsCount)
	}
	// AC-154: upstream_updated_at は未設定（""。cli が null へ変換する）。
	if sb.UpstreamUpdatedAt != "" {
		t.Errorf("UpstreamUpdatedAt = %q, want unset (AC-154)", sb.UpstreamUpdatedAt)
	}
	// AC-155: read_upstream_updated_at も未設定。
	if sb.ReadUpstreamUpdatedAt != "" {
		t.Errorf("ReadUpstreamUpdatedAt = %q, want unset (AC-155)", sb.ReadUpstreamUpdatedAt)
	}
}

// --- AC-156〜159: 0003 の前に作られた対応が初めて観測値を得るときの扱い ---

func TestIngest_PreSchemaV3Binding_FirstObservation(t *testing.T) {
	_, s := openSchemaVersion2FixtureWithBinding(t, "o/r#1")
	fixedActor(t, "alice")

	// この検証の主眼は観測値の反映であり、fingerprint の一致・不一致
	// （result が unchanged か updated か）は問わない。
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", "new body", "carol", nil, nil, 2, "2026-09-28T00:00:00Z")},
	}}

	res, err := s.Ingest(context.Background(), ChannelCLI, IngestInput{Sources: []SourceEntry{src}, Upstream: up})
	if err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	item := findIngestItem(res, "o/r#1")
	if item == nil {
		t.Fatalf("item = nil")
	}

	detail, err := s.GetChallenge(context.Background(), "C-1")
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	sb := detail.SourceBinding
	// AC-156: read_upstream_updated_at が観測値の upstream_updated_at と同じ値になる。
	if sb.ReadUpstreamUpdatedAt != sb.UpstreamUpdatedAt || sb.UpstreamUpdatedAt != "2026-09-28T00:00:00Z" {
		t.Errorf("ReadUpstreamUpdatedAt/UpstreamUpdatedAt = %q/%q, want both 2026-09-28T00:00:00Z (AC-156)", sb.ReadUpstreamUpdatedAt, sb.UpstreamUpdatedAt)
	}

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	d := findDiscrepancy(ov.Discrepancies, "C-1")
	// AC-157: kinds は upstream_updated を含まない。
	if d != nil && kindsContain(d.Kinds, DiscrepancyKindUpstreamUpdated) {
		t.Errorf("discrepancy = %+v, must not contain upstream_updated (AC-157)", d)
	}
	// AC-158: Issue にコメントがあるので kinds は upstream_commented を含む
	// （作成時と同じ扱い＝QH11）。
	if d == nil || !kindsContain(d.Kinds, DiscrepancyKindUpstreamCommented) {
		t.Errorf("discrepancy = %+v, want upstream_commented (AC-158)", d)
	}

	activities, err := s.ListActivities(context.Background(), stringPtr("C-1"))
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	var observationEntry *Activity
	for i := range activities {
		if activities[i].Action == "upstream_observation_change" {
			observationEntry = &activities[i]
		}
	}
	if observationEntry == nil {
		t.Fatalf("no upstream_observation_change activity recorded")
	}
	var before, after map[string]any
	if err := json.Unmarshal(observationEntry.Before, &before); err != nil {
		t.Fatalf("unmarshal before: %v", err)
	}
	if err := json.Unmarshal(observationEntry.After, &after); err != nil {
		t.Fatalf("unmarshal after: %v", err)
	}
	// AC-159: before・after に read_upstream_updated_at を含む（before は null）。
	if v, ok := before["read_upstream_updated_at"]; !ok || v != nil {
		t.Errorf("before[read_upstream_updated_at] = %#v (present=%v), want null (AC-159)", v, ok)
	}
	if after["read_upstream_updated_at"] != "2026-09-28T00:00:00Z" {
		t.Errorf("after[read_upstream_updated_at] = %#v, want 2026-09-28T00:00:00Z (AC-159)", after["read_upstream_updated_at"])
	}
}

func stringPtr(s string) *string { return &s }

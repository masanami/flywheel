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

// --- 作成 ---

func TestInsertSourceBinding_CreatesBindingReadableByChallengeIDAndExternalKey(t *testing.T) {
	s := newStoreForTest(t)
	ch := mustCreateChallenge(t, s, "issue-49")
	at := time.Date(2026, 9, 26, 1, 2, 3, 0, time.UTC)

	in := createSourceBindingInput{
		SourceID:      "harness-repo-issues",
		ExternalKey:   "masanami/flywheel#49",
		URL:           "https://github.com/masanami/flywheel/issues/49",
		Fingerprint:   "2:9c56b5c0a92d",
		UpstreamState: upstreamStateOpen,
		PolicyState:   policyStateInPolicy,
	}
	sb, err := createSourceBindingForTest(s, ch.ID, at, in)
	if err != nil {
		t.Fatalf("insertSourceBinding() error = %v", err)
	}
	want := sourceBinding{
		ChallengeID: ch.ID, SourceID: in.SourceID, ExternalKey: in.ExternalKey, URL: in.URL,
		Fingerprint: in.Fingerprint, UpstreamState: upstreamStateOpen, PolicyState: policyStateInPolicy,
		CreatedAt: at, UpdatedAt: at,
	}
	if *sb != want {
		t.Fatalf("insertSourceBinding() = %+v, want %+v", *sb, want)
	}

	byChallenge, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("getSourceBindingByChallengeID() error = %v", err)
	}
	if byChallenge == nil || *byChallenge != want {
		t.Fatalf("getSourceBindingByChallengeID() = %+v, want %+v", byChallenge, want)
	}
	byExternalKey, err := s.getSourceBindingByExternalKey(context.Background(), in.ExternalKey)
	if err != nil {
		t.Fatalf("getSourceBindingByExternalKey() error = %v", err)
	}
	if byExternalKey == nil || *byExternalKey != want {
		t.Fatalf("getSourceBindingByExternalKey() = %+v, want %+v", byExternalKey, want)
	}
}

// TestInsertSourceBinding_StoreRejectsSecondBindingForSameExternalKey は AC-104 の検証:
// 1 つの外部キーに 2 つ目の対応を書き込もうとすると、ストアが一意制約で拒否する
// （insertSourceBinding は事前の読み取りで重複を判定せず、制約の違反を翻訳する）。
func TestInsertSourceBinding_StoreRejectsSecondBindingForSameExternalKey(t *testing.T) {
	s := newStoreForTest(t)
	first := mustCreateChallenge(t, s, "first")
	second := mustCreateChallenge(t, s, "second")

	if _, err := createSourceBindingForTest(s, first.ID, time.Now(), validBindingInput("masanami/flywheel#49")); err != nil {
		t.Fatalf("insertSourceBinding(1回目) error = %v", err)
	}
	other := validBindingInput("masanami/flywheel#49")
	other.SourceID = "renamed-source" // 宣言の id を変えても同じ Issue は二重に対応を持てない
	_, err := createSourceBindingForTest(s, second.ID, time.Now(), other)
	if !errors.Is(err, errSourceBindingExternalKeyTaken) {
		t.Fatalf("insertSourceBinding(2回目・同じ external_key) error = %v, want errSourceBindingExternalKeyTaken", err)
	}

	got, err := s.getSourceBindingByExternalKey(context.Background(), "masanami/flywheel#49")
	if err != nil {
		t.Fatalf("getSourceBindingByExternalKey() error = %v", err)
	}
	if got == nil || got.ChallengeID != first.ID || got.SourceID != "src" {
		t.Fatalf("getSourceBindingByExternalKey() = %+v, want 1 つ目の課題 %s の対応のまま", got, first.ID)
	}
	if sb, err := s.getSourceBindingByChallengeID(context.Background(), second.ID); err != nil || sb != nil {
		t.Fatalf("getSourceBindingByChallengeID(second) = %+v, %v, want nil, nil（拒否された対応は残らない）", sb, err)
	}
}

func TestInsertSourceBinding_RejectsSecondBindingForSameChallenge(t *testing.T) {
	s := newStoreForTest(t)
	ch := mustCreateChallenge(t, s, "one binding only")

	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), validBindingInput("owner/repo#1")); err != nil {
		t.Fatalf("insertSourceBinding(1回目) error = %v", err)
	}
	_, err := createSourceBindingForTest(s, ch.ID, time.Now(), validBindingInput("owner/repo#2"))
	if !errors.Is(err, errSourceBindingExists) {
		t.Fatalf("insertSourceBinding(同じ課題に2つ目) error = %v, want errSourceBindingExists", err)
	}
	if errors.Is(err, errSourceBindingExternalKeyTaken) {
		t.Fatalf("insertSourceBinding(同じ課題に2つ目) error = %v, 外部キーの重複と区別されていない", err)
	}

	got, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || got == nil || got.ExternalKey != "owner/repo#1" {
		t.Fatalf("getSourceBindingByChallengeID() = %+v, %v, want 1 つ目の対応 owner/repo#1 のまま", got, err)
	}
	if sb, err := s.getSourceBindingByExternalKey(context.Background(), "owner/repo#2"); err != nil || sb != nil {
		t.Fatalf("getSourceBindingByExternalKey(owner/repo#2) = %+v, %v, want nil, nil", sb, err)
	}
}

func TestInsertSourceBinding_RejectsInvalidInput(t *testing.T) {
	cases := map[string]func(in *createSourceBindingInput){
		"source_id が空":         func(in *createSourceBindingInput) { in.SourceID = "" },
		"external_key が空":      func(in *createSourceBindingInput) { in.ExternalKey = "" },
		"url が空":               func(in *createSourceBindingInput) { in.URL = "" },
		"fingerprint が空":       func(in *createSourceBindingInput) { in.Fingerprint = "" },
		"upstream_state が閉集合外": func(in *createSourceBindingInput) { in.UpstreamState = "reopened" },
		"upstream_state が空":    func(in *createSourceBindingInput) { in.UpstreamState = "" },
		"policy_state が閉集合外":   func(in *createSourceBindingInput) { in.PolicyState = "Out_of_policy" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStoreForTest(t)
			ch := mustCreateChallenge(t, s, "invalid")
			in := validBindingInput("owner/repo#1")
			mutate(&in)
			if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); !errors.Is(err, ErrValidation) {
				t.Fatalf("insertSourceBinding() error = %v, want ErrValidation", err)
			}
			if sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID); err != nil || sb != nil {
				t.Fatalf("getSourceBindingByChallengeID() = %+v, %v, want nil, nil（拒否された対応は残らない）", sb, err)
			}
		})
	}
}

// --- 取得 ---

func TestGetSourceBindingByChallengeID_ReturnsNilWhenChallengeHasNoBinding(t *testing.T) {
	s := newStoreForTest(t)
	ch := mustCreateChallenge(t, s, "no binding")

	sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || sb != nil {
		t.Fatalf("getSourceBindingByChallengeID() = %+v, %v, want nil, nil", sb, err)
	}
}

func TestGetSourceBindingByChallengeID_RejectsMalformedOrMissingChallenge(t *testing.T) {
	s := newStoreForTest(t)
	for _, id := range []string{"not-an-id", "C-999"} {
		if _, err := s.getSourceBindingByChallengeID(context.Background(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("getSourceBindingByChallengeID(%q) error = %v, want ErrNotFound", id, err)
		}
	}
}

func TestGetSourceBindingByExternalKey_ReturnsNilWhenNoBindingExists(t *testing.T) {
	s := newStoreForTest(t)
	sb, err := s.getSourceBindingByExternalKey(context.Background(), "owner/repo#1")
	if err != nil || sb != nil {
		t.Fatalf("getSourceBindingByExternalKey() = %+v, %v, want nil, nil", sb, err)
	}
}

// --- 更新 ---

func TestApplySourceBindingUpdate_UpdatesGivenFieldsAndAdvancesUpdatedAt(t *testing.T) {
	s := newStoreForTest(t)
	ch := mustCreateChallenge(t, s, "issue-49")
	createdAt := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	editedAt := createdAt.Add(time.Hour)
	created, err := createSourceBindingForTest(s, ch.ID, createdAt, validBindingInput("owner/repo#1"))
	if err != nil {
		t.Fatalf("insertSourceBinding() error = %v", err)
	}

	fp := "2:def"
	closed := upstreamStateClosed
	before, after, err := updateSourceBindingForTest(s, ch.ID, editedAt, updateSourceBindingInput{Fingerprint: &fp, UpstreamState: &closed})
	if err != nil {
		t.Fatalf("applySourceBindingUpdate() error = %v", err)
	}
	if *before != *created {
		t.Errorf("before = %+v, want %+v", *before, *created)
	}
	want := *created
	want.Fingerprint = fp
	want.UpstreamState = upstreamStateClosed
	want.UpdatedAt = editedAt
	if *after != want {
		t.Fatalf("after = %+v, want %+v（指定していない項目と created_at は変わらない）", *after, want)
	}
	stored, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || stored == nil || *stored != want {
		t.Fatalf("getSourceBindingByChallengeID() = %+v, %v, want %+v", stored, err, want)
	}
}

func TestApplySourceBindingUpdate_DoesNotWriteWhenNothingChanges(t *testing.T) {
	s := newStoreForTest(t)
	ch := mustCreateChallenge(t, s, "issue-49")
	createdAt := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	created, err := createSourceBindingForTest(s, ch.ID, createdAt, validBindingInput("owner/repo#1"))
	if err != nil {
		t.Fatalf("insertSourceBinding() error = %v", err)
	}

	same := created.Fingerprint
	open := upstreamStateOpen
	before, after, err := updateSourceBindingForTest(s, ch.ID, createdAt.Add(time.Hour), updateSourceBindingInput{Fingerprint: &same, UpstreamState: &open})
	if err != nil {
		t.Fatalf("applySourceBindingUpdate() error = %v", err)
	}
	if *before != *created || *after != *created {
		t.Fatalf("before, after = %+v, %+v, want both %+v", *before, *after, *created)
	}
	stored, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || stored == nil || !stored.UpdatedAt.Equal(createdAt) {
		t.Fatalf("stored = %+v, %v, want updated_at %v のまま（値が変わらなければ書かない）", stored, err, createdAt)
	}
}

func TestApplySourceBindingUpdate_ReturnsNilWhenNoBindingExists(t *testing.T) {
	s := newStoreForTest(t)
	ch := mustCreateChallenge(t, s, "no binding")

	closed := upstreamStateClosed
	before, after, err := updateSourceBindingForTest(s, ch.ID, time.Now(), updateSourceBindingInput{UpstreamState: &closed})
	if err != nil || before != nil || after != nil {
		t.Fatalf("applySourceBindingUpdate() = %+v, %+v, %v, want nil, nil, nil", before, after, err)
	}
}

func TestApplySourceBindingUpdate_RejectsInvalidInput(t *testing.T) {
	empty := ""
	badUpstream := upstreamState("reopened")
	badPolicy := policyState("unknown")
	cases := map[string]updateSourceBindingInput{
		"url が空":               {URL: &empty},
		"fingerprint が空":       {Fingerprint: &empty},
		"upstream_state が閉集合外": {UpstreamState: &badUpstream},
		"policy_state が閉集合外":   {PolicyState: &badPolicy},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStoreForTest(t)
			ch := mustCreateChallenge(t, s, "invalid")
			created, err := createSourceBindingForTest(s, ch.ID, time.Now(), validBindingInput("owner/repo#1"))
			if err != nil {
				t.Fatalf("insertSourceBinding() error = %v", err)
			}
			if _, _, err := updateSourceBindingForTest(s, ch.ID, time.Now(), in); !errors.Is(err, ErrValidation) {
				t.Fatalf("applySourceBindingUpdate() error = %v, want ErrValidation", err)
			}
			stored, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
			if err != nil || stored == nil || *stored != *created {
				t.Fatalf("stored = %+v, %v, want %+v のまま", stored, err, *created)
			}
		})
	}
}

// --- スキーマ版の更新: AC-99〜AC-102 ---

// TestOpenWorkspace_UpgradesSchemaVersion1StoreToVersion2AndPreservesExistingData は
// AC-99〜AC-102 の検証: スキーマ版 1（M1）のストアを開くとスキーマ版が 2 になり、
// 既存の課題・承認の記録・作業ログが件数と内容とも保持される。既存の課題に対応は
// 無い。
func TestOpenWorkspace_UpgradesSchemaVersion1StoreToVersion2AndPreservesExistingData(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, flywheelDirName))
	dbPath := storeDBPath(dir)
	const ts = "2026-09-21T00:00:00.000Z"
	fixtureTime := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)

	legacy, err := store.Open(dbPath, v1OnlyMigrationsForTest(t))
	if err != nil {
		t.Fatalf("store.Open(v1) error = %v", err)
	}
	if err := legacy.Write(context.Background(), func(tx *sql.Tx) error {
		stmts := []string{
			`INSERT INTO challenge (id, title, description, done_criteria, urgency, status, version, reporter, created_at, updated_at)
			 VALUES (1, 'legacy', 'desc', 'done', '高', 'unclassified', 3, 'alice', '` + ts + `', '` + ts + `')`,
			`INSERT INTO challenge (id, title, status, reporter, created_at, updated_at)
			 VALUES (2, 'legacy-2', 'unclassified', 'bob', '` + ts + `', '` + ts + `')`,
			`INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, decided_at)
			 VALUES (1, 'plan', 'approved', 1, 'alice', 'cli', 'tty_confirm', '` + ts + `')`,
			`INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action, before, after)
			 VALUES ('` + ts + `', 'alice', 'cli', 'none', 'challenge', 1, 'create', NULL, '{"title":"legacy"}')`,
			`INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action, before, after)
			 VALUES ('` + ts + `', 'bob', 'cli', 'none', 'challenge', 2, 'create', NULL, '{"title":"legacy-2"}')`,
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

	s, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace() error = %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	// 0003（#72）を足した後の最新版は 3（AC-99 の文言と同じ改訂）。
	if got := schemaVersionForTest(t, s); got != 3 {
		t.Fatalf("schema version = %d, want 3 (AC-99)", got)
	}

	// AC-100: 課題の件数と内容。
	list, err := s.ListChallenges(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("ListChallenges() error = %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(ListChallenges()) = %d, want 2 (AC-100)", len(list))
	}
	high := UrgencyHigh
	wantC1 := Challenge{
		ID: "C-1", Title: "legacy", Description: "desc", DoneCriteria: "done", Urgency: &high,
		Status: StatusUnclassified, Version: 3, Reporter: "alice", CreatedAt: fixtureTime, UpdatedAt: fixtureTime,
	}
	detail, err := s.GetChallenge(ctx, "C-1")
	if err != nil {
		t.Fatalf("GetChallenge(C-1) error = %v", err)
	}
	got := detail.Challenge
	if got.Urgency == nil || *got.Urgency != high || got.Priority != nil {
		t.Errorf("C-1 urgency/priority = %v/%v, want high/nil (AC-100)", got.Urgency, got.Priority)
	}
	got.Urgency, wantC1.Urgency = nil, nil
	if got != wantC1 {
		t.Errorf("C-1 = %+v, want %+v (AC-100)", got, wantC1)
	}
	wantC2 := Challenge{
		ID: "C-2", Title: "legacy-2", Status: StatusUnclassified, Version: 1, Reporter: "bob",
		CreatedAt: fixtureTime, UpdatedAt: fixtureTime,
	}
	detail2, err := s.GetChallenge(ctx, "C-2")
	if err != nil {
		t.Fatalf("GetChallenge(C-2) error = %v", err)
	}
	if detail2.Challenge != wantC2 {
		t.Errorf("C-2 = %+v, want %+v (AC-100)", detail2.Challenge, wantC2)
	}

	// AC-101: 承認の記録の件数と内容。
	if len(detail.Approvals) != 1 {
		t.Fatalf("len(Approvals) = %d, want 1 (AC-101)", len(detail.Approvals))
	}
	a := detail.Approvals[0]
	if a.Kind != "plan" || a.Decision != "approved" || a.TargetVersion != 1 || a.Actor != "alice" ||
		a.Channel != "cli" || a.Verification != "tty_confirm" || !a.DecidedAt.Equal(fixtureTime) {
		t.Errorf("approval = %+v, want plan/approved/1/alice/cli/tty_confirm/%v (AC-101)", a, fixtureTime)
	}

	// AC-102: 作業ログの件数と内容。
	activities, err := s.ListActivities(ctx, nil)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	if len(activities) != 2 {
		t.Fatalf("len(activities) = %d, want 2 (AC-102)", len(activities))
	}
	wantAfter := map[string]string{"C-1": `{"title":"legacy"}`, "C-2": `{"title":"legacy-2"}`}
	wantActor := map[string]string{"C-1": "alice", "C-2": "bob"}
	for _, act := range activities {
		if act.Entity != "challenge" || act.Action != "create" || act.Before != nil ||
			string(act.After) != wantAfter[act.EntityID] || !act.At.Equal(fixtureTime) ||
			act.Actor != wantActor[act.EntityID] || act.Channel != "cli" || act.Verification != "none" {
			t.Errorf("activity = %+v, want create of %s by %s/cli/none with after %s (AC-102)",
				act, act.EntityID, wantActor[act.EntityID], wantAfter[act.EntityID])
		}
	}

	for _, id := range []string{"C-1", "C-2"} {
		sb, err := s.getSourceBindingByChallengeID(ctx, id)
		if err != nil || sb != nil {
			t.Fatalf("getSourceBindingByChallengeID(%s) = %+v, %v, want nil, nil（既存の課題に対応は無い）", id, sb, err)
		}
	}
}

// --- テスト用ヘルパー ---

func mustCreateChallenge(t *testing.T, s *Store, title string) *Challenge {
	t.Helper()
	fixedActor(t, "alice")
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: title})
	if err != nil {
		t.Fatalf("CreateChallenge(%q) error = %v", title, err)
	}
	return ch
}

func validBindingInput(externalKey string) createSourceBindingInput {
	return createSourceBindingInput{
		SourceID: "src", ExternalKey: externalKey, URL: "https://example.invalid/" + externalKey,
		Fingerprint: "2:abc", UpstreamState: upstreamStateOpen, PolicyState: policyStateInPolicy,
	}
}

// createSourceBindingForTest は insertSourceBinding を 1 つの書き込み
// トランザクションで呼ぶ（本番の呼び出し側〔#56〜#58〕は mutate の中で呼ぶ）。
func createSourceBindingForTest(s *Store, id string, at time.Time, in createSourceBindingInput) (*sourceBinding, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var result *sourceBinding
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		sb, err := insertSourceBinding(context.Background(), tx, at, cid, in)
		result = sb
		return err
	})
	return result, classifyReadWriteErr(err)
}

// updateSourceBindingForTest は applySourceBindingUpdate を 1 つの書き込み
// トランザクションで呼ぶ。
func updateSourceBindingForTest(s *Store, id string, at time.Time, in updateSourceBindingInput) (before, after *sourceBinding, err error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, nil, ErrNotFound
	}
	err = s.db.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		before, after, err = applySourceBindingUpdate(context.Background(), tx, at, cid, in)
		return err
	})
	return before, after, classifyReadWriteErr(err)
}

// v1OnlyMigrationsForTest は store.Migrations()（0002 を足した後は版 1・2 を返す）
// から版 1 だけを取り出す。「版 1 のストア」のフィクスチャを作るためのテスト専用
// ヘルパー（internal/core/internal/store の v1OnlyMigrations と同じ考え方）。
func v1OnlyMigrationsForTest(t *testing.T) []store.Migration {
	t.Helper()
	all, err := store.Migrations()
	if err != nil {
		t.Fatalf("store.Migrations(): %v", err)
	}
	var v1 []store.Migration
	for _, m := range all {
		if m.Version == 1 {
			v1 = append(v1, m)
		}
	}
	if len(v1) != 1 {
		t.Fatalf("v1OnlyMigrationsForTest: found %d migrations with Version == 1, want 1", len(v1))
	}
	return v1
}

// schemaVersionForTest は s が開いているストアの PRAGMA user_version を返す。
func schemaVersionForTest(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("PRAGMA user_version").Scan(&v)
	}); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

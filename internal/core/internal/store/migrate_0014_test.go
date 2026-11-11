package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 13 のストアを開くと 0014 が当たり、既存の保留の行は種類が既定値（human＝人の対応を待つ）で
// 埋まり、理由・回答・回答の記録の値は変わらない。新しい表は空で、版は 14 になる。
func TestOpen_AppliesHoldKindMigrationToAVersion13Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")
	var upTo13 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 13 {
			upTo13 = append(upTo13, m)
		}
	}
	old, err := Open(path, upTo13)
	if err != nil {
		t.Fatalf("Open(v13) error = %v", err)
	}
	if got := userVersion(t, old); got != 13 {
		t.Fatalf("user_version = %d, want 13", got)
	}
	insertChallenge(t, old, "a")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by, answered_channel, answered_verification, relay_session, relay_quote)
			VALUES (1, '答えてください', 'classified', '2026-11-01T00:00:00.000Z', '了解', '2026-11-01T01:00:00.000Z', 'owner', 'cli', 'relayed', 'sess-1', '了解と言った')`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (1, '未回答の問い', 'classified', '2026-11-02T00:00:00.000Z')`)
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
	if got := userVersion(t, db); got != 19 {
		t.Fatalf("user_version = %d, want 19", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		type row struct {
			id                                                int64
			kind, question, from, answer, answeredBy, channel string
			verification, session, quote                      sql.NullString
		}
		rows, err := tx.Query(`SELECT id, kind, question, from_status, COALESCE(answer, ''), COALESCE(answered_by, ''), COALESCE(answered_channel, ''),
			answered_verification, relay_session, relay_quote FROM hold ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		var got []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.kind, &r.question, &r.from, &r.answer, &r.answeredBy, &r.channel, &r.verification, &r.session, &r.quote); err != nil {
				return err
			}
			got = append(got, r)
		}
		if len(got) != 2 {
			t.Fatalf("hold rows = %d, want 2", len(got))
		}
		if got[0].kind != "human" || got[0].question != "答えてください" || got[0].from != "classified" || got[0].answer != "了解" ||
			got[0].answeredBy != "owner" || got[0].channel != "cli" || got[0].verification.String != "relayed" ||
			got[0].session.String != "sess-1" || got[0].quote.String != "了解と言った" {
			t.Errorf("answered hold changed by migration: %+v", got[0])
		}
		if got[1].kind != "human" || got[1].question != "未回答の問い" || got[1].answer != "" {
			t.Errorf("unanswered hold changed by migration: %+v", got[1])
		}
		var dc, art int
		if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM direct_completion), (SELECT COUNT(*) FROM direct_completion_artifact)`).Scan(&dc, &art); err != nil {
			return err
		}
		if dc != 0 || art != 0 {
			t.Errorf("new tables not empty: direct_completion %d, direct_completion_artifact %d", dc, art)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
}

// 新しい保留の行は、種類を指定しなければ human になる（既存の挿入の経路が種類を知らなくても壊れない）。
func TestHold_KindDefaultsToHuman(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "flywheel.db"), prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	insertChallenge(t, db, "a")
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (1, 'q', 'classified', '2026-11-01T00:00:00.000Z')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var kind string
		if err := tx.QueryRow(`SELECT kind FROM hold WHERE id = 1`).Scan(&kind); err != nil {
			return err
		}
		if kind != "human" {
			t.Errorf("hold.kind = %q, want human", kind)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// 成果物の行は直接行った完了の記録（と、その課題・保留）の外部キーを守り、1 つの記録の中の位置は重ならない。
func TestDirectCompletion_ForeignKeysAndArtifactPositions(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "flywheel.db"), prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	insertChallenge(t, db, "a")
	const at = "'2026-11-01T00:00:00.000Z'"
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, kind) VALUES (1, 'q', 'classified', ` + at + `, 'parent_direct')`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO direct_completion (challenge_id, hold_id, note, created_at) VALUES (1, 1, NULL, ` + at + `)`); err != nil {
			t.Errorf("insert direct_completion: %v", err)
		}
		if _, err := tx.Exec(`INSERT INTO direct_completion_artifact (direct_completion_id, position, ref) VALUES (1, 1, 'https://example.com/pull/1')`); err != nil {
			t.Errorf("insert artifact: %v", err)
		}
		if _, err := tx.Exec(`INSERT INTO direct_completion_artifact (direct_completion_id, position, ref) VALUES (1, 1, 'dup')`); err == nil {
			t.Error("duplicate (direct_completion_id, position) inserted, want a unique violation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO direct_completion_artifact (direct_completion_id, position, ref) VALUES (99, 1, 'x')`); err == nil {
			t.Error("artifact for a missing direct_completion inserted, want a foreign key violation")
		}
		if _, err := tx.Exec(`INSERT INTO direct_completion (challenge_id, hold_id, note, created_at) VALUES (99, 1, NULL, ` + at + `)`); err == nil {
			t.Error("direct_completion for a missing challenge inserted, want a foreign key violation")
		}
		if _, err := tx.Exec(`INSERT INTO direct_completion (challenge_id, hold_id, note, created_at) VALUES (1, 99, NULL, ` + at + `)`); err == nil {
			t.Error("direct_completion for a missing hold inserted, want a foreign key violation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

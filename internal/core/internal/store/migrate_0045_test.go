package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// 要求の記録と区切りを分ける記録の表（docs/features/request-to-plan-s3.md「DB スキーマ: ストアの版 45」）のスキーマ。
const migrationRequirementVersion = 45

func TestMigrations_0045_Requirement_AC157_AC158_AC159_AC160(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "flywheel.db")
	old, err := Open(path, migrationsUpTo(t, migrationRequirementVersion-1))
	if err != nil {
		t.Fatal(err)
	}
	insertChallenge(t, old, "a")
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path, migrationsUpTo(t, migrationRequirementVersion))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if got := userVersion(t, db); got != migrationRequirementVersion {
		t.Errorf("user_version = %d, want %d", got, migrationRequirementVersion)
	}

	// 列の名前 → NOT NULL か（主キーの列は notNull の写像に入れず、件数で数える）。
	for table, want := range map[string]map[string]bool{
		"requirement": {"origin": true, "title": true, "external_key": false, "url": false, "chat_session_id": false,
			"origin_challenge_id": false, "evidence_path": true, "created_at": true, "resolved_at": false},
		"slice_split": {"state": true, "classify_reason": true, "proposal": false, "evidence_path": false,
			"requirement_id": false, "hold_id": false, "created_at": true, "updated_at": true},
	} {
		cols := tableColumnInfo(t, db, table)
		for name, notNull := range want {
			if nn, ok := cols[name]; !ok || nn != notNull {
				t.Errorf("%s.%s: present=%v notnull=%v, want notnull=%v", table, name, ok, nn, notNull)
			}
		}
		if len(cols) != len(want)+1 {
			t.Errorf("%s columns = %v, want the primary key and %v", table, cols, want)
		}
	}

	var issueTables int
	var requirementID sql.NullInt64
	if err := db.Read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'challenge_issue'`).Scan(&issueTables); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT requirement_id FROM challenge WHERE id = 1`).Scan(&requirementID)
	}); err != nil {
		t.Fatal(err)
	}
	if issueTables != 0 {
		t.Error("challenge_issue exists at version 45")
	}
	if requirementID.Valid {
		t.Errorf("requirement_id of an existing challenge = %d, want NULL", requirementID.Int64)
	}

	exec := func(q string) error {
		return db.Write(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(q); return err })
	}
	split := `INSERT INTO slice_split (challenge_id, state, classify_reason, created_at, updated_at) VALUES (%CID%, 'pending', 'why', 't', 't')`
	if err := exec(replaceCID(split, "1")); err != nil {
		t.Fatal(err)
	}
	if err := exec(replaceCID(split, "1")); err == nil {
		t.Error("a second slice_split of the same challenge was accepted")
	}
	if err := exec(replaceCID(split, "99")); err == nil {
		t.Error("a slice_split of a missing challenge was accepted")
	}
	if err := exec(`UPDATE challenge SET requirement_id = 99 WHERE id = 1`); err == nil {
		t.Error("a reference to a missing requirement was accepted")
	}
}

func replaceCID(q, cid string) string { return strings.Replace(q, "%CID%", cid, 1) }

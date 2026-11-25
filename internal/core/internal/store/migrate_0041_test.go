package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// 対話の欄の作り替え（docs/features/chat-session.md「DB スキーマ」）のスキーマ（版 41）。
// chat_session.engine_cost_total・chat_message.charged_challenge_id・表 chat_prompt・chat_link。

const migrationChatSessionVersion = 41

func TestChatSessionMigration(t *testing.T) {
	const ts = "'2026-01-01T00:00:00Z'"
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "flywheel.db")
	old, err := Open(path, migrationsUpTo(t, migrationChatSessionVersion-1))
	if err != nil {
		t.Fatal(err)
	}
	insertChallenge(t, old, "a")
	if err := old.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO chat_session (engine_session, engine_started, started_at) VALUES ('u', 1, ` + ts + `)`,
			`INSERT INTO chat_message (session_id, role, body, state, cost_usd, activity, created_at) VALUES (1, 'orchestrator', 'b', 'done', 5, '[{"command":"ls","ok":true}]', ` + ts + `)`,
			`INSERT INTO chat_message (session_id, role, body, state, cost_usd, created_at) VALUES (1, 'orchestrator', 'c', 'done', 9, ` + ts + `)`,
			// 費用の無い返事（止めた返事）は、最後に記録した累計を決めない。
			`INSERT INTO chat_message (session_id, role, body, state, cost_usd, created_at) VALUES (1, 'orchestrator', 'd', 'stopped', 0, ` + ts + `)`,
			// 費用を記録していない会話は 0 のまま。
			`INSERT INTO chat_session (engine_session, engine_started, started_at, closed_at) VALUES ('w', 0, ` + ts + `, ` + ts + `)`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
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

	if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationChatSessionVersion {
		t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationChatSessionVersion)
	}
	if nn, ok := tableColumnInfo(t, db, "chat_session")["engine_cost_total"]; !ok || !nn {
		t.Errorf("chat_session.engine_cost_total: present=%v notnull=%v; want present and NOT NULL", ok, nn)
	}
	if nn, ok := tableColumnInfo(t, db, "chat_message")["charged_challenge_id"]; !ok || nn {
		t.Errorf("chat_message.charged_challenge_id: present=%v notnull=%v; want present and nullable", ok, nn)
	}
	wantColumns := map[string]map[string]bool{ // 列名 → NOT NULL か（id は主キー）
		"chat_prompt": {"id": false, "message_id": true, "request_id": true, "kind": true, "tool": true, "target": true,
			"questions": false, "state": true, "answer": false, "created_at": true, "answered_at": false},
		"chat_link": {"id": false, "session_id": true, "challenge_id": true, "action": true, "message_id": true, "created_at": true},
	}
	for table, want := range wantColumns {
		if got := tableColumnInfo(t, db, table); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s columns = %v, want %v", table, got, want)
		}
	}

	var row string
	if err := db.Read(ctx, func(tx *sql.Tx) error {
		var total, cost int64
		var charged sql.NullInt64
		var activity string
		if err := tx.QueryRow(`SELECT s.engine_cost_total, m.cost_usd, m.charged_challenge_id, m.activity FROM chat_message m JOIN chat_session s ON s.id = m.session_id WHERE m.id = 1`).
			Scan(&total, &cost, &charged, &activity); err != nil {
			return err
		}
		row = fmt.Sprintf("%d %d %v %s", total, cost, charged.Valid, activity)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if want := `9 5 false [{"command":"ls","ok":true}]`; row != want {
		t.Errorf("existing rows = %q, want %q (the last recorded total, the cost unchanged, NULL, and activity unchanged)", row, want)
	}
	var unpaid int
	if err := db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT engine_cost_total FROM chat_session WHERE engine_session = 'w'`).Scan(&unpaid)
	}); err != nil || unpaid != 0 {
		t.Errorf("a session without a recorded cost: total = %d, %v; want 0", unpaid, err)
	}

	// 紐づけは会話と課題の組ごとに 1 つ。開いている会話は高々 1 つの索引は残る。
	link := `INSERT INTO chat_link (session_id, challenge_id, action, message_id, created_at) VALUES (1, 1, 'create', 1, ` + ts + `)`
	if err := db.Write(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(link); return err }); err != nil {
		t.Fatalf("first link: %v", err)
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(link); return err }); err == nil {
		t.Error("a second link of the same session and challenge was accepted")
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO chat_session (engine_session, engine_started, started_at) VALUES ('v', 0, ` + ts + `)`)
		return err
	}); err == nil {
		t.Error("a second open session was accepted")
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// 対話の欄のコマンドの側（docs/features/chat-session.md）の E2E。ビルドしたバイナリを、欄の実行エンジンが
// 起こすのと同じ形（印の環境変数を付けた子プロセス）で起動し、1 つの流れを通して確かめる。サーバが行う会話の
// 記録（発言・問い・返事の終わり）は core の公開 API で行う（実物の Claude Code は起動しない）。

type chatE2E struct {
	t       *testing.T
	bin, ws string
}

// run は args を実行する。mark が空でなければ印の環境変数を付け、空なら外す（ターミナル相当）。
func (e chatE2E) run(mark string, args ...string) (stdout, stderr string, err error) {
	cmd := newChildCmd(e.bin, append(args, "--workspace", e.ws)...)
	cmd.Env = withoutEnv(cmd.Env, core.ChatEnvSession)
	if mark != "" {
		cmd.Env = append(cmd.Env, core.ChatEnvSession+"="+mark)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

func (e chatE2E) ok(mark string, args ...string) string {
	e.t.Helper()
	out, stderr, err := e.run(mark, args...)
	if err != nil {
		e.t.Fatalf("%v: %v: %s", args, err, stderr)
	}
	return out
}

func (e chatE2E) refused(mark string, args ...string) {
	e.t.Helper()
	_, stderr, err := e.run(mark, append(args, "--json")...)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || !strings.Contains(stderr, "verification_rejected") {
		e.t.Fatalf("%v: err=%v stderr=%s, want verification_rejected", args, err, stderr)
	}
}

func (e chatE2E) json(args ...string) map[string]any {
	e.t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(e.ok("", append(args, "--json")...)), &v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

func (e chatE2E) challenge(id string) map[string]any {
	e.t.Helper()
	c, ok := e.json("show", id)["challenge"].(map[string]any)
	if !ok {
		e.t.Fatalf("show %s has no challenge", id)
	}
	return c
}

func (e chatE2E) planFile() string {
	e.t.Helper()
	path := filepath.Join(e.t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte("計画の本文"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return path
}

// store はサーバの役の操作を 1 つ行う。
func (e chatE2E) store(fn func(s *core.Store) error) {
	e.t.Helper()
	s, err := core.OpenWorkspace(e.ws)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := fn(s); err != nil {
		e.t.Fatal(err)
	}
}

func (e chatE2E) post(text string) (turn *core.ChatTurn) {
	e.t.Helper()
	e.store(func(s *core.Store) (err error) {
		turn, err = s.PostChatMessage(context.Background(), text, nil)
		return err
	})
	return turn
}

func (e chatE2E) finish(turn *core.ChatTurn, totalUSD float64) {
	e.t.Helper()
	e.store(func(s *core.Store) error {
		return s.FinishChatReplyWithTotal(context.Background(), turn.ReplyMessageID, core.ChatStateDone, &totalUSD)
	})
}

func (e chatE2E) count(table string) int64 {
	e.t.Helper()
	return coretest.QueryRows(e.t, e.ws, "SELECT COUNT(*) FROM "+table)[0][0].(int64)
}

func newChatE2E(t *testing.T) chatE2E {
	t.Helper()
	return chatE2E{t: t, bin: buildBinary(t), ws: t.TempDir()}
}

func TestBinary_ChatFlow_InitLinkQuoteAndSpecCost(t *testing.T) {
	e := newChatE2E(t)

	// init は設定のファイルを作り、2 回目は触らない。
	var first map[string]any
	if err := json.Unmarshal([]byte(e.ok("", "init", "--json")), &first); err != nil {
		t.Fatal(err)
	}
	if first["claude_settings_created"] != true {
		t.Fatalf("first init = %v, want claude_settings_created true", first)
	}
	settings := filepath.Join(e.ws, ".claude", "settings.json")
	if data, err := os.ReadFile(settings); err != nil || !strings.Contains(string(data), `"Bash(flywheel:*)"`) {
		t.Fatalf("settings = %s, %v", data, err)
	}
	if err := os.WriteFile(settings, []byte("not json {"), 0o644); err != nil {
		t.Fatal(err)
	}
	if again := e.json("init"); again["claude_settings_created"] != false {
		t.Fatalf("second init = %v, want claude_settings_created false", again)
	}
	if data, _ := os.ReadFile(settings); string(data) != "not json {" {
		t.Fatalf("settings was changed: %s", data)
	}

	// 1 回目の返事は費用だけ。2 回目の返事の中で課題を作ると紐づく。
	t1 := e.post("相談です")
	e.finish(t1, 0.25)
	t2 := e.post("課題にしてください")
	mark := t2.EngineSession
	e.ok(mark, "create", "--title", "欄から", "--done-criteria", "ok")
	e.finish(t2, 0.75)
	if c := e.challenge("C-1"); c["spec_cost_usd"] != 0.75 {
		t.Fatalf("spec_cost_usd = %v, want 0.75", c["spec_cost_usd"])
	}

	// 紐づいた後の返事は、次に紐づくまで数えない。
	t3 := e.post("雑談")
	e.finish(t3, 1.0)
	if c := e.challenge("C-1"); c["spec_cost_usd"] != 0.75 {
		t.Fatalf("spec_cost_usd after an unlinked reply = %v, want 0.75", c["spec_cost_usd"])
	}

	// 問いに答えた返事の中の承認。記録の「発言」に問いと答えがつながる。見直し前の一覧に無いコマンドも通る。
	t4 := e.post("計画を確かめたい")
	e.ok(mark, "classify", "C-1", "--priority", "P1")
	e.ok(mark, "plan", "C-1", "--file", e.planFile())
	e.store(func(s *core.Store) error {
		ctx := context.Background()
		id, err := s.RecordChatPrompt(ctx, t4.ReplyMessageID, core.ChatPromptRequest{
			RequestID: "r1", Kind: core.ChatPromptQuestion, Tool: "AskUserQuestion", Target: "q",
			Questions: []core.ChatQuestion{{Question: "承認しますか", Options: []core.ChatQuestionOption{{Label: "承認する"}}}},
		})
		if err != nil {
			return err
		}
		_, err = s.AnswerChatPrompt(ctx, id, core.ChatPromptAnswer{Answers: map[string]string{"承認しますか": "承認する"}})
		return err
	})
	e.ok(mark, "approve", "C-1", "--no-cycle")
	e.finish(t4, 1.5)

	rows := coretest.QueryRows(t, e.ws, `SELECT channel, verification, relay_session, relay_quote FROM approval`)
	want := "計画を確かめたい\n\n問い: 承認しますか\n答え: 承認する"
	if len(rows) != 1 || rows[0][0] != "chat" || rows[0][1] != "relayed" || rows[0][2] != mark || rows[0][3] != want {
		t.Fatalf("approval = %v, want chat/relayed/%s/%q", rows, mark, want)
	}
	c := e.challenge("C-1")
	if c["spec_cost_usd"] != 1.5 {
		t.Fatalf("spec_cost_usd after the second operation = %v, want 1.5", c["spec_cost_usd"])
	}
	sessions, _ := c["chat_sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("chat_sessions = %v, want 1", c["chat_sessions"])
	}
	if s := sessions[0].(map[string]any); s["id"] != mark || s["cost_usd"] != 1.5 || s["first_message"] != "相談です" {
		t.Fatalf("chat session = %v", s)
	}
	if links := coretest.QueryRows(t, e.ws, `SELECT action FROM chat_link`); len(links) != 1 || links[0][0] != "create" {
		t.Fatalf("chat_link = %v, want one create", links)
	}
	if text := e.ok("", "show", "C-1"); !strings.Contains(text, "仕様づくりの費用: 1.5 USD") {
		t.Fatalf("show text lacks the spec cost: %s", text)
	}
}

func TestBinary_ChatMarkRefusalsWriteNothing(t *testing.T) {
	e := newChatE2E(t)
	e.ok("", "init")
	e.ok("", "create", "--title", "t", "--done-criteria", "ok")
	e.ok("", "classify", "C-1", "--priority", "P1")
	e.ok("", "plan", "C-1", "--file", e.planFile())
	e.ok("", "op", "add", "C-1", "--kind", "external_send", "--summary", "notify")

	// 宣言に無い種類の不可逆操作の承認は、返事が進行中でも断る。
	turn := e.post("送ってください")
	mark := turn.EngineSession
	e.refused(mark, "approve", "OP-1")

	// 返事が進行中でない・会話が閉じている・無い会話の印は、承認も作成も断る。
	e.finish(turn, 0.5)
	e.refused(mark, "approve", "C-1", "--no-cycle")
	e.refused(mark, "create", "--title", "x", "--done-criteria", "ok")
	e.store(func(s *core.Store) error { return s.CloseChat(context.Background()) })
	e.refused(mark, "approve", "C-1", "--no-cycle")
	e.refused("00000000-0000-4000-8000-000000000000", "approve", "C-1", "--no-cycle")

	if n := e.count("approval"); n != 0 {
		t.Errorf("approval rows = %d, want 0", n)
	}
	if n := e.count("challenge"); n != 1 {
		t.Errorf("challenges = %d, want 1", n)
	}
	if n := e.count("chat_link"); n != 0 {
		t.Errorf("chat_link rows = %d, want 0", n)
	}
	if c := e.challenge("C-1"); c["status"] != "awaiting_plan_approval" || c["spec_cost_usd"] != 0.0 {
		t.Errorf("status/spec_cost = %v/%v", c["status"], c["spec_cost_usd"])
	}

	// 印の無い --relayed の承認は成立し、紐づけない。
	e.ok("", "approve", "C-1", "--no-cycle", "--relayed", "--session", "s", "--quote", "q")
	if n := e.count("chat_link"); n != 0 {
		t.Errorf("chat_link rows after a terminal --relayed approval = %d, want 0", n)
	}
}

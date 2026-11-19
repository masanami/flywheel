package cli

import (
	"strings"
	"testing"
)

// このファイルは、区切り S2c の文書の受入基準（#814。docs/features/core-redesign.md の「文書」の節）を、基準ごとに文書の本文で確かめる。
// AC-461（make gate が終了コード 0）は、テストで確かめず、make gate の実行で確かめる。

// docHead は文書の先頭の n 行を返す（冒頭の注記を見る）。
func docHead(t *testing.T, n int, rel ...string) string {
	t.Helper()
	lines := strings.Split(readRepoDoc(t, rel...), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// apiRows は m4-ui-server.md の、`| ` で始まる表の行のうち prefix で始まるものを返す。
func apiRows(t *testing.T, prefix string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(readRepoDoc(t, "docs", "features", "m4-ui-server.md"), "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// AC-446 #814: m4-ui-server.md の冒頭に、S2c で画面を作り替えた旨の注記と core-redesign.md への参照がある。
func TestSpec_AC446_M4HeadNoteSaysS2cRebuiltUI(t *testing.T) {
	head := docHead(t, 6, "docs", "features", "m4-ui-server.md")
	if !strings.Contains(head, "S2c") || !strings.Contains(head, "作り替えた") {
		t.Error("AC-446: the head of m4-ui-server.md does not say that S2c rebuilt the screens")
	}
	if !strings.Contains(head, "core-redesign.md") {
		t.Error("AC-446: the head of m4-ui-server.md does not refer to core-redesign.md")
	}
}

// AC-447 #814: m4-ui-server.md の API の表に、課題のツリーの API の行がある。
func TestSpec_AC447_APITableHasTreeRoute(t *testing.T) {
	if len(apiRows(t, "| `GET /api/v1/workspaces/{name}/tree`")) != 1 {
		t.Error("AC-447: the API table must have exactly one row for GET /api/v1/workspaces/{name}/tree")
	}
}

// AC-448 #814: m4-ui-server.md の API の表に、グラフの API の行は無い。
func TestSpec_AC448_APITableHasNoGraphRoute(t *testing.T) {
	if rows := apiRows(t, "| `GET /api/v1/workspaces/{name}/graph"); len(rows) != 0 {
		t.Errorf("AC-448: the API table still has a row for the graph API: %q", rows)
	}
}

// AC-449 #814: m4-ui-server.md に、課題の詳細の API の tree の説明がある。
func TestSpec_AC449_M4DescribesDetailTree(t *testing.T) {
	rows := apiRows(t, "| `GET /api/v1/workspaces/{name}/challenges/{id}` |")
	if len(rows) != 1 || !strings.Contains(rows[0], "`tree`") {
		t.Errorf("AC-449: the challenge detail row does not describe `tree`: %q", rows)
	}
}

// AC-450 #814: m4-ui-server.md に、承認の要約の API の summary と budget_usd の説明がある。
func TestSpec_AC450_M4DescribesApprovalSummaryAndBudget(t *testing.T) {
	rows := apiRows(t, "| `GET /api/v1/workspaces/{name}/challenges/{id}/approval`")
	if len(rows) != 1 {
		t.Fatalf("AC-450: want one approval row, got %d", len(rows))
	}
	for _, key := range []string{"`summary`", "`budget_usd`"} {
		if !strings.Contains(rows[0], key) {
			t.Errorf("AC-450: the approval row does not describe %s", key)
		}
	}
}

// AC-451 #814: m4-ui-server.md に、問いの一覧の API の task_id の説明がある。
func TestSpec_AC451_M4DescribesQuestionsTaskID(t *testing.T) {
	rows := apiRows(t, "| `GET /api/v1/questions`")
	if len(rows) != 1 || !strings.Contains(rows[0], "`task_id`") {
		t.Errorf("AC-451: the questions row does not describe `task_id`: %q", rows)
	}
}

// AC-452 #814: ui-relation-graph.md の冒頭に、S2c でグラフの画面と API を外した旨の注記がある。
func TestSpec_AC452_RelationGraphHeadNote(t *testing.T) {
	head := docHead(t, 4, "docs", "features", "ui-relation-graph.md")
	if !strings.Contains(head, "S2c") || !strings.Contains(head, "外した") {
		t.Error("AC-452: the head of ui-relation-graph.md does not say that S2c removed the graph screen and API")
	}
}

// AC-453 #814: architecture.md の §12 に、S2c で画面を作り替えた旨の注記がある。
func TestSpec_AC453_ArchitectureSection12Note(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), "## 12. UI")
	if idx := strings.Index(body, "### 12.1"); idx >= 0 {
		body = body[:idx]
	}
	if !strings.Contains(body, "S2c") || !strings.Contains(body, "作り替えた") {
		t.Error("AC-453: §12 does not say that S2c rebuilt the screens")
	}
}

// AC-454 #814: architecture.md の §21 の冒頭の注記に、区切り S2c で実装した範囲がある。
func TestSpec_AC454_ArchitectureSection21NoteHasS2c(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), "## 21. 設計の見直し（2026-11-17）: 新しい形")
	first := strings.SplitN(strings.TrimLeft(body, "\n"), "\n", 2)[0]
	if !strings.Contains(first, "区切り S2c") || !strings.Contains(first, "S2c で実装した範囲は") {
		t.Error("AC-454: the opening note of §21 does not list the range implemented in S2c")
	}
}

// AC-455 #814: 用語集の「エージェント（画面での呼び方）」の行の「いつから」に、S2c で入った旨がある。
func TestSpec_AC455_GlossaryAgentRowSinceS2c(t *testing.T) {
	cols := glossaryRow(t, "エージェント（画面での呼び方）")
	if !strings.Contains(cols[4], "S2c") || strings.Contains(cols[4], "予定") {
		t.Errorf("AC-455: since = %q, want it to say it came in with S2c", cols[4])
	}
}

// AC-456 #814: 用語集の「問い」の行に、計画の承認の画面で論点ごとにまとめて見せる形が S2c で入った旨がある。
func TestSpec_AC456_GlossaryQuestionRowGroupedInS2c(t *testing.T) {
	cols := glossaryRow(t, "問い")
	if !strings.Contains(cols[1], "論点ごとにまとめて見せる形は、S2c で入った") {
		t.Error("AC-456: the 問い row does not say the grouped view in the plan approval screen came in with S2c")
	}
	if strings.Contains(cols[1], "予定〔画面は S2c〕") {
		t.Error("AC-456: the 問い row still says the screen is planned")
	}
}

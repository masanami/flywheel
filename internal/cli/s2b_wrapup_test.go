package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは、区切り S2b の全体の確かめ（flywheel#770）で足した確かめを持つ。受入基準の言い回しまで届いていなかった
// 所（CLI のコマンドを通した確かめ）と、全体のレビューで見つかった点の直しを固定する。

// 進められるものが 0 件のワークスペースでは、gh・claude が見つからなくても、cycle は「始めなかった」で終わる
// （終了コード 0。数えは道具を使わない。道具が要るのは、サイクルを始めるときだけ）。
func TestCycle_NothingToAdvance_DoesNotNeedTheTools(t *testing.T) {
	parallelTools(t, false)
	ws := setupWorkspaceWithPosition(t)
	// 取り込み元の宣言があると、始めるときは gh が要る。0 件なら、gh を探す前に終わる。
	writeSourcesDeclaration(t, ws, `{"version":1,"sources":[{"id":"s","type":"github-issue","repos":["o/r"],"self_assignees":["someone"]}]}`)
	restrictToolDirs(t, 0) // gh も claude も見つからない

	doc := cycleDocAllowNotStarted(t, ws)
	if doc["started"] != false || doc["not_started_reason"] != "nothing_to_advance" || doc["cycle"] != nil {
		t.Fatalf("doc = %v, want started=false, not_started_reason=nothing_to_advance, cycle=null", doc)
	}
	// 進められるものがあれば、道具が無いことは、前と同じくエラーになる。
	createForCase(t, ws)
	code, _, stderr := runCLIRaw(t, "cycle", "--workspace", ws, "--json")
	if code == 0 || !strings.Contains(stderr, "_unavailable") {
		t.Errorf("cycle with work but without the tools: exit=%d stderr=%s, want an *_unavailable error", code, stderr)
	}
}

// AC-9・AC-10: followup・source_child に行があるストアを `flywheel export` で書き出し、その書き出しから
// `flywheel import` で再構築すると、その行が再構築したストアにある。
func TestExportImport_AC9_AC10_FollowupAndSourceChildRowsSurviveTheCLIRoundTrip(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	if _, err := core.Init(src); err != nil {
		t.Fatal(err)
	}
	coretest.FillAllTables(t, src)
	coretest.InsertFollowup(t, src, coretest.FollowupRow{Title: "課題に属さない", Detail: "説明", Status: "done", Note: "確かめた"})
	rows := func(ws, table string) string {
		return fmt.Sprint(coretest.QueryRows(t, ws, "SELECT * FROM "+table+" ORDER BY id"))
	}
	wantFollowups, wantChildren := rows(src, "followup"), rows(src, "source_child")
	if n := len(coretest.QueryRows(t, src, "SELECT id FROM followup")); n < 2 {
		t.Fatalf("setup: followup rows = %d, want >= 2", n)
	}
	if n := len(coretest.QueryRows(t, src, "SELECT id FROM source_child")); n < 1 {
		t.Fatalf("setup: source_child rows = %d, want >= 1", n)
	}

	path := filepath.Join(t.TempDir(), "export.json")
	runJSON(t, src, "export", "--out", path)
	dst := t.TempDir()
	if _, err := core.Init(dst); err != nil {
		t.Fatal(err)
	}
	runJSON(t, dst, "import", "--in", path)

	if got := rows(dst, "followup"); got != wantFollowups { // AC-9
		t.Errorf("AC-9: followup rows differ after the round trip:\n%s\n---\n%s", wantFollowups, got)
	}
	if got := rows(dst, "source_child"); got != wantChildren { // AC-10
		t.Errorf("AC-10: source_child rows differ after the round trip:\n%s\n---\n%s", wantChildren, got)
	}
	// 再構築したストアでも、フォローアップの一覧が同じに読める。
	want, _ := runOK(t, "followup", "list", "--status", "all", "--workspace", src, "--json")
	got, _ := runOK(t, "followup", "list", "--status", "all", "--workspace", dst, "--json")
	if got != want {
		t.Errorf("followup list differs after the round trip:\n%s\n---\n%s", want, got)
	}
}

// AC-99・AC-100・AC-101: 上げた先の課題の完了を `flywheel approve` で承認して、課題が done になると（遷移 T13）、
// フォローアップが済みになり、確かめた結果に課題の ID が入り、followup_done の記録が 1 件増える。
func TestApprove_AC99_AC100_AC101_CompletingThePromotedChallengeCompletesTheFollowup(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	origin := createForCase(t, ws)
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: challengeIDToInternalID(t, origin), Title: "上げるもの"})
	promoted := runJSON(t, ws, "followup", "promote", "F-1", "--relayed", "--session", "s", "--quote", "q")
	cid := promoted["challenge"].(map[string]any)["id"].(string)
	// 上げた先の課題を、完了の承認待ちまで進める（検証の結果を「満たした」で記録する）。
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, cid), "verifying")
	runJSON(t, ws, "verify", cid, "--result", "met")
	doneActs := func() int {
		return len(coretest.QueryRows(t, ws, "SELECT id FROM activity WHERE entity = 'followup' AND action = 'followup_done'"))
	}
	before := doneActs()

	code, _, stderr := runCLIRaw(t, relayedArgs(ws, "approve", cid)...)
	if code != 0 {
		t.Fatalf("approve: exit=%d stderr=%s", code, stderr)
	}
	if got := runJSON(t, ws, "show", cid)["challenge"].(map[string]any)["status"]; got != "done" {
		t.Fatalf("challenge status = %v, want done (T13)", got)
	}
	var f map[string]any
	for _, e := range runJSON(t, ws, "followup", "list", "--status", "all")["followups"].([]any) {
		if m := e.(map[string]any); m["id"] == "F-1" {
			f = m
		}
	}
	if f == nil || f["status"] != "done" { // AC-99
		t.Fatalf("AC-99: followup = %v, want status done", f)
	}
	if note, _ := f["note"].(string); !strings.Contains(note, cid) { // AC-100
		t.Errorf("AC-100: note = %q, want it to carry %s", note, cid)
	}
	if got := doneActs(); got != before+1 { // AC-101
		t.Errorf("AC-101: followup_done activities %d → %d, want +1", before, got)
	}
}

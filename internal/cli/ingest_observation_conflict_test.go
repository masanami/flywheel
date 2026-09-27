package cli

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #72（親要件チケット #51 §作業ログと版 「観測値の変化で
// 版を上げることは QH12 で決めた」）の受入基準「計画の承認の要約を表示した後、
// 確認の入力の前に、ingest がその課題の観測値だけを置き換えると、確認を
// 入力しても終了コード 1・conflict で終わる」を検証する。
// ingest_conflict_process_test.go（AC-93。人間記入欄の変化）と同じ手法を使い、
// 「変える」内容を観測値だけ（本文は変えない）に差し替える。

func TestApprove_ConflictWhenIngestUpdatesObservationOnlyAfterSummaryShown(t *testing.T) {
	ws := initializedWorkspace(t)
	const repo = "owner/confirm-window-observation-repo"
	const title = "distinctive-observation-conflict-title"
	const body = "distinctive-observation-conflict-body"

	created := runJSON(t, ws, "create", "--title", title)
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.InsertSourceBinding(t, ws, challengeIDToInternalID(t, id),
		"confirm-window-observation-source", repo+"#1", "https://example.invalid/"+repo+"#1",
		core.Fingerprint(body), "open", "in_policy", "2026-09-26T00:00:00.000Z")
	runJSON(t, ws, "classify", id, "--priority", "P0")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "do the thing"))

	decl := `{
  "version": 1,
  "sources": [
    {"id": "confirm-window-observation-source", "type": "github-issue", "repos": ["` + repo + `"], "self_assignees": ["someone"]}
  ]
}`
	writeSourcesDeclaration(t, ws, decl)

	cmd, stdout, stderr := newChildCmd([]string{"approve", "--workspace", ws, "--json", id})
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(drainSnapshot(drain), id) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the summary to appear on the pty")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 別プロセス相当: ingest が本文は変えず、観測値（コメント数）だけを置き換える。
	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute(repo, 1, fakeGHIssueListBody(t, []fakeGHIssue{
			{Number: 1, Title: title, Body: body, Repo: repo, Comments: 3, UpdatedAt: "2026-09-27T00:00:00Z"},
		}), 0),
	})
	ingestResult := runJSON(t, ws, "ingest")
	ingestSources := ingestResult["sources"].([]any)
	ingestItems := itemsOf(t, repoOf(t, findSourceByID(t, ingestSources, "confirm-window-observation-source"), repo))
	item := findItemByExternalKey(t, ingestItems, repo+"#1")
	if item["result"] != "unchanged" {
		t.Fatalf("ingest の item.result = %v, want %q（このテストの前提: 本文は変えず観測値だけ変える）", item["result"], "unchanged")
	}
	if item["comments_count"] != 3.0 {
		t.Fatalf("ingest の item.comments_count = %v, want 3（このテストの前提が崩れている）", item["comments_count"])
	}

	if _, err := master.Write([]byte(id + "\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeConflict) {
		t.Fatalf("error code = %q, want %q (stderr=%s)", got, CodeConflict, stderr.String())
	}

	show := runJSON(t, ws, "show", id)
	if len(show["approvals"].([]any)) != 0 {
		t.Errorf("approvals = %+v, want none (conflict must not record an approval)", show["approvals"])
	}
	c := show["challenge"].(map[string]any)
	if c["status"] != "awaiting_plan_approval" {
		t.Errorf("status = %v, want awaiting_plan_approval (approval must not have applied)", c["status"])
	}
	sb := show["source_binding"].(map[string]any)
	if sb["comments_count"] != 3.0 {
		t.Errorf("comments_count = %v, want 3（conflict は ingest の反映自体を取り消さない）", sb["comments_count"])
	}
}

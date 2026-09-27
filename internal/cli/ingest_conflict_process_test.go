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

// このファイルは AC-93「計画の承認の要約を表示した後、確認の入力の前に
// ingest がその課題の人間記入欄を置き換えると、確認を入力しても終了コード 1・
// conflict で終わる」を検証する。approval_process_test.go の
// TestApprove_ConflictWhenChallengeEditedAfterSummaryShown と同じ手法
// （疑似端末の子プロセスで approve を起動し、要約が表示された〔＝対象の版を
// 読み終えた〕ことを pty の出力で確かめてから、親プロセス側で対象を変える）を
// 使い、「変える」操作を edit ではなく実際の ingest（偽の gh 経由）に差し替える。

func TestApprove_ConflictWhenIngestUpdatesChallengeAfterSummaryShown(t *testing.T) {
	ws := initializedWorkspace(t)
	const repo = "owner/confirm-window-repo"
	const title = "distinctive-ingest-conflict-title"

	created := runJSON(t, ws, "create", "--title", title)
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.InsertSourceBinding(t, ws, challengeIDToInternalID(t, id),
		"confirm-window-source", repo+"#1", "https://example.invalid/"+repo+"#1",
		core.Fingerprint("original body"), "open", "in_policy", "2026-09-26T00:00:00.000Z")
	runJSON(t, ws, "classify", id, "--priority", "P0")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "do the thing"))

	decl := `{
  "version": 1,
  "sources": [
    {"id": "confirm-window-source", "type": "github-issue", "repos": ["` + repo + `"], "self_assignees": ["someone"]}
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

	// 子プロセスが要約を書き終える（＝対象の版を読み終えた）まで、pty の
	// 出力に対象 ID が現れるのを待つ。
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(drainSnapshot(drain), id) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the summary to appear on the pty")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 別プロセス相当: ingest が上流の本文の変化を検出し、この課題の人間記入欄
	// （タイトル・説明・fingerprint）を置き換える（確認の入力より前）。
	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute(repo, 1, fakeGHIssueListBody(t, []fakeGHIssue{
			{Number: 1, Title: title, Body: "changed body", Repo: repo},
		}), 0),
	})
	ingestResult := runJSON(t, ws, "ingest")
	ingestSources := ingestResult["sources"].([]any)
	ingestItems := itemsOf(t, repoOf(t, findSourceByID(t, ingestSources, "confirm-window-source"), repo))
	if item := findItemByExternalKey(t, ingestItems, repo+"#1"); item["result"] != "updated" {
		t.Fatalf("ingest の item.result = %v, want %q（このテストの前提が崩れている）", item["result"], "updated")
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
	// code-reviewer 指摘の再発防止: タイトルは create と偽の gh の応答で
	// 同じ値（title 定数）にしているため、"title" での検査は ingest の反映が
	// 巻き戻っていても常に真になり検出力が無かった。ingest が実際に変えた
	// description（"original body" → "changed body"）で確かめる。
	if c["description"] != "changed body" {
		t.Errorf("description = %v, want %q（ingest が更新した値のままであること。conflict は ingest の反映自体を取り消さない）", c["description"], "changed body")
	}
}

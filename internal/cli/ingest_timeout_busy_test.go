package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// このファイルは AC-20（応答を返さない偽の gh は時間の上限でその呼び出しが
// 失敗として扱われ、コマンドが終わる）と AC-21（取得の間、ストアの書き込み
// ロックを保持しない）を検証する。

const singleSourceForTimingDeclaration = `{
  "version": 1,
  "sources": [
    {"id": "timing-source", "type": "github-issue", "repos": ["owner/timing-repo"], "self_assignees": ["someone"]}
  ]
}`

// --- AC-20 ---

func TestIngest_GHCallTimesOut_RepoFailsButCommandFinishes(t *testing.T) {
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, singleSourceForTimingDeclaration)

	dir, _ := writeSleepyFakeGH(t, "30", "") // 実際には短い ingestGHTimeout で先に打ち切られる
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	original := ingestGHTimeout
	ingestGHTimeout = 100 * time.Millisecond
	t.Cleanup(func() { ingestGHTimeout = original })

	start := time.Now()
	got := runJSON(t, ws, "ingest")
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Fatalf("ingest took %s, want it to finish quickly once the short timeout elapses", elapsed)
	}

	sources := got["sources"].([]any)
	source := findSourceByID(t, sources, "timing-source")
	repos := reposOf(t, source)
	if len(repos) != 1 {
		t.Fatalf("repos = %v, want 1", repos)
	}
	if repos[0]["error"] == nil {
		t.Errorf("repo.error = nil, want non-null (the gh call must be treated as failed once the time limit is exceeded)")
	}
}

// --- AC-21 ---

// TestIngest_DoesNotHoldStoreWriteLockWhileGHIsInFlight は、AC-21 の文言
// 「別のプロセスの `create` が `store_busy` にならずに成功する」を、実際に
// 別プロセスの `create` で検証する（design/code-reviewer 指摘の再発防止:
// 以前は create も同じテストプロセス内の goroutine で実行しており、AC の
// 「別のプロセス」という前提を厳密には満たしていなかった）。`create` は
// newChildCmd（confirm_process_test.go と同じ手法。このテストバイナリ自身を
// FLYWHEEL_CLI_TEST_CONFIRM_HELPER=1 の子プロセスとして起動し、
// confirmHelperMain 経由で本物の defaultCommands() を実行する）で、本当に
// 別の OS プロセスとして起動する。`ingest` 自身は（`gh` を実プロセスとして
// 起動する以外は）このテストプロセス内の goroutine で走らせる。
func TestIngest_DoesNotHoldStoreWriteLockWhileGHIsInFlight(t *testing.T) {
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, singleSourceForTimingDeclaration)

	startedFile := filepath.Join(t.TempDir(), "gh-started")
	dir, _ := writeSleepyFakeGH(t, "3", startedFile)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ingestDone := make(chan int, 1)
	var ingestStdout, ingestStderr bytes.Buffer
	go func() {
		ingestDone <- run([]string{"ingest", "--workspace", ws, "--json"}, strings.NewReader(""), &ingestStdout, &ingestStderr, defaultCommands())
	}()
	// ingest の goroutine は、このテストがどの経路で終わっても（成功・
	// t.Fatal のどちらでも）必ず回収する（code-reviewer 指摘の再発防止:
	// 以前は失敗経路で t.Fatal すると goroutine を回収せずに抜け、t.TempDir
	// の後始末やプロセス終了と競合しうる状態のまま残っていた）。
	t.Cleanup(func() {
		select {
		case <-ingestDone:
		case <-time.After(15 * time.Second):
			t.Errorf("ingest goroutine did not finish during cleanup (gh may still be running)")
		}
	})

	// 偽の gh が実際に呼ばれた（＝取得が始まった）ことを合図ファイルで確かめて
	// から create する（タイミング依存にしない）。
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(startedFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the fake gh to start (started marker never appeared)")
		}
		time.Sleep(10 * time.Millisecond)
	}

	createCmd, createStdout, createStderr := newChildCmd([]string{"create", "--title", "concurrent-during-fetch", "--workspace", ws, "--json"})
	if err := createCmd.Start(); err != nil {
		t.Fatalf("start create child process: %v", err)
	}
	// waitChild は上限に達すると自身でプロセスを kill してから t.Fatal する
	// （confirm_test.go）。ここでは「別プロセスの create が待たされずに
	// 成功する」ことを確かめたいので、上限は余裕を持たせつつ短めにする。
	code := waitChild(t, createCmd, 2*time.Second)
	if code != 0 {
		t.Fatalf("create（別プロセス）while gh is in flight: exit=%d (stdout=%q stderr=%q), want 0 (store_busy であってはならない)",
			code, createStdout.String(), createStderr.String())
	}
}

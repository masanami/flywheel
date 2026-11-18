package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは、進められるものが無ければサイクルを始めないこと（core の見直し S2b・masanami/flywheel#777）のうち、
// ストアの行数・バックアップのコミット・開始の合図の行を、受入基準の 1 つずつに照らして確かめる。

// tableRows は表 table の行数を返す。
func tableRows(t *testing.T, ws, table string) string {
	t.Helper()
	return fmt.Sprint(coretest.QueryRows(t, ws, "SELECT COUNT(*) FROM "+table)[0][0])
}

// AC-192・AC-194・AC-195: 進められるものが 0 件なら、サイクルの記録も実行の記録も作らず、バックアップのコミットも作らない。
func TestCycle_NothingToAdvance_LeavesNoCycleRowNoRunRowAndNoBackupCommit(t *testing.T) {
	ws := backupWorkspace(t)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")
	head := backupGit(t, ws, "rev-parse", "HEAD")
	cycles, runs := tableRows(t, ws, "cycle"), tableRows(t, ws, "run")

	doc := cycleDocAllowNotStarted(t, ws)

	if doc["started"] != false {
		t.Fatalf("doc = %v, want started=false", doc)
	}
	if got := tableRows(t, ws, "cycle"); got != cycles {
		t.Errorf("cycle rows = %s, want %s (unchanged)", got, cycles)
	}
	if got := tableRows(t, ws, "run"); got != runs {
		t.Errorf("run rows = %s, want %s (unchanged)", got, runs)
	}
	if got := backupGit(t, ws, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD = %s, want %s (no backup commit)", got, head)
	}
}

// AC-201・AC-202・AC-203: 未分類の課題が 1 件あれば、サイクルの記録が 1 つ増え、started は真、not_started_reason は null。
func TestCycle_UnclassifiedChallenge_StartsAndSaysSo(t *testing.T) {
	parallelTools(t, false)
	ws := setupWorkspaceWithPosition(t)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")
	createTitled(t, ws, "t")
	if got := tableRows(t, ws, "cycle"); got != "0" {
		t.Fatalf("cycle rows before = %s, want 0", got)
	}

	doc := cycleDocAllowNotStarted(t, ws)

	if got := tableRows(t, ws, "cycle"); got != "1" {
		t.Errorf("cycle rows = %s, want 1", got)
	}
	if doc["started"] != true {
		t.Errorf("started = %v, want true", doc["started"])
	}
	if reason, ok := doc["not_started_reason"]; !ok || reason != nil {
		t.Errorf("not_started_reason = %v (present=%v), want null", reason, ok)
	}
}

// AC-227: 画面からの起動（--trigger ui）でも、始めなかったときの標準出力に「を開始した」を含む行は出ない
// （サーバが開始の合図と取り違えない）。
func TestCycle_NothingToAdvance_TriggerUIPrintsNoStartSignalLine(t *testing.T) {
	parallelTools(t, false)
	ws := setupWorkspaceWithPosition(t)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")

	out := runText(t, ws, "cycle", "--trigger", "ui")

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "を開始した") {
			t.Errorf("stdout has a start signal line: %q", line)
		}
	}
	if got := tableRows(t, ws, "cycle"); got != "0" {
		t.Errorf("cycle rows = %s, want 0", got)
	}
}

package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// #988 AC-151（Issue #1037。番号は #988 本文の「## 受入基準」の並びの順）: `flywheel cycle status` のテキストは、走っているとき、
// 見ているタスクを実行ごとに 1 行（課題 ID・題名・状態・経過・費用）で出す。作業中の実行を 2 件置いて、2 行になることを見る。
func TestCycleStatusText_AC151_OneLinePerWatchedRun(t *testing.T) {
	ws, _ := runningCycleWorkspace(t, time.Second) // 課題 C-1 "t"
	coretest.InsertChallenge(t, ws, "二つ目の課題")
	coretest.Exec(t, ws, `UPDATE challenge SET title = '一つ目の課題' WHERE id = 1`)
	coretest.InsertContractRun(t, ws, 1, "conn", "ext-1", "running")
	coretest.InsertContractRun(t, ws, 2, "conn", "ext-2", "running")
	coretest.Exec(t, ws, `UPDATE contract_run SET cost_usd = 1230000 WHERE external_run_id = 'ext-1'`)
	coretest.Exec(t, ws, `UPDATE contract_run SET cost_usd = 450000 WHERE external_run_id = 'ext-2'`)

	code, out, errOut := runCLIRaw(t, "cycle", "status", "--workspace", ws)
	if code != 0 {
		t.Fatalf("exit = %d (%s)", code, errOut)
	}

	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "  C-") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("watching lines = %d, want 2 (one per run):\n%s", len(lines), out)
	}
	for i, want := range [][]string{
		{"C-1", "一つ目の課題", "作業中", "経過 ", "1.2300 USD"},
		{"C-2", "二つ目の課題", "作業中", "経過 ", "0.4500 USD"},
	} {
		for _, w := range want {
			if !strings.Contains(lines[i], w) {
				t.Errorf("line %d lacks %q: %q", i, w, lines[i])
			}
		}
	}
}

// #988 AC-141・AC-142（JSON の側）: `cycle status --json` の watching の要素の title は題名で、run_started_at は実行の始まった時刻の文字列。
func TestCycleStatusJSON_AC141_AC142_WatchingTitleAndRunStartedAt(t *testing.T) {
	ws, _ := runningCycleWorkspace(t, time.Second)
	coretest.Exec(t, ws, `UPDATE challenge SET title = '一つ目の課題' WHERE id = 1`)
	coretest.InsertContractRun(t, ws, 1, "conn", "ext-1", "running")
	coretest.Exec(t, ws, `UPDATE contract_run SET started_at = '2026-10-21T03:04:05.678Z'`) // observed_at とは別の時刻

	doc := runJSON(t, ws, "cycle", "status")

	w := doc["running"].(map[string]any)["watching"].([]any)
	if len(w) != 1 {
		t.Fatalf("watching = %v, want one element", w)
	}
	el := w[0].(map[string]any)
	if el["title"] != "一つ目の課題" || el["run_started_at"] != "2026-10-21T03:04:05.678Z" {
		t.Errorf("watching[0] = %v, want title 一つ目の課題 and run_started_at 2026-10-21T03:04:05.678Z", el)
	}
}

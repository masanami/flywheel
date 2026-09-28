package cli

import (
	"os"
	"os/exec"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// deadPIDForRunsTest starts and waits for a trivial child process, returning
// its (now free) pid — a value guaranteed not to be alive on this host.
func deadPIDForRunsTest(t *testing.T) int64 {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start throwaway process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait throwaway process: %v", err)
	}
	return int64(pid)
}

// このファイルは #81（docs/features/m3-invoker-delegation.md §観測・
// §IF / API「runs」）の CLI 固有の項目（AC-151〜153）を検証する。ジェネリック
// な横断検査（jsondoc・not_found・usage_error 等）は allcommands_test.go・
// idcommands_test.go・jsondoc_test.go が
// `allCommandSuccessCases["runs"]` 経由で検証する。

func insertRunFixture(t *testing.T, ws, challengeID string, overrides func(*coretest.InsertRunInput)) int64 {
	t.Helper()
	in := coretest.InsertRunInput{
		ChallengeID:      challengeIDToInternalID(t, challengeID),
		Kind:             "judgment",
		Judgment:         "J1",
		ChallengeVersion: 1,
		SessionID:        "11111111-1111-1111-1111-111111111111",
		PID:              4242,
		Host:             "test-host",
		HeartbeatAt:      "2026-09-28T00:00:00.000Z",
		StartedAt:        "2026-09-28T00:00:00.000Z",
		MaxBudgetUSD:     1_000_000,
		BudgetBucket:     "judgment",
	}
	if overrides != nil {
		overrides(&in)
	}
	return coretest.InsertRun(t, ws, in)
}

// AC-151: `flywheel runs --json` は run を新しい順に出力する。
func TestRuns_NewestFirst(t *testing.T) {
	ws := initializedWorkspace(t)
	id1 := createForCase(t, ws)
	id2 := createForCase(t, ws)
	insertRunFixture(t, ws, id1, func(in *coretest.InsertRunInput) {
		in.EndedAt = "2026-09-28T00:01:00.000Z"
		in.Result = "succeeded"
		cost := int64(500_000)
		in.CostUSD = &cost
		in.CostSource = "reported"
	})
	insertRunFixture(t, ws, id2, func(in *coretest.InsertRunInput) {
		in.SessionID = "22222222-2222-2222-2222-222222222222"
	})

	doc := runJSON(t, ws, "runs")
	runs, ok := doc["runs"].([]any)
	if !ok || len(runs) != 2 {
		t.Fatalf("runs = %+v, want 2 elements", doc["runs"])
	}
	first := runs[0].(map[string]any)
	second := runs[1].(map[string]any)
	if first["id"] != "R-2" || second["id"] != "R-1" {
		t.Errorf("order = [%v,%v], want [R-2,R-1] (newest first)", first["id"], second["id"])
	}
}

// AC-152: `flywheel runs <C-ID> --json` は、その課題の run だけを出力する。
func TestRuns_FiltersByChallengeID(t *testing.T) {
	ws := initializedWorkspace(t)
	id1 := createForCase(t, ws)
	id2 := createForCase(t, ws)
	insertRunFixture(t, ws, id1, nil)
	insertRunFixture(t, ws, id2, func(in *coretest.InsertRunInput) {
		in.SessionID = "22222222-2222-2222-2222-222222222222"
	})

	doc := runJSON(t, ws, "runs", id1)
	runs, ok := doc["runs"].([]any)
	if !ok || len(runs) != 1 {
		t.Fatalf("runs = %+v, want exactly 1 element", doc["runs"])
	}
	elem := runs[0].(map[string]any)
	if elem["challenge_id"] != id1 {
		t.Errorf("challenge_id = %v, want %s", elem["challenge_id"], id1)
	}
}

// AC-153: `flywheel runs --open --json` は、終了していない run だけを出力する。
func TestRuns_OpenFlagExcludesEndedRuns(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	insertRunFixture(t, ws, id, func(in *coretest.InsertRunInput) {
		in.EndedAt = "2026-09-28T00:01:00.000Z"
		in.Result = "succeeded"
		cost := int64(1)
		in.CostUSD = &cost
		in.CostSource = "reported"
	})

	doc := runJSON(t, ws, "runs", "--open")
	runs, ok := doc["runs"].([]any)
	if !ok || len(runs) != 0 {
		t.Fatalf("runs(--open) = %+v, want 0 elements (the only run has ended)", doc["runs"])
	}
}

// §IF / API「runs」の形: 終了していないrunはresult・cost_usd・cost_source・
// ended_atがnull、judgmentが値を持つ。
func TestRuns_OpenRunHasNullResultAndCost(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	insertRunFixture(t, ws, id, nil)

	doc := runJSON(t, ws, "runs", id)
	runs := doc["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want 1 element", doc["runs"])
	}
	elem := runs[0].(map[string]any)
	for _, key := range []string{"result", "cost_usd", "cost_source", "ended_at"} {
		if elem[key] != nil {
			t.Errorf("%s = %v, want null for an open run", key, elem[key])
		}
	}
	if elem["judgment"] != "J1" {
		t.Errorf("judgment = %v, want J1", elem["judgment"])
	}
	if elem["session_id"] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("session_id = %v", elem["session_id"])
	}
	if elem["max_budget_usd"] != float64(1) {
		t.Errorf("max_budget_usd = %v, want 1", elem["max_budget_usd"])
	}
}

func TestRuns_EndedRunHasCostAndResult(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	insertRunFixture(t, ws, id, func(in *coretest.InsertRunInput) {
		in.EndedAt = "2026-09-28T00:01:00.000Z"
		in.Result = "succeeded"
		cost := int64(750_000)
		in.CostUSD = &cost
		in.CostSource = "delta"
		in.RateLimited = true
	})

	doc := runJSON(t, ws, "runs", id)
	runs := doc["runs"].([]any)
	elem := runs[0].(map[string]any)
	if elem["result"] != "succeeded" {
		t.Errorf("result = %v, want succeeded", elem["result"])
	}
	if elem["cost_usd"] != float64(0.75) {
		t.Errorf("cost_usd = %v, want 0.75", elem["cost_usd"])
	}
	if elem["cost_source"] != "delta" {
		t.Errorf("cost_source = %v, want delta", elem["cost_source"])
	}
	if elem["rate_limited"] != true {
		t.Errorf("rate_limited = %v, want true", elem["rate_limited"])
	}
	if elem["ended_at"] == nil {
		t.Error("ended_at is null, want a timestamp")
	}
}

// AC「次にflywheel runsの実行でその run の結果がinterruptedになる」（heartbeat
// が300秒より古く、記録したプロセスが生きていない）。
func TestRuns_ReapsStaleHeartbeatIntoInterrupted(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	insertRunFixture(t, ws, id, func(in *coretest.InsertRunInput) {
		in.PID = deadPIDForRunsTest(t)
		in.Host = host
		// heartbeat は既定の閾値（300秒）より古い固定の過去日時。
		in.HeartbeatAt = "2000-01-01T00:00:00.000Z"
		in.StartedAt = "2000-01-01T00:00:00.000Z"
	})

	doc := runJSON(t, ws, "runs", id)
	runs := doc["runs"].([]any)
	elem := runs[0].(map[string]any)
	if elem["result"] != "interrupted" {
		t.Errorf("result = %v, want interrupted (stale heartbeat + dead process)", elem["result"])
	}
}

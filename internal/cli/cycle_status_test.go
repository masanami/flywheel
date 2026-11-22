package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは `flywheel cycle status`・`flywheel cycle stop`（区切り S4a。AC-130〜AC-174）を、実ストアと
// 疑似の排他で確かめる。

const csTimeLayout = "2006-01-02T15:04:05.000Z"

// runningCycleWorkspace は、周 1 件が排他を持って走っている（heartbeat が今）ワークスペースと周の内部整数 ID を返す。
func runningCycleWorkspace(t *testing.T, heartbeatAge time.Duration) (string, int64) {
	t.Helper()
	ws := initializedWorkspace(t)
	started := time.Now().UTC().Add(-5 * time.Minute).Format(csTimeLayout)
	id := coretest.InsertCycle(t, ws, "manual", 300_000_000, started)
	coretest.Exec(t, ws, `UPDATE cycle SET rounds = 2 WHERE id = ?`, id)
	// 判断点 0.50 と、終わっていない実行の読み取り 1.20（どちらも、その周の終わった run の費用）。
	coretest.InsertChallenge(t, ws, "t")
	for i, cost := range []int64{500000, 1200000} {
		c := cost
		coretest.InsertRun(t, ws, coretest.InsertRunInput{
			ChallengeID: 1, CycleID: &id, Kind: []string{"judgment", "contract"}[i], PID: 1, Host: "h",
			HeartbeatAt: started, StartedAt: started, EndedAt: started, Result: "succeeded",
			MaxBudgetUSD: 2000000, BudgetBucket: "impl", CostUSD: &c, CostSource: "reported",
		})
	}
	hb := time.Now().UTC().Add(-heartbeatAge).Format(csTimeLayout)
	coretest.InsertLock(t, ws, id, 1, "host", started, hb)
	return ws, id
}

func stopRequestedAtOf(t *testing.T, ws string, id int64) any {
	t.Helper()
	return coretest.QueryRows(t, ws, `SELECT stop_requested_at FROM cycle WHERE id = ?`, id)[0][0]
}

// AC-130〜AC-136・AC-147: 走っていれば running に値が入り、走っていなければ null。
func TestCycleStatus_JSON_RunningAndNotRunning(t *testing.T) {
	ws := initializedWorkspace(t)
	doc := runJSON(t, ws, "cycle", "status")
	if v, ok := doc["running"]; !ok || v != nil {
		t.Errorf("running = %v (present=%v), want null", v, ok)
	}
	if v, ok := doc["last_end_reason"]; !ok || v != nil {
		t.Errorf("last_end_reason = %v (present=%v), want null", v, ok)
	}

	ws, _ = runningCycleWorkspace(t, time.Second)
	doc = runJSON(t, ws, "cycle", "status")
	r, ok := doc["running"].(map[string]any)
	if !ok {
		t.Fatalf("running = %v, want object", doc["running"])
	}
	if r["trigger"] != "manual" || r["rounds"] != float64(2) || r["spent_usd"] != 1.7 || r["budget_usd"] != 300.0 ||
		r["max_parallel_runs"] != float64(2) || r["active_runs"] != float64(0) || r["stop_requested"] != false {
		t.Errorf("running = %v", r)
	}
	if _, ok := r["started_at"].(string); !ok {
		t.Errorf("started_at = %v", r["started_at"])
	}
	if w, ok := r["watching"].([]any); !ok || len(w) != 0 {
		t.Errorf("watching = %v, want []", r["watching"])
	}
	if doc["state"] != "running" {
		t.Errorf("state = %v", doc["state"])
	}
}

// AC-152〜AC-156: 走っていないとき、最後のサイクルの終わった時刻と理由を出し、budget・stalled には案内を足す。
func TestCycleStatus_Text_NotRunningShowsEndAndGuidance(t *testing.T) {
	cases := []struct {
		reason string
		want   []string
	}{
		{"budget", []string{"費用の上限で止まりました", "cycle_budget_usd"}},
		{"stalled", []string{"進まないので止まりました"}},
		{"settled", nil},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			ws := initializedWorkspace(t)
			id := coretest.InsertCycle(t, ws, "manual", 1_000_000, "2026-11-01T00:00:00.000Z")
			coretest.Exec(t, ws, `UPDATE cycle SET ended_at = '2026-11-01T01:02:03.000Z', result = 'completed', end_reason = ? WHERE id = ?`, c.reason, id)
			code, out, errOut := runCLIRaw(t, "cycle", "status", "--workspace", ws)
			if code != 0 {
				t.Fatalf("exit = %d (%s)", code, errOut)
			}
			for _, w := range append([]string{"2026-11-01T01:02:03.000Z", "終わった理由: " + c.reason}, c.want...) {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			if c.reason != "budget" && strings.Contains(out, "cycle_budget_usd") || c.reason != "stalled" && strings.Contains(out, "進まないので") {
				t.Errorf("unexpected guidance for %s:\n%s", c.reason, out)
			}
			_, jout, _ := runCLIRaw(t, "cycle", "status", "--workspace", ws, "--json")
			var doc map[string]any
			if err := json.Unmarshal([]byte(jout), &doc); err != nil || doc["last_end_reason"] != c.reason {
				t.Errorf("last_end_reason = %v (err %v), want %s", doc["last_end_reason"], err, c.reason)
			}
		})
	}
}

// AC-151・AC-161・AC-163・AC-164: 走っているときのテキストは、作業中の件数と上限・費用と上限を出し、止める指示を受けていれば、その旨を出す。
func TestCycleStatus_Text_RunningShowsCountsCostAndStopNotice(t *testing.T) {
	ws, id := runningCycleWorkspace(t, time.Second)
	_, out, _ := runCLIRaw(t, "cycle", "status", "--workspace", ws)
	for _, w := range []string{"サイクルが走っています", "回り 2 回", "作業中 0 件 / 上限 2 件", "費用 1.7000 / 上限 300.00 USD"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "止める指示を受けています") {
		t.Errorf("stop notice shown before a stop request:\n%s", out)
	}
	if code, _, e := runCLIRaw(t, "cycle", "stop", "--workspace", ws); code != 0 {
		t.Fatalf("stop exit = %d (%s)", code, e)
	}
	_ = id
	_, out, _ = runCLIRaw(t, "cycle", "status", "--workspace", ws)
	if !strings.Contains(out, "止める指示を受けています") {
		t.Errorf("output lacks the stop notice:\n%s", out)
	}
	_, jout, _ := runCLIRaw(t, "cycle", "status", "--workspace", ws, "--json")
	if !strings.Contains(jout, `"stop_requested":true`) {
		t.Errorf("json lacks stop_requested=true: %s", jout)
	}
}

// AC-157・AC-172: 走っているサイクルがあれば stop_requested_at に時刻が入る（本人確認の入力は求めない）。
func TestCycleStop_Running_WritesStopRequestedAtWithoutConfirmation(t *testing.T) {
	ws, id := runningCycleWorkspace(t, time.Second)
	code, out, e := runCLIRaw(t, "cycle", "stop", "--workspace", ws)
	if code != 0 {
		t.Fatalf("exit = %d (%s)", code, e)
	}
	if !strings.Contains(out, "止める指示を残しました") {
		t.Errorf("output = %q", out)
	}
	if v := stopRequestedAtOf(t, ws, id); v == nil {
		t.Error("stop_requested_at is NULL, want a time")
	}
}

// AC-165: 回収の時間を過ぎた排他は、走っているサイクルが無い扱いで、何も書かない（AC-170 の終了コード 0）。
func TestCycleStop_StaleLock_WritesNothingAndExitsZero(t *testing.T) {
	ws, id := runningCycleWorkspace(t, time.Hour)
	code, out, e := runCLIRaw(t, "cycle", "stop", "--workspace", ws)
	if code != 0 {
		t.Fatalf("exit = %d (%s)", code, e)
	}
	if !strings.Contains(out, "走っているサイクルは無い") {
		t.Errorf("output = %q", out)
	}
	if v := stopRequestedAtOf(t, ws, id); v != nil {
		t.Errorf("stop_requested_at = %v, want NULL", v)
	}
}

// AC-170・AC-171: 走っているサイクルが無ければ終了コード 0 で、サイクルの記録は変わらない。
func TestCycleStop_NoCycle_ExitsZeroAndLeavesRecordsUnchanged(t *testing.T) {
	ws := initializedWorkspace(t)
	id := coretest.InsertCycle(t, ws, "manual", 1_000_000, "2026-11-01T00:00:00.000Z")
	coretest.Exec(t, ws, `UPDATE cycle SET ended_at = '2026-11-01T01:00:00.000Z', result = 'completed', end_reason = 'settled' WHERE id = ?`, id)
	before := coretest.DumpTables(t, ws, "cycle")
	code, _, e := runCLIRaw(t, "cycle", "stop", "--workspace", ws)
	if code != 0 {
		t.Fatalf("exit = %d (%s)", code, e)
	}
	if after := coretest.DumpTables(t, ws, "cycle"); after != before {
		t.Errorf("cycle rows changed:\n%s\n->\n%s", before, after)
	}
	_, jout, _ := runCLIRaw(t, "cycle", "stop", "--workspace", ws, "--json")
	if !strings.Contains(jout, `"requested":false`) {
		t.Errorf("json = %s, want requested=false", jout)
	}
}

// AC-162・AC-173・AC-174: 委譲の子の環境からの cycle status・cycle stop は 0 でない終了コードで断られ、止める指示は書かれない。
func TestCycleStatusAndStop_DelegatedEnv_Rejected(t *testing.T) {
	ws, id := runningCycleWorkspace(t, time.Second)
	t.Setenv(core.DelegatedRunEnvVar, "R-7")
	for _, sub := range []string{"status", "stop"} {
		code, out, errOut := runCLIRaw(t, "cycle", sub, "--workspace", ws)
		if code == 0 || out != "" || !strings.Contains(errOut, core.DelegatedRunEnvVar) {
			t.Errorf("cycle %s: exit=%d stdout=%q stderr=%q, want rejected", sub, code, out, errOut)
		}
	}
	if v := stopRequestedAtOf(t, ws, id); v != nil {
		t.Errorf("stop_requested_at = %v, want NULL", v)
	}
}

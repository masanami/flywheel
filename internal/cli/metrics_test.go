package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは M4.5 S3 の `flywheel metrics`（受入基準 AC-288〜AC-305）を CLI の入口から検査する。
// 集計の規則は core の metrics_test.go が実ストアで検査する。ここは期間の引数・出力の形・
// 読み取りだけであること・環境（端末なし・CLAUDECODE あり・connectors.json なし）を確かめる。

// localStamp はローカルの時刻の日付 d の hh 時の、ストアの保存形（UTC）。
func localStamp(t *testing.T, d string, hour int) string {
	t.Helper()
	day, err := time.ParseInLocation("2006-01-02", d, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, time.Local).UTC().Format("2006-01-02T15:04:05.000Z")
}

// metricsWorkspace は 3 つの課題を持つワークスペースを作る。C-1 は 3/1 に完了の承認（その前に完了の差し戻し 1 回〔issues〕）、
// C-2 は 3/3 に完了の承認、C-3 は 3/9 に完了の承認。
func metricsWorkspace(t *testing.T) string {
	t.Helper()
	ws := initializedWorkspace(t)
	for i := 0; i < 3; i++ {
		createForCase(t, ws)
	}
	ins := func(cid int, decision, at, fields string) {
		var f any
		if fields != "" {
			f = fields
		}
		coretest.Exec(t, ws, `INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, decided_at, rejected_fields)
			VALUES (?, 'completion', ?, 1, 'a', 'cli', 'tty_confirm', ?, ?)`, cid, decision, at, f)
	}
	ins(1, "rejected", localStamp(t, "2026-02-28", 12), `["issues"]`)
	ins(1, "approved", localStamp(t, "2026-03-01", 0), "")
	ins(2, "approved", localStamp(t, "2026-03-03", 23), "")
	ins(3, "approved", localStamp(t, "2026-03-09", 12), "")
	return ws
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %#v", v)
	}
	return m
}

func TestMetrics_PeriodSelectsTargetsInclusivelyAndEchoesThePeriod(t *testing.T) {
	ws := metricsWorkspace(t)
	out := runJSON(t, ws, "metrics", "--since", "2026-03-01", "--until", "2026-03-03")
	if out["target_challenges"] != 2.0 {
		t.Fatalf("target_challenges = %v, want 2 (C-1 on the since day and C-2 on the until day)", out["target_challenges"])
	}
	p := asMap(t, out["period"])
	if p["since"] != "2026-03-01" || p["until"] != "2026-03-03" {
		t.Errorf("period = %v", p)
	}
	rr := asMap(t, asMap(t, out["metrics"])["rejection_rate"])
	if rr["numerator"] != 1.0 || rr["denominator"] != 2.0 || rr["value"] != 0.5 {
		t.Errorf("rejection_rate = %v, want 1/2 (the C-1 rejection predates the period but C-1 is a target)", rr)
	}
	if bf := asMap(t, rr["by_field"]); bf["issues"] != 1.0 || bf["figure"] != 0.0 {
		t.Errorf("by_field = %v", bf)
	}

	out = runJSON(t, ws, "metrics")
	p = asMap(t, out["period"])
	if p["since"] != nil || p["until"] != nil || out["target_challenges"] != 3.0 {
		t.Errorf("unbounded: period = %v targets = %v", p, out["target_challenges"])
	}
	out = runJSON(t, ws, "metrics", "--since", "2026-03-04")
	if out["target_challenges"] != 1.0 {
		t.Errorf("since only: targets = %v, want 1", out["target_challenges"])
	}
}

func TestMetrics_ZeroTargetsHaveNullValues(t *testing.T) {
	ws := initializedWorkspace(t)
	out := runJSON(t, ws, "metrics")
	ms := asMap(t, out["metrics"])
	if rr := asMap(t, ms["rejection_rate"]); rr["value"] != nil || rr["denominator"] != 0.0 {
		t.Errorf("rejection_rate = %v, want a null value", rr)
	}
	if by, ok := asMap(t, ms["approval_count"])["by"].([]any); !ok || len(by) != 0 {
		t.Errorf("approval_count.by = %#v, want an empty array", asMap(t, ms["approval_count"])["by"])
	}
}

func TestMetrics_InvalidDatesAreUsageErrors(t *testing.T) {
	ws := metricsWorkspace(t)
	for _, args := range [][]string{
		{"metrics", "--since", "2026-3-1"},
		{"metrics", "--since", "20260301"},
		{"metrics", "--until", "2026-02-30"},
		{"metrics", "--until", "yesterday"},
		{"metrics", "--since", ""},
		{"metrics", "extra"},
	} {
		requireErrorCode(t, append(append([]string{}, args...), "--workspace", ws), 2, CodeUsageError)
	}
}

func TestMetrics_JSONHasDefinitionsForEveryMetricAndIsDeterministic(t *testing.T) {
	ws := metricsWorkspace(t)
	args := []string{"metrics", "--since", "2026-03-01", "--workspace", ws, "--json"}
	code1, out1, _ := runPiped(t, args)
	code2, out2, _ := runPiped(t, args)
	if code1 != 0 || code2 != 0 || out1 != out2 {
		t.Fatalf("exit %d/%d; two runs differ:\n%s\n%s", code1, code2, out1, out2)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out1), &doc); err != nil {
		t.Fatal(err)
	}
	defs := asMap(t, doc["definitions"])
	for _, name := range []string{"rejection_rate", "resume_count", "cost_per_challenge", "approval_count"} {
		d := asMap(t, defs[name])
		for _, k := range []string{"numerator", "denominator", "period"} {
			if s, ok := d[k].(string); !ok || s == "" {
				t.Errorf("definitions.%s.%s = %#v, want a non-empty string", name, k, d[k])
			}
		}
	}
	// キーは辞書順。
	var keys []string
	dec := json.NewDecoder(strings.NewReader(out1))
	_, _ = dec.Token()
	for dec.More() {
		k, _ := dec.Token()
		keys = append(keys, k.(string))
		var skip json.RawMessage
		_ = dec.Decode(&skip)
	}
	if strings.Join(keys, ",") != "definitions,metrics,occurrences,period,target_challenges" {
		t.Errorf("top-level key order = %v", keys)
	}
}

func TestMetrics_TextShowsPeriodTargetsMetricsAndOccurrences(t *testing.T) {
	ws := metricsWorkspace(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"metrics", "--since", "2026-03-01", "--until", "2026-03-03", "--workspace", ws}, strings.NewReader(""), &stdout, &stderr, defaultCommands()); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"2026-03-01", "2026-03-03", "対象の課題: 2 件", "差し戻し率", "resume の回数", "1 課題あたりの費用", "人の承認の件数", "発生の件数", "未分類", "未処理"} {
		if !strings.Contains(got, want) {
			t.Errorf("text output lacks %q:\n%s", want, got)
		}
	}
}

// AC-288・AC-300・AC-304・AC-305: 判断点も claude も起動せず、ストアの行を変えず、端末なし・CLAUDECODE あり・
// connectors.json なしで動く。
func TestMetrics_ReadsOnlyWithoutClaudeConnectorsOrTerminal(t *testing.T) {
	ws := metricsWorkspace(t)
	if _, err := os.Stat(filepath.Join(ws, ".flywheel", "connectors.json")); err == nil {
		t.Fatal("fixture unexpectedly has connectors.json")
	}
	started := filepath.Join(t.TempDir(), "started")
	argv, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{}`, StartedFile: started})
	before := coretest.DumpTables(t, ws, "challenge", "approval", "run", "cycle", "activity", "hold", "occurrence", "operation")

	code, stdout, stderr := runPiped(t, []string{"metrics", "--workspace", ws, "--json"}, claudeCodeEnvVar+"=1")
	if code != 0 || !strings.Contains(stdout, `"target_challenges"`) {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
	if _, err := os.Stat(started); err == nil {
		t.Error("metrics started claude")
	}
	if b, err := os.ReadFile(argv); err == nil && len(b) > 0 {
		t.Errorf("claude was invoked: %s", b)
	}
	if after := coretest.DumpTables(t, ws, "challenge", "approval", "run", "cycle", "activity", "hold", "occurrence", "operation"); after != before {
		t.Error("metrics changed the store")
	}
}

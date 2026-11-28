package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// フローのカスタマイズ S1: タスクの詳細の委譲先の実行に、動いた定義の出どころ（ワークスペースの定義か組み込みか）とハッシュの先頭 12 文字を出す
// （show のテキストと --json）。記録の無い実行は出さない（--json は null）。
func TestShow_TaskDelegateShowsTheFlowDefinition(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	cid := challengeIDToInternalID(t, id)
	coretest.InsertTaskContractRun(t, ws, cid, "ka", "tool", "ext-1", "running")
	coretest.InsertTaskContractRun(t, ws, cid, "kb", "tool", "ext-2", "running")
	sum := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	coretest.Exec(t, ws, `UPDATE contract_run SET definition_source = 'workspace', definition_sha256 = ? WHERE external_run_id = 'ext-1'`, sum)

	doc := runJSON(t, ws, "show", id)
	tasks := objectList(t, doc["tasks"], "tasks")
	d, ok := tasks[0]["delegate"].(map[string]any)["definition"].(map[string]any)
	if !ok || d["source"] != "workspace" || d["sha256"] != sum || d["sha256_prefix"] != "0123456789ab" {
		t.Fatalf("tasks[0].delegate.definition = %#v", tasks[0]["delegate"])
	}
	if v, has := tasks[1]["delegate"].(map[string]any)["definition"]; !has || v != nil {
		t.Errorf("tasks[1].delegate.definition = %#v (present=%v), want null", v, has)
	}

	code, stdout, stderr := runCLIRaw(t, "show", id, "--workspace", ws)
	if code != 0 {
		t.Fatalf("show exit = %d (stderr=%s)", code, stderr)
	}
	if !strings.Contains(stdout, "の定義: ワークスペースの定義・ハッシュ 0123456789ab\n") {
		t.Errorf("show text has no definition line:\n%s", stdout)
	}
	if strings.Count(stdout, "の定義: ") != 1 {
		t.Errorf("show text shows a definition for a run without the record:\n%s", stdout)
	}
}

// レビューのステップの 1 回ごとに、指摘の数・費用・時間を show のテキストに出し、--json の delegate.steps に findings_count を出す。
func TestShow_TaskDelegateShowsReviewFindingsCount(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	cid := challengeIDToInternalID(t, id)
	coretest.InsertTaskContractRun(t, ws, cid, "ka", "tool", "ext-1", "running")
	ins := `INSERT INTO contract_run_step (contract_run_id, seq, step, kind, attempt, state, outcome, started_at, ended_at, cost_usd, cost_unknown, model, models_used, findings_count)
		VALUES ((SELECT id FROM contract_run WHERE external_run_id = 'ext-1'), ?, ?, 'llm', 1, ?, ?, '2026-11-01T00:00:00Z', ?, ?, 0, NULL, '[]', ?)`
	coretest.Exec(t, ws, ins, 1, "implement", "finished", "pass", "2026-11-01T00:01:00Z", 1000000, nil)
	coretest.Exec(t, ws, ins, 2, "self-review", "finished", "findings", "2026-11-01T00:02:30Z", 1500000, 2)
	coretest.Exec(t, ws, ins, 3, "self-review-fix", "finished", "pass", "2026-11-01T00:03:00Z", 500000, nil)
	coretest.Exec(t, ws, ins, 4, "self-review", "running", nil, nil, nil, 0)

	doc := runJSON(t, ws, "show", id)
	tasks := objectList(t, doc["tasks"], "tasks")
	steps := objectList(t, tasks[0]["delegate"].(map[string]any)["steps"], "steps")
	var got []any
	for _, s := range steps {
		v, has := s["findings_count"]
		if !has {
			t.Fatalf("step %v has no findings_count key", s)
		}
		got = append(got, v)
	}
	if len(got) != 4 || got[0] != nil || got[1] != float64(2) || got[2] != nil || got[3] != float64(0) {
		t.Errorf("findings_count = %v, want [nil 2 nil 0]", got)
	}

	code, stdout, stderr := runCLIRaw(t, "show", id, "--workspace", ws)
	if code != 0 {
		t.Fatalf("show exit = %d (stderr=%s)", code, stderr)
	}
	for _, want := range []string{"    レビュー self-review（1 回目）: 指摘 2 件・費用 1.5 USD・時間 2m30s\n", "    レビュー self-review（2 回目）: 指摘 0 件・費用 -・時間 実行中\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show text lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Count(stdout, "    レビュー ") != 2 {
		t.Errorf("show text lists steps that are not reviews:\n%s", stdout)
	}
}

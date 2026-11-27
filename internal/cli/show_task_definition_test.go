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

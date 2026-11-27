package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	j1NotOneSliceFixture = `{"is_error":false,"structured_output":{"verdict":"mine","fits_one_slice":false,"priority":"P1","reason":"SLICE-REASON","headline":"CLI の課題を分類する","context":"全体計画の一部で、分類のために要る。"}}`
	j10SingleFixture     = `{"is_error":false,"structured_output":{"verdict":"single","reason":"CLI test fixture","slices":null,"evidence":[{"path":"docs/a.md","note":"n"}]}}`
	// j10InvalidFixture は判定が split で区切りの無い、検査に落ちる出力（記録は pending のまま残る）。
	j10InvalidFixture = `{"is_error":false,"structured_output":{"verdict":"split","reason":"CLI test fixture","slices":null,"evidence":[{"path":"docs/a.md","note":"n"}]}}`
	j10SplitFixture   = `{"is_error":false,"structured_output":{"verdict":"split","reason":"CLI test fixture","slices":[` +
		`{"title":"a","headline":"h","context":"c","summary":"s","scope":"sc","repo":"direct-repo","depends_on":[]},` +
		`{"title":"b","headline":"h","context":"c","summary":"s","scope":"sc","repo":"direct-repo","depends_on":[1]}],` +
		`"evidence":[{"path":"docs/a.md","note":"n"}]}}`
	// j10Match は区切りを分ける判断点の出力スキーマだけが持つ値。
	j10Match = `"single"`
)

// setupPendingSliceSplit は、区切りを分ける記録が pending の課題を持つワークスペースと、その課題の ID を返す
// （区切りを分ける判断点の出力は検査に落とすので、写されない）。
func setupPendingSliceSplit(t *testing.T) (ws, id string) {
	t.Helper()
	ws, id, second := setupClassifiedSliceSplit(t, j10InvalidFixture)
	if second["result"] != "invalid_output" {
		t.Fatalf("classify --auto slice split item = %v, want invalid_output", second)
	}
	return ws, id
}

// setupClassifiedSliceSplit は、取り込んだ課題に classify --auto を 1 回当てる。同じ classify --auto が区切りを分ける
// 判断点（出力は j10Fixture）も呼ぶことを確かめ、ワークスペース・課題の ID・区切りを分ける判断点の項目を返す。
func setupClassifiedSliceSplit(t *testing.T, j10Fixture string) (ws, id string, splitItem map[string]any) {
	t.Helper()
	ws = setupJ2Workspace(t)
	writeSourcesDeclaration(t, ws, cycleSourcesJSON)
	issue := marshalFakeGHIssue(t, fakeGHIssue{Number: 1, Title: "ingested-issue", Body: "body", Repo: "o/r", UpdatedAt: "2026-09-25T09:00:00Z"})
	order := filepath.Join(t.TempDir(), "order.log")
	withFakeGHRoutesOrderedOnPATH(t, []fakeGHRoute{
		fakeGHListRoute("o/r", 1, "["+issue+"]", 0),
		fakeGHGetIssueStatusLineRoute("o/r", 1, "HTTP/2.0 200 OK", issue, 0),
		{match: "repos/o/r/issues/1/comments?per_page=100&page=1", stdout: "[]"},
	}, order)
	runJSON(t, ws, "ingest", "o/r#1")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{
		{Match: fakeClaudeJ1Match, Tag: "J1", Stdout: j1NotOneSliceFixture},
		{Match: j10Match, Tag: "J10", Stdout: j10Fixture},
	}, order)

	phase := runJSON(t, ws, "classify", "--auto")["phase"].(map[string]any)
	items := phase["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("classify --auto items = %v, want the classification and the slice split", items)
	}
	first, second := items[0].(map[string]any), items[1].(map[string]any)
	if first["outcome"] != "mine" {
		t.Fatalf("classify --auto items = %v", items)
	}
	log, err := os.ReadFile(order)
	if err != nil || !strings.Contains(string(log), "J10") {
		t.Fatalf("the fake claude was not called for J10: log=%q err=%v", log, err)
	}
	return ws, first["challenge_id"].(string), second
}

// flywheel classify --auto は区切りを分ける判断点の出力を写す。single は記録を single にし、split は分けるかの保留で
// 課題を人間対応待ちにする（その課題を指定した plan --auto は、状態の検査で invalid_transition になる）。
func TestClassifyAuto_AppliesTheSliceSplitOutput(t *testing.T) {
	parallelTools(t, false)
	t.Run("single", func(t *testing.T) {
		ws, id, item := setupClassifiedSliceSplit(t, j10SingleFixture)
		shown := runJSON(t, ws, "show", id)
		status := shown["challenge"].(map[string]any)["status"]
		split := shown["slice_split"].(map[string]any)
		if item["status"] != "classified" || status != "classified" || split["state"] != "single" {
			t.Errorf("item = %v status = %v slice_split = %v", item, status, split)
		}
	})
	t.Run("split", func(t *testing.T) {
		ws, id, item := setupClassifiedSliceSplit(t, j10SplitFixture)
		shown := runJSON(t, ws, "show", id)
		status := shown["challenge"].(map[string]any)["status"]
		split := shown["slice_split"].(map[string]any)
		if item["status"] != "awaiting_human" || status != "awaiting_human" || split["state"] != "awaiting_owner" ||
			split["evidence_path"] != ".flywheel/requirements/split-"+id+".md" {
			t.Errorf("item = %v status = %v slice_split = %v", item, status, split)
		}
		if _, err := os.Stat(filepath.Join(ws, ".flywheel", "requirements", "split-"+id+".md")); err != nil {
			t.Errorf("evidence file: %v", err)
		}
		holds := shown["holds"].([]any)
		if len(holds) != 1 {
			t.Fatalf("holds = %v, want one", holds)
		}
		var options []string
		for _, o := range holds[0].(map[string]any)["options"].([]any) {
			m := o.(map[string]any)
			options = append(options, fmt.Sprint(m["id"]))
		}
		if want := "split single"; strings.Join(options, " ") != want {
			t.Errorf("options = %v, want %s", options, want)
		}

		code, _, stderr := runCLIRaw(t, "plan", "--auto", id, "--workspace", ws, "--json")
		if code == 0 || !strings.Contains(stderr, "invalid_transition") {
			t.Errorf("plan --auto: exit=%d stderr=%s, want invalid_transition", code, stderr)
		}
	})
}

// flywheel classify --auto は pending の課題に区切りを分ける判断点を呼び、その課題を指定した plan --auto は
// 計画を作る判断点を呼ばず、「着手しなかった」に slice_split_pending を出す。
func TestPlanAuto_PendingSliceSplit_ReportsNotStarted(t *testing.T) {
	parallelTools(t, false)
	ws, id := setupPendingSliceSplit(t)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j2RoutePlan(t)}, "")

	pm := runJSON(t, ws, "plan", "--auto", id)["phase"].(map[string]any)

	if items := pm["items"].([]any); len(items) != 0 {
		t.Errorf("plan items = %v, want none", items)
	}
	ns := phaseNotStarted(t, pm)
	if len(ns) != 1 || ns[0]["challenge_id"] != id || ns[0]["reason"] != "slice_split_pending" {
		t.Errorf("not_started = %v, want slice_split_pending for %s", ns, id)
	}
}

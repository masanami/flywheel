package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは、core の見直しの区切り S1（docs/features/core-redesign.md）の受入基準のうち宣言の古い項目を止める分を、基準ごとに CLI の口で確かめる。
// サブテスト名の先頭が基準の番号（AC-<n>）。番号は masanami/flywheel#732 の受入基準の並び順で、ほかの仕様の同じ番号とは無関係。

// removedKeyCase は、無くした宣言の項目 1 つを、その項目が元あった位置に置いた宣言。
type removedKeyCase struct {
	location string // 項目の位置（表示用）
	key      string // エラーの文に出るはずの項目名
	// acInvalid・acNamed・acRemoved は、それぞれ config_invalid・文に項目名・文に removed の基準の番号。
	acInvalid, acNamed, acRemoved string
	write                         func(t *testing.T, ws string)
}

func removedKeyCases() []removedKeyCase {
	connectors := func(old, replacement string) func(t *testing.T, ws string) {
		return func(t *testing.T, ws string) {
			t.Helper()
			if !strings.Contains(j2ConnectorsFixture, old) {
				t.Fatalf("fixture lacks %q", old)
			}
			writeConnectorsJSONForTest(t, ws, strings.Replace(j2ConnectorsFixture, old, replacement, 1))
		}
	}
	agent := func(extra string) func(t *testing.T, ws string) {
		return func(t *testing.T, ws string) {
			t.Helper()
			writeAgentJSONForTest(t, ws, `{"position_file": "position.md", `+extra+`}`)
		}
	}
	return []removedKeyCase{
		{"connectors.json connectors[]", "conflict_prediction", "AC-12", "AC-18", "AC-24",
			connectors(`{"id": "direct", "form": "brief",`,
				`{"id": "direct", "conflict_prediction": {"command": ["predict", "--json"], "schema": "harness.conflict-prediction/v1"}, "form": "brief",`)},
		{"connectors.json repos[]", "serial_judgment", "AC-13", "AC-19", "AC-25",
			connectors(`"connector": "direct"}`, `"connector": "direct", "serial_judgment": true}`)},
		{"connectors.json repos[]", "conflict_exclude_paths", "AC-14", "AC-20", "AC-26",
			connectors(`"connector": "direct"}`, `"connector": "direct", "conflict_exclude_paths": ["docs/**"]}`)},
		{"agent.json", "conflict_prediction_budget_usd", "AC-15", "AC-21", "AC-27",
			agent(`"conflict_prediction_budget_usd": 1`)},
		{"agent.json", "serial_judgment_cycle_budget_usd", "AC-16", "AC-22", "AC-28",
			agent(`"serial_judgment_cycle_budget_usd": 3`)},
		{"agent.json judgment_budget_usd", "J9", "AC-17", "AC-23", "AC-29",
			agent(`"judgment_budget_usd": {"J9": 0.5}`)},
	}
}

// cycleErrorWithRemovedKey は、c の宣言を置いたワークスペースで cycle --json を実行し、
// 終了コードと、標準エラー出力のエラーの code・message を返す。
func cycleErrorWithRemovedKey(t *testing.T, c removedKeyCase) (exit int, code, message string) {
	t.Helper()
	ws := setupJ2Workspace(t)
	createTitled(t, ws, "t")
	c.write(t, ws)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")
	exit, stdout, stderr := runCLIRaw(t, "cycle", "--workspace", ws, "--json")
	if stdout != "" {
		t.Fatalf("stdout should stay empty on failure, got %q", stdout)
	}
	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &doc); err != nil {
		t.Fatalf("stderr is not one JSON document: %v (%q)", err, stderr)
	}
	return exit, doc.Error.Code, doc.Error.Message
}

// AC-12〜AC-17: 無くした項目が残っている宣言は、終了コード 2・config_invalid で止まる。
// AC-18〜AC-23: そのエラーの文に項目名が含まれる。
// AC-24〜AC-29: そのエラーの文に removed が含まれる（知らないキーを拒む unknown key の文ではない）。
func TestCycle_RemovedDeclarationKeys_ConfigInvalidNamingTheKeyAndRemoved(t *testing.T) {
	parallelTools(t, false)
	for _, c := range removedKeyCases() {
		t.Run(c.acInvalid+" "+c.location+" の "+c.key+" は config_invalid", func(t *testing.T) {
			exit, code, _ := cycleErrorWithRemovedKey(t, c)
			if exit != 2 || code != string(CodeConfigInvalid) {
				t.Fatalf("exit=%d code=%q, want 2 %s", exit, code, CodeConfigInvalid)
			}
		})
		t.Run(c.acNamed+" "+c.location+" の "+c.key+" の文に項目名", func(t *testing.T) {
			_, _, msg := cycleErrorWithRemovedKey(t, c)
			if !strings.Contains(msg, c.key) {
				t.Fatalf("message = %q, want it to contain %q", msg, c.key)
			}
		})
		t.Run(c.acRemoved+" "+c.location+" の "+c.key+" の文に removed", func(t *testing.T) {
			_, _, msg := cycleErrorWithRemovedKey(t, c)
			if !strings.Contains(msg, "removed") || strings.Contains(msg, "unknown key") {
				t.Fatalf("message = %q, want it to say removed (not unknown key)", msg)
			}
		})
	}
}

const legacyRunAt = "2026-10-01T00:00:00.000Z"

// insertLegacyPredictRun は、過去の記録として残っている kind = predict の終わった run を生の行で置く
// （新しく作る経路は無い）。
func insertLegacyPredictRun(t *testing.T, ws string) {
	t.Helper()
	coretest.Exec(t, ws,
		`INSERT INTO run (kind, pid, host, heartbeat_at, started_at, ended_at, result, rate_limited, max_budget_usd, budget_bucket, repo, cost_usd, cost_source)
		 VALUES ('predict', 7, 'h', ?, ?, ?, 'succeeded', 0, 100000, 'predict', 'o/r', 2000, 'reported')`,
		legacyRunAt, legacyRunAt, legacyRunAt)
}

// insertLegacyJ9Run は、過去の記録として残っている judgment = J9 の終わった run を生の行で置く。
func insertLegacyJ9Run(t *testing.T, ws string) {
	t.Helper()
	coretest.Exec(t, ws,
		`INSERT INTO run (kind, judgment, session_id, pid, host, heartbeat_at, started_at, ended_at, result, rate_limited, max_budget_usd, budget_bucket, cost_usd, cost_source)
		 VALUES ('judgment', 'J9', 's', 1, 'h', ?, ?, ?, 'succeeded', 0, 500000, 'judgment', 1000, 'reported')`,
		legacyRunAt, legacyRunAt, legacyRunAt)
}

// runsWithField は runs --json の出力のうち、field の値が want の run の数を返す。
func runsWithField(t *testing.T, stdout, field, want string) int {
	t.Helper()
	n := 0
	for _, r := range decodeJSON(t, stdout)["runs"].([]any) {
		if v, _ := r.(map[string]any)[field].(string); v == want {
			n++
		}
	}
	return n
}

// AC-35・AC-36・AC-40・AC-41: 過去の predict・J9 の run があるストアで、runs --json は終了コード 0 で
// 終わり、その run を元の値のまま出す。
func TestRuns_LegacyPredictAndJ9Runs_AreListedWithTheirValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		acExit, acValue string
		insert          func(t *testing.T, ws string)
		field, want     string
	}{
		{"AC-35", "AC-36", insertLegacyPredictRun, "kind", "predict"},
		{"AC-40", "AC-41", insertLegacyJ9Run, "judgment", "J9"},
	}
	for _, c := range cases {
		t.Run(c.acExit+" "+c.field+"="+c.want+" の run があるストアで runs --json が終了コード 0", func(t *testing.T) {
			t.Parallel()
			ws := initializedWorkspace(t)
			c.insert(t, ws)
			if code, _, stderr := runCLIRaw(t, "runs", "--workspace", ws, "--json"); code != 0 {
				t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr)
			}
		})
		t.Run(c.acValue+" runs --json にその run が "+c.field+"="+c.want+" で出る", func(t *testing.T) {
			t.Parallel()
			ws := initializedWorkspace(t)
			c.insert(t, ws)
			stdout, _ := runOK(t, "runs", "--workspace", ws, "--json")
			if got := runsWithField(t, stdout, c.field, c.want); got != 1 {
				t.Fatalf("runs with %s=%s = %d, want 1 (%s)", c.field, c.want, got, stdout)
			}
		})
	}
}

// AC-42: 過去の predict の run と J9 の run があるストアで、metrics --json は終了コード 0 で終わる。
func TestMetrics_LegacyPredictAndJ9Runs_ExitZero(t *testing.T) {
	t.Parallel()
	t.Run("AC-42 kind=predict と judgment=J9 の run があるストアで metrics --json が終了コード 0", func(t *testing.T) {
		t.Parallel()
		ws := initializedWorkspace(t)
		insertLegacyPredictRun(t, ws)
		insertLegacyJ9Run(t, ws)
		code, stdout, stderr := runCLIRaw(t, "metrics", "--workspace", ws, "--json")
		if code != 0 {
			t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr)
		}
		decodeJSON(t, stdout)
	})
}

// AC-43: 過去の predict の run と J9 の run があるストアを export で書き出し、その書き出しから
// 再構築（import）したストアに、2 つの run がある。
func TestExportImport_LegacyPredictAndJ9Runs_SurviveTheRoundTrip(t *testing.T) {
	t.Parallel()
	t.Run("AC-43 export と再構築の後も kind=predict と judgment=J9 の run がある", func(t *testing.T) {
		t.Parallel()
		src := initializedWorkspace(t)
		insertLegacyPredictRun(t, src)
		insertLegacyJ9Run(t, src)
		path := filepath.Join(t.TempDir(), "export.json")
		runJSON(t, src, "export", "--out", path)

		dst := t.TempDir()
		if _, err := core.Init(dst); err != nil {
			t.Fatal(err)
		}
		runJSON(t, dst, "import", "--in", path)

		stdout, _ := runOK(t, "runs", "--workspace", dst, "--json")
		if n := len(decodeJSON(t, stdout)["runs"].([]any)); n != 2 {
			t.Fatalf("runs in the rebuilt store = %d, want 2 (%s)", n, stdout)
		}
		if runsWithField(t, stdout, "kind", "predict") != 1 || runsWithField(t, stdout, "judgment", "J9") != 1 {
			t.Fatalf("the rebuilt store lacks the predict run or the J9 run: %s", stdout)
		}
	})
}

// AC-38: 終わっていない predict の run（心拍が古く、起動したプロセスが残っていない）があるストアで
// サイクルを回すと、その run は「中断」で終わり、サイクルは失敗しない。
func TestCycle_UnfinishedLegacyPredictRun_IsInterruptedAndTheCycleCompletes(t *testing.T) {
	parallelTools(t, false)
	t.Run("AC-38 終わっていない predict の run が中断で終わりサイクルは失敗しない", func(t *testing.T) {
		ws := setupWorkspaceWithPosition(t)
		createTitled(t, ws, "t")
		coretest.Exec(t, ws,
			`INSERT INTO run (kind, pid, host, heartbeat_at, started_at, rate_limited, max_budget_usd, budget_bucket, repo)
			 VALUES ('predict', ?, ?, '2026-09-28T00:00:00.000Z', '2026-09-28T00:00:00.000Z', 0, 100000, 'predict', 'o/r')`,
			deadPID(t), currentHost(t))
		if before := coretest.DumpTables(t, ws, "run"); strings.Contains(before, "interrupted") {
			t.Fatalf("the predict run is already ended before the cycle: %s", before)
		}
		putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")

		doc := cycleDoc(t, ws) // 終了コード 0

		if got := doc["cycle"].(map[string]any)["result"]; got != "completed" {
			t.Errorf("cycle.result = %v, want completed", got)
		}
		// サイクルが終わらせたことを、閲覧のコマンドを挟まずに生の行で確かめる。
		if after := coretest.DumpTables(t, ws, "run"); !strings.Contains(after, "interrupted") {
			t.Fatalf("the predict run was not ended as interrupted by the cycle: %s", after)
		}
		stdout, _ := runOK(t, "runs", "--workspace", ws, "--json")
		var found bool
		for _, r := range decodeJSON(t, stdout)["runs"].([]any) {
			m := r.(map[string]any)
			if m["kind"] == "predict" {
				found = true
				if m["result"] != "interrupted" {
					t.Errorf("predict run result = %v, want interrupted", m["result"])
				}
			}
		}
		if !found {
			t.Fatalf("runs --json lacks the predict run: %s", stdout)
		}
	})
}

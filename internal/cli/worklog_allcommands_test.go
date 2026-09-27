package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは Issue #30 の受入基準（AC-63・AC-64）を、登録表
// （defaultCommands()）と遷移表（core.Table）から導いた全操作について検証する。
//
//   - AC-63: 状態・人間記入欄・計画・保留・承認・不可逆操作を変える操作が成功する
//     たびに、日時・actor・経路・本人確認の方式・対象・操作・変更前・変更後を持つ
//     エントリが作業ログに追加される
//   - AC-64: 本人確認のない操作のエントリは、本人確認の方式が none である
//
// 「変更系のコマンド」の判定基準: AC-63 が列挙する対象（課題の状態・人間記入欄・
// 計画・保留・承認・不可逆操作）のいずれかを変えうるコマンド。そうでないコマンド
// （worklogReadOnlyCommands）は、成功経路（allCommandSuccessCases）で作業ログを
// 増やさずストアのファイルも変えないことを
// TestWorklog_ReadOnlyCommandsAddNoActivityAndLeaveStoreUnchanged で確かめる。init は
// ストアを作るが課題も不可逆操作もまだ無く、作業ログの対象を変えないため後者に入れる。
//
// 取りこぼしを防ぐ仕組み:
//   - 登録表の全コマンドが、変更系（worklogCases に 1 件以上）か
//     worklogReadOnlyCommands のちょうど一方に載る（TestWorklog_CasesCoverRegistrationTable）
//   - 遷移表 core.Table の全行（T11 は遷移元ごと）が worklogCases に載る
//     （TestWorklog_CasesCoverEveryTransitionTableRow）
//   - 各ケースの command は実行する引数列と、遷移表の行は記録された操作・状態と照合する
//   - 本人確認の方式の期待値はケースごとに書かず、コマンドが本人確認つきか
//     （allCommandSuccessCases の needsTTY。違えば tty_required で落ちる）から導き、
//     遷移表の RequiresVerification とも照合する

// worklogReadOnlyCommands は変更系でないコマンド（上の判定基準）。
var worklogReadOnlyCommands = map[string]bool{
	"init":   true,
	"show":   true,
	"list":   true,
	"status": true,
	"log":    true,
}

// worklogIngestRepo は worklogCases の "ingest create" ケースが使う偽の
// リポジトリ名（#59 で取得と反映を結線した後、ingest は変更系のコマンドに
// なった。以前はここに列挙し worklogReadOnlyCommands 側だった）。
const worklogIngestRepo = "owner/worklog-repo"

// wantActivity は作業ログの 1 エントリの期待値。before・after が nil なら
// JSON の null を期待する。actor・channel・verification・at は全エントリに
// 共通の規則で検査するため、ここには持たない。
type wantActivity struct {
	entity   string
	entityID string
	action   string
	before   map[string]any
	after    map[string]any
}

// worklogCase は 1 つの変更操作の成功経路。setup はワークスペース ws を必要な
// 状態まで進め、--workspace/--json を除いた引数列を返す（引数列の
// len(Path) 番目は対象の ID。本人確認つきのコマンドはそれを確認入力に使う）。
// transition は遷移表の行を指す（遷移を伴わない edit・op add・不可逆操作の
// ID への approve/reject は空）。want は、この操作で作業ログに追加される
// エントリを追加順に並べたもの。
type worklogCase struct {
	name       string
	command    string
	transition *core.Transition
	setup      func(t *testing.T, ws string) []string
	want       func(actor string) []wantActivity
}

func tableRow(id string, from core.Status) *core.Transition {
	for i := range core.Table {
		if core.Table[i].ID == id && core.Table[i].From == from {
			return &core.Table[i]
		}
	}
	return &core.Transition{ID: id + " (not in core.Table)", From: from}
}

// awaitingCompletionWithReleasesForCase は完了確認待ちの課題 C-1 と、その未承認の
// release OP-1・OP-2 を用意する（版: create=1 → verify met=2 → op add=3 → op add=4）。
func awaitingCompletionWithReleasesForCase(t *testing.T, ws string) string {
	t.Helper()
	id := createWithStatusForCase(t, ws, "verifying")
	runJSON(t, ws, "verify", id, "--result", "met")
	runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "s1")
	runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "s2")
	return id
}

func holdFromCase(from string) worklogCase {
	return worklogCase{
		name:       "T11 hold from " + from,
		command:    "hold",
		transition: tableRow("T11", core.Status(from)),
		setup: func(t *testing.T, ws string) []string {
			return []string{"hold", createWithStatusForCase(t, ws, from), "--question", "q"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "hold",
				map[string]any{"status": from},
				map[string]any{"question": "q", "status": "awaiting_human", "version": 2.0}}}
		},
	}
}

var worklogCases = []worklogCase{
	{
		name: "T1 create", command: "create", transition: tableRow("T1", core.NoStatus),
		setup: func(_ *testing.T, _ string) []string {
			return []string{"create", "--title", "t", "--description", "d", "--done-criteria", "c", "--urgency", "高"}
		},
		want: func(actor string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "create", nil,
				map[string]any{"title": "t", "description": "d", "done_criteria": "c", "urgency": "高", "reporter": actor, "status": "unclassified"}}}
		},
	},
	{
		name: "edit", command: "edit",
		setup: func(t *testing.T, ws string) []string {
			return []string{"edit", createForCase(t, ws), "--title", "x", "--description", "d1", "--done-criteria", "d2", "--urgency", "低"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "edit",
				map[string]any{"title": "t", "description": "", "done_criteria": "d", "urgency": nil},
				map[string]any{"title": "x", "description": "d1", "done_criteria": "d2", "urgency": "低", "version": 2.0}}}
		},
	},
	{
		name: "T2 classify", command: "classify", transition: tableRow("T2", core.StatusUnclassified),
		setup: func(t *testing.T, ws string) []string {
			return []string{"classify", createForCase(t, ws), "--priority", "P1"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "classify",
				map[string]any{"priority": nil, "status": "unclassified"},
				map[string]any{"priority": "P1", "status": "classified", "version": 2.0}}}
		},
	},
	{
		name: "T3 plan", command: "plan", transition: tableRow("T3", core.StatusClassified),
		setup: func(t *testing.T, ws string) []string {
			id := createForCase(t, ws)
			runJSON(t, ws, "classify", id, "--priority", "P0")
			return []string{"plan", id, "--file", writePlanFileForTest(t, "do it")}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "plan",
				// S3（Issue #39）: 未設定（計画が無い）から値が入る初回は、
				// before にそのキーを null で載せる（classify の priority・edit
				// の urgency とそろえる）。
				map[string]any{"status": "classified", "plan_version": nil},
				map[string]any{"plan_version": 1.0, "status": "awaiting_plan_approval", "version": 3.0}}}
		},
	},
	{
		name: "T4 plan (resubmit)", command: "plan", transition: tableRow("T4", core.StatusAwaitingPlanApproval),
		setup: func(t *testing.T, ws string) []string {
			return []string{"plan", createPlannedForCase(t, ws), "--file", writePlanFileForTest(t, "do it again")}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "plan",
				map[string]any{"plan_version": 1.0},
				map[string]any{"plan_version": 2.0, "version": 4.0}}}
		},
	},
	{
		name: "T5 approve (plan)", command: "approve", transition: tableRow("T5", core.StatusAwaitingPlanApproval),
		setup: func(t *testing.T, ws string) []string {
			return []string{"approve", createPlannedForCase(t, ws)}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "approve",
				map[string]any{"status": "awaiting_plan_approval"},
				map[string]any{"approval_kind": "plan", "decision": "approved", "status": "in_progress", "target_version": 3.0, "version": 4.0}}}
		},
	},
	{
		name: "T6 reject (plan)", command: "reject", transition: tableRow("T6", core.StatusAwaitingPlanApproval),
		setup: func(t *testing.T, ws string) []string {
			return []string{"reject", createPlannedForCase(t, ws), "--reason", "r"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "reject",
				map[string]any{"status": "awaiting_plan_approval"},
				map[string]any{"approval_kind": "plan", "decision": "rejected", "reason": "r", "status": "classified", "target_version": 3.0, "version": 4.0}}}
		},
	},
	{
		name: "T7 submit", command: "submit", transition: tableRow("T7", core.StatusInProgress),
		setup: func(t *testing.T, ws string) []string {
			return []string{"submit", createWithStatusForCase(t, ws, "in_progress")}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "submit",
				map[string]any{"status": "in_progress"},
				map[string]any{"status": "verifying", "version": 2.0}}}
		},
	},
	{
		name: "T8 verify met", command: "verify", transition: tableRow("T8", core.StatusVerifying),
		setup: func(t *testing.T, ws string) []string {
			return []string{"verify", createWithStatusForCase(t, ws, "verifying"), "--result", "met"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "verify_met",
				map[string]any{"status": "verifying"},
				map[string]any{"status": "awaiting_completion_approval", "version": 2.0}}}
		},
	},
	{
		name: "T9 verify not_met", command: "verify", transition: tableRow("T9", core.StatusVerifying),
		setup: func(t *testing.T, ws string) []string {
			return []string{"verify", createWithStatusForCase(t, ws, "verifying"), "--result", "not_met"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "verify_not_met",
				map[string]any{"status": "verifying"},
				map[string]any{"status": "in_progress", "version": 2.0}}}
		},
	},
	{
		name: "T10 verify uncertain", command: "verify", transition: tableRow("T10", core.StatusVerifying),
		setup: func(t *testing.T, ws string) []string {
			return []string{"verify", createWithStatusForCase(t, ws, "verifying"), "--result", "uncertain", "--question", "q"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "verify_uncertain",
				map[string]any{"status": "verifying"},
				map[string]any{"question": "q", "status": "awaiting_human", "version": 2.0}}}
		},
	},
	holdFromCase("unclassified"),
	holdFromCase("classified"),
	holdFromCase("in_progress"),
	holdFromCase("verifying"),
	{
		name: "T12 answer", command: "answer", transition: tableRow("T12", core.StatusAwaitingHuman),
		setup: func(t *testing.T, ws string) []string {
			id := createWithStatusForCase(t, ws, "verifying")
			runJSON(t, ws, "verify", id, "--result", "uncertain", "--question", "q")
			return []string{"answer", id, "--answer", "a"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "answer",
				// S3（Issue #39）: 未設定（保留の answer が無い）から値が入る
				// ので、before にそのキーを null で載せる。
				map[string]any{"status": "awaiting_human", "answer": nil},
				map[string]any{"answer": "a", "status": "verifying", "version": 3.0}}}
		},
	},
	{
		name: "T13 approve (completion, D12 with release)", command: "approve", transition: tableRow("T13", core.StatusAwaitingCompletionApproval),
		setup: func(t *testing.T, ws string) []string {
			id := awaitingCompletionWithReleasesForCase(t, ws)
			return []string{"approve", id}
		},
		want: func(string) []wantActivity {
			return []wantActivity{
				// D12: release ごとの承認と完了の承認を別々のエントリとして残す。
				// 仕様はエントリの順を定めていないが、ここでは実装の記録順
				// （不可逆操作が先・課題が後）を固定する。順を変えたらここを直す。
				{"operation", "OP-1", "approve",
					map[string]any{"state": "pending"},
					map[string]any{"state": "approved", "version": 2.0}},
				{"operation", "OP-2", "approve",
					map[string]any{"state": "pending"},
					map[string]any{"state": "approved", "version": 2.0}},
				{"challenge", "C-1", "approve",
					map[string]any{"status": "awaiting_completion_approval"},
					map[string]any{"approval_kind": "completion", "decision": "approved", "status": "done", "target_version": 4.0, "version": 5.0}},
			}
		},
	},
	{
		name: "T14 approve --hold-release", command: "approve", transition: tableRow("T14", core.StatusAwaitingCompletionApproval),
		setup: func(t *testing.T, ws string) []string {
			id := awaitingCompletionWithReleasesForCase(t, ws)
			return []string{"approve", id, "--hold-release"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "approve_hold_release",
				map[string]any{"status": "awaiting_completion_approval"},
				map[string]any{"approval_kind": "completion", "decision": "approved", "status": "done", "target_version": 4.0, "version": 5.0}}}
		},
	},
	{
		name: "T15 reject (completion)", command: "reject", transition: tableRow("T15", core.StatusAwaitingCompletionApproval),
		setup: func(t *testing.T, ws string) []string {
			id := awaitingCompletionWithReleasesForCase(t, ws)
			return []string{"reject", id, "--reason", "r"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "reject",
				map[string]any{"status": "awaiting_completion_approval"},
				map[string]any{"approval_kind": "completion", "decision": "rejected", "reason": "r", "status": "in_progress", "target_version": 4.0, "version": 5.0}}}
		},
	},
	{
		name: "op add", command: "op add",
		setup: func(t *testing.T, ws string) []string {
			return []string{"op", "add", createForCase(t, ws), "--kind", "release", "--summary", "s", "--ref", "https://example.com/pr/1"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{
				{"operation", "OP-1", "op_add", nil,
					map[string]any{"kind": "release", "operation_id": "OP-1", "ref": "https://example.com/pr/1", "state": "pending", "summary": "s"}},
				// S2（Issue #39）: op add は課題の版も上げるため、その課題自身の
				// entity="challenge" のエントリも同じ操作で残る。
				{"challenge", "C-1", "op_add", nil,
					map[string]any{"operation_id": "OP-1", "version": 2.0}},
			}
		},
	},
	{
		name: "op add (no ref)", command: "op add",
		setup: func(t *testing.T, ws string) []string {
			return []string{"op", "add", createForCase(t, ws), "--kind", "external_send", "--summary", "s"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{
				{"operation", "OP-1", "op_add", nil,
					map[string]any{"kind": "external_send", "operation_id": "OP-1", "ref": nil, "state": "pending", "summary": "s"}},
				{"challenge", "C-1", "op_add", nil,
					map[string]any{"operation_id": "OP-1", "version": 2.0}},
			}
		},
	},
	{
		name: "approve <OP-ID>", command: "approve",
		setup: func(t *testing.T, ws string) []string {
			op := runJSON(t, ws, "op", "add", createForCase(t, ws), "--kind", "release", "--summary", "s")
			return []string{"approve", op["operation"].(map[string]any)["id"].(string)}
		},
		want: func(string) []wantActivity {
			return []wantActivity{
				{"operation", "OP-1", "approve",
					map[string]any{"state": "pending"},
					map[string]any{"state": "approved", "version": 2.0}},
				// S2（Issue #39）: approve <OP-ID> も課題の版を上げるため、その
				// 課題自身の entity="challenge" のエントリも同じ操作で残る
				// （action は entity=operation 側の approve と紛れないよう
				// op_approve を使う）。
				{"challenge", "C-1", "op_approve", nil,
					map[string]any{"operation_id": "OP-1", "version": 3.0}},
			}
		},
	},
	{
		name: "reject <OP-ID>", command: "reject",
		setup: func(t *testing.T, ws string) []string {
			op := runJSON(t, ws, "op", "add", createForCase(t, ws), "--kind", "release", "--summary", "s")
			return []string{"reject", op["operation"].(map[string]any)["id"].(string), "--reason", "r"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{
				{"operation", "OP-1", "reject",
					map[string]any{"state": "pending"},
					map[string]any{"reason": "r", "state": "rejected", "version": 2.0}},
				{"challenge", "C-1", "op_reject", nil,
					map[string]any{"operation_id": "OP-1", "version": 3.0}},
			}
		},
	},
	{
		// #72: mark-read は未読の更新があるとき、読んだ時点の値を現在の
		// 観測値で上書きし upstream_read を記録する（docs/features/
		// m2-github-issue-ingest.md §上流の更新の観測と既読）。
		name: "mark-read", command: "mark-read",
		setup: func(t *testing.T, ws string) []string {
			id := createForCase(t, ws)
			bindSourceForDiscrepancyCase(t, ws, id, "o/r#1", "open", "in_policy")
			coretest.SetSourceBindingObservation(t, ws, challengeIDToInternalID(t, id), 3, "2026-09-25T08:00:00.000Z", 0, "")
			return []string{"mark-read", id}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "upstream_read",
				map[string]any{"read_comments_count": 0, "read_upstream_updated_at": nil},
				map[string]any{"read_comments_count": 3, "read_upstream_updated_at": "2026-09-25T08:00:00.000Z", "version": 2.0}}}
		},
	},
	{
		// #59: ingest は取得と反映を結線した後、対応の無い・ポリシーに合う
		// Issue から課題を作るたびに ingest_create を記録する（§作業ログと版）。
		name: "ingest create", command: "ingest",
		setup: func(t *testing.T, ws string) []string {
			decl := `{
  "version": 1,
  "sources": [
    {"id": "worklog-source", "type": "github-issue", "repos": ["` + worklogIngestRepo + `"], "self_assignees": ["someone"]}
  ]
}`
			writeSourcesDeclaration(t, ws, decl)
			withFakeGHRoutesOnPATH(t, []fakeGHRoute{
				fakeGHListRoute(worklogIngestRepo, 1, fakeGHIssueListBody(t, []fakeGHIssue{
					{Number: 1, Title: "ingest smoke title", Body: "ingest smoke body", Reporter: "reporter", Repo: worklogIngestRepo},
				}), 0),
			})
			return []string{"ingest"}
		},
		want: func(string) []wantActivity {
			return []wantActivity{{"challenge", "C-1", "ingest_create", nil,
				map[string]any{
					"title":         "ingest smoke title",
					"description":   "ingest smoke body",
					"done_criteria": "",
					"urgency":       nil,
					"status":        "unclassified",
					"reporter":      "reporter",
					"external_key":  worklogIngestRepo + "#1",
				}}}
		},
	},
}

func TestWorklog_CasesCoverRegistrationTable(t *testing.T) {
	registered := registeredCommands()
	mutating := map[string]bool{}
	for _, c := range worklogCases {
		if _, ok := registered[c.command]; !ok {
			t.Errorf("worklogCases %q names %q, which is not a registered command", c.name, c.command)
		}
		mutating[c.command] = true
	}
	for name := range worklogReadOnlyCommands {
		if _, ok := registered[name]; !ok {
			t.Errorf("worklogReadOnlyCommands has %q, which is not a registered command (stale entry?)", name)
		}
	}
	for _, name := range sortedCommandNames(registered) {
		switch {
		case mutating[name] && worklogReadOnlyCommands[name]:
			t.Errorf("command %q is both in worklogCases and worklogReadOnlyCommands", name)
		case !mutating[name] && !worklogReadOnlyCommands[name]:
			t.Errorf("registered command %q is neither covered by worklogCases nor listed in worklogReadOnlyCommands", name)
		}
	}
}

func TestWorklog_CasesCoverEveryTransitionTableRow(t *testing.T) {
	covered := map[core.Transition]bool{}
	for _, c := range worklogCases {
		if c.transition != nil {
			covered[*c.transition] = true
		}
	}
	for _, row := range core.Table {
		if !covered[row] {
			t.Errorf("transition %s (from %q, op %q) has no case in worklogCases", row.ID, row.From, row.Op)
		}
	}
}

// runCommandForWorklog は args（--workspace/--json を除く）を実行し、成功を
// 確かめる。本人確認つきのコマンドは疑似端末の子プロセスで、対象の ID を
// 確認入力として与えて実行する。
func runCommandForWorklog(t *testing.T, command string, args []string, ws string) {
	t.Helper()
	full := append(append([]string{}, args...), "--workspace", ws, "--json")
	if allCommandSuccessCases[command].needsTTY {
		target := args[len(strings.Fields(command))]
		code, pty, stdout, stderr := runConfirmedChild(t, full, target)
		if code != 0 {
			t.Fatalf("args=%v exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", args, code, stdout, stderr, pty)
		}
		return
	}
	var stdout, stderr bytes.Buffer
	if code := run(full, strings.NewReader(""), &stdout, &stderr, defaultCommands()); code != 0 {
		t.Fatalf("args=%v exit=%d, want 0 (stderr=%s)", args, code, stderr.String())
	}
}

// allActivities は `log --json`（課題 ID なし＝全件）の activities を返す。
func allActivities(t *testing.T, ws string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, a := range runJSON(t, ws, "log")["activities"].([]any) {
		out = append(out, a.(map[string]any))
	}
	return out
}

// requireJSONEqual は got（log --json の before/after）が want と同じ JSON 値で
// あることを検査する。want が nil なら null を期待する。
func requireJSONEqual(t *testing.T, what string, got any, want map[string]any) {
	t.Helper()
	var wantVal any
	if want != nil {
		b, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal want: %v", err)
		}
		if err := json.Unmarshal(b, &wantVal); err != nil {
			t.Fatalf("unmarshal want: %v", err)
		}
	}
	if !reflect.DeepEqual(got, wantVal) {
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(wantVal)
		t.Errorf("%s = %s, want %s", what, gb, wb)
	}
}

// AC-63・AC-64: 変更系の全操作について、成功のたびに追加される作業ログの
// エントリを全項目（日時・actor・経路・本人確認の方式・対象・操作・変更前・
// 変更後）で検査する。本人確認の方式は、本人確認つきのコマンド（approve・
// reject・answer）なら tty_confirm、それ以外（本人確認のない操作）なら none。
func TestWorklog_EveryMutatingOperationRecordsAllFields(t *testing.T) {
	actor := resolveExpectedActor(t)
	for _, c := range worklogCases {
		t.Run(c.name, func(t *testing.T) {
			// 期待値は core の定数ではなく仕様の文字列で書く（定数の値が変わっても
			// 気付けるように）。
			wantVerification := "none"
			if allCommandSuccessCases[c.command].needsTTY {
				wantVerification = "tty_confirm"
			}
			if c.transition != nil && c.transition.RequiresVerification != (wantVerification == "tty_confirm") {
				t.Fatalf("transition %s RequiresVerification=%v, but command %q needsTTY=%v", c.transition.ID, c.transition.RequiresVerification, c.command, allCommandSuccessCases[c.command].needsTTY)
			}

			ws := initializedWorkspace(t)
			args := c.setup(t, ws)
			if cmd, _, ok := matchCommand(args, defaultCommands()); !ok || strings.Join(cmd.Path, " ") != c.command {
				t.Fatalf("case command %q does not match the executed arguments %v", c.command, args)
			}
			before := allActivities(t, ws)

			start := time.Now().UTC().Truncate(time.Millisecond)
			runCommandForWorklog(t, c.command, args, ws)
			end := time.Now().UTC()

			added := allActivities(t, ws)[len(before):]
			want := c.want(actor)
			if len(added) != len(want) {
				t.Fatalf("added %d activities, want %d: %v", len(added), len(want), added)
			}
			for i, w := range want {
				a := added[i]
				label := func(field string) string { return fmt.Sprintf("activity[%d].%s", i, field) }
				atStr, _ := a["at"].(string)
				at, err := time.Parse(time.RFC3339Nano, atStr)
				if err != nil || FormatTimestamp(at) != atStr {
					t.Errorf("%s = %v, want the work-log timestamp format (err=%v)", label("at"), a["at"], err)
				} else if at.Before(start) || at.After(end) {
					t.Errorf("%s = %v, want within the command run [%v, %v]", label("at"), a["at"], start, end)
				}
				if a["actor"] != actor {
					t.Errorf("%s = %v, want %q", label("actor"), a["actor"], actor)
				}
				if a["channel"] != "cli" {
					t.Errorf("%s = %v, want %q", label("channel"), a["channel"], "cli")
				}
				if a["verification"] != wantVerification {
					t.Errorf("%s = %v, want %q", label("verification"), a["verification"], wantVerification)
				}
				if a["entity"] != w.entity || a["entity_id"] != w.entityID {
					t.Errorf("%s = %v %v, want %s %s", label("entity/entity_id"), a["entity"], a["entity_id"], w.entity, w.entityID)
				}
				if a["action"] != w.action {
					t.Errorf("%s = %v, want %q", label("action"), a["action"], w.action)
				}
				requireJSONEqual(t, label("before"), a["before"], w.before)
				requireJSONEqual(t, label("after"), a["after"], w.after)
			}
			// 遷移を伴う操作は、課題のエントリの変更前・変更後の状態が遷移表の
			// 遷移元・遷移先と一致する（手書きの期待値と遷移表のずれを検出する）。
			// 状態が変わらない遷移（T4）は、変わった項目だけを持つため status を含まない。
			if c.transition != nil {
				requireStatusMatchesTransition(t, *c.transition, want)
			}
		})
	}
}

// 変更系でないコマンドは、成功しても作業ログを増やさず、ストアのファイルも
// 変えない（変更系を worklogReadOnlyCommands へ誤って分類すると落ちる）。
func TestWorklog_ReadOnlyCommandsAddNoActivityAndLeaveStoreUnchanged(t *testing.T) {
	for _, name := range sortedCommandNames(registeredCommands()) {
		if !worklogReadOnlyCommands[name] {
			continue
		}
		tc := allCommandSuccessCases[name]
		t.Run(name, func(t *testing.T) {
			ws := t.TempDir()
			if !tc.uninitialized {
				if _, err := core.Init(ws); err != nil {
					t.Fatalf("core.Init: %v", err)
				}
			}
			args := tc.setup(t, ws)
			var before []map[string]any
			var storeBefore []byte
			dbPath := filepath.Join(ws, ".flywheel", "flywheel.db")
			if !tc.uninitialized {
				before = allActivities(t, ws)
				var err error
				if storeBefore, err = os.ReadFile(dbPath); err != nil {
					t.Fatalf("read store before: %v", err)
				}
			}
			runCommandForWorklog(t, name, args, ws)
			// ファイルの比較は log（作業ログの読み出し）を挟まずに行う。
			if !tc.uninitialized {
				storeAfter, err := os.ReadFile(dbPath)
				if err != nil {
					t.Fatalf("read store after: %v", err)
				}
				if !bytes.Equal(storeBefore, storeAfter) {
					t.Errorf("%s changed the store file", name)
				}
			}
			if added := allActivities(t, ws)[len(before):]; len(added) != 0 {
				t.Errorf("%s added %d activities, want 0: %v", name, len(added), added)
			}
		})
	}
}

// requireStatusMatchesTransition は、want のうち課題のエントリの status の
// 変更前・変更後が、遷移表の行 tr の遷移元・遷移先と一致することを検査する。
func requireStatusMatchesTransition(t *testing.T, tr core.Transition, want []wantActivity) {
	t.Helper()
	to := tr.Target.Fixed
	if tr.Target.ReturnToPrecedingHoldStatus {
		// T12 は「保留に入る直前の状態」へ戻る。T12 のケースの setup は
		// verifying から保留に入れている（setup を変えたらここも直す）。
		to = core.StatusVerifying
	}
	var beforeStatus, afterStatus any
	found := false
	for _, w := range want {
		if w.entity == "challenge" {
			if w.action != string(tr.Op) {
				t.Errorf("transition %s: want action %q, but the table says %q", tr.ID, w.action, tr.Op)
			}
			beforeStatus, afterStatus, found = w.before["status"], w.after["status"], true
			break
		}
	}
	if !found {
		t.Fatalf("transition %s: want has no challenge entry", tr.ID)
	}
	var wantBefore, wantAfter any
	if tr.From != core.NoStatus && tr.From != to {
		wantBefore = string(tr.From)
	}
	if tr.From != to {
		wantAfter = string(to)
	}
	if beforeStatus != wantBefore || afterStatus != wantAfter {
		t.Errorf("transition %s: want status %v -> %v, but the table says %v -> %v", tr.ID, beforeStatus, afterStatus, wantBefore, wantAfter)
	}
}

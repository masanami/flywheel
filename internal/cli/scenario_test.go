package cli

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// このファイルは Issue #15 の完了条件「通しのシナリオ（M1 の完了の目安）:
// CLI だけで init → create → classify → plan → approve → submit →
// verify --result met → op add --kind release → approve で完了まで進み、
// log と status が整合する（端末ありのテスト）」を検証する。
//
// approve だけは本人確認つきの操作のため、approval_process_test.go の
// runConfirmedChild（このテストバイナリ自身を confirmHelperMain として疑似端末
// 付きの子プロセスに起動し、対象 ID を書き込む）で実行する。それ以外の7コマンド
// （init・create・classify・plan・submit・verify・op add）は本人確認を
// 要さないため、in-process の run()（runJSON）で直接実行する。
// runtime.GOOS によるスキップはしない（macOS・Linux の両方の CI で通す）。

// approveViaPTY は `flywheel approve <id> --workspace ws --json` を疑似端末つきの
// 子プロセスで実行し、id を確認入力として書き込む。
func approveViaPTY(t *testing.T, ws, id string) {
	t.Helper()
	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", ws, "--json", id}, id)
	if code != 0 {
		t.Fatalf("approve %s: exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", id, code, stdout, stderr, ptyOutput)
	}
	if !strings.Contains(ptyOutput, confirmPrompt(id)) {
		t.Fatalf("approve %s: pty output = %q, want it to contain the confirmation prompt for %s", id, ptyOutput, id)
	}
}

func TestScenario_CLIOnly_InitThroughApprove_ReachesDoneWithConsistentLogAndStatus(t *testing.T) {
	ws := t.TempDir()

	// init（冪等性は他のテストが検証済み。ここではストアが作られることだけ
	// 確認して先へ進む）。
	initDoc := runJSON(t, ws, "init")
	if initDoc["created"] != true {
		t.Fatalf("init.created = %v, want true", initDoc["created"])
	}

	// create（T1）。
	created := runJSON(t, ws, "create", "--title", "scenario challenge", "--done-criteria", "it ships")
	id := created["challenge"].(map[string]any)["id"].(string)
	if created["challenge"].(map[string]any)["status"] != "unclassified" {
		t.Fatalf("create.status = %v, want unclassified", created["challenge"].(map[string]any)["status"])
	}

	// classify（T2）。
	classified := runJSON(t, ws, "classify", id, "--priority", "P1")
	if classified["challenge"].(map[string]any)["status"] != "classified" {
		t.Fatalf("classify.status = %v, want classified", classified["challenge"].(map[string]any)["status"])
	}

	// plan（T3）。
	planned := runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "ship the scenario"))
	if planned["challenge"].(map[string]any)["status"] != "awaiting_plan_approval" {
		t.Fatalf("plan.status = %v, want awaiting_plan_approval", planned["challenge"].(map[string]any)["status"])
	}

	// approve（T5: 計画の承認。本人確認つき＝疑似端末）。
	approveViaPTY(t, ws, id)
	afterPlanApproval := runJSON(t, ws, "show", id)
	if afterPlanApproval["challenge"].(map[string]any)["status"] != "in_progress" {
		t.Fatalf("after plan approval status = %v, want in_progress", afterPlanApproval["challenge"].(map[string]any)["status"])
	}

	// submit（T7）。
	submitted := runJSON(t, ws, "submit", id)
	if submitted["challenge"].(map[string]any)["status"] != "verifying" {
		t.Fatalf("submit.status = %v, want verifying", submitted["challenge"].(map[string]any)["status"])
	}

	// verify --result met（T8）。
	verified := runJSON(t, ws, "verify", id, "--result", "met")
	if verified["challenge"].(map[string]any)["status"] != "awaiting_completion_approval" {
		t.Fatalf("verify.status = %v, want awaiting_completion_approval", verified["challenge"].(map[string]any)["status"])
	}

	// op add --kind release（不可逆操作の登録。本人確認は不要）。
	opAdded := runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "ship to prod")
	opID := opAdded["operation"].(map[string]any)["id"].(string)
	if opAdded["operation"].(map[string]any)["state"] != "pending" {
		t.Fatalf("op add.state = %v, want pending", opAdded["operation"].(map[string]any)["state"])
	}

	// approve（T13: 完了の承認。D12 により同じ操作で release も承認される。
	// 本人確認つき＝疑似端末）。
	approveViaPTY(t, ws, id)

	// --- 完了まで進んだことの確認 ---
	final := runJSON(t, ws, "show", id)
	// 計画・承認・不可逆操作を持つ課題の show・log・list・status の出力も、
	// 文書の成功時の JSON の形と一致する（成功経路の列挙では一覧が空になりがち
	// なため、ここで要素の形まで照合する）。
	doc := loadDocumentedJSON(t)
	assertDocumentedJSON(t, doc, "show", final)
	assertDocumentedJSON(t, doc, "list", runJSON(t, ws, "list"))
	fc := final["challenge"].(map[string]any)
	if fc["status"] != "done" || fc["status_label"] != "完了" {
		t.Fatalf("final status/status_label = %v/%v, want done/完了", fc["status"], fc["status_label"])
	}
	finalOps := final["operations"].([]any)
	if len(finalOps) != 1 || finalOps[0].(map[string]any)["id"] != opID || finalOps[0].(map[string]any)["state"] != "approved" {
		t.Fatalf("final operations = %+v, want 1 entry id=%s state=approved", finalOps, opID)
	}
	// 承認は計画・完了・release（D12 の一括承認）の 3 件で、すべて approved。
	gotApprovals := []string{}
	for _, ap := range final["approvals"].([]any) {
		m := ap.(map[string]any)
		gotApprovals = append(gotApprovals, fmt.Sprintf("%v/%v/%v", m["kind"], m["decision"], m["operation_id"]))
	}
	sort.Strings(gotApprovals)
	wantApprovals := []string{"completion/approved/<nil>", "plan/approved/<nil>", "release/approved/" + opID}
	if !reflect.DeepEqual(gotApprovals, wantApprovals) {
		t.Fatalf("final approvals (kind/decision/operation_id) = %v, want %v", gotApprovals, wantApprovals)
	}

	// --- log の整合: シナリオの各段が、実行した順に 1 件ずつ記録されている ---
	logDoc := runJSON(t, ws, "log", id)
	assertDocumentedJSON(t, doc, "log", logDoc)
	gotLog := []string{}
	for _, act := range logDoc["activities"].([]any) {
		m := act.(map[string]any)
		gotLog = append(gotLog, fmt.Sprintf("%v %v %v", m["entity"], m["entity_id"], m["action"]))
	}
	wantLog := []string{
		"challenge " + id + " create",
		"challenge " + id + " classify",
		"challenge " + id + " plan",
		"challenge " + id + " approve", // 計画の承認（T5）
		"challenge " + id + " submit",
		"challenge " + id + " verify_met",
		"operation " + opID + " op_add",
		// S2（Issue #39）: op add は課題の版も上げるため、その課題自身の
		// entity="challenge" のエントリも同じ操作で残る（log <C-ID> で課題の
		// エントリの version が飛ばないようにするため）。
		"challenge " + id + " op_add",
		// 完了の承認（T13）と D12 による release の一括承認は同じ操作で記録され、
		// 実装は release の承認を先に書く（仕様は両者の順を定めていない）。
		"operation " + opID + " approve",
		"challenge " + id + " approve",
	}
	if !reflect.DeepEqual(gotLog, wantLog) {
		t.Fatalf("log (entity entity_id action) =\n%s\nwant\n%s", strings.Join(gotLog, "\n"), strings.Join(wantLog, "\n"))
	}
	// S2（Issue #39）: 課題のエントリの after.version は create（版 1）の次から
	// 飛ばずに 1 ずつ増え（op add・D12 を含む）、最後の値が show の version と一致する。
	wantVersion := 2.0
	for _, act := range logDoc["activities"].([]any) {
		m := act.(map[string]any)
		if m["entity"] != "challenge" || m["action"] == "create" {
			continue
		}
		if v := m["after"].(map[string]any)["version"]; v != wantVersion {
			t.Fatalf("challenge entry %v: after.version = %v, want %v (versions must be consecutive)", m["action"], v, wantVersion)
		}
		wantVersion++
	}
	if fc["version"] != wantVersion-1 {
		t.Fatalf("final challenge version = %v, want %v (the last challenge entry's version)", fc["version"], wantVersion-1)
	}

	// --- status の整合: 完了した課題はどの区分にも現れず、承認済みの
	// release だけが approved.operations に現れる ---
	statusDoc := runJSON(t, ws, "status")
	assertDocumentedJSON(t, doc, "status", statusDoc)
	needsHuman := statusDoc["needs_human"].(map[string]any)
	for _, c := range needsHuman["challenges"].([]any) {
		if c.(map[string]any)["id"] == id {
			t.Errorf("done challenge %s unexpectedly appears in status.needs_human.challenges", id)
		}
	}
	for _, op := range needsHuman["operations"].([]any) {
		if op.(map[string]any)["id"] == opID {
			t.Errorf("approved operation %s unexpectedly appears in status.needs_human.operations", opID)
		}
	}
	actionable := statusDoc["actionable"].(map[string]any)
	for _, c := range actionable["challenges"].([]any) {
		if c.(map[string]any)["id"] == id {
			t.Errorf("done challenge %s unexpectedly appears in status.actionable.challenges", id)
		}
	}
	approved := statusDoc["approved"].(map[string]any)
	foundApprovedOp := false
	for _, op := range approved["operations"].([]any) {
		if op.(map[string]any)["id"] == opID {
			foundApprovedOp = true
		}
	}
	if !foundApprovedOp {
		t.Errorf("status.approved.operations = %+v, want it to contain %s", approved["operations"], opID)
	}
}

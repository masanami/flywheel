package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// このファイルは Issue #31 の受入基準を検証する。
//   - AC-17: 計画の複数の版・承認・差し戻し・保留・不可逆操作をすべて持つ課題で、
//     CLI の `show --json` がすべての要素を出す
//   - `show` の --json 無しの出力にも計画の全版の本文と保留への回答を出す
//     （受入基準 17・35 をテキスト出力にも適用する【決定 2026-09-23（オーナー）】）
//   - `init` の --json 無しの出力が人の読める形である（Go の map 表記でない）
//
// 課題はストアへの直接挿入ではなく CLI のコマンドだけで作る（reject・approve・
// answer は本人確認つきのため、疑似端末の子プロセス＝runConfirmedChild で実行する）。

const (
	fullDescription      = "full description"
	fullDoneCriteria     = "full done criteria"
	fullPlanV1Body       = "first plan line 1\nfirst plan line 2"
	fullPlanV2Body       = "second plan"
	fullPlanV3Body       = "third plan"
	fullRejectReason     = "plan v1 is too vague"
	fullHoldQuestion     = "which region first?"
	fullHoldAnswer       = "tokyo first"
	fullOperationSummary = "ship to prod"
)

// confirmedViaPTY は args（--workspace ws --json を付ける）を疑似端末つきの子
// プロセスで実行し、id を確認入力として書き込む。
func confirmedViaPTY(t *testing.T, ws, id string, args ...string) {
	t.Helper()
	full := append(append([]string{}, args...), "--workspace", ws, "--json")
	code, ptyOutput, stdout, stderr := runConfirmedChild(t, full, id)
	if code != 0 {
		t.Fatalf("%v: exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", args, code, stdout, stderr, ptyOutput)
	}
}

// createFullChallenge は人間記入欄をすべて埋め、計画 3 版（v1 は差し戻し、v2 を
// v3 へ改訂して承認）・承認 1 件と差し戻し 1 件・回答済みの保留 1 件・不可逆操作
// 1 件を持つ課題を CLI だけで作り、課題の ID と不可逆操作の ID を返す。
func createFullChallenge(t *testing.T, ws string) (id, opID string) {
	t.Helper()
	created := runJSON(t, ws, "create", "--title", "full", "--description", fullDescription, "--done-criteria", fullDoneCriteria, "--urgency", "高")
	id = created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P1")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, fullPlanV1Body))
	confirmedViaPTY(t, ws, id, "reject", id, "--reason", fullRejectReason)        // T6
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, fullPlanV2Body)) // T3
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, fullPlanV3Body)) // T4
	confirmedViaPTY(t, ws, id, "approve", id)                                     // T5
	runJSON(t, ws, "hold", id, "--question", fullHoldQuestion)                    // T11
	confirmedViaPTY(t, ws, id, "answer", id, "--answer", fullHoldAnswer)          // T12
	op := runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", fullOperationSummary)
	opID = op["operation"].(map[string]any)["id"].(string)
	return id, opID
}

// TestRunShow_FullChallenge は疑似端末の子プロセスを伴う準備が重いため、1 つの
// 課題を JSON とテキストの両方の検査で共有する。
func TestRunShow_FullChallenge(t *testing.T) {
	ws := initializedWorkspace(t)
	id, opID := createFullChallenge(t, ws)

	t.Run("JSONIncludesEveryElement", func(t *testing.T) {
		doc := runJSON(t, ws, "show", id)
		assertDocumentedJSON(t, loadDocumentedJSON(t), "show", doc)

		c := doc["challenge"].(map[string]any)
		if c["id"] != id || c["title"] != "full" || c["description"] != fullDescription || c["done_criteria"] != fullDoneCriteria || c["urgency"] != "高" || c["priority"] != "P1" {
			t.Errorf("challenge = %+v, want the human-entry fields and priority=P1", c)
		}
		if c["status"] != "in_progress" || c["status_label"] != "着手中" {
			t.Errorf("status/status_label = %v/%v, want in_progress/着手中", c["status"], c["status_label"])
		}

		// 計画: 全版が昇順で、各版の本文を持つ。
		plans := doc["plans"].([]any)
		wantBodies := []string{fullPlanV1Body, fullPlanV2Body, fullPlanV3Body}
		if len(plans) != len(wantBodies) {
			t.Fatalf("len(plans) = %d, want %d (%+v)", len(plans), len(wantBodies), plans)
		}
		for i, want := range wantBodies {
			p := plans[i].(map[string]any)
			if p["version"] != float64(i+1) || p["body"] != want {
				t.Errorf("plans[%d] = %+v, want version=%d body=%q", i, p, i+1, want)
			}
		}

		// 承認と差し戻し: 計画への差し戻し（理由つき）と承認。
		approvals := doc["approvals"].([]any)
		if len(approvals) != 2 {
			t.Fatalf("len(approvals) = %d, want 2 (%+v)", len(approvals), approvals)
		}
		var rejected, approved map[string]any
		for _, a := range approvals {
			m := a.(map[string]any)
			switch m["decision"] {
			case "rejected":
				rejected = m
			case "approved":
				approved = m
			}
		}
		if rejected == nil || rejected["kind"] != "plan" || rejected["reason"] != fullRejectReason {
			t.Fatalf("rejection = %+v, want kind=plan reason=%q", rejected, fullRejectReason)
		}
		if approved == nil || approved["kind"] != "plan" || approved["reason"] != nil {
			t.Fatalf("approval = %+v, want kind=plan reason=null", approved)
		}
		// target_version は判断した時点の課題の版（計画の版ではない）。差し戻しの
		// 後に承認しているため、差し戻しの版のほうが小さい。
		if rv, av := rejected["target_version"].(float64), approved["target_version"].(float64); !(rv < av) {
			t.Errorf("target_version rejected=%v approved=%v, want rejected < approved", rv, av)
		}

		// 保留: 問いと回答がそろっている。
		holds := doc["holds"].([]any)
		if len(holds) != 1 {
			t.Fatalf("len(holds) = %d, want 1 (%+v)", len(holds), holds)
		}
		h := holds[0].(map[string]any)
		if h["question"] != fullHoldQuestion || h["answer"] != fullHoldAnswer || h["from_status"] != "in_progress" {
			t.Errorf("hold = %+v, want question=%q answer=%q from_status=in_progress", h, fullHoldQuestion, fullHoldAnswer)
		}
		if h["answered_at"] == nil || h["answered_by"] == nil {
			t.Errorf("hold answered_at/answered_by = %v/%v, want both set", h["answered_at"], h["answered_by"])
		}

		// 不可逆操作。
		ops := doc["operations"].([]any)
		if len(ops) != 1 {
			t.Fatalf("len(operations) = %d, want 1 (%+v)", len(ops), ops)
		}
		op := ops[0].(map[string]any)
		if op["id"] != opID || op["challenge_id"] != id || op["kind"] != "release" || op["summary"] != fullOperationSummary || op["state"] != "pending" {
			t.Errorf("operation = %+v, want id=%s challenge_id=%s kind=release summary=%q state=pending", op, opID, id, fullOperationSummary)
		}
	})

	t.Run("TextIncludesPlanBodiesAndHoldAnswer", func(t *testing.T) {
		out := runText(t, ws, "show", id)

		// 計画の各版の見出しの直後に、その版の本文（各行を字下げ）が続く。
		requireInOrder(t, out,
			"  v1 (", "    first plan line 1\n", "    first plan line 2\n",
			"  v2 (", "    "+fullPlanV2Body+"\n",
			"  v3 (", "    "+fullPlanV3Body+"\n",
			"承認・差し戻し:",
		)
		// 差し戻しの理由・保留の問いと回答・不可逆操作。
		requireInOrder(t, out,
			"plan rejected by ", "理由: "+fullRejectReason,
			"plan approved by ",
			"保留:", "問い: "+fullHoldQuestion, "回答: "+fullHoldAnswer, "回答者: ",
			"不可逆操作:", opID, fullOperationSummary,
		)
		for _, want := range []string{fullDescription, fullDoneCriteria, "高"} {
			if !strings.Contains(out, want) {
				t.Errorf("show text lacks %q:\n%s", want, out)
			}
		}
	})
}

// requireInOrder は out の中に wants がこの順で現れることを確かめる。
func requireInOrder(t *testing.T, out string, wants ...string) {
	t.Helper()
	rest := out
	for _, want := range wants {
		i := strings.Index(rest, want)
		if i < 0 {
			t.Errorf("text lacks %q after the preceding items:\n%s", want, out)
			return
		}
		rest = rest[i+len(want):]
	}
}

func TestRunShow_TextMarksUnansweredHold(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	runJSON(t, ws, "hold", id, "--question", fullHoldQuestion)

	out := runText(t, ws, "show", id)
	requireInOrder(t, out, "問い: "+fullHoldQuestion, "回答: (未回答)")
}

// TestInit_TextModeIsHumanReadable は init の --json 無しの出力が Go の map 表記
// （map[created:true …]）ではなく、項目名と値で読めることを確かめる。
func TestInit_TextModeIsHumanReadable(t *testing.T) {
	dir := t.TempDir()
	first := runText(t, dir, "init")
	second := runText(t, dir, "init")
	for name, out := range map[string]string{"first": first, "second": second} {
		if strings.Contains(out, "map[") {
			t.Errorf("%s init text is a Go map literal: %q", name, out)
		}
		// t.TempDir() は macOS では /var → /private/var のシンボリックリンクを
		// 含みうるため、末尾の要素で照合する。
		requireInOrder(t, out,
			"ワークスペース: ", filepath.Base(dir)+"\n",
			"ストア:", filepath.Join(filepath.Base(dir), ".flywheel", "flywheel.db")+"\n",
		)
	}
	if !strings.Contains(first, "新規作成:       はい\n") {
		t.Errorf("first init text should say the store was created: %q", first)
	}
	if !strings.Contains(second, "新規作成:       いいえ") {
		t.Errorf("second init text should say the store already existed: %q", second)
	}
}

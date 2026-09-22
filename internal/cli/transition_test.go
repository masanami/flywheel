package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// --- classify ---

func TestRunClassify_SetsPriorityAndReturnsChallengeJSON(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	doc := runJSON(t, ws, "classify", id, "--priority", "P1")
	c := doc["challenge"].(map[string]any)
	if c["priority"] != "P1" {
		t.Errorf("priority = %v, want P1", c["priority"])
	}
	if c["status"] != "classified" || c["status_label"] != "分類済" {
		t.Errorf("status/status_label = %v/%v, want classified/分類済", c["status"], c["status_label"])
	}
	if c["version"] != float64(2) {
		t.Errorf("version = %v, want 2", c["version"])
	}
}

func TestRunClassify_InvalidPriorityIsValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	requireErrorCode(t, []string{"classify", id, "--priority", "bogus", "--workspace", ws}, 1, CodeValidationFailed)
}

// --- plan ---

func TestRunPlan_FileFlagReadsFileAsBody(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")

	planPath := filepath.Join(t.TempDir(), "plan.txt")
	if err := os.WriteFile(planPath, []byte("do the thing"), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}

	doc := runJSON(t, ws, "plan", id, "--file", planPath)
	c := doc["challenge"].(map[string]any)
	if c["status"] != "awaiting_plan_approval" {
		t.Errorf("status = %v, want awaiting_plan_approval", c["status"])
	}
	plan := doc["plan"].(map[string]any)
	if plan["version"] != float64(1) || plan["body"] != "do the thing" {
		t.Errorf("plan = %+v, want version=1 body=%q", plan, "do the thing")
	}
}

func TestRunPlan_StdinReadsStdinAsBody(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", id, "--stdin", "--workspace", ws, "--json"}, strings.NewReader("via stdin"), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	plan := doc["plan"].(map[string]any)
	if plan["body"] != "via stdin" {
		t.Errorf("plan.body = %v, want %q", plan["body"], "via stdin")
	}
}

func TestRunPlan_MissingFileIsUsageErrorAndUnreadableFileIsInternalError(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")

	requireErrorCode(t, []string{"plan", id, "--file", filepath.Join(t.TempDir(), "missing.txt"), "--workspace", ws}, 2, CodeUsageError)
	// 存在はするが読めない（ディレクトリを指定）場合は引数の誤りではなく
	// 入出力の失敗＝internal_error（終了コードは同じ 2）。
	requireErrorCode(t, []string{"plan", id, "--file", t.TempDir(), "--workspace", ws}, 2, CodeInternalError)
}

// AC-35: 計画承認待ちの課題に plan を再度行うと、計画の版が1増え、以前の版も show で読める。
func TestRunPlan_RerunBumpsPlanVersionAndShowHasBothVersions(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")

	var stdout1, stderr1 bytes.Buffer
	if code := run([]string{"plan", id, "--stdin", "--workspace", ws, "--json"}, strings.NewReader("v1"), &stdout1, &stderr1, defaultCommands()); code != 0 {
		t.Fatalf("plan 1 exit=%d (stderr=%s)", code, stderr1.String())
	}
	var stdout2, stderr2 bytes.Buffer
	if code := run([]string{"plan", id, "--stdin", "--workspace", ws, "--json"}, strings.NewReader("v2"), &stdout2, &stderr2, defaultCommands()); code != 0 {
		t.Fatalf("plan 2 exit=%d (stderr=%s)", code, stderr2.String())
	}
	var doc2 map[string]any
	if err := json.Unmarshal(stdout2.Bytes(), &doc2); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if doc2["plan"].(map[string]any)["version"] != float64(2) {
		t.Errorf("2nd plan version = %v, want 2", doc2["plan"].(map[string]any)["version"])
	}

	show := runJSON(t, ws, "show", id)
	plans, ok := show["plans"].([]any)
	if !ok || len(plans) != 2 {
		t.Fatalf("show.plans = %#v, want 2 entries", show["plans"])
	}
	if plans[0].(map[string]any)["body"] != "v1" || plans[1].(map[string]any)["body"] != "v2" {
		t.Errorf("plans = %+v, want bodies v1, v2 in order", plans)
	}
}

// plan は body が無いと ID の有無チェックに到達できないため、idCommandCases の
// 汎用枠組み（stdin を常に空にする requireErrorCode）では検証できない。
// 専用に --file で内容のあるファイルを渡し、not_found を確認する。
func TestRunPlan_NotFoundForMissingOrMalformedID(t *testing.T) {
	ws := initializedWorkspace(t)
	planPath := filepath.Join(t.TempDir(), "plan.txt")
	if err := os.WriteFile(planPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	for _, id := range []string{"C-999", "OP-1"} {
		requireErrorCode(t, []string{"plan", id, "--file", planPath, "--workspace", ws}, 1, CodeNotFound)
	}
}

// --- submit ---

func TestRunSubmit_AdvancesToVerifying(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "in_progress")

	doc := runJSON(t, ws, "submit", id)
	c := doc["challenge"].(map[string]any)
	if c["status"] != "verifying" {
		t.Errorf("status = %v, want verifying", c["status"])
	}
}

// --- verify ---

func TestRunVerify_MetAdvancesToAwaitingCompletionApproval(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "verifying")

	doc := runJSON(t, ws, "verify", id, "--result", "met")
	c := doc["challenge"].(map[string]any)
	if c["status"] != "awaiting_completion_approval" {
		t.Errorf("status = %v, want awaiting_completion_approval", c["status"])
	}
}

func TestRunVerify_UncertainWithQuestionAdvancesToAwaitingHuman(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "verifying")

	doc := runJSON(t, ws, "verify", id, "--result", "uncertain", "--question", "why?")
	c := doc["challenge"].(map[string]any)
	if c["status"] != "awaiting_human" {
		t.Errorf("status = %v, want awaiting_human", c["status"])
	}

	show := runJSON(t, ws, "show", id)
	holds := show["holds"].([]any)
	if len(holds) != 1 {
		t.Fatalf("holds = %+v, want 1 entry", holds)
	}
	h := holds[0].(map[string]any)
	if h["question"] != "why?" || h["from_status"] != "verifying" {
		t.Errorf("hold = %+v, want question=why? from_status=verifying", h)
	}
}

func TestRunVerify_InvalidResultIsValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "verifying")

	requireErrorCode(t, []string{"verify", id, "--result", "bogus", "--workspace", ws}, 1, CodeValidationFailed)
}

// AC-30: `verify --result met`（または `not_met`）に `--question` を添えると usage_error（終了コード2）。
func TestRunVerify_MetOrNotMetWithQuestionIsUsageError(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "verifying")

	for _, result := range []string{"met", "not_met"} {
		requireErrorCode(t, []string{"verify", id, "--result", result, "--question", "q", "--workspace", ws}, 2, CodeUsageError)
	}
}

// AC-32: 問いの文が空の `verify --result uncertain` は validation_failed（終了コード1）。
func TestRunVerify_UncertainWithoutQuestionIsValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "verifying")

	requireErrorCode(t, []string{"verify", id, "--result", "uncertain", "--workspace", ws}, 1, CodeValidationFailed)
	requireErrorCode(t, []string{"verify", id, "--result", "uncertain", "--question", "   ", "--workspace", ws}, 1, CodeValidationFailed)
}

// --- hold ---

func TestRunHold_AdvancesToAwaitingHumanAndRecordsQuestion(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	doc := runJSON(t, ws, "hold", id, "--question", "why?")
	c := doc["challenge"].(map[string]any)
	if c["status"] != "awaiting_human" {
		t.Errorf("status = %v, want awaiting_human", c["status"])
	}

	show := runJSON(t, ws, "show", id)
	holds := show["holds"].([]any)
	if len(holds) != 1 || holds[0].(map[string]any)["from_status"] != "unclassified" {
		t.Errorf("holds = %+v, want 1 entry with from_status=unclassified", holds)
	}
}

// AC-31: 問いの文が空の `hold` は validation_failed（終了コード1）。
func TestRunHold_EmptyQuestionIsValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	requireErrorCode(t, []string{"hold", id, "--workspace", ws}, 1, CodeValidationFailed)
	requireErrorCode(t, []string{"hold", id, "--question", "   ", "--workspace", ws}, 1, CodeValidationFailed)
}

// --- テキスト出力 ---

func TestRunTransitionCommands_TextOutputIsChallengeID(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	if got := runText(t, ws, "classify", id, "--priority", "P0"); got != id+"\n" {
		t.Errorf("classify text = %q, want %q", got, id+"\n")
	}

	planPath := filepath.Join(t.TempDir(), "plan.txt")
	if err := os.WriteFile(planPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	if got := runText(t, ws, "plan", id, "--file", planPath); got != id+"\n" {
		t.Errorf("plan text = %q, want %q", got, id+"\n")
	}
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "in_progress")
	if got := runText(t, ws, "submit", id); got != id+"\n" {
		t.Errorf("submit text = %q, want %q", got, id+"\n")
	}
	if got := runText(t, ws, "verify", id, "--result", "met"); got != id+"\n" {
		t.Errorf("verify text = %q, want %q", got, id+"\n")
	}
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "in_progress")
	if got := runText(t, ws, "hold", id, "--question", "why?"); got != id+"\n" {
		t.Errorf("hold text = %q, want %q", got, id+"\n")
	}
}

// --- 5コマンド × 8状態: 遷移表に無い組は invalid_transition（done は terminal_state） ---

// transitionCommandCase は状態機械コマンド 1 つの、not-defined ケースでの実行方法。
// args は *testing.T を受け取り（plan が t.TempDir() に内容のあるファイルを
// 用意するため）、id を対象にしたコマンド引数を返す。
type transitionCommandCase struct {
	name string
	args func(t *testing.T, id string) []string
}

var transitionCommandCases = []transitionCommandCase{
	{"classify", func(_ *testing.T, id string) []string { return []string{"classify", id, "--priority", "P0"} }},
	{"plan", func(t *testing.T, id string) []string {
		path := filepath.Join(t.TempDir(), "plan.txt")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write plan file: %v", err)
		}
		return []string{"plan", id, "--file", path}
	}},
	{"submit", func(_ *testing.T, id string) []string { return []string{"submit", id} }},
	{"verify", func(_ *testing.T, id string) []string { return []string{"verify", id, "--result", "met"} }},
	{"hold", func(_ *testing.T, id string) []string { return []string{"hold", id, "--question", "why?"} }},
}

// operationByCommand は各コマンドが遷移表で引く操作トークン。可否の期待値は
// この対応だけを持ち、遷移元の集合・終端状態・状態の一覧はすべて core の
// Table／Lookup／IsTerminal／StatusVocabulary から導出する（design-reviewer
// 指摘: CLI 側のテストに遷移表の影を持たない。verify はテストで
// --result met を使うため OpVerifyMet）。
var operationByCommand = map[string]core.Operation{
	"classify": core.OpClassify,
	"plan":     core.OpPlan,
	"submit":   core.OpSubmit,
	"verify":   core.OpVerifyMet,
	"hold":     core.OpHold,
}

func TestTransitionCommands_UndefinedStatusCombinationsAreRejected(t *testing.T) {
	tested := 0
	for _, tc := range transitionCommandCases {
		op, ok := operationByCommand[tc.name]
		if !ok {
			t.Fatalf("no operation token registered for command %q", tc.name)
		}
		for _, entry := range core.StatusVocabulary {
			st := entry.Code
			status := string(st)
			if _, defined := core.Lookup(st, op); defined {
				continue // 遷移表にある組は別のテストが検証する
			}
			tested++
			t.Run(tc.name+"/"+status, func(t *testing.T) {
				ws := initializedWorkspace(t)
				created := runJSON(t, ws, "create", "--title", "t")
				id := created["challenge"].(map[string]any)["id"].(string)
				coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), status)

				wantCode := CodeInvalidTransition
				wantExit := 1
				if core.IsTerminal(core.Table, core.StatusVocabulary, st) {
					wantCode = CodeTerminalState
				}
				requireErrorCode(t, append(tc.args(t, id), "--workspace", ws), wantExit, wantCode)

				show := runJSON(t, ws, "show", id)
				c := show["challenge"].(map[string]any)
				if c["status"] != status {
					t.Errorf("status changed despite rejection: got %v, want %v", c["status"], status)
				}
				if c["version"] != float64(1) {
					t.Errorf("version changed despite rejection: got %v, want 1", c["version"])
				}
			})
		}
	}
	// 5 コマンド × 8 状態 = 40 組のうち、遷移表にある組（T2=1・T3/T4=2・T7=1・
	// T8=1・T11=4 の計 9）を除いた 31 組を回したことを固定する。
	if tested != 31 {
		t.Fatalf("tested %d undefined (command, status) combinations, want 31", tested)
	}
}

// --- AC-36 (CLI 経由): 着手中の同じ課題に submit と hold を並行実行すると直列化される ---

// このテストは CLI の入口（run）を 2 本同時に叩いた結果が「いずれかの直列化と
// 一致し、失われた変更が無い」ことを見るスモークである。重なり（後発が先発の
// ロック解放まで返らないこと）と最新状態での再検査は、書き込みロックを保持した
// まま止められる core 側の runConcurrentTransition（internal/core/
// transition_exec_test.go）が実証する。AC-36 の根拠は core 側にある。
func TestRun_ConcurrentSubmitAndHold_NoLostUpdateSmoke(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "in_progress")

	run1 := func(args []string) (int, map[string]any, string) {
		var stdout, stderr bytes.Buffer
		full := append(append([]string{}, args...), "--workspace", ws, "--json")
		code := run(full, strings.NewReader(""), &stdout, &stderr, defaultCommands())
		var doc map[string]any
		if code == 0 {
			_ = json.Unmarshal(stdout.Bytes(), &doc)
		}
		return code, doc, stderr.String()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	var submitCode, holdCode int
	var submitDoc, holdDoc map[string]any
	var submitStderr, holdStderr string
	go func() {
		defer wg.Done()
		submitCode, submitDoc, submitStderr = run1([]string{"submit", id})
	}()
	go func() {
		defer wg.Done()
		holdCode, holdDoc, holdStderr = run1([]string{"hold", id, "--question", "why?"})
	}()
	wg.Wait()

	// 両方成功、または一方だけ成功（他方は invalid_transition）のいずれかに
	// 決まり、失われた変更は無い（PD7: 後発は最新状態で再検査される）。
	show := runJSON(t, ws, "show", id)
	finalStatus := show["challenge"].(map[string]any)["status"]

	switch {
	case submitCode == 0 && holdCode == 0:
		// submit 先勝ち: verifying は T11 の遷移元にも含まれるため hold も成功する。
		if submitDoc["challenge"].(map[string]any)["status"] != "verifying" {
			t.Errorf("submit result status = %v, want verifying", submitDoc["challenge"].(map[string]any)["status"])
		}
		if holdDoc["challenge"].(map[string]any)["status"] != "awaiting_human" {
			t.Errorf("hold result status = %v, want awaiting_human", holdDoc["challenge"].(map[string]any)["status"])
		}
		if finalStatus != "awaiting_human" {
			t.Errorf("final status = %v, want awaiting_human", finalStatus)
		}
	case submitCode == 0 && holdCode != 0:
		// submit 先勝ちなら後発の hold は verifying → awaiting_human（T11）で
		// 成功しなければならない。ここへ来るのは直列化後の再検査が遷移表と
		// 食い違っている場合だけ。
		t.Fatalf("hold failed after submit won, but verifying is a valid hold source (T11): hold(code=%d stderr=%s)", holdCode, holdStderr)
	case holdCode == 0 && submitCode != 0:
		if !strings.Contains(submitStderr, string(CodeInvalidTransition)) {
			t.Errorf("submit stderr = %q, want invalid_transition", submitStderr)
		}
		if finalStatus != "awaiting_human" {
			t.Errorf("final status = %v, want awaiting_human", finalStatus)
		}
	default:
		t.Fatalf("both commands failed: submit(code=%d stderr=%s) hold(code=%d stderr=%s)", submitCode, submitStderr, holdCode, holdStderr)
	}
}

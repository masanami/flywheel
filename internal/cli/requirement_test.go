package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S3a の要求の記録の口（docs/features/request-to-plan-s3.md「要求の記録とチャットの欄の口」）。

func jsonObject(t *testing.T, s string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return out
}

// assertRequirementCreated は requirement create --json の出力の 4 つのキーと、証跡のファイルが無いことを確かめる。
func assertRequirementCreated(t *testing.T, ws, stdout, wantID, wantTitle, wantOrigin string) {
	t.Helper()
	got := jsonObject(t, stdout)
	want := map[string]any{"id": wantID, "title": wantTitle, "origin": wantOrigin, "evidence_path": ".flywheel/requirements/" + wantID + ".md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("requirement create = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(ws, ".flywheel", "requirements", wantID+".md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the evidence file exists or cannot be checked: %v", err)
	}
}

func TestRequirementCreate_WithoutTheMarkIsManual_AC164_AC165_AC166_AC167_AC168_AC169(t *testing.T) {
	ws := initializedWorkspace(t)
	unsetEnv(t, core.ChatEnvSession)
	code, stdout, stderr := chatRun(t, ws, "requirement", "create", "--title", "通知")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	assertRequirementCreated(t, ws, stdout, "RQ-1", "通知", "manual")
}

func TestRequirementCreate_WithTheMarkIsChatAndKeepsTheSession_AC163_AC167(t *testing.T) {
	ws := initializedWorkspace(t)
	f := openChat(t, ws)
	f.set(t)
	code, stdout, stderr := chatRun(t, ws, "requirement", "create", "--title", "通知")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	assertRequirementCreated(t, ws, stdout, "RQ-1", "通知", "chat")
	if code, _, stderr := chatRun(t, ws, "create", "--title", "区切り", "--requirement", "RQ-1"); code != 0 {
		t.Fatalf("create: exit=%d stderr=%s", code, stderr)
	}
	req := runJSON(t, ws, "show", "C-1")["requirement"].(map[string]any)
	if req["origin"] != "chat" || req["chat_session"] != f.session {
		t.Errorf("requirement = %v, want origin chat with the session %s", req, f.session)
	}
}

func TestRequirementCreate_TitleLimits_AC170(t *testing.T) {
	ws := initializedWorkspace(t)
	unsetEnv(t, core.ChatEnvSession)
	for _, title := range []string{"", " \t　\n", strings.Repeat("あ", 201)} {
		code, _, stderr := chatRun(t, ws, "requirement", "create", "--title", title)
		if code == 0 || !strings.Contains(stderr, string(CodeValidationFailed)) {
			t.Errorf("title of %d bytes: exit=%d stderr=%s, want validation_failed", len(title), code, stderr)
		}
	}
	code, stdout, stderr := chatRun(t, ws, "requirement", "create", "--title", strings.Repeat("あ", 200))
	if code != 0 {
		t.Fatalf("200 characters: exit=%d stderr=%s", code, stderr)
	}
	// 断られた呼び出しは記録を作っていないので、最初の番号になる。
	if got := jsonObject(t, stdout)["id"]; got != "RQ-1" {
		t.Errorf("id = %v, want RQ-1", got)
	}
}

func TestRequirementCreate_EmptyMarkIsRefused_AC171(t *testing.T) {
	ws := initializedWorkspace(t)
	t.Setenv(core.ChatEnvSession, "")
	code, _, stderr := chatRun(t, ws, "requirement", "create", "--title", "通知")
	if code == 0 || !strings.Contains(stderr, string(CodeVerificationRejected)) {
		t.Fatalf("exit=%d stderr=%s, want verification_rejected", code, stderr)
	}
	if !needsChatMarkCheck(Command{Path: []string{"requirement", "create"}}, Args{}) {
		t.Error("requirement create is not a recording command")
	}
	unsetEnv(t, core.ChatEnvSession)
	if got := runJSON(t, ws, "requirement", "create", "--title", "後")["id"]; got != "RQ-1" {
		t.Errorf("id = %v, want RQ-1 (the refused call must not create a record)", got)
	}
}

func TestCreate_Requirement_AC172_AC173(t *testing.T) {
	ws := initializedWorkspace(t)
	unsetEnv(t, core.ChatEnvSession)
	runJSON(t, ws, "requirement", "create", "--title", "通知")
	runJSON(t, ws, "create", "--title", "t", "--requirement", "RQ-1")
	req := runJSON(t, ws, "show", "C-1")["requirement"].(map[string]any)
	if req["id"] != "RQ-1" {
		t.Errorf("requirement.id = %v, want RQ-1", req["id"])
	}
	if got := coretest.ChallengeOrigin(t, ws, 1); got != "manual" {
		t.Errorf("origin = %q, want manual", got)
	}
}

func TestCreate_UnknownRequirementCreatesNothing_AC174(t *testing.T) {
	ws := initializedWorkspace(t)
	unsetEnv(t, core.ChatEnvSession)
	runJSON(t, ws, "requirement", "create", "--title", "通知")
	for _, id := range []string{"RQ-2", "RQ-01", "1", ""} {
		code, _, stderr := chatRun(t, ws, "create", "--title", "t", "--requirement", id)
		if code == 0 || !strings.Contains(stderr, string(CodeValidationFailed)) {
			t.Errorf("--requirement %q: exit=%d stderr=%s, want validation_failed", id, code, stderr)
		}
	}
	if got := runJSON(t, ws, "list")["challenges"].([]any); len(got) != 0 {
		t.Errorf("challenges = %v, want none", got)
	}
}

func TestCreate_ResolvedRequirementCreatesNothing_AC175(t *testing.T) {
	ws := initializedWorkspace(t)
	unsetEnv(t, core.ChatEnvSession)
	runJSON(t, ws, "requirement", "create", "--title", "通知")
	coretest.ResolveRequirement(t, ws, 1, "2026-11-27T10:00:00.000Z")
	code, _, stderr := chatRun(t, ws, "create", "--title", "t", "--requirement", "RQ-1")
	if code == 0 || !strings.Contains(stderr, string(CodeValidationFailed)) {
		t.Fatalf("exit=%d stderr=%s, want validation_failed", code, stderr)
	}
	if got := runJSON(t, ws, "list")["challenges"].([]any); len(got) != 0 {
		t.Errorf("challenges = %v, want none", got)
	}
}

func TestHelp_HasRequirementCreateAndCreateRequirementFlag_AC223(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runWithLookPath([]string{"help"}, strings.NewReader(""), &out, &errb, defaultCommands(), lookPathFor(t)); code != 0 {
		t.Fatalf("help: %s", errb.String())
	}
	createLine := ""
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "  create ") {
			createLine = line
		}
	}
	if !strings.Contains(createLine, "[--requirement <RQ-ID>]") {
		t.Errorf("create line = %q, want --requirement", createLine)
	}
	if !strings.Contains(out.String(), "\n  requirement create --title <t>   （要求の記録を作り") {
		t.Errorf("help lacks the description of requirement create:\n%s", out.String())
	}
}

// AC-214・AC-215・AC-218・AC-219: show --json の requirement と slice_split のキー。
func TestShowJSON_RequirementAndSliceSplitKeys_AC214_AC215_AC218_AC219(t *testing.T) {
	ws := initializedWorkspace(t)
	coretest.FillAllTables(t, ws)
	unsetEnv(t, core.ChatEnvSession)

	shown := runJSON(t, ws, "show", "C-1")
	req := shown["requirement"].(map[string]any)
	wantKeys := []string{"challenges", "chat_session", "created_at", "evidence_path", "external_key", "id", "origin", "origin_challenge_id", "resolved_at", "title", "url"}
	if got := sortedKeys(keysOf(req)); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("requirement keys = %v, want %v", got, wantKeys)
	}
	challenges := req["challenges"].([]any)
	if len(challenges) != 1 || !reflect.DeepEqual(sortedKeys(keysOf(challenges[0].(map[string]any))), []string{"id", "status", "title"}) {
		t.Errorf("challenges = %v, want only C-2 with id, status and title", challenges)
	}
	split := shown["slice_split"].(map[string]any)
	wantKeys = []string{"evidence_path", "hold_id", "reason", "slices", "state"}
	if got := sortedKeys(keysOf(split)); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("slice_split keys = %v, want %v", got, wantKeys)
	}

	runJSON(t, ws, "create", "--title", "plain")
	plain := runJSON(t, ws, "show", "C-3")
	for _, key := range []string{"requirement", "slice_split"} {
		if v, ok := plain[key]; !ok || v != nil {
			t.Errorf("%s = %v (present=%v), want a null key", key, v, ok)
		}
	}
}

// 案の区切りは出力の型の 7 つの項目で出し、回答の後に足す challenge_id は出さない。
func TestShowJSON_SliceSplitSlices_AC219(t *testing.T) {
	ws := initializedWorkspace(t)
	unsetEnv(t, core.ChatEnvSession)
	runJSON(t, ws, "create", "--title", "t")
	coretest.Exec(t, ws, `INSERT INTO slice_split (challenge_id, state, classify_reason, proposal, created_at, updated_at)
		VALUES (1, 'awaiting_owner', 'why', '{"reason":"分ける","slices":[{"title":"a","headline":"h","context":"c","summary":"s","scope":"sc","repo":"app","depends_on":[],"challenge_id":"C-9"},{"title":"b","headline":"h","context":"c","summary":"s","scope":"sc","repo":"app","depends_on":[1]}],"evidence":[]}', 't', 't')`)
	split := runJSON(t, ws, "show", "C-1")["slice_split"].(map[string]any)
	if split["reason"] != "分ける" || split["state"] != "awaiting_owner" || split["hold_id"] != nil || split["evidence_path"] != nil {
		t.Errorf("slice_split = %v", split)
	}
	slices := split["slices"].([]any)
	if len(slices) != 2 {
		t.Fatalf("slices = %v", slices)
	}
	want := []string{"context", "depends_on", "headline", "repo", "scope", "summary", "title"}
	if got := sortedKeys(keysOf(slices[0].(map[string]any))); !reflect.DeepEqual(got, want) {
		t.Errorf("slice keys = %v, want %v", got, want)
	}
	if got := slices[1].(map[string]any)["depends_on"]; !reflect.DeepEqual(got, []any{float64(1)}) {
		t.Errorf("depends_on = %v", got)
	}

	// proposal の無い記録は、理由が null で区切りが空の配列。
	coretest.Exec(t, ws, `UPDATE slice_split SET state = 'pending', proposal = NULL`)
	split = runJSON(t, ws, "show", "C-1")["slice_split"].(map[string]any)
	if split["reason"] != nil || !reflect.DeepEqual(split["slices"], []any{}) {
		t.Errorf("pending slice_split = %v", split)
	}
}

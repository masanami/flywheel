package cli

import (
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは `op add`（本人確認を要さない）の実コマンドを検証する
// （Issue #13）。approve <OP-ID> / reject <OP-ID>（本人確認を要する）は
// approval_test.go（端末を開く前に決着する経路）・approval_process_test.go
// （疑似端末を使う経路）が担う。

func TestRunOpAdd_RegistersPendingOperationAndReturnsID(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	doc := runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "ship it", "--ref", "https://example.com/pr/1")
	op := doc["operation"].(map[string]any)
	if op["id"] != "OP-1" {
		t.Errorf("id = %v, want OP-1", op["id"])
	}
	if op["challenge_id"] != id {
		t.Errorf("challenge_id = %v, want %q", op["challenge_id"], id)
	}
	if op["kind"] != "release" || op["summary"] != "ship it" || op["ref"] != "https://example.com/pr/1" {
		t.Errorf("operation = %+v, unexpected fields", op)
	}
	if op["state"] != "pending" {
		t.Errorf("state = %v, want pending", op["state"])
	}
	if op["version"] != float64(1) {
		t.Errorf("version = %v, want 1", op["version"])
	}

	show := runJSON(t, ws, "show", id)
	c := show["challenge"].(map[string]any)
	// create=1 -> op add=2。
	if c["version"] != float64(2) {
		t.Errorf("challenge version = %v, want 2 (op add bumps the challenge version)", c["version"])
	}
	ops := show["operations"].([]any)
	if len(ops) != 1 || ops[0].(map[string]any)["id"] != "OP-1" {
		t.Errorf("show.operations = %+v, want 1 entry OP-1", ops)
	}
}

func TestRunOpAdd_RefIsOptional(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	doc := runJSON(t, ws, "op", "add", id, "--kind", "delete", "--summary", "remove x")
	op := doc["operation"].(map[string]any)
	if op["ref"] != nil {
		t.Errorf("ref = %v, want nil", op["ref"])
	}
}

// AC: 続けて登録した不可逆操作の ID は課題をまたいで作成順に単調増加する。
func TestRunOpAdd_IDsAreMonotonicAcrossChallenges(t *testing.T) {
	ws := initializedWorkspace(t)
	c1 := runJSON(t, ws, "create", "--title", "t1")["challenge"].(map[string]any)["id"].(string)
	c2 := runJSON(t, ws, "create", "--title", "t2")["challenge"].(map[string]any)["id"].(string)

	op1 := runJSON(t, ws, "op", "add", c1, "--kind", "release", "--summary", "a")["operation"].(map[string]any)["id"]
	op2 := runJSON(t, ws, "op", "add", c2, "--kind", "delete", "--summary", "b")["operation"].(map[string]any)["id"]
	if op1 != "OP-1" || op2 != "OP-2" {
		t.Errorf("ids = %v, %v, want OP-1, OP-2", op1, op2)
	}
}

func TestRunOpAdd_InvalidKindIsValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	requireErrorCode(t, []string{"op", "add", id, "--kind", "bogus", "--summary", "s", "--workspace", ws}, 1, CodeValidationFailed)

	show := runJSON(t, ws, "show", id)
	if len(show["operations"].([]any)) != 0 {
		t.Errorf("operations = %+v, want none (rejected op add must not register)", show["operations"])
	}
}

func TestRunOpAdd_MissingRequiredFlagsIsUsageError(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	requireErrorCode(t, []string{"op", "add", id, "--summary", "s", "--workspace", ws}, 2, CodeUsageError)
	requireErrorCode(t, []string{"op", "add", id, "--kind", "release", "--workspace", ws}, 2, CodeUsageError)
}

func TestRunOpAdd_DoneChallengeIsTerminalState(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "done")

	requireErrorCode(t, []string{"op", "add", id, "--kind", "release", "--summary", "s", "--workspace", ws}, 1, CodeTerminalState)
}

func TestRunOpAdd_TextOutputIsOperationID(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	if got := runText(t, ws, "op", "add", id, "--kind", "release", "--summary", "s"); got != "OP-1\n" {
		t.Errorf("text = %q, want %q", got, "OP-1\n")
	}
}

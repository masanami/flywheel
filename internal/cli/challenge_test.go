package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/user"
	"strconv"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// runJSON は args を --workspace ws --json 付きで実行し、成功時は stdout の
// JSON をデコードして返す。失敗すれば Fatal する。
func runJSON(t *testing.T, ws string, args ...string) map[string]any {
	t.Helper()
	full := append(append([]string{}, args...), "--workspace", ws, "--json")
	var stdout, stderr bytes.Buffer
	code := run(full, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("args=%v exit=%d, want 0 (stderr=%s)", args, code, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("args=%v stdout is not valid JSON: %v (%q)", args, err, stdout.String())
	}
	return doc
}

// resolveExpectedActor は internal/core.resolveActor と同じ優先順位
// （os/user.Current().Username → $USER → $LOGNAME）で、このテストプロセス自身の
// actor を解決する。internal/cli のテストは internal/core の非公開の actorSource
// を差し替えられないため、実際に得られる reporter/actor をここで独立に計算し比較する。
func resolveExpectedActor(t *testing.T) string {
	t.Helper()
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	if v := os.Getenv("LOGNAME"); v != "" {
		return v
	}
	t.Fatal("could not resolve an actor for the test process (no user.Current, USER, LOGNAME)")
	return ""
}

func challengeIDToInternalID(t *testing.T, id string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimPrefix(id, "C-"))
	if err != nil {
		t.Fatalf("challengeIDToInternalID(%q): %v", id, err)
	}
	return n
}

func TestRunCreate_ReturnsUnclassifiedChallengeWithExpectedFields(t *testing.T) {
	ws := initializedWorkspace(t)
	wantActor := resolveExpectedActor(t)

	doc := runJSON(t, ws, "create", "--title", "t", "--description", "d", "--done-criteria", "dc", "--urgency", "高")
	c, ok := doc["challenge"].(map[string]any)
	if !ok {
		t.Fatalf("doc[challenge] = %#v, want an object", doc["challenge"])
	}
	if c["id"] != "C-1" {
		t.Errorf("id = %v, want C-1", c["id"])
	}
	if c["status"] != "unclassified" || c["status_label"] != "未分類" {
		t.Errorf("status/status_label = %v/%v, want unclassified/未分類", c["status"], c["status_label"])
	}
	if c["title"] != "t" || c["description"] != "d" || c["done_criteria"] != "dc" {
		t.Errorf("human-entry fields = %+v", c)
	}
	if c["urgency"] != "高" {
		t.Errorf("urgency = %v, want 高", c["urgency"])
	}
	if c["priority"] != nil {
		t.Errorf("priority = %v, want null", c["priority"])
	}
	if c["version"] != float64(1) {
		t.Errorf("version = %v, want 1", c["version"])
	}
	if c["reporter"] != wantActor {
		t.Errorf("reporter = %v, want %q", c["reporter"], wantActor)
	}
	for _, key := range []string{"created_at", "updated_at"} {
		s, ok := c[key].(string)
		if !ok || !strings.HasSuffix(s, "Z") {
			t.Errorf("%s = %v, want an RFC3339 UTC string", key, c[key])
		}
	}
}

func TestRunCreate_EmptyTitleIsValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	requireErrorCode(t, []string{"create", "--title", "   ", "--workspace", ws}, 1, CodeValidationFailed)
	if coretest.CountChallenges(t, ws) != 0 {
		t.Fatal("a challenge was created despite the empty title")
	}
}

func TestRunCreate_InvalidUrgencyIsValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	requireErrorCode(t, []string{"create", "--title", "t", "--urgency", "bogus", "--workspace", ws}, 1, CodeValidationFailed)
	if coretest.CountChallenges(t, ws) != 0 {
		t.Fatal("a challenge was created despite the invalid urgency")
	}
}

func TestRunShow_ReturnsEmptyListsForAFreshChallenge(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	doc := runJSON(t, ws, "show", id)
	for _, key := range []string{"plans", "approvals", "holds", "operations"} {
		arr, ok := doc[key].([]any)
		if !ok {
			t.Fatalf("doc[%s] = %#v, want an array", key, doc[key])
		}
		if len(arr) != 0 {
			t.Errorf("doc[%s] = %+v, want empty", key, arr)
		}
	}
	c := doc["challenge"].(map[string]any)
	if c["id"] != id {
		t.Errorf("challenge.id = %v, want %v", c["id"], id)
	}
}

func TestRunShow_NotFoundForMissingID(t *testing.T) {
	ws := initializedWorkspace(t)
	requireErrorCode(t, []string{"show", "C-999", "--workspace", ws}, 1, CodeNotFound)
}

func TestRunList_FiltersByStatusCodeAndLabel(t *testing.T) {
	ws := initializedWorkspace(t)
	runJSON(t, ws, "create", "--title", "a")
	created := runJSON(t, ws, "create", "--title", "b")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "classified")

	for _, status := range []string{"unclassified", "未分類"} {
		doc := runJSON(t, ws, "list", "--status", status)
		challenges, ok := doc["challenges"].([]any)
		if !ok {
			t.Fatalf("status=%q: doc[challenges] = %#v, want an array", status, doc["challenges"])
		}
		if len(challenges) != 1 {
			t.Fatalf("status=%q: len(challenges) = %d, want 1", status, len(challenges))
		}
	}
}

func TestRunList_InvalidStatusIsValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	for _, status := range []string{"計画承認待ち（未分類 → … → 完了）", "レビュー待ち"} {
		requireErrorCode(t, []string{"list", "--status", status, "--workspace", ws}, 1, CodeValidationFailed)
	}
}

func TestRunList_EmptyWorkspaceReturnsEmptyArray(t *testing.T) {
	ws := initializedWorkspace(t)
	doc := runJSON(t, ws, "list")
	challenges, ok := doc["challenges"].([]any)
	if !ok {
		t.Fatalf("doc[challenges] = %#v, want an array", doc["challenges"])
	}
	if len(challenges) != 0 {
		t.Fatalf("challenges = %+v, want empty", challenges)
	}
}

func TestRunEdit_UpdatesFieldsAndBumpsVersion(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	doc := runJSON(t, ws, "edit", id, "--title", "t2")
	c := doc["challenge"].(map[string]any)
	if c["title"] != "t2" {
		t.Errorf("title = %v, want t2", c["title"])
	}
	if c["version"] != float64(2) {
		t.Errorf("version = %v, want 2", c["version"])
	}
}

func TestRunEdit_NoChangeFlagsIsUsageError(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	requireErrorCode(t, []string{"edit", id, "--workspace", ws}, 2, CodeUsageError)
}

func TestRunEdit_EmptyTitleIsValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	requireErrorCode(t, []string{"edit", id, "--title", "   ", "--workspace", ws}, 1, CodeValidationFailed)
}

func TestRunEdit_DoneChallengeIsTerminalState(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "done")

	requireErrorCode(t, []string{"edit", id, "--title", "x", "--workspace", ws}, 1, CodeTerminalState)
}

func TestRunLog_OrdersActivitiesOldestFirstAndOmitsRejectedOrNoopChanges(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	// no-op edit (同じ値) は作業ログを増やさない。
	runJSON(t, ws, "edit", id, "--title", "t")
	// 実際に変わる edit は増える。
	runJSON(t, ws, "edit", id, "--title", "t2")

	doc := runJSON(t, ws, "log", id)
	activities, ok := doc["activities"].([]any)
	if !ok {
		t.Fatalf("doc[activities] = %#v, want an array", doc["activities"])
	}
	if len(activities) != 2 {
		t.Fatalf("len(activities) = %d, want 2 (create + 1 real edit, no-op excluded)", len(activities))
	}
	first := activities[0].(map[string]any)
	second := activities[1].(map[string]any)
	if first["action"] != "create" || second["action"] != "edit" {
		t.Errorf("actions = %v, %v, want create, edit", first["action"], second["action"])
	}
	if first["verification"] != "none" || second["verification"] != "none" {
		t.Errorf("verification = %v, %v, want none, none", first["verification"], second["verification"])
	}
	if first["before"] != nil {
		t.Errorf("create entry before = %v, want null", first["before"])
	}
}

func TestRunLog_WithoutIDReturnsAllChallenges(t *testing.T) {
	ws := initializedWorkspace(t)
	runJSON(t, ws, "create", "--title", "a")
	runJSON(t, ws, "create", "--title", "b")

	doc := runJSON(t, ws, "log")
	activities, ok := doc["activities"].([]any)
	if !ok {
		t.Fatalf("doc[activities] = %#v, want an array", doc["activities"])
	}
	if len(activities) != 2 {
		t.Fatalf("len(activities) = %d, want 2", len(activities))
	}
}

func TestRunLog_NotFoundForMissingChallengeID(t *testing.T) {
	ws := initializedWorkspace(t)
	requireErrorCode(t, []string{"log", "C-999", "--workspace", ws}, 1, CodeNotFound)
}

// runText は args を --workspace ws 付き（--json 無し）で実行し、stdout を返す。
func runText(t *testing.T, ws string, args ...string) string {
	t.Helper()
	full := append(append([]string{}, args...), "--workspace", ws)
	var stdout, stderr bytes.Buffer
	code := run(full, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("args=%v exit=%d, want 0 (stderr=%s)", args, code, stderr.String())
	}
	return stdout.String()
}

// TestRun_TextOutputIsHumanReadable は --json 無しの出力が map の %v ダンプではなく
// 人間向けの行であることを確認する（形式の安定は保証しないため、緩い検査に留める）。
func TestRun_TextOutputIsHumanReadable(t *testing.T) {
	ws := initializedWorkspace(t)
	if got := runText(t, ws, "create", "--title", "テキスト表示"); got != "C-1\n" {
		t.Fatalf("create text = %q, want %q", got, "C-1\n")
	}
	if got := runText(t, ws, "edit", "C-1", "--description", "d"); got != "C-1\n" {
		t.Fatalf("edit text = %q, want %q", got, "C-1\n")
	}
	for _, args := range [][]string{{"list"}, {"show", "C-1"}, {"log"}, {"log", "C-1"}} {
		got := runText(t, ws, args...)
		if strings.Contains(got, "map[") {
			t.Errorf("args=%v text output looks like a %%v dump: %q", args, got)
		}
		if !strings.Contains(got, "C-1") || !strings.Contains(got, "未分類") && args[0] != "log" {
			t.Errorf("args=%v text output = %q, want ID と状態の表示名を含む", args, got)
		}
	}
}

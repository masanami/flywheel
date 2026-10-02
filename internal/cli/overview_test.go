package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは `flywheel status`（Issue #14）の CLI レイヤーを検証する。
// 区分の判定ロジック自体（8状態すべての振り分け）は internal/core/overview_test.go
// が網羅的に検証するため、ここでは CLI が core.GetOverview の結果を正しい JSON
// キー・テキスト形式へ写像していること、コマンドの配線（--json・--workspace）が
// 機能することを確認する。

func TestRunStatus_EmptyWorkspaceReturnsThreeBucketsWithEmptyArrays(t *testing.T) {
	ws := initializedWorkspace(t)

	doc := runJSON(t, ws, "status")
	if len(doc) != 3 {
		t.Errorf("status top-level keys = %d, want 3 (one per bucket): %+v", len(doc), doc)
	}
	for _, path := range []string{
		"needs_human.challenges", "needs_human.operations",
		"actionable.challenges", "approved.operations",
	} {
		if ids := statusIDs(t, doc, path); len(ids) != 0 {
			t.Errorf("status[%q] = %v, want empty", path, ids)
		}
	}
	// AC「status --json の needs_human.triage の無いワークスペースでは [] で
	// ある」（#84）。
	needsHuman := doc["needs_human"].(map[string]any)
	if triage, ok := needsHuman["triage"].([]any); !ok || len(triage) != 0 {
		t.Errorf("status[\"needs_human\"][\"triage\"] = %v, want an empty array", needsHuman["triage"])
	}
	if slots, ok := needsHuman["slots"].([]any); !ok || len(slots) != 0 {
		t.Errorf("status[\"needs_human\"][\"slots\"] = %v, want an empty array", needsHuman["slots"])
	}
	for bucket, want := range map[string]int{"needs_human": 5, "actionable": 1, "approved": 1} {
		if m := doc[bucket].(map[string]any); len(m) != want {
			t.Errorf("status[%q] has %d keys, want %d: %+v", bucket, len(m), want, m)
		}
	}
}

func TestRunStatus_ChallengesAreClassifiedIntoNeedsHumanAndActionableBuckets(t *testing.T) {
	ws := initializedWorkspace(t)

	human := runJSON(t, ws, "create", "--title", "needs plan approval")["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, human), "awaiting_plan_approval")

	actionable := runJSON(t, ws, "create", "--title", "still unclassified")["challenge"].(map[string]any)["id"].(string)

	doneID := runJSON(t, ws, "create", "--title", "already done")["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, doneID), "done")

	doc := runJSON(t, ws, "status")
	humanChallenges := statusIDs(t, doc, "needs_human.challenges")
	actionableChallenges := statusIDs(t, doc, "actionable.challenges")

	if !containsString(humanChallenges, human) {
		t.Errorf("needs_human.challenges = %v, want to contain %s", humanChallenges, human)
	}
	if !containsString(actionableChallenges, actionable) {
		t.Errorf("actionable.challenges = %v, want to contain %s", actionableChallenges, actionable)
	}
	if containsString(humanChallenges, doneID) || containsString(actionableChallenges, doneID) {
		t.Errorf("done challenge %s must not appear in either challenge bucket (human=%v actionable=%v)", doneID, humanChallenges, actionableChallenges)
	}
	if containsString(humanChallenges, actionable) || containsString(actionableChallenges, human) {
		t.Errorf("challenges must not cross buckets: human=%v actionable=%v", humanChallenges, actionableChallenges)
	}
}

func TestRunStatus_PendingOperationAppearsInNeedsHumanOperations(t *testing.T) {
	ws := initializedWorkspace(t)
	id := runJSON(t, ws, "create", "--title", "t")["challenge"].(map[string]any)["id"].(string)
	opID := runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "ship it")["operation"].(map[string]any)["id"].(string)

	doc := runJSON(t, ws, "status")
	ops := statusIDs(t, doc, "needs_human.operations")
	if !containsString(ops, opID) {
		t.Errorf("needs_human.operations = %v, want to contain %s", ops, opID)
	}
	approved := statusIDs(t, doc, "approved.operations")
	if containsString(approved, opID) {
		t.Errorf("approved.operations unexpectedly contains pending operation %s", opID)
	}
}

func TestRunStatus_ApprovedOperationAppearsInApprovedOperationsList(t *testing.T) {
	ws := initializedWorkspace(t)
	id := runJSON(t, ws, "create", "--title", "t")["challenge"].(map[string]any)["id"].(string)
	opID := runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "ship it")["operation"].(map[string]any)["id"].(string)

	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", ws, "--json", opID}, opID)
	if code != 0 {
		t.Fatalf("approve exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOutput)
	}

	doc := runJSON(t, ws, "status")
	approved := statusIDs(t, doc, "approved.operations")
	if !containsString(approved, opID) {
		t.Errorf("approved.operations = %v, want to contain %s", approved, opID)
	}
	pending := statusIDs(t, doc, "needs_human.operations")
	if containsString(pending, opID) {
		t.Errorf("needs_human.operations unexpectedly contains approved operation %s", opID)
	}
}

func TestRunStatus_RejectedOperationIsExcludedFromStatus(t *testing.T) {
	ws := initializedWorkspace(t)
	id := runJSON(t, ws, "create", "--title", "t")["challenge"].(map[string]any)["id"].(string)
	opID := runJSON(t, ws, "op", "add", id, "--kind", "delete", "--summary", "cleanup")["operation"].(map[string]any)["id"].(string)

	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"reject", "--workspace", ws, "--json", "--reason", "not needed", opID}, opID)
	if code != 0 {
		t.Fatalf("reject exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOutput)
	}

	doc := runJSON(t, ws, "status")
	pending := statusIDs(t, doc, "needs_human.operations")
	approved := statusIDs(t, doc, "approved.operations")
	if containsString(pending, opID) || containsString(approved, opID) {
		t.Errorf("rejected operation %s must not appear anywhere (pending=%v approved=%v)", opID, pending, approved)
	}
}

// TestRunStatus_TextOutputHasThreeLabeledSectionsWithItemsInTheCorrectSection は、
// 3見出しが出力されることに加え、各見出しの配下に対応する項目（人間待ちの課題・
// 進められる課題・承認済みの不可逆操作）が実際に現れることを、見出しで区切った
// セクションごとに検証する（self-review 指摘: 旧版は文字列が出力全体のどこかに
// 現れるかしか見ておらず、項目が誤った見出しの下に出ていても、あるいは承認済みの
// 不可逆操作が一度もテキストに出なくても検出できなかった）。
func TestRunStatus_TextOutputHasThreeLabeledSectionsWithItemsInTheCorrectSection(t *testing.T) {
	ws := initializedWorkspace(t)

	waitingID := runJSON(t, ws, "create", "--title", "waiting title")["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, waitingID), "awaiting_plan_approval")

	runJSON(t, ws, "create", "--title", "actionable title")

	approvedID := runJSON(t, ws, "create", "--title", "t3")["challenge"].(map[string]any)["id"].(string)
	opID := runJSON(t, ws, "op", "add", approvedID, "--kind", "release", "--summary", "approved summary")["operation"].(map[string]any)["id"].(string)
	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", ws, "--json", opID}, opID)
	if code != 0 {
		t.Fatalf("approve exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOutput)
	}

	got := runText(t, ws, "status")
	humanHeading := "人間の操作を待っているもの"
	actionableHeading := "システムが次に進められるもの"
	approvedHeading := "承認済みの不可逆操作"

	humanIdx := strings.Index(got, humanHeading)
	actionableIdx := strings.Index(got, actionableHeading)
	approvedIdx := strings.Index(got, approvedHeading)
	if humanIdx < 0 || actionableIdx < 0 || approvedIdx < 0 {
		t.Fatalf("status text output = %q, want all three section headings", got)
	}
	if humanIdx >= actionableIdx || actionableIdx >= approvedIdx {
		t.Fatalf("section headings out of order: human=%d actionable=%d approved=%d (output=%q)", humanIdx, actionableIdx, approvedIdx, got)
	}

	humanSection := got[humanIdx:actionableIdx]
	actionableSection := got[actionableIdx:approvedIdx]
	approvedSection := got[approvedIdx:]

	if !strings.Contains(humanSection, "waiting title") {
		t.Errorf("human section = %q, want it to contain %q", humanSection, "waiting title")
	}
	if strings.Contains(actionableSection, "waiting title") || strings.Contains(approvedSection, "waiting title") {
		t.Errorf("\"waiting title\" leaked outside the human section (actionable=%q approved=%q)", actionableSection, approvedSection)
	}

	if !strings.Contains(actionableSection, "actionable title") {
		t.Errorf("actionable section = %q, want it to contain %q", actionableSection, "actionable title")
	}
	if strings.Contains(humanSection, "actionable title") || strings.Contains(approvedSection, "actionable title") {
		t.Errorf("\"actionable title\" leaked outside the actionable section (human=%q approved=%q)", humanSection, approvedSection)
	}

	if !strings.Contains(approvedSection, "approved summary") {
		t.Errorf("approved section = %q, want it to contain %q", approvedSection, "approved summary")
	}
	if strings.Contains(humanSection, "approved summary") || strings.Contains(actionableSection, "approved summary") {
		t.Errorf("\"approved summary\" leaked outside the approved section (human=%q actionable=%q)", humanSection, actionableSection)
	}
}

func TestRunStatus_DoesNotMutateChallengeState(t *testing.T) {
	ws := initializedWorkspace(t)
	id := runJSON(t, ws, "create", "--title", "t")["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "awaiting_plan_approval")

	before := runJSON(t, ws, "show", id)["challenge"].(map[string]any)
	runJSON(t, ws, "status")
	after := runJSON(t, ws, "show", id)["challenge"].(map[string]any)

	if before["version"] != after["version"] || before["status"] != after["status"] {
		t.Errorf("status must not mutate the challenge: before=%+v after=%+v", before, after)
	}
}

// statusIDs は status の JSON から「区分.一覧」（例 "needs_human.operations"）の
// 配列を取り出し、各要素の id を返す。
func statusIDs(t *testing.T, doc map[string]any, path string) []string {
	t.Helper()
	bucket, list, _ := strings.Cut(path, ".")
	m, ok := doc[bucket].(map[string]any)
	if !ok {
		t.Fatalf("status[%q] = %T, want object: %+v", bucket, doc[bucket], doc)
	}
	arr, ok := m[list].([]any)
	if !ok {
		t.Fatalf("status[%q] = %T, want array: %+v", path, m[list], doc)
	}
	out := []string{}
	for _, v := range arr {
		e, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("status[%q] element = %T, want object", path, v)
		}
		out = append(out, e["id"].(string))
	}
	return out
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

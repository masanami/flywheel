package cli

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは区切り S2b の課題 #773（followup list・show・log の出力）の受入基準を、実ストアで確かめる。

// followupFixture は課題 C-1・C-2 と、次のフォローアップを持つワークスペースを作る。
// F-1 未（C-1）・F-2 済み（C-1）・F-3 捨てた（C-2）・F-4 未（課題に属さない）・F-5 未で上げてある（C-2。上げた先は C-1）。
func followupFixture(t *testing.T) (ws, c1, c2 string) {
	t.Helper()
	ws = initializedWorkspace(t)
	c1 = createForCase(t, ws)
	c2 = createForCase(t, ws)
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 1, Title: "未の 1", Detail: "説明"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 1, Title: "済みの 1", Status: "done", Note: "確かめた"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 2, Title: "捨てた 1", Status: "dropped", Reason: "不要"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{Title: "課題に属さない"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 2, Title: "上げてある", PromotedChallengeID: 1})
	return ws, c1, c2
}

func followupIDs(t *testing.T, doc map[string]any) []string {
	t.Helper()
	list, ok := doc["followups"].([]any)
	if !ok {
		t.Fatalf("followups is not an array: %#v", doc["followups"])
	}
	ids := []string{}
	for _, e := range list {
		ids = append(ids, e.(map[string]any)["id"].(string))
	}
	sort.Strings(ids)
	return ids
}

// AC-32・AC-33・AC-34 #773: 根は followups、要素は決まったキーを持ち、id は F-<番号>。
func TestFollowupList_AC32_AC33_AC34_RootKeyElementKeysAndIDShape(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	doc := runJSON(t, ws, "followup", "list")
	if got := keysOf2(doc); !reflect.DeepEqual(got, []string{"followups"}) { // AC-32
		t.Errorf("AC-32: root keys = %v, want [followups]", got)
	}
	list := doc["followups"].([]any)
	if len(list) == 0 {
		t.Fatal("no followups returned")
	}
	want := []string{"challenge_id", "created_at", "detail", "id", "note", "promoted_challenge_id", "reason", "source", "status", "title", "updated_at"}
	for _, e := range list {
		m := e.(map[string]any)
		if got := keysOf2(m); !reflect.DeepEqual(got, want) { // AC-33
			t.Errorf("AC-33: element keys = %v, want %v", got, want)
		}
		if id := m["id"].(string); !strings.HasPrefix(id, "F-") || strings.Trim(id[2:], "0123456789") != "" || id == "F-" { // AC-34
			t.Errorf("AC-34: id = %q, want F-<番号>", id)
		}
	}
}

func keysOf2(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AC-35 #773: --status なしは open だけ。
func TestFollowupList_AC35_DefaultIsOpenOnly(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	if got := followupIDs(t, runJSON(t, ws, "followup", "list")); !reflect.DeepEqual(got, []string{"F-1", "F-4", "F-5"}) {
		t.Errorf("AC-35: ids = %v", got)
	}
}

// AC-36 #773
func TestFollowupList_AC36_StatusDone(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	if got := followupIDs(t, runJSON(t, ws, "followup", "list", "--status", "done")); !reflect.DeepEqual(got, []string{"F-2"}) {
		t.Errorf("AC-36: ids = %v", got)
	}
}

// AC-37 #773
func TestFollowupList_AC37_StatusDropped(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	if got := followupIDs(t, runJSON(t, ws, "followup", "list", "--status", "dropped")); !reflect.DeepEqual(got, []string{"F-3"}) {
		t.Errorf("AC-37: ids = %v", got)
	}
}

// AC-38 #773
func TestFollowupList_AC38_StatusAll(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	if got := followupIDs(t, runJSON(t, ws, "followup", "list", "--status", "all")); !reflect.DeepEqual(got, []string{"F-1", "F-2", "F-3", "F-4", "F-5"}) {
		t.Errorf("AC-38: ids = %v", got)
	}
}

// AC-39 #773: 3 つの状態と all 以外は usage_error（何も出さない）。
func TestFollowupList_AC39_UnknownStatusIsUsageError(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	for _, v := range []string{"bogus", "OPEN", "", "closed"} {
		requireErrorCode(t, []string{"followup", "list", "--status", v, "--workspace", ws}, 2, CodeUsageError)
	}
}

// AC-40 #773
func TestFollowupList_AC40_ChallengeFilter(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	doc := runJSON(t, ws, "followup", "list", "--status", "all", "--challenge", "C-2")
	if got := followupIDs(t, doc); !reflect.DeepEqual(got, []string{"F-3", "F-5"}) {
		t.Errorf("AC-40: ids = %v", got)
	}
	for _, e := range doc["followups"].([]any) {
		if cid := e.(map[string]any)["challenge_id"]; cid != "C-2" {
			t.Errorf("AC-40: challenge_id = %v, want C-2", cid)
		}
	}
}

// AC-41 #773: 存在しない課題は not_found。
func TestFollowupList_AC41_UnknownChallengeIsNotFound(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	requireErrorCode(t, []string{"followup", "list", "--challenge", "C-99", "--workspace", ws}, 1, CodeNotFound)
}

// AC-42 #773: 上げてある未のフォローアップは既定の一覧に含まれ、上げた先が出る。
func TestFollowupList_AC42_PromotedOpenIsIncluded(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	doc := runJSON(t, ws, "followup", "list")
	found := false
	for _, e := range doc["followups"].([]any) {
		m := e.(map[string]any)
		if m["id"] == "F-5" {
			found = true
			if m["status"] != "open" || m["promoted_challenge_id"] != "C-1" {
				t.Errorf("AC-42: F-5 = %v", m)
			}
		}
	}
	if !found {
		t.Error("AC-42: promoted open followup F-5 is missing from the default list")
	}
}

// AC-43 #773: 文の出力で、課題に属さないものが属するものより後に出る。
func TestFollowupList_AC43_TextPutsOrphansAfterChallengeOnes(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	code, out, errOut := runCLIRaw(t, "followup", "list", "--workspace", ws)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	orphan, c1, c2 := strings.Index(out, "課題に属さない"), strings.Index(out, "未の 1"), strings.Index(out, "上げてある")
	if orphan < 0 || c1 < 0 || c2 < 0 {
		t.Fatalf("AC-43: missing lines in %q", out)
	}
	if orphan < c1 || orphan < c2 {
		t.Errorf("AC-43: orphan line comes before a challenge's followup:\n%s", out)
	}
	if c1 > c2 {
		t.Errorf("AC-43: followups are not grouped by challenge in ascending order:\n%s", out)
	}
}

// AC-44 #773: 0 件でも終了コード 0。
func TestFollowupList_AC44_ZeroItemsExitsZero(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	code, out, errOut := runCLIRaw(t, "followup", "list", "--workspace", ws)
	if code != 0 || out != "" {
		t.Errorf("AC-44: exit=%d out=%q stderr=%q", code, out, errOut)
	}
	doc := runJSON(t, ws, "followup", "list")
	if l, ok := doc["followups"].([]any); !ok || len(l) != 0 {
		t.Errorf("AC-44: followups = %#v, want empty array", doc["followups"])
	}
}

// AC-288・AC-290 #773: show --json の根に followups があり、状態を問わず課題の全部を含む。
func TestShow_AC288_AC290_FollowupsAllStatusesOfTheChallenge(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	doc := runJSON(t, ws, "show", "C-1")
	if _, ok := doc["followups"]; !ok { // AC-288
		t.Fatal("AC-288: show --json has no followups key")
	}
	if got := followupIDs(t, doc); !reflect.DeepEqual(got, []string{"F-1", "F-2"}) { // AC-290
		t.Errorf("AC-290: ids = %v, want [F-1 F-2] (open and done of C-1 only)", got)
	}
	if got := followupIDs(t, runJSON(t, ws, "show", "C-2")); !reflect.DeepEqual(got, []string{"F-3", "F-5"}) {
		t.Errorf("AC-290: C-2 ids = %v", got)
	}
}

// AC-289 #773: 0 件の課題では空の配列。
func TestShow_AC289_NoFollowupsIsEmptyArray(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	l, ok := runJSON(t, ws, "show", id)["followups"].([]any)
	if !ok || len(l) != 0 {
		t.Errorf("AC-289: followups = %#v, want []", l)
	}
}

// AC-295・AC-296 #773: show の文の出力に未・済み・捨てたの件数が出て、上げてある未も未に数える。
func TestShow_AC295_AC296_TextCountsByStatusIncludingPromoted(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	_, out, _ := runCLIRaw(t, "show", "C-1", "--workspace", ws)
	if !strings.Contains(out, "フォローアップ: 未 1 件・済み 1 件・捨てた 0 件") { // AC-295
		t.Errorf("AC-295: C-1 show output lacks the counts:\n%s", out)
	}
	_, out, _ = runCLIRaw(t, "show", "C-2", "--workspace", ws)
	// F-5 は上げてあるが未のまま。AC-296: 未に数える。
	if !strings.Contains(out, "フォローアップ: 未 1 件・済み 0 件・捨てた 1 件") {
		t.Errorf("AC-296: C-2 show output does not count the promoted open followup as open:\n%s", out)
	}
}

// AC-301 #773: log <課題の ID> に、その課題のフォローアップの記録（entity が followup）が含まれる。
func TestLog_AC301_IncludesFollowupActivitiesOfTheChallenge(t *testing.T) {
	t.Parallel()
	ws, _, _ := followupFixture(t)
	doc := runJSON(t, ws, "log", "C-1")
	var ids []string
	for _, e := range doc["activities"].([]any) {
		m := e.(map[string]any)
		if m["entity"] == "followup" {
			ids = append(ids, m["entity_id"].(string))
		}
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"F-1", "F-2"}) {
		t.Errorf("AC-301: followup activity ids = %v, want [F-1 F-2]", ids)
	}
}

// AC-303 #773: 使い方の文に followup のコマンドがある。
func TestHelp_AC303_UsageMentionsFollowupList(t *testing.T) {
	t.Parallel()
	code, out, _ := runCLIRaw(t, "--help")
	if code != 0 || !strings.Contains(out, "followup list") {
		t.Errorf("AC-303: exit=%d, help lacks `followup list`:\n%s", code, out)
	}
}

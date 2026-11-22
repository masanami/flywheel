package cli

import (
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// fakeClaudeSVMatch は SV の起動の引数列を見分ける部分文字列（出力スキーマの手の値。他の判断点のスキーマには無い）。
const fakeClaudeSVMatch = `"split_followup"`

// AC-244・AC-245: 1 つのサイクルでスーパーバイザーの判断点を 2 回呼び、1 回は結果が executed の reverify、1 回は結果が failed のとき、
// `cycle --json` の phases の supervise の processed は 2、hands の reverify は 1。
// 偽の claude は同じ出力（reverify）を返し、止まり方 verify_uncertain の保留では選べる手なので executed、
// 止まり方 upstream_unreadable の保留では選べる手の外なので failed になる。
func TestCycle_SupervisePhase_CountsExecutedAndFailedCalls_AC244_AC245(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	writePositionFileForTest(t, ws, "pos")
	writeAgentJSONForTest(t, ws, cycleFullAgentJSON)
	sv := `{"is_error":false,"total_cost_usd":0.5,"structured_output":{"hand":"reverify","findings":"調べた","note":"もう一度確かめる","basis":[],"recommendation":"","options":[],"conditions":[]}}`
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{
		{Match: fakeClaudeSVMatch, Tag: "SV", Stdout: sv},
		j1RouteMine("P1"),
	}, "")
	for _, kind := range []string{"verify_uncertain", "upstream_unreadable"} {
		id := createForCase(t, ws)
		n := challengeIDToInternalID(t, id)
		coretest.InsertSupervisorHold(t, ws, n, "どうしますか？", "verifying", svRaisedAt, kind, "pending", "", "")
		coretest.SetChallengeStatus(t, ws, n, "awaiting_human")
	}

	doc := cycleDoc(t, ws)
	p := cyclePhase(t, doc, "supervise")
	if p["processed"] != float64(2) {
		t.Errorf("supervise processed = %v, want 2 (an executed call and a failed call)", p["processed"])
	}
	hands, ok := p["hands"].(map[string]any)
	if !ok {
		t.Fatalf("supervise hands = %#v, want an object", p["hands"])
	}
	for _, h := range []string{"defer", "escalate", "replan", "retry", "reverify", "send_back", "split_followup"} {
		want := float64(0)
		if h == "reverify" {
			want = 1
		}
		if hands[h] != want {
			t.Errorf("supervise hands[%s] = %v, want %v (the failed call is not counted as a hand)", h, hands[h], want)
		}
	}
}

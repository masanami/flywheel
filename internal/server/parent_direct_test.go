package server

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// 親が直接行った作業を完了として記録する道（#452）を、実ストア・実際の HTTP の要求で検証する。
// 終わった課題・取り下げの課題・関係を持たない課題も混じる状態で、区分・承認の要約・詳細を読む。

// parentDirectWorkspace は次の課題を持つワークスペース。
//
//	C-1 親（分類済）                           — 親子の親
//	C-2 種類が親が直接行う保留（C-1 の子）      — parent_agent
//	C-3 種類が人の対応を待つ保留                — human（answer_question）
//	C-4 親が直接行った記録を済ませた課題        — human（approve_completion。成果物の参照つき）
//	C-5 完了 / C-6 取り下げ / C-7 関係を持たない分類済
func parentDirectWorkspace(t *testing.T) string {
	t.Helper()
	ws := initWorkspace(t)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 1; i <= 7; i++ {
		if _, err := st.CreateChallenge(ctx, core.ChannelCLI, core.CreateInput{Title: "課題" + string(rune('0'+i)), DoneCriteria: "結果が Issue に書かれている"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()
	for _, id := range []int{1, 2, 3, 4, 7} {
		coretest.SetChallengeStatus(t, ws, id, "classified")
	}
	coretest.SetChallengeStatus(t, ws, 5, "done")
	coretest.SetChallengeStatus(t, ws, 6, "withdrawn")
	st, err = core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for _, id := range []string{"C-2", "C-4"} {
		if _, err := st.HoldChallenge(ctx, core.ChannelCLI, id, core.HoldInput{Question: "親が直接行う", Kind: string(core.HoldKindParentDirect)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.HoldChallenge(ctx, core.ChannelCLI, "C-3", core.HoldInput{Question: "オーナーに聞く"}); err != nil {
		t.Fatal(err)
	}
	note := "案 B を動かして確かめた"
	if _, _, err := st.CompleteDirect(ctx, core.ChannelCLI, "C-4", core.CompleteDirectInput{
		Artifacts: []string{"https://github.com/o/r/issues/337#issuecomment-1", "o/r#12"}, Note: &note,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetParent(ctx, core.ChannelCLI, "C-2", "C-1"); err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestParentDirect_GraphStatusBoardAndQuestionsSeparateItFromHumanTurns(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	ws := parentDirectWorkspace(t)
	port := startServerAt(t, now, core.FleetWorkspace{Name: "w", Path: ws})

	// 関係グラフ: C-2 だけが parent_agent（状態は人間対応待ちのまま、人間の操作は空）。
	code, body := apiGet(t, port, "/api/v1/workspaces/w/graph")
	if code != http.StatusOK {
		t.Fatalf("graph = %d %s", code, body)
	}
	category, state, actions := map[string]string{}, map[string]string{}, map[string][]string{}
	for _, n := range decodeMap(t, body)["nodes"].([]any) {
		m := n.(map[string]any)
		id := m["id"].(string)
		category[id], state[id], actions[id] = m["category"].(string), m["state"].(string), strs(m["next_human_actions"])
	}
	wantCategory := map[string]string{"C-1": "system", "C-2": "parent_agent", "C-3": "human", "C-4": "human", "C-5": "done", "C-6": "withdrawn", "C-7": "system"}
	if !reflect.DeepEqual(category, wantCategory) {
		t.Errorf("categories = %v, want %v", category, wantCategory)
	}
	if state["C-2"] != "awaiting_human" || len(actions["C-2"]) != 0 {
		t.Errorf("C-2 state/actions = %s/%v, want awaiting_human/[]", state["C-2"], actions["C-2"])
	}
	if !reflect.DeepEqual(actions["C-3"], []string{"answer_question"}) || !reflect.DeepEqual(actions["C-4"], []string{"approve_completion"}) {
		t.Errorf("human actions: C-3=%v C-4=%v", actions["C-3"], actions["C-4"])
	}

	// status: parent_agent に C-2 だけ、needs_human には C-3・C-4。
	status := mustJSON(t, port, "/api/v1/workspaces/w/status")
	ids := func(v any) []string {
		var out []string
		for _, c := range v.([]any) {
			out = append(out, c.(map[string]any)["id"].(string))
		}
		return out
	}
	if got := ids(status["parent_agent"].(map[string]any)["challenges"]); !reflect.DeepEqual(got, []string{"C-2"}) {
		t.Errorf("status.parent_agent.challenges = %v, want [C-2]", got)
	}
	if got := ids(status["needs_human"].(map[string]any)["challenges"]); !reflect.DeepEqual(got, []string{"C-3", "C-4"}) {
		t.Errorf("status.needs_human.challenges = %v, want [C-3 C-4]", got)
	}

	// ボードのカード: C-2 に問いに答える操作は出ない。
	for _, c := range mustJSON(t, port, "/api/v1/workspaces/w/challenges")["challenges"].([]any) {
		m := c.(map[string]any)
		if m["id"] == "C-2" && len(strs(m["card"].(map[string]any)["next_human_actions"])) != 0 {
			t.Errorf("C-2 card next_human_actions = %v, want none", m["card"])
		}
	}

	// 未回答の問い（オーナーが答えるもの）には C-3 だけ。
	var qIDs []string
	for _, q := range mustJSON(t, port, "/api/v1/questions")["questions"].([]any) {
		qIDs = append(qIDs, q.(map[string]any)["challenge_id"].(string))
	}
	if !reflect.DeepEqual(qIDs, []string{"C-3"}) {
		t.Errorf("questions = %v, want [C-3]", qIDs)
	}
}

func TestParentDirect_ApprovalSummaryAndDetailCarryTheArtifactsThenApprovalCompletes(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	ws := parentDirectWorkspace(t)
	port := startServerAt(t, now, core.FleetWorkspace{Name: "w", Path: ws})

	code, body := apiGet(t, port, "/api/v1/workspaces/w/challenges/C-4/approval")
	if code != http.StatusOK {
		t.Fatalf("approval = %d %s", code, body)
	}
	ap := decodeMap(t, body)["approval"].(map[string]any)
	if ap["kind"] != "completion" || ap["done_criteria"] != "結果が Issue に書かれている" {
		t.Fatalf("approval = %v", ap)
	}
	dc := ap["direct_completion"].(map[string]any)
	if !reflect.DeepEqual(strs(dc["artifacts"]), []string{"https://github.com/o/r/issues/337#issuecomment-1", "o/r#12"}) || dc["note"] != "案 B を動かして確かめた" {
		t.Errorf("direct_completion = %v", dc)
	}

	// 詳細（ボードのカードと同じ show）: 記録と保留の種類が出る。
	show := mustJSON(t, port, "/api/v1/workspaces/w/challenges/C-4")
	dcs := show["direct_completions"].([]any)
	if len(dcs) != 1 || len(strs(dcs[0].(map[string]any)["artifacts"])) != 2 {
		t.Errorf("show.direct_completions = %v", dcs)
	}
	if h := show["holds"].([]any)[0].(map[string]any); h["kind"] != "parent_direct" || h["answer"] != nil || h["closed_at"] == nil {
		t.Errorf("show.holds[0] = %v, want a closed parent_direct hold without an answer", h)
	}
	// 通常の課題には記録が無い（空の配列）。
	if got := mustJSON(t, port, "/api/v1/workspaces/w/challenges/C-7")["direct_completions"]; !reflect.DeepEqual(got, []any{}) {
		t.Errorf("C-7 direct_completions = %v, want []", got)
	}

	// 承認はオーナーの承認だけで完了になり、記録（親が直接行った印）は残る。
	version, err := json.Marshal(ap["version"])
	if err != nil {
		t.Fatal(err)
	}
	code, body = apiPost(t, port, "/api/v1/workspaces/w/challenges/C-4/approve", `{"version":`+string(version)+`}`)
	if code != http.StatusOK {
		t.Fatalf("approve = %d %s", code, body)
	}
	after := mustJSON(t, port, "/api/v1/workspaces/w/challenges/C-4")
	if after["challenge"].(map[string]any)["status"] != "done" || len(after["direct_completions"].([]any)) != 1 {
		t.Errorf("after approval: %v", after)
	}
	// 完了の承認の要約は、承認後は読めない（完了は終端）。
	if code, _ := apiGet(t, port, "/api/v1/workspaces/w/challenges/C-4/approval"); code == http.StatusOK {
		t.Errorf("approval summary after completion = %d, want an error", code)
	}
}

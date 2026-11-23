package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S4c-3 の見せ方（AC-105・AC-106・AC-107・AC-117〜AC-124）。twoHoldsWorkspace は、タスク a・b を止める保留 H-1・H-2 が開いている人間対応待ちの課題。

func TestShowJSON_HoldsCarryHoldIDAndScope(t *testing.T) { // AC-106, AC-107
	t.Parallel()
	ws, id := twoHoldsWorkspace(t)
	holds := anySlice(t, runJSON(t, ws, "show", id)["holds"], "holds")
	if len(holds) != 2 {
		t.Fatalf("holds = %d, want 2", len(holds))
	}
	for i, want := range []string{"H-1", "H-2"} {
		h := holds[i].(map[string]any)
		if h["hold_id"] != want || h["scope"] != "task" {
			t.Errorf("holds[%d] hold_id/scope = %v/%v, want %s/task", i, h["hold_id"], h["scope"], want)
		}
	}
}

func TestShowJSON_ChallengeWideAndClosedHoldsAreReadInTheSameForm(t *testing.T) { // AC-106, AC-107
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	coretest.InsertAnsweredHold(t, ws, n, "答え済み", "in_progress", "2026-11-22T00:00:00.000Z")
	coretest.InsertHold(t, ws, n, "課題への問い", "in_progress", "2026-11-22T00:00:01.000Z", nil)
	holds := anySlice(t, runJSON(t, ws, "show", id)["holds"], "holds")
	if len(holds) != 2 {
		t.Fatalf("holds = %d, want 2", len(holds))
	}
	for i, want := range []string{"H-1", "H-2"} {
		h := holds[i].(map[string]any)
		if h["hold_id"] != want || h["scope"] != "challenge" {
			t.Errorf("holds[%d] hold_id/scope = %v/%v, want %s/challenge", i, h["hold_id"], h["scope"], want)
		}
	}
}

func TestShowText_TwoOpenHoldsListIDTaskTitleQuestionAndHoldGuidance(t *testing.T) { // AC-118, AC-119, AC-120, AC-124
	t.Parallel()
	ws, id := twoHoldsWorkspace(t)
	out := runText(t, ws, "show", id)
	requireInOrder(t, out, "保留:", "H-1", "タスク: a", "問い: a への問い", "H-2", "タスク: b", "問い: b への問い")
	if !strings.Contains(out, "flywheel answer "+id+" --hold <H-ID>") {
		t.Errorf("show の案内に --hold が無い: %q", out)
	}
}

func TestShowText_OneOpenHoldHasNoHoldGuidance(t *testing.T) { // 開いている保留が 1 つなら、いまの案内（--hold なし）のまま
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	coretest.InsertHold(t, ws, challengeIDToInternalID(t, id), "問い", "in_progress", "2026-11-22T00:00:00.000Z", nil)
	if out := runText(t, ws, "show", id); strings.Contains(out, "--hold") {
		t.Errorf("開いている保留が 1 つなのに --hold の案内が出ている: %q", out)
	}
}

func TestStatusText_TwoOpenHoldsListIDTaskTitleAndHoldGuidance(t *testing.T) { // AC-121, AC-122, AC-123
	t.Parallel()
	ws, id := twoHoldsWorkspace(t)
	out := runText(t, ws, "status")
	requireInOrder(t, out, "あなたの回答を待っているもの", id, "H-1", "タスク: a", "H-2", "タスク: b")
	if !strings.Contains(out, "flywheel answer "+id+" --hold <H-ID>") {
		t.Errorf("status の案内に --hold が無い: %q", out)
	}
}

func TestStatusText_OneOpenHoldHasNoHoldGuidance(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	coretest.InsertHold(t, ws, n, "問い", "in_progress", "2026-11-22T00:00:00.000Z", nil)
	coretest.SetChallengeStatus(t, ws, n, "awaiting_human")
	out := runText(t, ws, "status")
	if strings.Contains(out, "--hold") {
		t.Errorf("開いている保留が 1 つなのに --hold の案内が出ている: %q", out)
	}
}

func TestStatusJSON_TaskHoldOnlyChallengeCountsOnceInNeedsHuman(t *testing.T) { // AC-105（保留の数ではなく課題の数）
	t.Parallel()
	ws, id := twoHoldsWorkspace(t)
	nh := runJSON(t, ws, "status")["needs_human"].(map[string]any)
	cs := anySlice(t, nh["challenges"], "challenges")
	if len(cs) != 1 || cs[0].(map[string]any)["id"] != id {
		t.Errorf("needs_human.challenges = %v, want [%s] の 1 件", cs, id)
	}
}

func TestStatusText_HoldOptionsArePerHold(t *testing.T) { // AC-117 の表示側: 選択肢は保留ごとに出る
	t.Parallel()
	ws, _ := twoHoldsWorkspace(t)
	out := runText(t, ws, "status")
	requireInOrder(t, out, "H-1", "選択肢:", "y: Y 案", "H-2", "選択肢:", "x: X 案")
}

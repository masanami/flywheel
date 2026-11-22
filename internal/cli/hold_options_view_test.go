package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは S4b-1 の表示の受入基準（`show`・`status` の保留の表示に選択肢の識別と見出し〔説明があれば説明〕、
// `show --json` の保留が API の保留と同じキー〔options・answer_option〕を持つ）を検証する。

const holdOptionsJSON = `[{"id":"add","label":"足す","description":"上限を 1 つ上げる"},{"id":"o2","label":"足さない","description":""}]`

func holdWithOptionsChallenge(t *testing.T, ws, answerOption string, status string) string {
	t.Helper()
	id := createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	coretest.InsertHoldWithOptions(t, ws, n, "上限を超えます。どうしますか？", "in_progress", "2026-11-22T00:00:00.000Z", holdOptionsJSON, answerOption)
	coretest.SetChallengeStatus(t, ws, n, status)
	return id
}

func TestShowText_HoldOptionsAreListedWithIDLabelAndDescription(t *testing.T) {
	t.Parallel() // S4b-1
	ws := initializedWorkspace(t)
	id := holdWithOptionsChallenge(t, ws, "", "awaiting_human")
	out := runText(t, ws, "show", id)
	requireInOrder(t, out,
		"保留:", "問い: 上限を超えます。どうしますか？",
		"選択肢:", "add: 足す", "上限を 1 つ上げる", "o2: 足さない",
		"回答: (未回答)")
}

func TestShowText_AnsweredHoldShowsChosenOption(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := holdWithOptionsChallenge(t, ws, "add", "in_progress")
	requireInOrder(t, runText(t, ws, "show", id), "保留:", "選択肢:", "o2: 足さない", "回答: add", "選んだ選択肢: add")
	h := anySlice(t, runJSON(t, ws, "show", id)["holds"], "holds")[0].(map[string]any)
	if h["answer_option"] != "add" {
		t.Errorf("answer_option = %#v, want add", h["answer_option"])
	}
}

func TestShowText_HoldWithoutOptionsHasNoOptionsLine(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	coretest.InsertHold(t, ws, challengeIDToInternalID(t, id), "問い", "in_progress", "2026-11-22T00:00:00.000Z", nil)
	if out := runText(t, ws, "show", id); strings.Contains(out, "選択肢:") {
		t.Errorf("選択肢の無い保留に「選択肢:」が出ている: %q", out)
	}
}

func TestShowJSON_HoldHasOptionsAndAnswerOptionKeys(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := holdWithOptionsChallenge(t, ws, "", "awaiting_human")
	plain := createForCase(t, ws)
	coretest.InsertHold(t, ws, challengeIDToInternalID(t, plain), "問い", "in_progress", "2026-11-22T00:00:00.000Z", nil)

	h := anySlice(t, runJSON(t, ws, "show", id)["holds"], "holds")[0].(map[string]any)
	opts := anySlice(t, h["options"], "options")
	if len(opts) != 2 {
		t.Fatalf("options = %#v, want 2", opts)
	}
	first := opts[0].(map[string]any)
	if first["id"] != "add" || first["label"] != "足す" || first["description"] != "上限を 1 つ上げる" {
		t.Errorf("options[0] = %#v", first)
	}
	if v, ok := h["answer_option"]; !ok || v != nil {
		t.Errorf("answer_option = %#v (present=%v), want null", v, ok)
	}

	p := anySlice(t, runJSON(t, ws, "show", plain)["holds"], "holds")[0].(map[string]any)
	if v, ok := p["options"]; !ok || v == nil || len(v.([]any)) != 0 {
		t.Errorf("options of a hold without options = %#v (present=%v), want []", v, ok)
	}
}

func TestStatusText_AnswerWaitingChallengeListsHoldOptions(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	holdWithOptionsChallenge(t, ws, "", "awaiting_human")
	out := runText(t, ws, "status")
	requireInOrder(t, out, "あなたの回答を待っているもの:", "選択肢:", "add: 足す", "上限を 1 つ上げる", "o2: 足さない")
}

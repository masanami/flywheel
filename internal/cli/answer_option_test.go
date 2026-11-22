package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// S4b-1 の 2: `answer --option`。保留を選択肢つきで作る口は CLI に無いので、選択肢つきの成立は core のテストが持ち、
// ここでは要約の出力と、CLI が断る道を確かめる。

func TestAnswerSummaryText_OptionShowsTheChosenLabelBeforeVerification(t *testing.T) {
	t.Parallel()
	p := &core.AnswerPreview{ChallengeID: "C-1", Title: "t", Question: "q", Option: "add", OptionLabel: "足す"}
	got := answerSummaryText(p, false)
	if !strings.Contains(got, "足す（add）") {
		t.Errorf("summary = %q, want the chosen label and id", got)
	}
	if strings.Contains(got, "添え書き") {
		t.Errorf("summary = %q, must not show an empty annotation", got)
	}
	p.Answer = "理由"
	if got := answerSummaryText(p, false); !strings.Contains(got, "添え書き: 理由") {
		t.Errorf("summary = %q, want the annotation", got)
	}
}

func TestRunAnswer_OptionIsRefusedWhereItCannotApply(t *testing.T) {
	t.Parallel()
	rel := []string{"--relayed", "--session", "s", "--quote", "選ぶ"}
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	runJSON(t, ws, append([]string{"approve", id}, rel...)...)
	runJSON(t, ws, "hold", id, "--question", "どうするか")

	// 選択肢を持たない保留への --option
	requireErrorCode(t, append([]string{"answer", id, "--option", "add", "--workspace", ws}, rel...), 1, CodeValidationFailed)
	// --option と --replan
	requireErrorCode(t, append([]string{"answer", id, "--option", "add", "--replan", "--workspace", ws}, rel...), 1, CodeValidationFailed)
	// --option と --choice
	requireErrorCode(t, append([]string{"answer", id, "--option", "add", "--choice", "1=1", "--workspace", ws}, rel...), 2, CodeUsageError)
	// 空の識別
	requireErrorCode(t, append([]string{"answer", id, "--option", " ", "--workspace", ws}, rel...), 2, CodeUsageError)
	if got := workStatus(t, ws, id); got != "awaiting_human" {
		t.Errorf("status = %q, want awaiting_human (nothing written)", got)
	}
	// 本人確認なしでは（--relayed も端末も無い）成立しない。
	requireErrorCode(t, []string{"answer", id, "--answer", "a", "--workspace", ws}, 1, CodeTTYRequired)
}

// 選択肢を持つ保留では --option に --replan を足した回答は検証の誤りで断られ、保留は開いたまま。--option だけなら成立する（対）。
func TestRunAnswer_OptionWithReplanIsRefusedOnAHoldWithOptions(t *testing.T) {
	t.Parallel()
	rel := []string{"--relayed", "--session", "s", "--quote", "選ぶ"}
	ws := initializedWorkspace(t)
	id := holdWithOptionsChallenge(t, ws, "", "awaiting_human")
	requireErrorCode(t, append([]string{"answer", id, "--option", "add", "--replan", "--workspace", ws}, rel...), 1, CodeValidationFailed)
	if got := workStatus(t, ws, id); got != "awaiting_human" {
		t.Errorf("status = %q, want awaiting_human (hold stays open)", got)
	}
	h := anySlice(t, runJSON(t, ws, "show", id)["holds"], "holds")[0].(map[string]any)
	if h["answer"] != nil {
		t.Errorf("answer = %#v, want none", h["answer"])
	}
	runJSON(t, ws, append([]string{"answer", id, "--option", "add", "--workspace", ws}, rel...)...)
	h = anySlice(t, runJSON(t, ws, "show", id)["holds"], "holds")[0].(map[string]any)
	if h["answer_option"] != "add" {
		t.Errorf("answer_option = %#v, want add", h["answer_option"])
	}
}

// 見出しに制御文字がある保留でも、検証の誤りの文には制御文字が出ず、見える形に直した見出しが並ぶ。
func TestRunAnswer_ValidationErrorEscapesControlCharsInOptionLabels(t *testing.T) {
	t.Parallel()
	rel := []string{"--relayed", "--session", "s", "--quote", "選ぶ"}
	const optionsJSON = `[{"id":"add","label":"足す\u001b[31m\t赤\u202e","description":""},{"id":"o2","label":"足さない","description":""}]`
	want := core.EscapeControl("足す\x1b[31m\t赤\u202e", false)
	for _, tc := range []struct {
		name  string
		args  []string
		extra string // 保留に足す前処理（識別の無い回答を断る印）
	}{
		{"unknown id", []string{"--option", "nope"}, ""},
		{"required hold without option", []string{"--answer", "自由な回答"}, "required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ws := initializedWorkspace(t)
			id := createForCase(t, ws)
			n := challengeIDToInternalID(t, id)
			coretest.InsertHoldWithOptions(t, ws, n, "どうしますか？", "in_progress", "2026-11-22T00:00:00.000Z", optionsJSON, "")
			coretest.SetChallengeStatus(t, ws, n, "awaiting_human")
			if tc.extra == "required" {
				coretest.Exec(t, ws, `UPDATE hold SET option_required = 1 WHERE challenge_id = ?`, n)
			}
			args := append(append([]string{"answer", id}, tc.args...), "--workspace", ws, "--json")
			args = append(args, rel...)
			var stdout, stderr bytes.Buffer
			if code := runWithLookPath(args, strings.NewReader(""), &stdout, &stderr, defaultCommands(), lookPathFor(t)); code != 1 {
				t.Fatalf("exit = %d, want 1 (stderr=%s)", code, stderr.String())
			}
			var doc struct {
				Error struct{ Code, Message string } `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Error.Code != string(CodeValidationFailed) || !strings.Contains(doc.Error.Message, "add ("+want+")") {
				t.Errorf("error = %+v, want a validation error listing %q", doc.Error, want)
			}
			if strings.ContainsAny(doc.Error.Message, "\x1b\t\u202e") {
				t.Errorf("message = %q has raw control characters", doc.Error.Message)
			}
			if got := workStatus(t, ws, id); got != "awaiting_human" {
				t.Errorf("status = %q, want awaiting_human", got)
			}
		})
	}
}

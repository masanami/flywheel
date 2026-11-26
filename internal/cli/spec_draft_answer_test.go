package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// 下書きづくりの保留への `flywheel answer --option`（--choice・--text の併用の可否と、誤りの写像）。core の規則は
// internal/core/spec_draft_answer_test.go が確かめる。テストの名前の AC の番号は、Issue #1354 の受け持ち。

const (
	saQuestionsJSON = `[{"question":"SQ-1","options":[{"label":"a1"},{"label":"a2"}],"recommended":1,"reason":"r1"},` +
		`{"question":"SQ-2","options":[{"label":"b1"},{"label":"b2"},{"label":"b3"}],"recommended":2,"reason":"r2"}]`
	saQuestionOptions = `[{"id":"answer","label":"推奨のまま進める"},{"id":"proceed","label":"いまの下書きで計画へ進む"},{"id":"skip","label":"仕様の文書なしで計画する"}]`
	saFailedOptions   = `[{"id":"retry","label":"やり直す"},{"id":"proceed","label":"いまの下書きで計画へ進む"},{"id":"skip","label":"仕様の文書なしで計画する"}]`
	saLimitOptions    = `[{"id":"proceed","label":"いまの下書きで計画へ進む"},{"id":"skip","label":"仕様の文書なしで計画する"}]`
)

func saWorkspace(t *testing.T, reason, questions, options string) (ws, id string) {
	t.Helper()
	ws = initializedWorkspace(t)
	id = createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	coretest.InsertSpecDraftHold(t, ws, n, reason, questions, options)
	coretest.SetChallengeStatus(t, ws, n, "awaiting_human")
	return ws, id
}

func saArgs(ws, id string, flags ...string) []string {
	return append(relayedAnswerArgs(id, flags...), "--workspace", ws)
}

func saRecorded(t *testing.T, ws, id string) []string {
	t.Helper()
	draft := runJSON(t, ws, "show", id)["challenge"].(map[string]any)["spec_draft"].(map[string]any)
	var out []string
	for _, a := range anySlice(t, draft["answers"], "answers") {
		out = append(out, a.(map[string]any)["answer"].(string))
	}
	return out
}

func TestAnswerSpecDraftCLI_QuestionHold(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		flags []string
		want  []string
	}{
		"AC-303 全部が推奨":               {nil, []string{"a1", "b2"}},
		"AC-304・AC-305 --choice 1=2": {[]string{"--choice", "1=2"}, []string{"a2", "b2"}},
		"AC-306 --text":              {[]string{"--text", "Q1=自由な答え"}, []string{"自由な答え", "b2"}},
		"AC-307 --hold の併用":          {[]string{"--choice", "1=2", "--hold", "H-1"}, []string{"a2", "b2"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ws, id := saWorkspace(t, "spec_question", saQuestionsJSON, saQuestionOptions)
			runJSON(t, ws, relayedAnswerArgs(id, append([]string{"--option", "answer"}, c.flags...)...)...)
			if got := saRecorded(t, ws, id); !reflect.DeepEqual(got, c.want) {
				t.Errorf("answers = %q, want %q", got, c.want)
			}
			if got := workStatus(t, ws, id); got != "classified" {
				t.Errorf("status = %q", got)
			}
		})
	}
}

func TestAnswerSpecDraftCLI_Errors(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		reason, questions, options string
		flags                      []string
		exit                       int
		code                       ErrorCode
	}{
		"AC-313 無い問いの番号":  {"spec_question", saQuestionsJSON, saQuestionOptions, []string{"--option", "answer", "--choice", "9=1"}, 1, CodeValidationFailed},
		"問いの番号 0":         {"spec_question", saQuestionsJSON, saQuestionOptions, []string{"--option", "answer", "--choice", "0=1"}, 1, CodeValidationFailed},
		"AC-315 無い選択肢の番号": {"spec_question", saQuestionsJSON, saQuestionOptions, []string{"--option", "answer", "--choice", "1=9"}, 1, CodeValidationFailed},
		"AC-316 同じ問いへの 2 つの答え": {"spec_question", saQuestionsJSON, saQuestionOptions,
			[]string{"--option", "answer", "--choice", "1=1", "--choice", "1=2"}, 1, CodeValidationFailed},
		"AC-317 proceed との併用": {"spec_question", saQuestionsJSON, saQuestionOptions, []string{"--option", "proceed", "--choice", "1=2"}, 1, CodeValidationFailed},
		"AC-318 失敗の保留":        {"spec_failed", "", saFailedOptions, []string{"--option", "retry", "--choice", "1=2"}, 2, CodeUsageError},
		"AC-319 回数の上限の保留":     {"spec_attempt_limit", saQuestionsJSON, saLimitOptions, []string{"--option", "proceed", "--choice", "1=2"}, 2, CodeUsageError},
		"AC-323 --answer との併用": {"spec_question", saQuestionsJSON, saQuestionOptions,
			[]string{"--option", "answer", "--choice", "1=2", "--answer", "文"}, 2, CodeUsageError},
		"--replan との併用":                {"spec_question", saQuestionsJSON, saQuestionOptions, []string{"--option", "answer", "--text", "1=x", "--replan"}, 2, CodeUsageError},
		"--option 無しの --hold":          {"spec_question", saQuestionsJSON, saQuestionOptions, []string{"--choice", "1=2", "--hold", "H-1"}, 2, CodeUsageError},
		"空の --hold":                    {"spec_question", saQuestionsJSON, saQuestionOptions, []string{"--option", "answer", "--choice", "1=2", "--hold", " "}, 2, CodeUsageError},
		"--choice の形が不正":               {"spec_question", saQuestionsJSON, saQuestionOptions, []string{"--option", "answer", "--choice", "1"}, 2, CodeUsageError},
		"AC-327 --option の無い --answer": {"spec_question", saQuestionsJSON, saQuestionOptions, []string{"--answer", "文"}, 1, CodeValidationFailed},
		"AC-328 選択肢に無い識別":              {"spec_question", saQuestionsJSON, saQuestionOptions, []string{"--option", "retry"}, 1, CodeValidationFailed},
		"AC-339 下書きの無い proceed":        {"spec_question", saQuestionsJSON, saQuestionOptions, []string{"--option", "proceed"}, 1, CodeValidationFailed},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ws, id := saWorkspace(t, c.reason, c.questions, c.options)
			requireErrorCode(t, saArgs(ws, id, c.flags...), c.exit, c.code)
			// AC-314・AC-340: 保留は開いたまま。
			h := anySlice(t, runJSON(t, ws, "show", id)["holds"], "holds")[0].(map[string]any)
			if h["answer"] != nil || workStatus(t, ws, id) != "awaiting_human" {
				t.Errorf("hold = %v, status = %s", h, workStatus(t, ws, id))
			}
		})
	}
}

// AC-320: 下書きづくりでない保留（選択肢 a を持つ）への --option a --choice は使い方の誤り。
func TestAnswerSpecDraftCLI_ChoiceWithOptionOnOtherHoldsIsAUsageError(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	coretest.InsertHoldWithOptions(t, ws, n, "どちらにする？", "in_progress", "2026-11-22T00:00:00.000Z", `[{"id":"a","label":"案 A"}]`, "")
	coretest.SetChallengeStatus(t, ws, n, "awaiting_human")
	requireErrorCode(t, saArgs(ws, id, "--option", "a", "--choice", "1=2"), 2, CodeUsageError)
	if got := workStatus(t, ws, id); got != "awaiting_human" {
		t.Errorf("status = %q", got)
	}
}

// AC-321・AC-322: 問いの保留へ --option 無しで --choice を渡すと使い方の誤りで、--option answer を案内する。
func TestAnswerSpecDraftCLI_ChoiceWithoutOptionGuidesToOptionAnswer(t *testing.T) {
	t.Parallel()
	ws, id := saWorkspace(t, "spec_question", saQuestionsJSON, saQuestionOptions)
	code, stderr := runAnswerCLI(t, ws, relayedAnswerArgs(id, "--choice", "1=2")...)
	if code != 2 || !strings.Contains(stderr, string(CodeUsageError)) || !strings.Contains(stderr, "--option answer") {
		t.Errorf("exit = %d, stderr = %s", code, stderr)
	}
}

// 下書きの問いの保留でない人間対応待ちの課題への --option 無しの --choice は、いままでどおり遷移できない誤り。
func TestAnswerSpecDraftCLI_ChoiceWithoutOptionOnOtherSpecHoldsIsUnchanged(t *testing.T) {
	t.Parallel()
	ws, id := saWorkspace(t, "spec_failed", "", saFailedOptions)
	requireErrorCode(t, saArgs(ws, id, "--choice", "1=2"), 1, CodeInvalidTransition)
}

func TestAnswerSummaryText_ListsTheAnswersPerQuestion(t *testing.T) {
	t.Parallel()
	got := answerSummaryText(&core.AnswerPreview{ChallengeID: "C-1", Option: "answer", OptionLabel: "推奨のまま進める",
		SpecAnswers: []core.SpecDraftAnswer{{Question: "SQ-1", Answer: "違う\n答え"}}}, false)
	if !strings.Contains(got, "Q1. SQ-1\n      → 違う\\n答え") {
		t.Errorf("summary = %q", got)
	}
}

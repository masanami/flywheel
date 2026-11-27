package cli

import (
	"bytes"
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
		"AC-217 --option の無い --choice と --answer": {"spec_question", saQuestionsJSON, saQuestionOptions,
			[]string{"--choice", "1=2", "--answer", "文"}, 2, CodeUsageError},
		"AC-218 --option answer と --choice と --replan": {"spec_question", saQuestionsJSON, saQuestionOptions,
			[]string{"--option", "answer", "--choice", "1=2", "--replan"}, 2, CodeUsageError},
		"--choice と --answer と --replan": {"spec_question", saQuestionsJSON, saQuestionOptions,
			[]string{"--option", "answer", "--choice", "1=2", "--answer", "文", "--replan"}, 2, CodeUsageError},
		"AC-220 失敗の保留の retry と --choice と --answer": {"spec_failed", "", saFailedOptions,
			[]string{"--option", "retry", "--choice", "1=2", "--answer", "文"}, 2, CodeUsageError},
		"回数の上限の保留と --choice と --answer": {"spec_attempt_limit", saQuestionsJSON, saLimitOptions,
			[]string{"--option", "proceed", "--choice", "1=2", "--answer", "文"}, 2, CodeUsageError},
		"AC-221 問いの保留の proceed と --choice と --answer": {"spec_question", saQuestionsJSON, saQuestionOptions,
			[]string{"--option", "proceed", "--choice", "1=2", "--answer", "文"}, 1, CodeValidationFailed},
		"AC-211・AC-212 2001 文字の添え書き": {"spec_question", saQuestionsJSON, saQuestionOptions,
			[]string{"--option", "answer", "--answer", strings.Repeat("あ", 2001)}, 1, CodeValidationFailed},
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

// --option があれば、--choice・--text と --answer を一緒に渡せる。
func TestAnswerSpecDraftCLI_ChoiceWithTheNote(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		flags []string
		want  []string
		text  string
	}{
		"AC-203 --choice と --answer": {[]string{"--choice", "1=2", "--answer", "短くする"}, []string{"a2", "b2", "短くする"},
			"問いに答えた（推奨と違う答え: Q1）\n短くする"},
		"AC-205 --text と --answer": {[]string{"--text", "1=自由な答え", "--answer", "短くする"}, []string{"自由な答え", "b2", "短くする"},
			"問いに答えた（推奨と違う答え: Q1）\n短くする"},
		"AC-214 空白だけの --answer": {[]string{"--answer", "   "}, []string{"a1", "b2"}, "推奨のまま進める"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ws, id := saWorkspace(t, "spec_question", saQuestionsJSON, saQuestionOptions)
			got := runJSON(t, ws, relayedAnswerArgs(id, append([]string{"--option", "answer"}, c.flags...)...)...)
			if h := got["hold"].(map[string]any); h["answer"] != c.text {
				t.Errorf("hold answer = %q, want %q", h["answer"], c.text)
			}
			if got := saRecorded(t, ws, id); !reflect.DeepEqual(got, c.want) {
				t.Errorf("answers = %q, want %q", got, c.want)
			}
		})
	}
}

// 下書きの問いの保留への回答の確認の要約（端末に出る）は、記録の残った問いを、番号・選択肢・推奨の印つきで出す。
func TestAnswerSpecDraftCLI_ConfirmationSummaryListsTheRemainingQuestions(t *testing.T) {
	t.Parallel()
	ws, id := saWorkspace(t, "spec_question", saQuestionsJSON, saQuestionOptions)
	code, out, _, stderr := runConfirmedChild(t, []string{"answer", id, "--option", "answer", "--workspace", ws}, id)
	if code != 0 {
		t.Fatalf("exit = %d\n%s\n%s", code, out, stderr)
	}
	out = strings.ReplaceAll(out, "\r", "")
	for name, want := range map[string]string{
		"AC-294 問いの文に Q1. を付ける": "\n残った問い:\n  Q1. SQ-1\n",
		"AC-295 選択肢の見出しを番号つきで":  "\n  Q2. SQ-2\n    1. b1\n    2. b2（推奨）\n    3. b3\n",
		"AC-296 推奨の後ろに（推奨）":     "\n    1. a1（推奨）\n    2. a2\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%s: summary lacks %q:\n%s", name, want, out)
		}
	}
}

// 問いの残っていない下書きづくりの保留（失敗の保留）と、ほかの保留の要約には、残った問いの一覧を出さない。
func TestAnswerSummaryText_RemainingQuestions(t *testing.T) {
	t.Parallel()
	for _, p := range []*core.AnswerPreview{
		{ChallengeID: "C-1", Option: "add", OptionLabel: "足す"},
		{ChallengeID: "C-1", Option: "retry", OptionLabel: "やり直す", SpecHoldReason: core.SpecDraftHoldFailed},
	} {
		if got := answerSummaryText(p, false); strings.Contains(got, "残った問い") {
			t.Errorf("summary = %q", got)
		}
	}
	got := answerSummaryText(&core.AnswerPreview{ChallengeID: "C-1", Option: "proceed", OptionLabel: "p",
		SpecHoldReason: core.SpecDraftHoldAttemptLimit, SpecQuestions: []core.SpecDraftQuestion{
			{Question: "偽の\n結果: 行", Options: []core.SpecDraftOption{{Label: "a\x1b[31m"}}, Recommended: 1}}}, false)
	if !strings.Contains(got, "Q1. 偽の\\n結果: 行\n") || strings.Contains(got, "\x1b") {
		t.Errorf("summary = %q, want escaped values", got)
	}
}

func TestHelp_AnswerExplainsTheNoteWithAnswersPerQuestion(t *testing.T) { // AC-225
	t.Parallel()
	var out, errb bytes.Buffer
	if code := run([]string{"help"}, strings.NewReader(""), &out, &errb, defaultCommands()); code != 0 {
		t.Fatalf("help = %d %s", code, errb.String())
	}
	_, after, ok := strings.Cut(out.String(), "\n  answer <ID> ")
	if !ok {
		t.Fatalf("help has no answer entry:\n%s", out.String())
	}
	entry, _, _ := strings.Cut(after, "\n  approve <ID>")
	const want = "下書きの問いの保留への --option answer では、--choice・--text と --answer（添え書き）を一緒に渡せる"
	if !strings.Contains(entry, want) {
		t.Errorf("answer entry lacks %q:\n%s", want, entry)
	}
}

// AC-219: 下書きづくりでない保留への --option a --choice --answer は使い方の誤り。
func TestAnswerSpecDraftCLI_ChoiceAndNoteOnOtherHoldsIsAUsageError(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	coretest.InsertHoldWithOptions(t, ws, n, "どちらにする？", "in_progress", "2026-11-22T00:00:00.000Z", `[{"id":"a","label":"案 A"}]`, "")
	coretest.SetChallengeStatus(t, ws, n, "awaiting_human")
	requireErrorCode(t, saArgs(ws, id, "--option", "a", "--choice", "1=2", "--answer", "文"), 2, CodeUsageError)
	if got := workStatus(t, ws, id); got != "awaiting_human" {
		t.Errorf("status = %q", got)
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

// 課題の詳細の spec_draft.answers の要素は、いつも kind を出す。種類を持たずに保存した要素は answer。
func TestShowJSON_SpecDraftAnswerKinds(t *testing.T) {
	t.Parallel()
	ws, id := saWorkspace(t, "spec_question", saQuestionsJSON, saQuestionOptions)
	coretest.SetSpecDraftAnswers(t, ws, challengeIDToInternalID(t, id),
		`[{"attempt":1,"question":"前の問い","answer":"前の答え","by":"owner","basis":[]}]`)
	draft := func() map[string]any {
		return runJSON(t, ws, "show", id)["challenge"].(map[string]any)["spec_draft"].(map[string]any)
	}
	kinds := func(d map[string]any) []any {
		var out []any
		for _, a := range anySlice(t, d["answers"], "answers") {
			out = append(out, a.(map[string]any)["kind"])
		}
		return out
	}
	before := draft()
	if got := kinds(before); !reflect.DeepEqual(got, []any{"answer"}) { // AC-279
		t.Errorf("kinds of a record without kind = %v", got)
	}
	if len(before) != 11 { // AC-281
		t.Errorf("spec_draft has %d keys: %v", len(before), before)
	}
	runJSON(t, ws, relayedAnswerArgs(id, "--option", "answer", "--answer", "短くする")...)
	after := draft()
	// AC-277（オーナーの答え）・AC-278（添え書き）
	if got := kinds(after); !reflect.DeepEqual(got, []any{"answer", "answer", "answer", "note"}) {
		t.Errorf("kinds = %v", got)
	}
	if len(after) != 11 { // AC-281
		t.Errorf("spec_draft has %d keys: %v", len(after), after)
	}
}

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは docs/features/core-redesign.md の「問いへの回答と計画の承認」の受入基準のうち、CLI の口
// （`flywheel answer <ID> --choice/--text`）で確かめるもの（AC-67〜AC-70・AC-74〜AC-80 と、口を通した
// AC-55・AC-59・AC-63〜AC-66・AC-71〜AC-73）を持つ。core の規則は internal/core/plan_answer_test.go が確かめる。

// planAnswerCaseSpec は問い 2 件（Q1: 選択肢 3 つ・推奨 2、Q2: 選択肢 2 つ・推奨 1）の計画の構造化した出力。
const planAnswerCaseSpec = `{"verdict":"plan","size":"S","topics":[{"title":"T","decision":"D","questions":[` +
	`{"question":"QTEXT-1","options":[{"label":"a1"},{"label":"a2"},{"label":"a3"}],"recommended":2},` +
	`{"question":"QTEXT-2","options":[{"label":"b1"},{"label":"b2"}],"recommended":1}]}]}`

const relayedTail = "--relayed --session SESS-1 --quote QUOTE-1"

// planAnswerWorkspace は計画の承認待ち（C-1。問い 2 件の計画の版 1）の課題を持つワークスペースを作る。
func planAnswerWorkspace(t *testing.T) (ws, id string) {
	t.Helper()
	ws = initializedWorkspace(t)
	id = createPlannedForCase(t, ws)
	coretest.SetPlanSpec(t, ws, challengeIDToInternalID(t, id), 1, planAnswerCaseSpec)
	return ws, id
}

// runAnswerCLI は answer を同じプロセスで実行する（--relayed は端末を要さない）。
func runAnswerCLI(t *testing.T, ws string, args ...string) (code int, stderr string) {
	t.Helper()
	full := append(append([]string{}, args...), "--workspace", ws, "--json")
	var out, errb bytes.Buffer
	code = runWithLookPath(full, strings.NewReader(""), &out, &errb, defaultCommands(), lookPathFor(t))
	return code, errb.String()
}

func relayedAnswerArgs(id string, flags ...string) []string {
	return append(append([]string{"answer", id}, flags...), strings.Fields(relayedTail)...)
}

func planAnswerRows(t *testing.T, ws string) [][]any {
	t.Helper()
	return coretest.QueryRows(t, ws, `SELECT question_number, choice, text, recommended, relay_session, relay_quote FROM plan_answer ORDER BY id`)
}

func paChallengeStatus(t *testing.T, ws, id string) string {
	t.Helper()
	return coretest.QueryRows(t, ws, `SELECT status FROM challenge WHERE id = ?`, challengeIDToInternalID(t, id))[0][0].(string)
}

// AC-55・AC-59・AC-63〜AC-67: --choice・--text の口が、推奨と違う答え・推奨と同じ答え・文の答え・複数・混在・番号だけの
// 問いの指定（1 と Q1 が同じ問い）を、core の規則どおりに記録する。
func TestAnswerPlanCLI_RecordsChoiceAndTextAnswers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		flags      []string
		wantStatus string
		wantRows   int
		check      func(t *testing.T, rows [][]any)
	}{
		{"non-recommended (AC-55)", []string{"--choice", "Q1=1"}, "classified", 1, func(t *testing.T, rows [][]any) {
			if rows[0][0] != int64(1) || rows[0][1] != int64(1) || rows[0][3] != int64(0) {
				t.Errorf("row = %v", rows[0])
			}
		}},
		{"recommended (AC-59)", []string{"--choice", "Q1=2"}, "awaiting_plan_approval", 1, func(t *testing.T, rows [][]any) {
			if rows[0][3] != int64(1) {
				t.Errorf("row = %v, want recommended", rows[0])
			}
		}},
		{"text (AC-63)", []string{"--text", "Q1=自由な答え"}, "classified", 1, func(t *testing.T, rows [][]any) {
			if rows[0][1] != nil || rows[0][2] != "自由な答え" {
				t.Errorf("row = %v, want text answer with NULL choice", rows[0])
			}
		}},
		{"two choices (AC-65)", []string{"--choice", "Q1=1", "--choice", "Q2=2"}, "classified", 2, nil},
		{"choice and text (AC-66)", []string{"--choice", "Q1=1", "--text", "Q2=文の答え"}, "classified", 2, func(t *testing.T, rows [][]any) {
			if rows[0][1] != int64(1) || rows[1][2] != "文の答え" || rows[1][1] != nil {
				t.Errorf("rows = %v", rows)
			}
		}},
		{"number only (AC-67)", []string{"--choice", "1=1"}, "classified", 1, func(t *testing.T, rows [][]any) {
			if rows[0][0] != int64(1) || rows[0][1] != int64(1) {
				t.Errorf("row = %v, want the same as --choice Q1=1", rows[0])
			}
		}},
		{"lowercase q", []string{"--choice", "q2=1"}, "awaiting_plan_approval", 1, func(t *testing.T, rows [][]any) {
			if rows[0][0] != int64(2) {
				t.Errorf("row = %v", rows[0])
			}
		}},
	}
	for _, tc := range cases {
		ws, id := planAnswerWorkspace(t)
		if code, stderr := runAnswerCLI(t, ws, relayedAnswerArgs(id, tc.flags...)...); code != 0 {
			t.Fatalf("%s: exit=%d stderr=%s", tc.name, code, stderr)
		}
		if got := paChallengeStatus(t, ws, id); got != tc.wantStatus {
			t.Errorf("%s: status = %s, want %s", tc.name, got, tc.wantStatus)
		}
		rows := planAnswerRows(t, ws)
		if len(rows) != tc.wantRows {
			t.Fatalf("%s: plan_answer rows = %d, want %d", tc.name, len(rows), tc.wantRows)
		}
		if tc.check != nil {
			tc.check(t, rows)
		}
	}
}

// AC-68〜AC-70・AC-74〜AC-76: 使い方の誤りは usage_error（終了コード 2）で、何も書かない。
func TestAnswerPlanCLI_UsageErrorsWriteNothing(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"same question twice via --choice (AC-68)":  {"--choice", "Q1=1", "--choice", "Q1=2"},
		"same question via choice and text (AC-69)": {"--choice", "Q1=1", "--text", "Q1=文"},
		"number-only duplicates Q-form":             {"--choice", "Q1=1", "--choice", "1=2"},
		"no = and no value (AC-70)":                 {"--choice", "Q1"},
		"text without =":                            {"--text", "Q1"},
		"empty value":                               {"--choice", "Q1="},
		"choice with --answer (AC-74)":              {"--choice", "Q1=1", "--answer", "a"},
		"text with --answer (AC-75)":                {"--text", "Q1=文", "--answer", "a"},
		"choice with --replan":                      {"--choice", "Q1=1", "--replan"},
		"neither choice, text nor answer (AC-76)":   {},
	}
	for name, flags := range cases {
		ws, id := planAnswerWorkspace(t)
		code, stderr := runAnswerCLI(t, ws, relayedAnswerArgs(id, flags...)...)
		if code != 2 || readErrorCode(t, stderr) != string(CodeUsageError) {
			t.Errorf("%s: exit=%d stderr=%s, want usage_error (2)", name, code, stderr)
		}
		if rows := planAnswerRows(t, ws); len(rows) != 0 {
			t.Errorf("%s: plan_answer rows = %v, want none", name, rows)
		}
		if got := paChallengeStatus(t, ws, id); got != "awaiting_plan_approval" {
			t.Errorf("%s: status = %s", name, got)
		}
	}
}

// AC-71〜AC-73: 計画に無い問いの番号・選択肢の数より大きい番号は validation_failed で、1 件でも落ちれば何も書かない。
func TestAnswerPlanCLI_ValidationFailuresWriteNothing(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"unknown question (AC-71/AC-72)":      {"--choice", "Q3=1"},
		"valid then unknown question (AC-72)": {"--choice", "Q1=1", "--choice", "Q9=1"},
		"choice over the options (AC-73)":     {"--choice", "Q1=4"},
		"valid then choice over (AC-73)":      {"--text", "Q2=文", "--choice", "Q1=4"},
	}
	for name, flags := range cases {
		ws, id := planAnswerWorkspace(t)
		code, stderr := runAnswerCLI(t, ws, relayedAnswerArgs(id, flags...)...)
		if code == 0 || readErrorCode(t, stderr) != string(CodeValidationFailed) {
			t.Errorf("%s: exit=%d stderr=%s, want validation_failed", name, code, stderr)
		}
		if rows := planAnswerRows(t, ws); len(rows) != 0 {
			t.Errorf("%s: plan_answer rows = %v, want none", name, rows)
		}
	}
}

// AC-77: 計画の承認待ちでない課題への答えは invalid_transition。
func TestAnswerPlanCLI_NotAwaitingPlanApprovalIsInvalidTransition(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	code, stderr := runAnswerCLI(t, ws, relayedAnswerArgs(id, "--choice", "Q1=1")...)
	if code == 0 || readErrorCode(t, stderr) != string(CodeInvalidTransition) {
		t.Fatalf("exit=%d stderr=%s, want invalid_transition", code, stderr)
	}
	if rows := planAnswerRows(t, ws); len(rows) != 0 {
		t.Errorf("plan_answer rows = %v, want none", rows)
	}
}

// AC-78: 端末でない実行（標準入力がパイプ）で --relayed が無ければ、--answer と同じく tty_required で、何も書かない。
func TestAnswerPlanCLI_WithoutTerminalAndWithoutRelayedIsTTYRequired(t *testing.T) {
	t.Parallel()
	ws, id := planAnswerWorkspace(t)
	for _, flag := range [][]string{{"--choice", "Q1=1"}, {"--text", "Q1=文"}, {"--choice", "Q1=2"}} {
		args := append([]string{"answer", id}, flag...)
		code, _, stderr := runPiped(t, append(args, "--workspace", ws, "--json"))
		if code != 1 || readErrorCode(t, stderr) != string(CodeTTYRequired) {
			t.Errorf("%v: code=%d stderr=%s, want tty_required", flag, code, stderr)
		}
	}
	if rows := planAnswerRows(t, ws); len(rows) != 0 {
		t.Errorf("plan_answer rows = %v, want none", rows)
	}
	if got := paChallengeStatus(t, ws, id); got != "awaiting_plan_approval" {
		t.Errorf("status = %s", got)
	}
}

// AC-79・AC-80: --relayed の答えは、plan_answer の relay_session に会話の識別、relay_quote に発言の要旨が残る。
func TestAnswerPlanCLI_RelayedRecordsSessionAndQuote(t *testing.T) {
	t.Parallel()
	for _, flag := range [][]string{{"--choice", "Q1=1"}, {"--text", "Q1=文"}, {"--choice", "Q1=2"}} {
		ws, id := planAnswerWorkspace(t)
		code, stderr := runAnswerCLI(t, ws, relayedAnswerArgs(id, flag...)...)
		if code != 0 {
			t.Fatalf("%v: exit=%d stderr=%s", flag, code, stderr)
		}
		rows := planAnswerRows(t, ws)
		if len(rows) != 1 || rows[0][4] != "SESS-1" || rows[0][5] != "QUOTE-1" {
			t.Errorf("%v: rows = %v, want relay_session SESS-1 and relay_quote QUOTE-1 (AC-79/AC-80)", flag, rows)
		}
	}
}

// 端末の本人確認（対象の ID の完全一致）が成立すれば、--relayed なしでも答えが記録される。
func TestAnswerPlanCLI_TerminalConfirmationRecordsTheAnswer(t *testing.T) {
	t.Parallel()
	ws, id := planAnswerWorkspace(t)
	code, pty, stdout, stderr := runConfirmedChild(t, []string{"answer", id, "--choice", "Q1=1", "--workspace", ws, "--json"}, id)
	if code != 0 {
		t.Fatalf("exit=%d pty=%q stdout=%q stderr=%q", code, pty, stdout, stderr)
	}
	for _, want := range []string{"QTEXT-1", "a1", "推奨と違う"} {
		if !strings.Contains(pty, want) {
			t.Errorf("the confirmation summary lacks %q: %q", want, pty)
		}
	}
	rows := planAnswerRows(t, ws)
	if len(rows) != 1 || rows[0][4] != nil {
		t.Errorf("rows = %v, want one row without relay evidence", rows)
	}
	if got := paChallengeStatus(t, ws, id); got != "classified" {
		t.Errorf("status = %s, want classified", got)
	}
}

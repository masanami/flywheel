package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// `flywheel show` の文の出力の、下書きづくりの残った問い。テストの名前の AC の番号は、Issue #1376 の受け持ち（親 #1374 の受入基準の並び順）。

// AC-282〜286
func TestRunShow_SpecDraftRemainingQuestions(t *testing.T) {
	t.Parallel()
	ws, id := saWorkspace(t, "spec_question", saQuestionsJSON, saQuestionOptions)
	want := "下書きづくり:  オーナーの回答待ち (awaiting_owner)・実行 1 回（上限 3 回）\n" +
		"残った問い:\n" +
		"  Q1. SQ-1\n" +
		"    1. a1（推奨）\n" +
		"    2. a2\n" +
		"    推奨の理由: r1\n" +
		"  Q2. SQ-2\n" +
		"    1. b1\n" +
		"    2. b2（推奨）\n" +
		"    3. b3\n" +
		"    推奨の理由: r2\n" +
		"作成:"
	if text := runText(t, ws, "show", id); !strings.Contains(text, want) {
		t.Errorf("show text lacks\n%s\ngot\n%s", want, text)
	}
}

// 問いの文の改行や制御文字で、偽の行を作れない。
func TestRunShow_SpecDraftQuestionsAreEscaped(t *testing.T) {
	t.Parallel()
	ws, id := saWorkspace(t, "spec_question",
		`[{"question":"q\n状態: 偽","options":[{"label":"a\u001b[2J"},{"label":"b"}],"recommended":2,"reason":"r\nID: 偽"}]`, saQuestionOptions)
	text := runText(t, ws, "show", id)
	for _, w := range []string{`  Q1. q\n状態: 偽` + "\n", `    1. a\x1b[2J` + "\n", `    推奨の理由: r\nID: 偽` + "\n"} {
		if !strings.Contains(text, w) {
			t.Errorf("show text lacks %q:\n%s", w, text)
		}
	}
}

// AC-287・293。回答待ちでない記録には、問いが入っていても出さない。下書きの本文は出さない。
func TestRunShow_SpecDraftWithoutRemainingQuestions(t *testing.T) {
	t.Parallel()
	const body = "下書きの本文にだけある文字列-7f3a"

	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	insertSpecDraftRow(t, ws, id)
	if text := runText(t, ws, "show", id); strings.Contains(text, "残った問い:") {
		t.Errorf("show text of a pending record has the questions line:\n%s", text)
	}
	coretest.Exec(t, ws, `UPDATE spec_draft SET questions = ? WHERE challenge_id = ?`, saQuestionsJSON, challengeIDToInternalID(t, id))
	if text := runText(t, ws, "show", id); strings.Contains(text, "残った問い:") {
		t.Errorf("show text of a pending record has the questions line:\n%s", text)
	}

	failedWS, failedID := saWorkspace(t, "spec_failed", "", saFailedOptions)
	if text := runText(t, failedWS, "show", failedID); strings.Contains(text, "残った問い:") {
		t.Errorf("show text of a failed record has the questions line:\n%s", text)
	}

	qws, qid := saWorkspace(t, "spec_question", saQuestionsJSON, saQuestionOptions)
	draft := filepath.Join(qws, ".flywheel", "specs", fmt.Sprintf("c-%d.md", challengeIDToInternalID(t, qid)))
	if err := os.MkdirAll(filepath.Dir(draft), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(draft, []byte("# 下書き\n"+body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if text := runText(t, qws, "show", qid); strings.Contains(text, body) || !strings.Contains(text, "残った問い:") {
		t.Errorf("show text =\n%s", text)
	}
}

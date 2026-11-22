package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
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

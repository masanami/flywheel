package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

const showSupervisorHeading = "スーパーバイザーが答えた問い:"

func showSpecDraftDetail(state core.SpecDraftState, answers ...core.SpecDraftAnswer) *core.ChallengeDetail {
	d := &core.ChallengeDetail{SpecDraft: &core.SpecDraft{Path: ".flywheel/specs/c-1.md", Repo: "app", State: state, Attempts: 1, Answers: answers}}
	if state == core.SpecDraftStateAwaitingOwner {
		d.SpecDraft.Questions = []core.SpecDraftQuestion{{Question: "残る問い", Options: []core.SpecDraftOption{{Label: "x"}, {Label: "y"}}, Recommended: 1, Reason: "r"}}
	}
	return d
}

// AC-288〜AC-291（Issue #1382）: show は、スーパーバイザーが答えた問い・答え・根拠の場所を、残った問いの下に出す。
func TestChallengeDetailText_SpecSupervisorAnswers(t *testing.T) {
	answer := core.SpecDraftAnswer{Kind: core.SpecDraftAnswerKindAnswer, Attempt: 1, Question: "答えた問いの文", Answer: "選んだ答え", By: "supervisor",
		Basis: []core.SpecDraftBasis{{Path: "docs/a.md", Locator: "節 2"}}}
	for _, state := range []core.SpecDraftState{core.SpecDraftStateAwaitingOwner, core.SpecDraftStatePending, core.SpecDraftStateDone} {
		got := challengeDetailText(showSpecDraftDetail(state, answer))
		for _, want := range []string{showSupervisorHeading + "\n", "答えた問いの文", "選んだ答え", "docs/a.md（節 2）"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: show lacks %q:\n%s", state, want, got)
			}
		}
		if state == core.SpecDraftStateAwaitingOwner && strings.Index(got, "残った問い:") > strings.Index(got, showSupervisorHeading) {
			t.Errorf("the supervisor answers are above the remaining questions:\n%s", got)
		}
	}
}

// AC-292・AC-297（Issue #1382）: スーパーバイザーの答えが無ければ見出しを出さない。添え書きはどちらの一覧にも出さない。
func TestChallengeDetailText_NoSupervisorAnswersAndNotesAreHidden(t *testing.T) {
	owner := core.SpecDraftAnswer{Kind: core.SpecDraftAnswerKindAnswer, Attempt: 1, Question: "q", Answer: "owner の答え", By: "owner"}
	note := core.SpecDraftAnswer{Kind: core.SpecDraftAnswerKindNote, Attempt: 1, Answer: "添え書きの文", By: "owner"}
	got := challengeDetailText(showSpecDraftDetail(core.SpecDraftStateAwaitingOwner, owner, note))
	if strings.Contains(got, showSupervisorHeading) || strings.Contains(got, "添え書きの文") {
		t.Errorf("show:\n%s", got)
	}
	// 種類が添え書きの要素は、答えた者に関わらず出さない。
	note.By = "supervisor"
	if got := challengeDetailText(showSpecDraftDetail(core.SpecDraftStateAwaitingOwner, note)); strings.Contains(got, showSupervisorHeading) || strings.Contains(got, "添え書きの文") {
		t.Errorf("show:\n%s", got)
	}
}

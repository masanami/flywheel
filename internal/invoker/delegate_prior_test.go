package invoker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// 前の成果物（前の委譲が残した PR）を、新しいセッションの標準入力と、すべての再開の標準入力に
// 渡す（値は外部由来のデータの区画、続け方の固定の文は区画の外）。

func openPrior() *core.PriorArtifact {
	return &core.PriorArtifact{State: core.PriorArtifactOpen, Branch: "feat/prior-x", PRURL: "https://github.com/o/r/pull/12", HeadSHA: "deadbeef01"}
}

// splitPriorSection は out から、データの区画「前の成果物」の中身と、区画の外の文を返す。区画が無ければ ok=false。
func splitPriorSection(out string) (inside, outside string, ok bool) {
	begin := dataSectionBeginPrefix + priorArtifactLabel + dataSectionBeginSuffix + "\n"
	i := strings.Index(out, begin)
	if i < 0 {
		return "", out, false
	}
	rest := out[i+len(begin):]
	j := strings.Index(rest, dataSectionEnd)
	if j < 0 {
		return "", out, false
	}
	return rest[:j], out[:i] + rest[j+len(dataSectionEnd):], true
}

func TestBuildDelegationStdin_PriorOpen_ValuesInADataSectionAfterTheBriefAndContinueSentenceOutside(t *testing.T) {
	out, err := BuildDelegationStdin(DelegationBrief{Decider: "child", DeciderRow: 4, Brief: "BRIEF-BODY", Prior: openPrior()})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	inside, outside, ok := splitPriorSection(s)
	if !ok {
		t.Fatalf("no data section for the prior artifact:\n%s", s)
	}
	for _, want := range []string{"feat/prior-x", "https://github.com/o/r/pull/12", "deadbeef01"} {
		if !strings.Contains(inside, want) {
			t.Errorf("the data section lacks %q:\n%s", want, inside)
		}
		if strings.Contains(outside, want) {
			t.Errorf("the value %q must be only inside the data section", want)
		}
	}
	for _, want := range []string{"同じ PR", "新しいブランチ", "新しい PR も作らない"} {
		if !strings.Contains(outside, want) {
			t.Errorf("the fixed sentence outside the section lacks %q:\n%s", want, outside)
		}
		if strings.Contains(inside, want) {
			t.Errorf("the fixed sentence %q must not be inside the data section", want)
		}
	}
	// 位置: J3 の出力の区画の後、完了報告の様式の前。
	fixed, _ := BriefFixedSections()
	var completion string
	for _, f := range fixed {
		if f.ID == "completion_report_style" {
			completion = f.Body
		}
	}
	end := strings.Index(s, briefRegionEnd)
	sec := strings.Index(s, dataSectionBeginPrefix+priorArtifactLabel)
	comp := strings.Index(s, completion)
	if end < 0 || comp < 0 || end >= sec || sec >= comp {
		t.Errorf("order: brief end %d, prior section %d, completion style %d", end, sec, comp)
	}
	if !strings.Contains(s, dataSectionGuardNote) {
		t.Error("the guard note for data sections is missing")
	}
	if strings.Contains(inside, "BRIEF-BODY") {
		t.Error("the brief must not be inside the prior section")
	}
}

func TestBuildDelegationStdin_PriorClosedOrMerged_AllowsANewPRAndNeverPassesTheURLOrBranch(t *testing.T) {
	for _, st := range []core.PriorArtifactState{core.PriorArtifactClosed, core.PriorArtifactMerged} {
		t.Run(string(st), func(t *testing.T) {
			// core は閉じた PR の値を入れないが、入っていても続け先として渡さない。
			p := openPrior()
			p.State = st
			out, err := BuildDelegationStdin(DelegationBrief{Decider: "child", DeciderRow: 4, Brief: "b", Prior: p})
			if err != nil {
				t.Fatal(err)
			}
			s := string(out)
			if !strings.Contains(s, "新しい PR を作ってよい") {
				t.Errorf("stdin lacks the sentence allowing a new PR:\n%s", s)
			}
			for _, bad := range []string{"https://github.com/o/r/pull/12", "feat/prior-x", "deadbeef01"} {
				if strings.Contains(s, bad) {
					t.Errorf("stdin must not contain %q for a closed PR", bad)
				}
			}
			if _, _, ok := splitPriorSection(s); ok {
				t.Error("a closed PR has no data section")
			}
			if strings.Contains(s, "同じ PR へ push") {
				t.Error("a closed PR must not ask to continue on the same PR")
			}
		})
	}
}

func TestBuildDelegationStdin_PriorUnknown_AsksToCheckWithGhPrViewAndCarriesTheValues(t *testing.T) {
	p := &core.PriorArtifact{State: core.PriorArtifactUnknown, Branch: "feat/prior-x", PRURL: "https://github.com/o/r/pull/12"}
	out, err := BuildDelegationStdin(DelegationBrief{Decider: "child", DeciderRow: 4, Brief: "b", Prior: p})
	if err != nil {
		t.Fatal(err)
	}
	inside, outside, ok := splitPriorSection(string(out))
	if !ok || !strings.Contains(inside, "https://github.com/o/r/pull/12") || !strings.Contains(inside, "feat/prior-x") {
		t.Fatalf("the unknown state must still carry the values in the section: ok=%v\n%s", ok, out)
	}
	for _, want := range []string{"確かめられなかった", "gh pr view", "同じ PR", "新しい PR を作ってよい"} {
		if !strings.Contains(outside, want) {
			t.Errorf("the fixed sentence lacks %q:\n%s", want, outside)
		}
	}
	if strings.Contains(inside, "head") {
		t.Errorf("an unknown head must not be written:\n%s", inside)
	}
}

func TestBuildDelegationStdin_NoPrior_HasNoPriorSectionOrSentence(t *testing.T) {
	out, err := BuildDelegationStdin(DelegationBrief{Decider: "child", DeciderRow: 4, Brief: "b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{priorArtifactLabel, "gh pr view", "新しい PR を作ってよい"} {
		if strings.Contains(string(out), bad) {
			t.Errorf("a delegation without a prior PR must not mention %q", bad)
		}
	}
}

func TestBuildDelegationResumeStdin_PriorIsCarriedByEveryResumeKind(t *testing.T) {
	for _, kind := range []core.ResumeKind{core.ResumeKindAnswer, core.ResumeKindInterrupted, core.ResumeKindBudget, core.ResumeKindRework} {
		t.Run(string(kind), func(t *testing.T) {
			fixed, err := ResumePrompt(kind)
			if err != nil {
				t.Fatal(err)
			}
			got, err := BuildDelegationResumeStdin(kind, "Q", "A", "feat/prior-x", "F", "", openPrior())
			if err != nil {
				t.Fatal(err)
			}
			s := string(got)
			if !strings.HasPrefix(s, fixed) {
				t.Errorf("stdin must start with the embedded fixed text verbatim:\n%s", s)
			}
			inside, outside, ok := splitPriorSection(s)
			if !ok {
				t.Fatalf("no prior section:\n%s", s)
			}
			for _, want := range []string{"https://github.com/o/r/pull/12", "deadbeef01"} {
				if !strings.Contains(inside, want) || strings.Contains(outside, want) {
					t.Errorf("%q must be only inside the prior section", want)
				}
			}
			if !strings.Contains(outside, "同じ PR") {
				t.Errorf("the fixed sentence is missing:\n%s", outside)
			}
			if !strings.Contains(s, dataSectionGuardNote) {
				t.Error("the guard note is missing")
			}
			if !strings.Contains(s, WrapDataSection("続けるブランチ", "feat/prior-x")) {
				t.Error("the existing branch section must stay")
			}
		})
	}
}

func TestBuildDelegationResumeStdin_PriorAloneStillGetsTheGuardNote(t *testing.T) {
	got, err := BuildDelegationResumeStdin(core.ResumeKindInterrupted, "", "", "", "", "", openPrior())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), dataSectionGuardNote) {
		t.Errorf("stdin lacks the guard note:\n%s", got)
	}
}

func TestBuildDelegationResumeStdin_PriorClosedHasNoValuesAndNoSection(t *testing.T) {
	got, err := BuildDelegationResumeStdin(core.ResumeKindRework, "", "", "", "F", "", &core.PriorArtifact{State: core.PriorArtifactMerged})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if _, _, ok := splitPriorSection(s); ok {
		t.Error("a merged PR has no data section")
	}
	if !strings.Contains(s, "新しい PR を作ってよい") {
		t.Errorf("stdin lacks the sentence allowing a new PR:\n%s", s)
	}
}

func TestBuildDelegationResumeStdin_NoPrior_IsUnchanged(t *testing.T) {
	a, _ := BuildDelegationResumeStdin(core.ResumeKindRework, "", "", "feat/x", "F", "", nil)
	if strings.Contains(string(a), priorArtifactLabel) {
		t.Errorf("no prior section expected:\n%s", a)
	}
}

// InvokeDelegation は PriorArtifact を新しいセッションにも再開にも標準入力へ渡す。
func TestLauncher_InvokeDelegation_PassesThePriorArtifactToStdinForNewSessionAndResume(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "new"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			setFakeClaudePath(t, newFakeClaudeDir(t))
			ws := newWorkspace(t)
			stdinLog := filepath.Join(t.TempDir(), "stdin.log")
			t.Setenv(envFixture, writeFixture(t, fakeClaudeFixture{
				Stdout:       `{"session_id":"22222222-2222-4222-8222-222222222222","is_error":false,"structured_output":{"outcome":"completed"}}`,
				StdinLogPath: stdinLog,
			}))
			in := baseDelegateInput(t, ws, t.TempDir())
			in.PriorArtifact = openPrior()
			if resume {
				in.IsResume, in.ResumeKind = true, core.ResumeKindRework
			}
			if _, err := NewLauncher().InvokeDelegation(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			stdin, _ := os.ReadFile(stdinLog)
			if _, _, ok := splitPriorSection(string(stdin)); !ok || !strings.Contains(string(stdin), "https://github.com/o/r/pull/12") {
				t.Errorf("stdin lacks the prior artifact:\n%s", stdin)
			}
		})
	}
}

package invoker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは、完了確認での差し戻しの理由（core が整形した文）が、J3 の出力に依らず委譲の標準入力
// （新しいセッションの起動・再開）へ機械的に差し込まれることを検証する（#457）。

const rejectionsText = "REJ-HEADER\n\n1. 2026-11-04\nREASON-FIRST\n\n2. 2026-11-06\nREASON-SECOND"

const rejectionsLeadWant = "次の「人間の差し戻し」の区画は人間の決定である。これには従う。" +
	"元の達成条件に足された、満たすべき条件であり、満たせない点は、問いまたは残りとして最終報告に書く。" +
	"後の差し戻しが前の差し戻しの方針を置き換えているときは、後のものに従う。"

func TestBuildDelegationStdin_RejectionsAreInsertedAfterTheBriefRegionAndBeforeTheReportStyle(t *testing.T) {
	out, err := BuildDelegationStdin(DelegationBrief{Decider: "child", DeciderRow: 4, Brief: "BRIEF-FROM-J3", HumanRejections: rejectionsText})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	want := rejectionsLeadWant + "\n\n" + WrapDataSection("人間の差し戻し", rejectionsText)
	if strings.Count(text, want) != 1 {
		t.Fatalf("stdin lacks the lead and the rejections section exactly once:\n%s", text)
	}
	fixed, _ := BriefFixedSections()
	var style string
	for _, f := range fixed {
		if f.ID == "completion_report_style" {
			style = strings.TrimRight(f.Body, "\n")
		}
	}
	end, at, next := strings.Index(text, briefRegionEnd), strings.Index(text, want), strings.Index(text, style)
	if end < 0 || end >= at || at >= next {
		t.Errorf("order wrong: region end %d, rejections %d, report style %d", end, at, next)
	}
	// 区画の中は J3 の出力だけ（差し戻しの理由は区画の外）。
	begin := strings.Index(text, briefRegionBegin)
	if region := text[begin+len(briefRegionBegin) : end]; strings.TrimSpace(region) != "BRIEF-FROM-J3" {
		t.Errorf("brief region = %q", region)
	}
}

func TestBuildDelegationStdin_WithoutRejectionsHasNoRejectionSection(t *testing.T) {
	out, err := BuildDelegationStdin(DelegationBrief{Decider: "child", DeciderRow: 4, Brief: "BRIEF-FROM-J3"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "人間の差し戻し") {
		t.Errorf("stdin has a rejection section for a challenge without a rejection:\n%s", out)
	}
}

func TestBuildDelegationResumeStdin_EveryKindCarriesTheRejectionsAsAHumanDecision(t *testing.T) {
	want := rejectionsLeadWant + "\n\n" + WrapDataSection("人間の差し戻し", rejectionsText)
	for _, kind := range []core.ResumeKind{core.ResumeKindAnswer, core.ResumeKindInterrupted, core.ResumeKindBudget, core.ResumeKindRework} {
		t.Run(string(kind), func(t *testing.T) {
			out, err := BuildDelegationResumeStdin(kind, "Q", "ANS", "feat/x", "FEEDBACK-TEXT", "", rejectionsText)
			if err != nil {
				t.Fatal(err)
			}
			text := string(out)
			fixed, _ := ResumePrompt(kind)
			if !strings.HasPrefix(text, fixed) {
				t.Errorf("stdin must start with the embedded fixed text verbatim")
			}
			if strings.Count(text, want) != 1 {
				t.Fatalf("stdin lacks the lead and the rejections section exactly once:\n%s", text)
			}
			if strings.Index(text, "REASON-FIRST") > strings.Index(text, "REASON-SECOND") {
				t.Error("the reasons are not oldest first")
			}
			// 区画の中の指示に従わない注意書きより後に置く（従う対象であることを、注意書きの後で述べる）。
			if note := strings.Index(text, dataSectionGuardNote); note < 0 || note > strings.Index(text, want) {
				t.Errorf("the guard note must come before the human decision")
			}
		})
	}
}

func TestBuildDelegationResumeStdin_WithoutRejectionsHasNoRejectionSection(t *testing.T) {
	for _, kind := range []core.ResumeKind{core.ResumeKindAnswer, core.ResumeKindInterrupted, core.ResumeKindBudget, core.ResumeKindRework} {
		out, err := BuildDelegationResumeStdin(kind, "Q", "ANS", "feat/x", "FEEDBACK-TEXT", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "人間の差し戻し") {
			t.Errorf("%s: stdin has a rejection section without a rejection:\n%s", kind, out)
		}
	}
}

// 回答の区画と併せて渡すとき、差し戻しの理由は回答より前に置く（回答が最新の決定）。
func TestBuildDelegationResumeStdin_RejectionsPrecedeTheAnswer(t *testing.T) {
	out, err := BuildDelegationResumeStdin(core.ResumeKindAnswer, "Q", "ANS", "feat/x", "", "", rejectionsText)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if r, a := strings.Index(text, WrapDataSection("人間の差し戻し", rejectionsText)), strings.Index(text, WrapDataSection("回答", "ANS")); r < 0 || a < 0 || r > a {
		t.Errorf("rejections at %d, answer at %d; want rejections first", r, a)
	}
}

// 起動の入力（core.DelegateLaunchInput）の HumanRejections が、新しいセッションの標準入力にも再開の標準入力にも届く。
func TestLauncher_InvokeDelegation_PassesTheRejectionsOnStdinForNewSessionAndResume(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "new session"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			setFakeClaudePath(t, newFakeClaudeDir(t))
			ws := newWorkspace(t)
			stdinLog := filepath.Join(t.TempDir(), "stdin.log")
			t.Setenv(envFixture, writeFixture(t, fakeClaudeFixture{
				Stdout:       `{"session_id":"22222222-2222-4222-8222-222222222222","is_error":false,"total_cost_usd":1,"structured_output":{}}`,
				StdinLogPath: stdinLog,
			}))
			in := baseDelegateInput(t, ws, t.TempDir())
			in.HumanRejections = rejectionsText
			if resume {
				in.IsResume, in.ResumeKind, in.Brief = true, core.ResumeKindRework, ""
				in.ResumeFeedback = "FEEDBACK-TEXT"
			}
			if _, err := NewLauncher().InvokeDelegation(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			stdin, _ := os.ReadFile(stdinLog)
			if !strings.Contains(string(stdin), WrapDataSection("人間の差し戻し", rejectionsText)) {
				t.Errorf("stdin lacks the rejections section:\n%s", stdin)
			}
		})
	}
}

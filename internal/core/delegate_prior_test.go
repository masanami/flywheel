package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// 前の委譲が残した PR（前の成果物）を、次の委譲の入力（DelegateLaunchInput と J3 の入力）へ渡す。
// 完了確認の差し戻し・J5 の not_met・保留への回答のいずれの後でも、前のブランチ・PR・head が渡り、
// PR の状態は委譲の前に core が確かめる。実ストア・偽の判断／委譲・偽の gh で検証する。

const priorPRURL = "https://github.com/o/r/pull/1"

// seedPriorPR は委譲の後の照合が見つけるブランチ feat/x と PR 1 を、状態 state で用意する。
func (f *delegateFixture) seedPriorPR(state string) {
	f.branches.branches["feat/x"] = true
	f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, state, "develop")}
	f.branches.pulls["o/r#1"] = UpstreamPullRequest{URL: priorPRURL, Title: "T", State: state, Base: "develop", HeadRef: "feat/x", HeadSHA: "sha-one"}
}

// recordPullLookupsAtLaunch は、委譲の起動の時点での、PR 1 件の取得の呼び出しの累計を起動のたびに追記する。
// 委譲の後の照合が呼ぶ取得は起動の後なので、起動の前までの増分は前の成果物の確認だけになる。
func (f *delegateFixture) recordPullLookupsAtLaunch() *[]int {
	var totals []int
	f.deleg.onInvoke = func(DelegateLaunchInput) { totals = append(totals, len(f.branches.pullCallLog())) }
	return &totals
}

func (f *delegateFixture) priorSection(t *testing.T, in JudgmentLaunchInput) (JudgmentDataSection, bool) {
	t.Helper()
	return sectionByLabel(in.Sections, "前の成果物")
}

// 完了確認で差し戻した後（検証中から着手中へ戻った後）の新しいセッションの委譲に、前のブランチ・PR・head が渡る。
func TestPrior_AfterCompletionRejectionTheNewSessionCarriesBranchPRAndHeadToTheChildAndJ3(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.seedPriorPR("open")
	lookups := f.recordPullLookupsAtLaunch()
	f.runDelegation(t, id)
	if got := f.detail(t, id).Status; got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	if first := f.lastLaunch(t); first.PriorArtifact != nil {
		t.Fatalf("the first delegation has no prior artifact, got %+v", first.PriorArtifact)
	}
	if len(f.j3Inputs) != 1 {
		t.Fatalf("J3 calls = %d", len(f.j3Inputs))
	}
	if _, ok := f.priorSection(t, f.j3Inputs[0]); ok {
		t.Error("the first delegation's J3 input must not have a prior artifact section")
	}
	f.setStatus(t, id, "in_progress") // 完了確認の差し戻しが課題を着手中に戻した後の状態
	afterFirst := len(f.branches.pullCallLog())

	f.runDelegation(t, id)
	got := f.lastLaunch(t)
	if got.IsResume {
		t.Fatalf("a rejected completion starts a new session, got a resume of kind %q", got.ResumeKind)
	}
	want := PriorArtifact{State: PriorArtifactOpen, Branch: "feat/x", PRURL: priorPRURL, HeadSHA: "sha-one"}
	if got.PriorArtifact == nil || *got.PriorArtifact != want {
		t.Errorf("PriorArtifact = %+v, want %+v", got.PriorArtifact, want)
	}
	if len(f.j3Inputs) != 2 {
		t.Fatalf("J3 calls = %d, want 2", len(f.j3Inputs))
	}
	sec, ok := f.priorSection(t, f.j3Inputs[1])
	if !ok {
		t.Fatalf("the J3 input lacks the prior artifact section:\n%s", sectionsText(f.j3Inputs[1].Sections))
	}
	for _, w := range []string{"開いている", "feat/x", priorPRURL, "sha-one"} {
		if !strings.Contains(sec.Content, w) {
			t.Errorf("J3 section lacks %q:\n%s", w, sec.Content)
		}
	}
	if len(*lookups) != 2 || (*lookups)[0] != 0 || (*lookups)[1]-afterFirst != 1 {
		t.Errorf("PR lookups at each launch = %v (after the first run: %d), want none before the first launch and exactly one before the second", *lookups, afterFirst)
	}
}

// J5 の not_met の後の再開（rework）にも、同じものが渡る。J3 は呼ばれない。
func TestPrior_ReworkResumeCarriesThePriorArtifact(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.branches.pulls["o/r#1"] = UpstreamPullRequest{URL: priorPRURL, State: "open", Base: "develop", HeadRef: "feat/x", HeadSHA: "sha-one"}
	f.notMetRound(t, "C-1")
	f.runDelegation(t, "C-1")
	got := f.lastLaunch(t)
	if !got.IsResume || got.ResumeKind != ResumeKindRework {
		t.Fatalf("launch = %+v, want a rework resume", got)
	}
	want := PriorArtifact{State: PriorArtifactOpen, Branch: "feat/x", PRURL: priorPRURL, HeadSHA: "sha-one"}
	if got.PriorArtifact == nil || *got.PriorArtifact != want {
		t.Errorf("PriorArtifact = %+v, want %+v", got.PriorArtifact, want)
	}
	if len(f.j3Inputs) != 1 {
		t.Errorf("a rework resume must not call J3 again (calls = %d)", len(f.j3Inputs))
	}
}

// 保留への回答の後の再開（answer）にも、同じものが渡る。
func TestPrior_AnswerResumeCarriesThePriorArtifact(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.seedPriorPR("open")
	f.deleg.queue = []JudgmentLaunchOutput{questionsOut("feat/x")}
	f.runDelegation(t, id)
	f.answerLatestHold(t, id, "ANS")
	f.runDelegation(t, id)
	got := f.lastLaunch(t)
	if !got.IsResume || got.ResumeKind != ResumeKindAnswer {
		t.Fatalf("launch = %+v, want an answer resume", got)
	}
	want := PriorArtifact{State: PriorArtifactOpen, Branch: "feat/x", PRURL: priorPRURL, HeadSHA: "sha-one"}
	if got.PriorArtifact == nil || *got.PriorArtifact != want {
		t.Errorf("PriorArtifact = %+v, want %+v", got.PriorArtifact, want)
	}
}

// 保留への回答の後でも、セッションが無ければ新しいセッションになり、そこにも渡る（J3 の入力にも入る）。
func TestPrior_AnswerWithoutASessionStartsANewSessionThatCarriesThePriorArtifact(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.seedPriorPR("open")
	f.runDelegation(t, id)
	f.setStatus(t, id, "in_progress")
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "why?"}); err != nil {
		t.Fatal(err)
	}
	f.answerLatestHold(t, id, "ans")
	j3Before := len(f.j3Inputs)
	f.runDelegation(t, id)
	got := f.lastLaunch(t)
	if got.IsResume || got.PriorArtifact == nil || got.PriorArtifact.State != PriorArtifactOpen || got.PriorArtifact.PRURL != priorPRURL {
		t.Errorf("launch = %+v prior = %+v", got, got.PriorArtifact)
	}
	if len(f.j3Inputs) != j3Before+1 {
		t.Fatalf("J3 calls = %d, want %d", len(f.j3Inputs), j3Before+1)
	}
	if _, ok := f.priorSection(t, f.j3Inputs[len(f.j3Inputs)-1]); !ok {
		t.Error("the J3 input lacks the prior artifact section")
	}
}

// 閉じている・マージ済みの PR は続け先として渡さない（URL もブランチも渡さない）。
func TestPrior_ClosedOrMergedPRIsNotHandedOverAsAPlaceToContinue(t *testing.T) {
	for _, state := range []string{"closed", "merged"} {
		t.Run(state, func(t *testing.T) {
			f := newDelegateFixture(t)
			id := f.newInProgress(t, "t", "P1", planSpec(nil))
			f.seedPriorPR("open") // 照合の時点では開いていて、検証中になる
			f.runDelegation(t, id)
			f.branches.pulls["o/r#1"] = UpstreamPullRequest{URL: priorPRURL, State: state, Base: "develop", HeadRef: "feat/x", HeadSHA: "sha-one"}
			f.setStatus(t, id, "in_progress")

			f.runDelegation(t, id)
			got := f.lastLaunch(t)
			if got.PriorArtifact == nil || *got.PriorArtifact != (PriorArtifact{State: PriorArtifactState(state)}) {
				t.Errorf("PriorArtifact = %+v, want only the state %q", got.PriorArtifact, state)
			}
			sec, ok := f.priorSection(t, f.j3Inputs[len(f.j3Inputs)-1])
			if !ok {
				t.Fatal("the J3 input lacks the prior artifact section")
			}
			for _, bad := range []string{priorPRURL, "feat/x", "sha-one"} {
				if strings.Contains(sec.Content, bad) {
					t.Errorf("the J3 section must not carry %q for a %s PR:\n%s", bad, state, sec.Content)
				}
			}
			if !strings.Contains(sec.Content, state) {
				t.Errorf("the J3 section lacks the state:\n%s", sec.Content)
			}
		})
	}
}

// 前の PR が無い課題（初回の委譲）では、前の成果物を出さない。PR の取得も呼ばない。
func TestPrior_FirstDelegationHasNoPriorArtifactAndNoLookup(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	lookups := f.recordPullLookupsAtLaunch()
	f.runDelegation(t, id)
	if got := f.lastLaunch(t); got.PriorArtifact != nil {
		t.Errorf("PriorArtifact = %+v, want nil", got.PriorArtifact)
	}
	if _, ok := f.priorSection(t, f.j3Inputs[0]); ok {
		t.Error("the J3 input must not have the prior artifact section")
	}
	if len(*lookups) != 1 || (*lookups)[0] != 0 {
		t.Errorf("PR lookups at the launch = %v, want [0]", *lookups)
	}
}

// PR の取得に失敗しても委譲は起動し、状態不明としてブランチと URL（head は無し）を渡す。
func TestPrior_LookupFailureLaunchesWithAnUnknownState(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.seedPriorPR("open")
	f.runDelegation(t, id)
	f.branches.pullErrs["o/r#1"] = errFakeGH
	f.setStatus(t, id, "in_progress")

	n := len(f.deleg.launched())
	res := f.runDelegation(t, id)
	if len(f.deleg.launched()) != n+1 || len(res.NotStarted) != 0 {
		t.Fatalf("launches %d→%d, notStarted %+v; the delegation must still start", n, len(f.deleg.launched()), res.NotStarted)
	}
	want := PriorArtifact{State: PriorArtifactUnknown, Branch: "feat/x", PRURL: priorPRURL}
	if got := f.lastLaunch(t); got.PriorArtifact == nil || *got.PriorArtifact != want {
		t.Errorf("PriorArtifact = %+v, want %+v", got.PriorArtifact, want)
	}
	sec, _ := f.priorSection(t, f.j3Inputs[len(f.j3Inputs)-1])
	if !strings.Contains(sec.Content, "確かめられなかった") || !strings.Contains(sec.Content, priorPRURL) {
		t.Errorf("J3 section = %q", sec.Content)
	}
}

// 最新の run の PR を渡す（古い run の PR ではなく）。同じ run に複数の PR があれば番号が最大のもの。
func TestPrior_LatestRunAndNewestPRWin(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	// 1 回目: ブランチ feat/x に PR 3（新）と PR 1（旧）。照合は新しい順に記録する。
	f.branches.branches["feat/x"] = true
	f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(3, "open", "develop"), mkPR(1, "closed", "develop")}
	f.branches.pulls["o/r#3"] = UpstreamPullRequest{URL: "https://github.com/o/r/pull/3", State: "open", Base: "develop", HeadRef: "feat/x", HeadSHA: "sha-three"}
	f.runDelegation(t, id)
	f.setStatus(t, id, "in_progress")
	f.runDelegation(t, id)
	if got := f.lastLaunch(t); got.PriorArtifact == nil || got.PriorArtifact.PRURL != "https://github.com/o/r/pull/3" {
		t.Fatalf("PriorArtifact = %+v, want PR 3 (the highest number in the run)", got.PriorArtifact)
	}

	// 2 回目の run が別のブランチ feat/y に PR 2 を作った。次の委譲は最新の run の PR 2。
	f.deleg.result = JudgmentLaunchOutput{Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(1),
		StructuredOutput: reportJSON(func(m map[string]any) { m["branch"] = "feat/y" })}
	f.branches.branches["feat/y"] = true
	f.branches.prs["feat/y"] = []UpstreamPullRequest{mkPR(2, "open", "develop")}
	f.branches.pulls["o/r#2"] = UpstreamPullRequest{URL: "https://github.com/o/r/pull/2", State: "open", Base: "develop", HeadRef: "feat/y", HeadSHA: "sha-two"}
	f.setStatus(t, id, "in_progress")
	f.runDelegation(t, id)
	f.setStatus(t, id, "in_progress")
	f.runDelegation(t, id)
	got := f.lastLaunch(t)
	if got.PriorArtifact == nil || got.PriorArtifact.PRURL != "https://github.com/o/r/pull/2" || got.PriorArtifact.Branch != "feat/y" {
		t.Errorf("PriorArtifact = %+v, want PR 2 on feat/y", got.PriorArtifact)
	}
}

// run_artifact に PR が無ければ、最新の成功した run の報告の pr_urls（委譲先のリポジトリのもの）を使う。
func TestPrior_FallsBackToTheReportedPRURLsOfTheRepository(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.seedPriorPR("open")
	f.runDelegation(t, id)
	// 照合の記録が無い run（gh が使えなかったなど）を作る。報告は他のリポジトリの PR も挙げる。
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM run_artifact`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE run SET output = ? WHERE kind = 'delegate'`, string(reportJSON(func(m map[string]any) {
			m["pr_urls"] = []string{"https://github.com/o/s/pull/9", priorPRURL}
		})))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f.setStatus(t, id, "in_progress")
	f.runDelegation(t, id)
	want := PriorArtifact{State: PriorArtifactOpen, Branch: "feat/x", PRURL: priorPRURL, HeadSHA: "sha-one"}
	if got := f.lastLaunch(t); got.PriorArtifact == nil || *got.PriorArtifact != want {
		t.Errorf("PriorArtifact = %+v, want %+v", got.PriorArtifact, want)
	}
}

// 別のリポジトリの PR だけなら、前の成果物は無い（PR も確かめない）。
func TestPrior_PRsOfAnotherRepositoryAreNotPrior(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = JudgmentLaunchOutput{Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(1),
		StructuredOutput: reportJSON(func(m map[string]any) { m["pr_urls"] = []string{"https://github.com/o/s/pull/9"} })}
	f.branches.pulls["o/s#9"] = UpstreamPullRequest{URL: "https://github.com/o/s/pull/9", State: "open", Base: "develop"}
	f.runDelegation(t, id)
	if len(f.artifacts(t)) == 0 {
		t.Fatal("the other repository's PR should have been recorded as an artifact for this test to mean anything")
	}
	f.setStatus(t, id, "in_progress")
	base := len(f.branches.pullCallLog())
	lookups := f.recordPullLookupsAtLaunch()
	f.runDelegation(t, id)
	if got := f.lastLaunch(t); got.PriorArtifact != nil {
		t.Errorf("PriorArtifact = %+v, want nil", got.PriorArtifact)
	}
	if len(*lookups) != 1 || (*lookups)[0] != base {
		t.Errorf("PR lookups at the launch = %v (before the run: %d), want no new lookup", *lookups, base)
	}
}

// resolvePriorArtifact は PR の状態ごとに、続け先として渡す値を決める。
func TestResolvePriorArtifact(t *testing.T) {
	ctx := context.Background()
	cand := priorPR{URL: priorPRURL, Branch: "feat/x"}
	src := func(pr UpstreamPullRequest, err error) *fakeBranchSource {
		s := newFakeBranchSource()
		if err != nil {
			s.pullErrs["o/r#1"] = err
		} else {
			s.pulls["o/r#1"] = pr
		}
		return s
	}
	t.Run("open prefers the head ref of the PR and carries the head sha", func(t *testing.T) {
		got := resolvePriorArtifact(ctx, src(UpstreamPullRequest{URL: priorPRURL, State: "open", HeadRef: "feat/renamed", HeadSHA: " abc "}, nil), cand, "main")
		want := PriorArtifact{State: PriorArtifactOpen, Branch: "feat/renamed", PRURL: priorPRURL, HeadSHA: "abc"}
		if *got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	t.Run("open without a head keeps the candidate branch and is not a failure", func(t *testing.T) {
		got := resolvePriorArtifact(ctx, src(UpstreamPullRequest{URL: priorPRURL, State: "open"}, nil), cand, "main")
		want := PriorArtifact{State: PriorArtifactOpen, Branch: "feat/x", PRURL: priorPRURL}
		if *got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	t.Run("a head ref that is the default branch or implausible is ignored", func(t *testing.T) {
		for _, ref := range []string{"main", "a..b", "-x"} {
			got := resolvePriorArtifact(ctx, src(UpstreamPullRequest{URL: priorPRURL, State: "open", HeadRef: ref}, nil), cand, "main")
			if got.Branch != "feat/x" {
				t.Errorf("head ref %q: branch = %q, want the candidate", ref, got.Branch)
			}
		}
	})
	t.Run("unknown when it cannot be looked up", func(t *testing.T) {
		want := PriorArtifact{State: PriorArtifactUnknown, Branch: "feat/x", PRURL: priorPRURL}
		for name, got := range map[string]*PriorArtifact{
			"error":         resolvePriorArtifact(ctx, src(UpstreamPullRequest{}, errors.New("boom")), cand, "main"),
			"nil source":    resolvePriorArtifact(ctx, nil, cand, "main"),
			"odd state":     resolvePriorArtifact(ctx, src(UpstreamPullRequest{URL: priorPRURL, State: "draft"}, nil), cand, "main"),
			"missing in gh": resolvePriorArtifact(ctx, newFakeBranchSource(), cand, "main"),
		} {
			if *got != want {
				t.Errorf("%s: got %+v, want %+v", name, got, want)
			}
		}
		bad := resolvePriorArtifact(ctx, newFakeBranchSource(), priorPR{URL: "not a pr url", Branch: "feat/x"}, "main")
		if bad.State != PriorArtifactUnknown || bad.PRURL != "not a pr url" {
			t.Errorf("an unparsable URL: got %+v", bad)
		}
	})
	t.Run("closed and merged carry only the state", func(t *testing.T) {
		for _, st := range []string{"closed", "merged"} {
			got := resolvePriorArtifact(ctx, src(UpstreamPullRequest{URL: priorPRURL, State: st, HeadRef: "feat/x", HeadSHA: "abc"}, nil), cand, "main")
			if *got != (PriorArtifact{State: PriorArtifactState(st)}) {
				t.Errorf("%s: got %+v", st, got)
			}
		}
	})
}

func TestNewestPRInRepo(t *testing.T) {
	urls := []string{"https://github.com/o/s/pull/50", "https://github.com/O/R/pull/7", "https://github.com/o/r/pull/12", "junk", "https://github.com/o/r/pull/3"}
	if got := newestPRInRepo(urls, "o/r"); got != "https://github.com/o/r/pull/12" {
		t.Errorf("got %q", got)
	}
	if got := newestPRInRepo(urls, "x/y"); got != "" {
		t.Errorf("got %q, want none", got)
	}
}

func TestFormatPriorArtifactSection(t *testing.T) {
	if got := formatPriorArtifactSection(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
	open := formatPriorArtifactSection(&PriorArtifact{State: PriorArtifactOpen, Branch: "b", PRURL: "u", HeadSHA: "h"})
	for _, w := range []string{"開いている", "ブランチ: b", "PR: u", "head: h"} {
		if !strings.Contains(open, w) {
			t.Errorf("open lacks %q: %s", w, open)
		}
	}
	unknown := formatPriorArtifactSection(&PriorArtifact{State: PriorArtifactUnknown, Branch: "b", PRURL: "u"})
	if !strings.Contains(unknown, "確かめられなかった") || strings.Contains(unknown, "head") {
		t.Errorf("unknown: %s", unknown)
	}
	merged := formatPriorArtifactSection(&PriorArtifact{State: PriorArtifactMerged, Branch: "b", PRURL: "u"})
	if !strings.Contains(merged, "マージ済み") || strings.Contains(merged, "u") && strings.Contains(merged, "PR: ") {
		t.Errorf("merged: %s", merged)
	}
}

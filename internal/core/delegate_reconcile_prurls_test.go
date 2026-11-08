package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 完了報告の成果物（種類 pr）の照合は、報告のブランチだけでなく pr_urls の PR を 1 件ずつ
// 調べる。pr_urls は子の申告であり、記録するのは gh が返した値だけ。宣言（connectors.json の
// repos[]）にあるリポジトリの PR だけを調べ、委譲先と別のリポジトリの PR も成果物として認める。
// このファイルの委譲先は o/r（main-repo）、宣言内の別のリポジトリは o/s（sibling）。

func pullPR(repo string, n int, state, base string) UpstreamPullRequest {
	return UpstreamPullRequest{
		URL: fmt.Sprintf("https://github.com/%s/pull/%d", repo, n), Title: fmt.Sprintf("PR %s#%d", repo, n), State: state, Base: base,
	}
}

func (f *delegateFixture) givePull(pr UpstreamPullRequest) {
	repo, n, _ := parseReleasePullRequestRef(pr.URL)
	f.branches.pulls[fmt.Sprintf("%s#%d", repo, n)] = pr
}

// reportPRs は、ブランチが無く pr_urls だけを持つ completed の報告にする。
func (f *delegateFixture) reportPRs(urls ...string) {
	f.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) {
		m["branch"] = nil
		m["pr_urls"] = urls
	})
}

func (f *delegateFixture) prArtifacts(t *testing.T) []runArtifactRow {
	t.Helper()
	var out []runArtifactRow
	for _, a := range f.artifacts(t) {
		if a.Kind == artifactKindPR {
			out = append(out, a)
		}
	}
	return out
}

// --- 委譲先のリポジトリの PR（ブランチで見つからなくても pr_urls で見つかる） ---

func TestReconcilePRURLs_DelegatedRepoOpenOrMergedIsAnArtifactEvenWithoutTheBranch(t *testing.T) {
	for _, state := range []string{"open", "merged"} {
		t.Run(state, func(t *testing.T) {
			f := newReconFixture(t, "impl")
			// リモートにブランチが無く、head で引いても PR が見つからない（マージ後にブランチを消した形）。
			f.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) {
				m["branch"] = "feat/gone"
				m["pr_urls"] = []string{"https://github.com/o/r/pull/9"}
			})
			f.givePull(pullPR("o/r", 9, state, "develop"))
			if _, err := f.run(t, nil); err != nil {
				t.Fatal(err)
			}
			if got := f.status(t); got != StatusVerifying {
				t.Fatalf("status = %s, want verifying", got)
			}
			prs := f.prArtifacts(t)
			if len(prs) != 1 || prs[0].Ref != "https://github.com/o/r/pull/9" || string(prs[0].State) != state || prs[0].Base != "develop" || prs[0].VerifiedAt == nil {
				t.Errorf("pr artifacts = %+v", prs)
			}
		})
	}
}

func TestReconcilePRURLs_DelegatedRepoMergedIntoDefaultWithoutApprovalStillHolds(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.reportPRs("https://github.com/o/r/pull/9")
	f.givePull(pullPR("o/r", 9, "merged", "main"))
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, "C-1")
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 || !strings.Contains(d.Holds[0].Question, "承認なしの本番反映を検出") || !strings.Contains(d.Holds[0].Question, "https://github.com/o/r/pull/9") {
		t.Fatalf("status=%s holds=%+v", d.Status, d.Holds)
	}
}

func TestReconcilePRURLs_DelegatedRepoOpenIntoDefaultRegistersARelease(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.reportPRs("https://github.com/o/r/pull/9")
	f.givePull(pullPR("o/r", 9, "open", "main"))
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	ops := f.detail(t, "C-1").Operations
	if len(ops) != 1 || ops[0].Kind != OperationKindRelease || ops[0].Ref == nil || *ops[0].Ref != "https://github.com/o/r/pull/9" {
		t.Fatalf("operations = %+v", ops)
	}
	if got := f.status(t); got != StatusVerifying {
		t.Errorf("status = %s", got)
	}
}

// --- 委譲先と別の、宣言内のリポジトリの PR ---

func TestReconcilePRURLs_OtherDeclaredRepoMergedGoesToVerificationWithGHValues(t *testing.T) {
	f := newReconFixture(t, "impl")
	// 子の書き方（大文字小文字・末尾の /files）に依らず、記録するのは gh が返した html_url。
	f.reportPRs("https://github.com/O/S/pull/5/files")
	f.givePull(pullPR("o/s", 5, "merged", "main"))
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, "C-1")
	if d.Status != StatusVerifying || len(d.Holds) != 0 {
		t.Fatalf("status=%s holds=%+v, want verifying with no hold", d.Status, d.Holds)
	}
	if len(d.Operations) != 0 {
		t.Errorf("a merged pull request of another repository must not register a release: %+v", d.Operations)
	}
	prs := f.prArtifacts(t)
	if len(prs) != 1 || prs[0].Ref != "https://github.com/o/s/pull/5" || prs[0].State != artifactStateMerged || prs[0].Base != "main" || prs[0].VerifiedAt == nil {
		t.Fatalf("pr artifacts = %+v", prs)
	}
	if got := f.branches.pullCallLog(); len(got) != 1 || got[0] != "pr o/s 5" {
		t.Errorf("pull lookups = %v", got)
	}
	// 別のリポジトリの既定ブランチへマージ済みであることは、結果の注記で見える。
	if note := res.Items[0].Note; !strings.Contains(note, "https://github.com/o/s/pull/5") || !strings.Contains(note, "main") {
		t.Errorf("note = %q, want the merge into the other repository's default branch", note)
	}
}

func TestReconcilePRURLs_OtherDeclaredRepoOpenRegistersAReleaseOnlyForItsDefaultBranch(t *testing.T) {
	t.Run("base is that repository's default branch", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.reportPRs("https://github.com/o/s/pull/5")
		f.givePull(pullPR("o/s", 5, "open", "main"))
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		d := f.detail(t, "C-1")
		if d.Status != StatusVerifying {
			t.Fatalf("status = %s", d.Status)
		}
		if len(d.Operations) != 1 || d.Operations[0].Kind != OperationKindRelease || d.Operations[0].Ref == nil ||
			*d.Operations[0].Ref != "https://github.com/o/s/pull/5" || d.Operations[0].Summary != "PR o/s#5" || d.Operations[0].State != OperationStatePending {
			t.Fatalf("operations = %+v", d.Operations)
		}
	})
	t.Run("base is not the default branch", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.reportPRs("https://github.com/o/s/pull/5")
		f.givePull(pullPR("o/s", 5, "open", "develop"))
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		d := f.detail(t, "C-1")
		if d.Status != StatusVerifying || len(d.Operations) != 0 {
			t.Fatalf("status=%s operations=%+v", d.Status, d.Operations)
		}
	})
}

// 別のリポジトリの PR を run の成果物に記録したとき、J5 の前に呼ぶチェックの取得は URL から
// リポジトリを引く。
func TestReconcilePRURLs_RecordedOtherRepoPRIsFetchedByItsOwnRepoForTheChecks(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.reportPRs("https://github.com/o/s/pull/5")
	f.givePull(pullPR("o/s", 5, "merged", "develop"))
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	checks := newFakeChecks()
	checks.prs["o/s#5"] = UpstreamPullRequestChecks{URL: "https://github.com/o/s/pull/5", State: "merged", Base: "develop", HeadSHA: "abc"}
	got, err := fetchPullRequestChecks(context.Background(), checks, f.artifacts(t))
	if err != nil || len(got) != 1 || got[0].HeadSHA != "abc" {
		t.Fatalf("checks = %+v, err = %v", got, err)
	}
	if calls := checks.callLog(); len(calls) != 1 || !strings.Contains(calls[0], "o/s") {
		t.Errorf("check lookups = %v, want one for o/s", calls)
	}
}

// --- 成果物なし（保留） ---

func TestReconcilePRURLs_NoArtifactHolds(t *testing.T) {
	cases := []struct {
		name      string
		urls      []string
		setup     func(f *delegateFixture)
		wantCalls []string // PR 1 件の取得の呼び出し
		wantInQ   []string // 保留の問いに含まれる文言
	}{
		{"closed only", []string{"https://github.com/o/s/pull/5"},
			func(f *delegateFixture) { f.givePull(pullPR("o/s", 5, "closed", "main")) },
			[]string{"pr o/s 5"}, []string{"成果物が見つからない", "closed"}},
		{"undeclared repository", []string{"https://github.com/x/y/pull/1"},
			func(f *delegateFixture) { f.givePull(pullPR("x/y", 1, "open", "main")) },
			nil, []string{"成果物が見つからない", "https://github.com/x/y/pull/1", "宣言（connectors.json）にない"}},
		{"malformed url", []string{"not a url", "https://github.com/o/s/issues/5", "http://github.com/o/s/pull/5", "https://example.com/o/s/pull/5"},
			func(f *delegateFixture) { f.givePull(pullPR("o/s", 5, "open", "main")) },
			nil, []string{"成果物が見つからない", "形が不正"}},
		{"fetch failure", []string{"https://github.com/o/s/pull/5"},
			func(f *delegateFixture) { f.branches.pullErrs["o/s#5"] = errors.New("boom 502") },
			[]string{"pr o/s 5"}, []string{"成果物が見つからない", "boom 502"}},
		{"gh returns a different pull request", []string{"https://github.com/o/s/pull/5"},
			func(f *delegateFixture) {
				f.branches.pulls["o/s#5"] = pullPR("x/y", 5, "open", "main")
			},
			[]string{"pr o/s 5"}, []string{"成果物が見つからない", "does not match"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newReconFixture(t, "impl")
			f.reportPRs(c.urls...)
			c.setup(f)
			if _, err := f.run(t, nil); err != nil {
				t.Fatal(err)
			}
			d := f.detail(t, "C-1")
			if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 {
				t.Fatalf("status=%s holds=%+v, want a hold", d.Status, d.Holds)
			}
			for _, w := range c.wantInQ {
				if !strings.Contains(d.Holds[0].Question, w) {
					t.Errorf("question lacks %q:\n%s", w, d.Holds[0].Question)
				}
			}
			if got := f.branches.pullCallLog(); strings.Join(got, "|") != strings.Join(c.wantCalls, "|") {
				t.Errorf("pull lookups = %v, want %v", got, c.wantCalls)
			}
			if prs := f.prArtifacts(t); len(prs) != 0 && c.name != "closed only" {
				t.Errorf("pr artifacts = %+v, want none", prs)
			}
			if len(d.Operations) != 0 {
				t.Errorf("operations = %+v, want none", d.Operations)
			}
		})
	}
}

// 取得に失敗した PR は、別に成果物ありの PR があっても、その PR だけが記録されない。
func TestReconcilePRURLs_OneFailureDoesNotHideAnotherPR(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.reportPRs("https://github.com/o/s/pull/4", "https://github.com/o/s/pull/5")
	f.branches.pullErrs["o/s#4"] = errors.New("boom")
	f.givePull(pullPR("o/s", 5, "open", "develop"))
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); got != StatusVerifying {
		t.Fatalf("status = %s", got)
	}
	prs := f.prArtifacts(t)
	if len(prs) != 1 || prs[0].Ref != "https://github.com/o/s/pull/5" {
		t.Errorf("pr artifacts = %+v", prs)
	}
}

// 同じ PR は 1 回だけ調べて 1 件だけ記録する（書き方が違っても、head で見つかった PR と重なっても）。
func TestReconcilePRURLs_DuplicatesAreCollapsed(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) {
		m["branch"] = "feat/x"
		m["pr_urls"] = []string{
			"https://github.com/o/s/pull/5", "https://github.com/o/s/pull/5/files", "https://github.com/O/S/pull/5",
			"https://github.com/o/r/pull/1", // head で見つかる PR と同じ
		}
	})
	f.branches.prs["feat/x"] = []UpstreamPullRequest{pullPR("o/r", 1, "open", "develop")}
	f.givePull(pullPR("o/r", 1, "open", "develop"))
	f.givePull(pullPR("o/s", 5, "open", "develop"))
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	prs := f.prArtifacts(t)
	if len(prs) != 2 || prs[0].Ref != "https://github.com/o/r/pull/1" || prs[1].Ref != "https://github.com/o/s/pull/5" {
		t.Fatalf("pr artifacts = %+v, want one each", prs)
	}
	if got := f.branches.pullCallLog(); len(got) != 2 {
		t.Errorf("pull lookups = %v, want one per distinct pull request", got)
	}
}

// 報告の pr_urls がどれほど多くても調べる件数は有限。
func TestReconcilePRURLs_TheNumberOfLookupsIsBounded(t *testing.T) {
	f := newReconFixture(t, "impl")
	var urls []string
	for i := 1; i <= maxReportedPRLookups+10; i++ {
		urls = append(urls, fmt.Sprintf("https://github.com/o/s/pull/%d", i))
	}
	f.reportPRs(urls...)
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(f.branches.pullCallLog()); got != maxReportedPRLookups {
		t.Errorf("pull lookups = %d, want %d", got, maxReportedPRLookups)
	}
}

// --- 照合の対象外（変わらないこと） ---

func TestReconcilePRURLs_OnlyForCompletedReportsOfPROperations(t *testing.T) {
	t.Run("operation with branch artifacts", func(t *testing.T) {
		f := newReconFixture(t, "impl-branch")
		f.reportPRs("https://github.com/o/s/pull/5")
		f.givePull(pullPR("o/s", 5, "open", "develop"))
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		if got := f.branches.pullCallLog(); len(got) != 0 {
			t.Errorf("pull lookups = %v, want none", got)
		}
		if f.status(t) != StatusAwaitingHuman {
			t.Errorf("status = %s, want the branch rule (missing branch) to hold", f.status(t))
		}
	})
	t.Run("operation with no artifacts", func(t *testing.T) {
		f := newReconFixture(t, "impl-none")
		f.reportPRs("https://github.com/o/s/pull/5")
		f.givePull(pullPR("o/s", 5, "open", "develop"))
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		if got := f.branches.pullCallLog(); len(got) != 0 {
			t.Errorf("pull lookups = %v, want none", got)
		}
		if prs := f.prArtifacts(t); len(prs) != 0 {
			t.Errorf("pr artifacts = %+v", prs)
		}
		if f.status(t) != StatusVerifying {
			t.Errorf("status = %s", f.status(t))
		}
	})
	t.Run("questions outcome", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.deleg.result = questionsOut("feat/x")
		f.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) {
			m["outcome"] = "questions"
			m["branch"] = nil
			m["pr_urls"] = []string{"https://github.com/o/s/pull/5"}
			m["questions"] = []any{map[string]any{"kind": "scope", "text": "Q", "options": []string{}, "recommendation": nil}}
		})
		f.givePull(pullPR("o/s", 5, "open", "develop"))
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		if got := f.branches.pullCallLog(); len(got) != 0 {
			t.Errorf("pull lookups = %v, want none", got)
		}
	})
	t.Run("not succeeded", func(t *testing.T) {
		f := newReconFixture(t, "impl")
		f.deleg.result = JudgmentLaunchOutput{Result: RunResultErrored}
		if _, err := f.run(t, nil); err != nil {
			t.Fatal(err)
		}
		if got := f.branches.pullCallLog(); len(got) != 0 {
			t.Errorf("pull lookups = %v, want none", got)
		}
	})
}

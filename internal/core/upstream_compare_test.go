package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeCompare は UpstreamCompareSource の偽の実装（(base, head) ごとに用意した応答を返す）。
type fakeCompare struct {
	results map[[2]string]UpstreamComparison
	errs    map[[2]string]error
	calls   [][2]string
}

func newFakeCompare() *fakeCompare {
	return &fakeCompare{results: map[[2]string]UpstreamComparison{}, errs: map[[2]string]error{}}
}

func (f *fakeCompare) set(base, head string, c UpstreamComparison) {
	f.results[[2]string{base, head}] = c
}

func (f *fakeCompare) CompareCommits(_ context.Context, _ string, base, head string) (UpstreamComparison, error) {
	k := [2]string{base, head}
	f.calls = append(f.calls, k)
	if err := f.errs[k]; err != nil {
		return UpstreamComparison{}, err
	}
	if c, ok := f.results[k]; ok {
		return c, nil
	}
	return UpstreamComparison{}, errors.New("no such comparison: " + base + "..." + head)
}

func commit(sha, msg string, parents ...string) UpstreamCommit {
	return UpstreamCommit{SHA: sha, Parents: parents, Message: msg}
}

func ahead(commits ...UpstreamCommit) UpstreamComparison {
	return UpstreamComparison{Status: UpstreamCompareAhead, TotalCommits: len(commits), Commits: commits}
}

// 履歴の想定: 検証の head v、PR 側のコミット p1(=v の親側)、既定ブランチ main の m1・m2。
// h1 は v に main の m1 を取り込んだマージ（親は v と m1）。

func TestClassifyHeadChange_MergeInOnly(t *testing.T) {
	f := newFakeCompare()
	// R: v..h1 は、取り込みのマージ h1 と、既定ブランチ側のコミット m1。
	f.set("v", "h1", ahead(commit("m1", "main change", "m0"), commit("h1", "Merge branch 'main'", "v", "m1")))
	// P: main..h1 は PR 側のコミットとマージ。m1 は既定ブランチ側なので入らない。
	f.set("main", "h1", ahead(commit("p0", "feat", "m0"), commit("v", "fix", "p0"), commit("h1", "Merge branch 'main'", "v", "m1")))
	// 衝突を直した差分: h1 は v とも m1 とも異なるファイル a.go を持つ。b.go は m1 のとりこみで増えただけ。
	f.set("v", "h1", withFiles(f.results[[2]string{"v", "h1"}], UpstreamFile{Filename: "a.go", Patch: "@@ a"}, UpstreamFile{Filename: "b.go", Patch: "@@ b"}))
	f.set("m1", "h1", UpstreamComparison{Status: UpstreamCompareAhead, TotalCommits: 2, Commits: []UpstreamCommit{commit("v", "fix", "p0")}, Files: []UpstreamFile{{Filename: "a.go", Patch: "@@ a2"}, {Filename: "c.go", Patch: "@@ c"}}})

	got := ClassifyHeadChange(context.Background(), f, "o/r", "main", "v", "h1")
	if got.Kind != HeadChangeMergeInOnly {
		t.Fatalf("kind = %q (%s), want merge_in_only", got.Kind, got.Detail)
	}
	if len(got.Commits) != 2 {
		t.Fatalf("commits = %+v, want 2", got.Commits)
	}
	roles := map[string]CommitRole{}
	for _, c := range got.Commits {
		roles[c.SHA] = c.Role
	}
	if roles["h1"] != CommitRoleMergeIn || roles["m1"] != CommitRoleDefaultBranch {
		t.Errorf("roles = %v, want h1 merge_in and m1 default_branch", roles)
	}
	if got.Commits[0].Subject == "" || strings.Contains(got.Commits[0].Subject, "\n") {
		t.Errorf("subject = %q, want first line", got.Commits[0].Subject)
	}
	if len(got.ConflictFixes) != 1 || got.ConflictFixes[0].MergeCommit != "h1" {
		t.Fatalf("conflict fixes = %+v", got.ConflictFixes)
	}
	files := got.ConflictFixes[0].Files
	if len(files) != 1 || files[0].Filename != "a.go" || files[0].Patch != "@@ a" {
		t.Errorf("fixed files = %+v, want only a.go with the patch against the PR-side parent", files)
	}
}

func withFiles(c UpstreamComparison, files ...UpstreamFile) UpstreamComparison {
	c.Files = files
	return c
}

func TestClassifyHeadChange_ConflictFixFileWithoutPatchIsMarked(t *testing.T) {
	f := newFakeCompare()
	f.set("v", "h1", ahead(commit("h1", "Merge", "v", "m1")))
	f.set("main", "h1", ahead(commit("v", "fix", "p0"), commit("h1", "Merge", "v", "m1")))
	f.set("v", "h1", withFiles(f.results[[2]string{"v", "h1"}], UpstreamFile{Filename: "big.bin"}))
	f.set("m1", "h1", withFiles(ahead(commit("v", "fix", "p0")), UpstreamFile{Filename: "big.bin", Patch: "x"}))
	got := ClassifyHeadChange(context.Background(), f, "o/r", "main", "v", "h1")
	if got.Kind != HeadChangeMergeInOnly {
		t.Fatalf("kind = %q (%s)", got.Kind, got.Detail)
	}
	fl := got.ConflictFixes[0].Files
	if len(fl) != 1 || !fl[0].PatchUnavailable {
		t.Errorf("files = %+v, want big.bin marked patch unavailable", fl)
	}
}

func TestClassifyHeadChange_NonMergeCommitOnPRSideIsContentChange(t *testing.T) {
	f := newFakeCompare()
	f.set("v", "h2", ahead(commit("c1", "add feature", "v"), commit("h2", "Merge branch 'main'", "c1", "m1"), commit("m1", "main", "m0")))
	f.set("main", "h2", ahead(commit("p0", "feat", "m0"), commit("v", "fix", "p0"), commit("c1", "add feature", "v"), commit("h2", "Merge", "c1", "m1")))
	got := ClassifyHeadChange(context.Background(), f, "o/r", "main", "v", "h2")
	if got.Kind != HeadChangeContentChanged {
		t.Fatalf("kind = %q, want content_changed", got.Kind)
	}
	if !strings.Contains(got.Detail, "c1") {
		t.Errorf("detail = %q, want the offending commit c1", got.Detail)
	}
	if len(got.ConflictFixes) != 0 {
		t.Errorf("conflict fixes = %+v, want none for a content change", got.ConflictFixes)
	}
}

func TestClassifyHeadChange_MergeOfAnotherUnmergedBranchIsContentChange(t *testing.T) {
	f := newFakeCompare()
	// h3 は、既定ブランチに入っていない別のブランチ x1 を取り込む（x1 も P に入る）。
	f.set("v", "h3", ahead(commit("x1", "other branch work", "x0"), commit("h3", "Merge branch 'other'", "v", "x1")))
	f.set("main", "h3", ahead(commit("v", "fix", "p0"), commit("x1", "other branch work", "x0"), commit("h3", "Merge", "v", "x1")))
	got := ClassifyHeadChange(context.Background(), f, "o/r", "main", "v", "h3")
	if got.Kind != HeadChangeContentChanged {
		t.Fatalf("kind = %q, want content_changed (the merged branch is not the default branch)", got.Kind)
	}
}

func TestClassifyHeadChange_MergeWithBothParentsOnPRSideIsContentChange(t *testing.T) {
	f := newFakeCompare()
	f.set("v", "h4", ahead(commit("h4", "Merge branch 'self'", "v", "s1"), commit("s1", "side", "p0")))
	f.set("main", "h4", ahead(commit("v", "fix", "p0"), commit("s1", "side", "p0"), commit("h4", "Merge", "v", "s1")))
	if got := ClassifyHeadChange(context.Background(), f, "o/r", "main", "v", "h4"); got.Kind != HeadChangeContentChanged {
		t.Fatalf("kind = %q, want content_changed", got.Kind)
	}
}

func TestClassifyHeadChange_Undeterminable(t *testing.T) {
	merge := func() *fakeCompare {
		f := newFakeCompare()
		f.set("v", "h1", ahead(commit("h1", "Merge", "v", "m1")))
		f.set("main", "h1", ahead(commit("v", "fix", "p0"), commit("h1", "Merge", "v", "m1")))
		f.set("v", "h1", withFiles(f.results[[2]string{"v", "h1"}], UpstreamFile{Filename: "a", Patch: "p"}))
		f.set("m1", "h1", withFiles(ahead(commit("v", "fix", "p0")), UpstreamFile{Filename: "a", Patch: "q"}))
		return f
	}
	cases := []struct {
		name string
		mod  func(f *fakeCompare)
	}{
		{"force-push (diverged)", func(f *fakeCompare) {
			c := f.results[[2]string{"v", "h1"}]
			c.Status = "diverged"
			f.set("v", "h1", c)
		}},
		{"behind", func(f *fakeCompare) {
			c := f.results[[2]string{"v", "h1"}]
			c.Status = "behind"
			f.set("v", "h1", c)
		}},
		{"commits truncated (verified..head)", func(f *fakeCompare) {
			c := f.results[[2]string{"v", "h1"}]
			c.TotalCommits = 500
			f.set("v", "h1", c)
		}},
		{"commits truncated (base..head)", func(f *fakeCompare) {
			c := f.results[[2]string{"main", "h1"}]
			c.TotalCommits = 500
			f.set("main", "h1", c)
		}},
		{"fetch failure (verified..head)", func(f *fakeCompare) { f.errs[[2]string{"v", "h1"}] = errors.New("boom") }},
		{"fetch failure (base..head)", func(f *fakeCompare) { f.errs[[2]string{"main", "h1"}] = errors.New("boom") }},
		{"fetch failure (conflict fix diff)", func(f *fakeCompare) { f.errs[[2]string{"m1", "h1"}] = errors.New("boom") }},
		{"head is not on the PR side", func(f *fakeCompare) { f.set("main", "h1", ahead(commit("v", "fix", "p0"))) }},
		{"files may be truncated", func(f *fakeCompare) {
			c := f.results[[2]string{"v", "h1"}]
			c.FilesTruncated = true
			f.set("v", "h1", c)
		}},
		{"no commits although ahead", func(f *fakeCompare) { f.set("v", "h1", UpstreamComparison{Status: UpstreamCompareAhead}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := merge()
			tc.mod(f)
			got := ClassifyHeadChange(context.Background(), f, "o/r", "main", "v", "h1")
			if got.Kind != HeadChangeUndeterminable {
				t.Fatalf("kind = %q, want undeterminable", got.Kind)
			}
			if got.Detail == "" {
				t.Errorf("detail is empty, want a reason")
			}
			// 履歴が書き換わったと上流が返したときだけ、検証し直す必要があると言える。取得の失敗・打ち切りでは言えない。
			rewritten := strings.HasPrefix(tc.name, "force-push") || tc.name == "behind"
			if got.HistoryRewritten != rewritten || got.RequiresReverification() != rewritten {
				t.Errorf("HistoryRewritten = %v, RequiresReverification = %v, want %v", got.HistoryRewritten, got.RequiresReverification(), rewritten)
			}
		})
	}
	// 対照: 何も崩さなければ取り込みだけ。
	if got := ClassifyHeadChange(context.Background(), merge(), "o/r", "main", "v", "h1"); got.Kind != HeadChangeMergeInOnly {
		t.Fatalf("control kind = %q (%s), want merge_in_only", got.Kind, got.Detail)
	}
}

func TestChecksAllPassed(t *testing.T) {
	ok := UpstreamCheck{Name: "a", Status: "completed", Conclusion: "success"}
	skipped := UpstreamCheck{Name: "s", Status: "completed", Conclusion: "skipped"}
	failed := UpstreamCheck{Name: "f", Status: "completed", Conclusion: "failure"}
	running := UpstreamCheck{Name: "r", Status: "in_progress"}
	cases := []struct {
		name   string
		checks []UpstreamCheck
		want   bool
	}{
		{"none", nil, false},
		{"all pass", []UpstreamCheck{ok, skipped}, true},
		{"one failed", []UpstreamCheck{ok, failed}, false},
		{"one running", []UpstreamCheck{ok, running}, false},
	}
	for _, tc := range cases {
		if got := ChecksAllPassed(tc.checks); got != tc.want {
			t.Errorf("%s: ChecksAllPassed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

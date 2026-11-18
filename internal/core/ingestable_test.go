package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// 状況の ingestable（masanami/flywheel#779）。受入基準の番号は AC-<番号>。

func ingIssue(key, parent string, assignees ...string) UpstreamIssue {
	repo, n, _ := splitExternalKey(key)
	u := issueFixture(key, "https://github.com/"+repo+"/issues/"+fmt.Sprint(n), "title "+key, "body", "carol", assignees, nil)
	u.Repo, u.Number, u.ParentExternalKey = repo, n, parent
	return u
}

func ingSource() SourceEntry {
	return excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
}

func listIngestable(t *testing.T, s *Store, up UpstreamIssueSource, sources ...SourceEntry) *Ingestable {
	t.Helper()
	got, err := s.ListIngestable(context.Background(), sources, up)
	if err != nil {
		t.Fatalf("ListIngestable() error = %v", err)
	}
	return got
}

func ingKeys(in *Ingestable) []string {
	out := []string{}
	for _, it := range in.Items {
		out = append(out, it.ExternalKey)
	}
	return out
}

func childKeys(it IngestableItem) []string {
	out := []string{}
	for _, c := range it.Children {
		out = append(out, c.ExternalKey)
	}
	return out
}

func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// AC-234・AC-235・AC-246（#779）: 取り込めるものが要素になり、要素は定められたフィールドを持ち、読めたときの error は nil。
func TestIngestable_AC234_AC235_AC246_ListsTheIngestableIssueWithItsFields(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {ingIssue("o/r#1", "")}}}
	got := listIngestable(t, s, up, ingSource())
	if got.Error != nil {
		t.Fatalf("Error = %q, want nil", *got.Error)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %v", ingKeys(got))
	}
	it := got.Items[0]
	if it.ExternalKey != "o/r#1" || it.Number != 1 || it.Repo != "o/r" || it.Title != "title o/r#1" || it.URL != "https://github.com/o/r/issues/1" ||
		it.ParentExternalKey != "" || it.ParentChallengeID != "" || it.Children == nil || len(it.Children) != 0 {
		t.Fatalf("item = %+v", it)
	}
}

// AC-236・AC-237・AC-238（#779）: 課題になっている・課題の子として記録されている・方針に合わない Issue は無い。
func TestIngestable_AC236_AC237_AC238_ExcludesChallengesChildrenAndOutOfPolicy(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {
		ingIssue("o/r#1", ""),
		ingIssue("o/r#2", "o/r#1"),
		ingIssue("o/r#3", ""),
		ingIssue("o/r#4", "", "eve"),
	}}}
	if _, err := s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{ingSource()}, Specs: []string{"o/r#1"}, Upstream: up}); err != nil {
		t.Fatal(err)
	}
	got := listIngestable(t, s, up, ingSource())
	if want := []string{"o/r#3"}; !sameKeys(ingKeys(got), want) {
		t.Fatalf("items = %v, want %v (#1 は課題・#2 は子・#4 は方針外)", ingKeys(got), want)
	}
}

// AC-239・AC-240・AC-241（#779）: 親子孫は根の 1 件にまとまり、子孫は深さを問わず根の children に入る。
func TestIngestable_AC239_AC240_AC241_DescendantsGoUnderTheRootFlatly(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {
		ingIssue("o/r#3", "o/r#2"),
		ingIssue("o/r#2", "o/r#1"),
		ingIssue("o/r#1", "o/other#9"),
	}}}
	got := listIngestable(t, s, up, ingSource())
	if want := []string{"o/r#1"}; !sameKeys(ingKeys(got), want) {
		t.Fatalf("roots = %v, want %v", ingKeys(got), want)
	}
	root := got.Items[0]
	if want := []string{"o/r#3", "o/r#2"}; !sameKeys(childKeys(root), want) {
		t.Fatalf("children = %v, want %v (孫も平らに入る)", childKeys(root), want)
	}
	if root.ParentExternalKey != "o/other#9" || root.ParentChallengeID != "" {
		t.Fatalf("root parent = %q / %q", root.ParentExternalKey, root.ParentChallengeID)
	}
	var grand IngestableChild
	for _, c := range root.Children {
		if c.ExternalKey == "o/r#3" {
			grand = c
		}
	}
	if grand.Number != 3 || grand.ParentExternalKey != "o/r#2" || grand.Title == "" || grand.URL == "" {
		t.Fatalf("grandchild = %+v", grand)
	}
}

// AC-242（#779）: 親の記録が輪になっている 2 つは、どちらも根に並び、互いを子孫にしない。
func TestIngestable_AC242_ParentCycleMembersAreBothRoots(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {
		ingIssue("o/r#1", "o/r#2"),
		ingIssue("o/r#2", "o/r#1"),
	}}}
	got := listIngestable(t, s, up, ingSource())
	if want := []string{"o/r#1", "o/r#2"}; !sameKeys(ingKeys(got), want) {
		t.Fatalf("roots = %v, want %v", ingKeys(got), want)
	}
	for _, it := range got.Items {
		if len(it.Children) != 0 {
			t.Fatalf("%s children = %v, want none", it.ExternalKey, childKeys(it))
		}
	}
}

// 16 段たどっても根に着かない Issue は一覧から消さず、根に並べる（#779 の決定）。
func TestIngestable_ChainDeeperThanTheLimitKeepsTheTooDeepOnesAsRoots(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	var issues []UpstreamIssue
	issues = append(issues, ingIssue("o/r#1", ""))
	for i := 2; i <= 19; i++ {
		issues = append(issues, ingIssue(fmt.Sprintf("o/r#%d", i), fmt.Sprintf("o/r#%d", i-1)))
	}
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": issues}}
	got := listIngestable(t, s, up, ingSource())
	if len(got.Items) != 3 || got.Items[0].ExternalKey != "o/r#1" || len(got.Items[0].Children) != 16 {
		t.Fatalf("roots = %v, children of first = %d, want 3 roots (#1, #18, #19) and 16 children", ingKeys(got), len(got.Items[0].Children))
	}
	total := len(got.Items)
	for _, it := range got.Items {
		total += len(it.Children)
	}
	if total != 19 {
		t.Fatalf("total listed = %d, want 19 (一覧から消さない)", total)
	}
}

// AC-243（#779）: 親がすでに課題になっているとき、parent_challenge_id は親の課題の ID。
func TestIngestable_AC243_ParentChallengeIDIsTheParentsChallenge(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {ingIssue("o/r#1", "")}}}
	res, err := s.IngestSelected(context.Background(), ChannelCLI, IngestSelectedInput{Sources: []SourceEntry{ingSource()}, Specs: []string{"o/r#1"}, Upstream: up})
	if err != nil {
		t.Fatal(err)
	}
	up.issues["o/r"] = append(up.issues["o/r"], ingIssue("o/r#2", "O/R#1"))
	got := listIngestable(t, s, up, ingSource())
	if len(got.Items) != 1 || got.Items[0].ExternalKey != "o/r#2" || got.Items[0].ParentChallengeID != res.Ingested[0].ChallengeID {
		t.Fatalf("items = %+v, want o/r#2 with parent_challenge_id %s", got.Items, res.Ingested[0].ChallengeID)
	}
}

// AC-245（#779）: 一覧を読めないとき、error は nil でなく、読めたものは返す。
func TestIngestable_AC245_ListFailureSetsError(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r", "o/bad"}, []string{"masanami"})
	up := &fakeUpstream{
		issues: map[string][]UpstreamIssue{"o/r": {ingIssue("o/r#1", "")}},
		errs:   map[string]error{"o/bad": errors.New("boom")},
	}
	got := listIngestable(t, s, up, src)
	if got.Error == nil || len(got.Items) != 1 {
		t.Fatalf("got = %+v, want error set and the readable repo's item kept", got)
	}
}

// AC-247（#779）: 取り込み元の宣言が無い（sources が空）とき、items は nil でない空の配列、error は nil。
func TestIngestable_AC247_NoSourcesMeansEmptyItemsAndNoError(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	got := listIngestable(t, s, &fakeUpstream{})
	if got.Items == nil || len(got.Items) != 0 || got.Error != nil {
		t.Fatalf("got = %+v, want empty non-nil items and nil error", got)
	}
}

// AC-248（#779）: self_assignees を解決できない取り込み元の Issue は無く、error は nil でない。
func TestIngestable_AC248_UnresolvedSelfAssigneesDropsTheSourceAndSetsError(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, nil)
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {ingIssue("o/r#1", "")}}, loginErr: errors.New("no login")}
	got := listIngestable(t, s, up, src)
	if len(got.Items) != 0 || got.Error == nil {
		t.Fatalf("got = %+v, want no items and an error", got)
	}
}

// 上流の口を組み立てられないとき（gh が無い等）は、items は空・error に理由。
func TestIngestableUnavailable_CarriesTheReason(t *testing.T) {
	t.Parallel()
	got := IngestableUnavailable(errors.New("gh not found"))
	if got.Items == nil || len(got.Items) != 0 || got.Error == nil {
		t.Fatalf("got = %+v", got)
	}
}

// AC-249・AC-250・AC-251（#779）: followups.open は、未の件数から上げてあって取り下げでない課題に上げたものを除く。
func TestOverview_AC249_AC250_AC251_FollowupsOpenCount(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	ctx := context.Background()
	count := func() int {
		ov, err := s.GetOverview(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return ov.FollowupsOpen
	}
	a, _ := fuCreate(t, s, "", "a", ""), fuCreate(t, s, "", "b", "")
	if count() != 2 {
		t.Fatalf("followups.open = %d, want 2 (AC-249)", count())
	}
	ca, _, err := s.PromoteFollowup(ctx, a.ID, fuAtt(t, a.ID))
	if err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatalf("followups.open after promote = %d, want 1 (AC-250)", count())
	}
	setChallengeStatus(t, s, mustParseChallengeID(t, ca.ID), StatusWithdrawn)
	if count() != 2 {
		t.Fatalf("followups.open after the promoted challenge is withdrawn = %d, want 2 (AC-251)", count())
	}
}

// AC-212（#779）: 未のフォローアップがあるだけでは、サイクルを回すと進むものは 0（サイクルは始まらない）。
func TestCountCycleWork_AC212_OpenFollowupAloneAdvancesNothing(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	fuCreate(t, s, "", "a", "")
	w, err := s.CountCycleWork(context.Background(), &AgentDeclaration{PositionFile: "position.md"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w.Total != 0 {
		t.Fatalf("cycle work = %+v, want 0", *w)
	}
}

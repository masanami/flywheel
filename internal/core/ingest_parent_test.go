package core

import (
	"fmt"
	"reflect"
	"testing"
)

// docs/features/ui-relation-graph.md §取り込み: 上流の sub-issue の関係を課題の親子へ写す。

func subIssue(key, parent string) UpstreamIssue {
	i := issueFixture(key, "https://github.com/"+key, "t "+key, "body "+key, "carol", nil, nil)
	i.ParentExternalKey = parent
	return i
}

func parentActions(t *testing.T, s *Store, id string) []string {
	t.Helper()
	var out []string
	for _, a := range depActivities(t, s, id) {
		if a.Action == ActivityActionParentSet || a.Action == ActivityActionParentClear {
			out = append(out, a.Action)
		}
	}
	return out
}

func parentTestSource(repos ...string) []SourceEntry {
	return []SourceEntry{excludeOthersSource("src", repos, []string{"masanami"})}
}

func TestIngest_MapsASubIssueToTheParentChallenge(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {subIssue("o/r#1", ""), subIssue("o/r#2", "o/r#1"), subIssue("o/r#3", "o/r#1")}}}
	runIngestForTest(t, s, parentTestSource("o/r"), up)

	if got := parentRows(t, s); !reflect.DeepEqual(got, map[int64]int64{2: 1, 3: 1}) {
		t.Fatalf("parents = %v, want C-2 and C-3 under C-1", got)
	}
	if got := parentActions(t, s, "C-2"); !reflect.DeepEqual(got, []string{ActivityActionParentSet}) {
		t.Errorf("C-2 parent activities = %v", got)
	}
	if got := parentActions(t, s, "C-1"); len(got) != 0 {
		t.Errorf("C-1 parent activities = %v, want none", got)
	}
	// 親子の変更は課題の版を上げない。
	d, err := s.GetChallenge(t.Context(), "C-2")
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != 1 {
		t.Errorf("C-2 version = %d, want 1 (親子は版を上げない)", d.Version)
	}
}

func TestIngest_ParentSyncIsIdempotent(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {subIssue("o/r#1", ""), subIssue("o/r#2", "o/r#1")}}}
	runIngestForTest(t, s, parentTestSource("o/r"), up)
	activitiesBefore := len(depActivities(t, s, "C-2"))
	parentsBefore := parentRows(t, s)

	res := runIngestForTest(t, s, parentTestSource("o/r"), up)
	if got := parentRows(t, s); !reflect.DeepEqual(got, parentsBefore) {
		t.Errorf("parents = %v, want %v", got, parentsBefore)
	}
	if got := len(depActivities(t, s, "C-2")); got != activitiesBefore {
		t.Errorf("C-2 activities = %d, want %d (変わらなければ作業ログも残さない)", got, activitiesBefore)
	}
	if item := findIngestItem(res, "o/r#2"); item == nil || item.Result != IngestOutcomeUnchanged {
		t.Errorf("item = %+v, want unchanged (親は fingerprint に入らない)", item)
	}
}

func TestIngest_RemovingTheSubIssueRelationClearsTheParent(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {subIssue("o/r#1", ""), subIssue("o/r#2", "o/r#1")}}}
	runIngestForTest(t, s, parentTestSource("o/r"), up)

	up.issues["o/r"] = []UpstreamIssue{subIssue("o/r#1", ""), subIssue("o/r#2", "")}
	runIngestForTest(t, s, parentTestSource("o/r"), up)

	if got := parentRows(t, s); len(got) != 0 {
		t.Errorf("parents = %v, want none", got)
	}
	if got := parentActions(t, s, "C-2"); !reflect.DeepEqual(got, []string{ActivityActionParentSet, ActivityActionParentClear}) {
		t.Errorf("C-2 parent activities = %v", got)
	}
}

func TestIngest_ReparentsWhenTheUpstreamParentChanges(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {subIssue("o/r#1", ""), subIssue("o/r#2", ""), subIssue("o/r#3", "o/r#1")}}}
	runIngestForTest(t, s, parentTestSource("o/r"), up)
	up.issues["o/r"] = []UpstreamIssue{subIssue("o/r#1", ""), subIssue("o/r#2", ""), subIssue("o/r#3", "o/r#2")}
	runIngestForTest(t, s, parentTestSource("o/r"), up)
	if got := parentRows(t, s); !reflect.DeepEqual(got, map[int64]int64{3: 2}) {
		t.Errorf("parents = %v, want C-3 under C-2", got)
	}
}

// 親の Issue が core に無い（取り込みの対象外）ときは付かない。後で親が取り込まれたら付く。
func TestIngest_NoParentWhileTheParentIssueHasNoChallenge(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {subIssue("o/r#2", "o/r#1")}}}
	runIngestForTest(t, s, parentTestSource("o/r"), up)
	if got := parentRows(t, s); len(got) != 0 {
		t.Fatalf("parents = %v, want none (親の課題が無い)", got)
	}
	up.issues["o/r"] = []UpstreamIssue{subIssue("o/r#1", ""), subIssue("o/r#2", "o/r#1")}
	runIngestForTest(t, s, parentTestSource("o/r"), up)
	// o/r#2 が C-1、後から取り込まれた o/r#1 が C-2。
	if got := parentRows(t, s); !reflect.DeepEqual(got, map[int64]int64{1: 2}) {
		t.Errorf("parents = %v, want C-1 (o/r#2) under C-2 (o/r#1) once the parent exists", got)
	}
}

// 親の外部キーは大文字小文字を区別せずに照合する。別のリポジトリの親でも、親が後で処理される順でも同じ結果になる。
func TestIngest_ParentKeyMatchesCaseInsensitivelyAcrossReposAndOrder(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/a": {subIssue("o/a#5", "O/B#9")}, // 子が先、親は後のリポジトリ
		"o/b": {subIssue("o/b#9", "")},
	}}
	runIngestForTest(t, s, parentTestSource("o/a", "o/b"), up)
	if got := parentRows(t, s); !reflect.DeepEqual(got, map[int64]int64{1: 2}) {
		t.Errorf("parents = %v, want C-1 (o/a#5) under C-2 (o/b#9)", got)
	}
}

// 一覧の取得に失敗したリポジトリと、その回の一覧に見えなかった Issue の親子は変えない。
func TestIngest_LeavesParentsOfUnseenIssuesAndFailedReposAlone(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {subIssue("o/r#1", ""), subIssue("o/r#2", "o/r#1"), subIssue("o/r#3", "o/r#1")},
		"o/f": {subIssue("o/f#1", "o/r#1")},
	}}
	runIngestForTest(t, s, parentTestSource("o/r", "o/f"), up)
	want := map[int64]int64{2: 1, 3: 1, 4: 1}
	if got := parentRows(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("setup parents = %v, want %v", got, want)
	}

	// o/r#3 は一覧から消え（GetIssue では親なしで見える）、o/f は一覧の取得に失敗する。
	up.issues["o/r"] = []UpstreamIssue{subIssue("o/r#1", ""), subIssue("o/r#2", "o/r#1")}
	up.errs = map[string]error{"o/f": fmt.Errorf("boom")}
	up.getIssue = map[string]UpstreamIssue{"o/r#3": subIssue("o/r#3", "")}
	runIngestForTest(t, s, parentTestSource("o/r", "o/f"), up)
	if got := parentRows(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("parents = %v, want unchanged %v", got, want)
	}
}

// 親を付けると輪になるときは、その親子を付けずに取り込みを続ける。
func TestIngest_SkipsAParentThatWouldCreateACycleAndContinues(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {subIssue("o/r#1", "o/r#2"), subIssue("o/r#2", "o/r#1"), subIssue("o/r#3", "o/r#1")}}}
	res := runIngestForTest(t, s, parentTestSource("o/r"), up)

	got := parentRows(t, s)
	if len(got) != 2 || got[3] != 1 {
		t.Fatalf("parents = %v, want exactly one of the cyclic pair plus C-3 under C-1", got)
	}
	if got[1] == 2 && got[2] == 1 {
		t.Errorf("parents = %v form a cycle", got)
	}
	if repo := findRepoResult(res, "src", "o/r"); repo == nil || repo.Error != nil || len(repo.Items) != 3 {
		t.Errorf("repo result = %+v, want 3 items and no error", repo)
	}
}

// 親と子が入れ替わる上流の変更は、課題の ID の順に左右されず入れ替わる。
func TestIngest_SwapsParentAndChildRegardlessOfIDOrder(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {subIssue("o/r#1", ""), subIssue("o/r#2", "o/r#1")}}}
	runIngestForTest(t, s, parentTestSource("o/r"), up)
	up.issues["o/r"] = []UpstreamIssue{subIssue("o/r#1", "o/r#2"), subIssue("o/r#2", "")}
	runIngestForTest(t, s, parentTestSource("o/r"), up)
	if got := parentRows(t, s); !reflect.DeepEqual(got, map[int64]int64{1: 2}) {
		t.Errorf("parents = %v, want C-1 under C-2", got)
	}
}

package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os/user"
	"sync"
	"testing"
	"time"
)

// このファイルは #56 の対応する受入基準（AC-12・AC-23〜AC-48・AC-87・AC-103の
// 一部）を検証する。#56 の範囲は「assignee のポリシーと新規課題の作成
// （ingest_create）」だけで、冪等な更新（#57）・上流の close の確かめ（#58）は
// 対象外。取得の IF は fakeUpstream（メモリ上の Issue の集合）で置き換える。

// --- テスト用の偽の取得 IF ---

type fakeUpstream struct {
	mu sync.Mutex

	issues   map[string][]UpstreamIssue // repo -> issues
	errs     map[string]error           // repo -> ListOpenIssues のエラー
	login    string
	loginErr error

	listCalls  int // ListOpenIssues の呼び出し回数
	loginCalls int // CurrentLogin の呼び出し回数

	// onListOpenIssues はテストが呼び出しのタイミングを検知・同期するための
	// フック（AC-21 相当の「取得中は書き込みロックを保持しない」ことの検証に使う）。
	onListOpenIssues func(repo string)
}

func (f *fakeUpstream) ListOpenIssues(_ context.Context, repo string) ([]UpstreamIssue, error) {
	if f.onListOpenIssues != nil {
		f.onListOpenIssues(repo)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if err, ok := f.errs[repo]; ok {
		return nil, err
	}
	return f.issues[repo], nil
}

func (f *fakeUpstream) GetIssue(_ context.Context, _ string, _ int) (UpstreamIssue, error) {
	return UpstreamIssue{}, errors.New("fakeUpstream.GetIssue: not used by #56 (close の確かめは #58)")
}

func (f *fakeUpstream) CurrentLogin(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginCalls++
	if f.loginErr != nil {
		return "", f.loginErr
	}
	return f.login, nil
}

// issueFixture は 1 件の UpstreamIssue を組み立てる（テストの既定値: state=open）。
func issueFixture(externalKey, url, title, body, reporter string, assignees, labels []string) UpstreamIssue {
	return UpstreamIssue{
		ExternalKey: externalKey,
		Title:       title,
		Body:        body,
		Reporter:    reporter,
		Assignees:   assignees,
		Labels:      labels,
		State:       "open",
		URL:         url,
	}
}

func selfOnlySource(id string, repos, selfAssignees []string) SourceEntry {
	p := AssigneePolicySelfOnly
	return SourceEntry{ID: id, Type: SourceTypeGitHubIssue, Repos: repos, AssigneePolicy: &p, SelfAssignees: selfAssignees}
}

func excludeOthersSource(id string, repos, selfAssignees []string) SourceEntry {
	p := AssigneePolicyExcludeOthers
	return SourceEntry{ID: id, Type: SourceTypeGitHubIssue, Repos: repos, AssigneePolicy: &p, SelfAssignees: selfAssignees}
}

func runIngestForTest(t *testing.T, s *Store, sources []SourceEntry, upstream UpstreamIssueSource) *IngestResult {
	t.Helper()
	res, err := s.Ingest(context.Background(), ChannelCLI, IngestInput{Sources: sources, Upstream: upstream})
	if err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	return res
}

func findIngestItem(res *IngestResult, externalKey string) *IngestItemResult {
	for _, src := range res.Sources {
		for _, repo := range src.Repos {
			for i := range repo.Items {
				if repo.Items[i].ExternalKey == externalKey {
					return &repo.Items[i]
				}
			}
		}
	}
	return nil
}

func findRepoResult(res *IngestResult, sourceID, repo string) *RepoIngestResult {
	for _, src := range res.Sources {
		if src.ID != sourceID {
			continue
		}
		for i := range src.Repos {
			if src.Repos[i].Repo == repo {
				return &src.Repos[i]
			}
		}
	}
	return nil
}

func sourceResult(t *testing.T, res *IngestResult, sourceID string) SourceIngestResult {
	t.Helper()
	for _, src := range res.Sources {
		if src.ID == sourceID {
			return src
		}
	}
	t.Fatalf("no source result for id %q", sourceID)
	return SourceIngestResult{}
}

// --- AC-12: assignee_policy 省略は exclude-others として振る舞う ---

func TestIngest_OmittedAssigneePolicyBehavesAsExcludeOthers(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	src := SourceEntry{ID: "src", Type: SourceTypeGitHubIssue, Repos: []string{"o/r"}, SelfAssignees: []string{"masanami"}}
	if src.Policy() != AssigneePolicyExcludeOthers {
		t.Fatalf("Policy() = %q, want exclude-others as the default", src.Policy())
	}
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "unassigned", "body", "carol", nil, nil)},
	}}

	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeCreated {
		t.Fatalf("item = %+v, want created (AC-12: 省略時は exclude-others として振る舞う)", item)
	}
}

// --- AC-23〜AC-29: assignee のポリシー ---

func TestIngest_AssigneePolicy(t *testing.T) {
	cases := []struct {
		name       string
		source     SourceEntry
		assignees  []string
		wantCreate bool
	}{
		{"AC-23 self-only: 1人だけ selfAssignees", selfOnlySource("s", []string{"o/r"}, []string{"masanami"}), []string{"masanami"}, true},
		{"AC-24 self-only: assignee無し", selfOnlySource("s", []string{"o/r"}, []string{"masanami"}), nil, false},
		{"AC-25 self-only: co-assignで他人を含む", selfOnlySource("s", []string{"o/r"}, []string{"masanami"}), []string{"masanami", "eve"}, false},
		{"AC-26 self-only: 2人とも selfAssignees", selfOnlySource("s", []string{"o/r"}, []string{"masanami", "bob"}), []string{"masanami", "bob"}, true},
		{"AC-27 exclude-others: assignee無し", excludeOthersSource("s", []string{"o/r"}, []string{"masanami"}), nil, true},
		{"AC-28 exclude-others: selfAssigneesに無い人を含む", excludeOthersSource("s", []string{"o/r"}, []string{"masanami"}), []string{"eve"}, false},
		{"exclude-others: 全員が selfAssignees", excludeOthersSource("s", []string{"o/r"}, []string{"masanami"}), []string{"masanami"}, true},
		{"AC-28 exclude-others: 自分との co-assign で他人を含む", excludeOthersSource("s", []string{"o/r"}, []string{"masanami"}), []string{"masanami", "eve"}, false},
		{"self-only: 明示した空配列は空集合（対象なし）", selfOnlySource("s", []string{"o/r"}, []string{}), []string{"masanami"}, false},
		{"exclude-others: 明示した空配列は空集合（assignee なしだけ）", excludeOthersSource("s", []string{"o/r"}, []string{}), []string{"masanami"}, false},
		{"self-only: \"@\" だけの要素はどの login にも一致しない", selfOnlySource("s", []string{"o/r"}, []string{"@", ""}), []string{"masanami"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			up := &fakeUpstream{login: "masanami", issues: map[string][]UpstreamIssue{
				"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", "b", "carol", tc.assignees, nil)},
			}}
			res := runIngestForTest(t, s, []SourceEntry{tc.source}, up)
			// self_assignees を明示した取り込み元（空配列を含む）は gh api user で
			// 補わない。
			if up.loginCalls != 0 {
				t.Fatalf("CurrentLogin called %d times, want 0 for an explicit self_assignees", up.loginCalls)
			}
			if !sourceResult(t, res, "s").SelfAssigneesResolved {
				t.Fatalf("SelfAssigneesResolved = false, want true for an explicit self_assignees")
			}
			item := findIngestItem(res, "o/r#1")
			created := item != nil && item.Result == IngestOutcomeCreated
			if created != tc.wantCreate {
				t.Fatalf("created = %v, want %v (item=%+v)", created, tc.wantCreate, item)
			}
			if !tc.wantCreate {
				repo := findRepoResult(res, "s", "o/r")
				if repo == nil || repo.Excluded != 1 {
					t.Fatalf("excluded = %+v, want 1", repo)
				}
			}
		})
	}
}

// AC-29: login の比較は大文字小文字を無視し、self_assignees の先頭の "@" を除く。
func TestIngest_LoginComparisonIgnoresCaseAndLeadingAt(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := selfOnlySource("s", []string{"o/r"}, []string{"@MasaNami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", "b", "carol", []string{"masanami"}, nil)},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeCreated {
		t.Fatalf("item = %+v, want created (AC-29)", item)
	}
}

// AC-29（比較の両側）: assignee 側の login（GitHub は正規の表記で返す）と
// gh api user の login も、大文字小文字を無視して比較する。
func TestIngest_LoginComparisonIgnoresCaseOnBothSides(t *testing.T) {
	cases := []struct {
		name     string
		self     []string
		login    string
		assignee string
	}{
		{"assignee 側が大文字を含む", []string{"masanami"}, "", "MasaNami"},
		{"gh api user の login が大文字を含む", nil, "MasaNami", "masanami"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			src := selfOnlySource("s", []string{"o/r"}, tc.self)
			up := &fakeUpstream{login: tc.login, issues: map[string][]UpstreamIssue{
				"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", "b", "carol", []string{tc.assignee}, nil)},
			}}
			res := runIngestForTest(t, s, []SourceEntry{src}, up)
			if item := findIngestItem(res, "o/r#1"); item == nil || item.Result != IngestOutcomeCreated {
				t.Fatalf("item = %+v, want created (AC-29)", item)
			}
		})
	}
}

// --- AC-30〜AC-33: self_assignees の省略と解決 ---

// AC-30: self_assignees を省略した取り込み元は gh api user の login を使う。
func TestIngest_OmittedSelfAssignees_ResolvesViaCurrentLogin(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := selfOnlySource("s", []string{"o/r"}, nil)
	up := &fakeUpstream{login: "masanami", issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", "b", "carol", []string{"masanami"}, nil)},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	sr := sourceResult(t, res, "s")
	if !sr.SelfAssigneesResolved {
		t.Fatalf("SelfAssigneesResolved = false, want true (AC-30)")
	}
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeCreated {
		t.Fatalf("item = %+v, want created (AC-30)", item)
	}
}

// AC-31: self_assignees を省略し gh api user が失敗すると、self-only は新しい
// Issue を1件も取り込まず、self_assignees_resolved が false。
func TestIngest_OmittedSelfAssignees_ResolutionFailure_SelfOnlyIngestsNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := selfOnlySource("s", []string{"o/r"}, nil)
	up := &fakeUpstream{loginErr: errors.New("gh api user failed"), issues: map[string][]UpstreamIssue{
		"o/r": {
			issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t1", "b", "carol", nil, nil),
			issueFixture("o/r#2", "https://github.com/o/r/issues/2", "t2", "b", "carol", []string{"masanami"}, nil),
		},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	sr := sourceResult(t, res, "s")
	if sr.SelfAssigneesResolved {
		t.Fatalf("SelfAssigneesResolved = true, want false (AC-31)")
	}
	repo := findRepoResult(res, "s", "o/r")
	if repo == nil || len(repo.Items) != 0 || repo.Excluded != 2 {
		t.Fatalf("repo = %+v, want 0 items, excluded=2 (AC-31: self-only は新しい Issue を1件も取り込まない)", repo)
	}
}

// AC-32: 同じ状況で exclude-others は assignee のいない Issue だけを取り込む。
func TestIngest_OmittedSelfAssignees_ResolutionFailure_ExcludeOthersIngestsUnassignedOnly(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, nil)
	up := &fakeUpstream{loginErr: errors.New("gh api user failed"), issues: map[string][]UpstreamIssue{
		"o/r": {
			issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t1", "b", "carol", nil, nil),
			issueFixture("o/r#2", "https://github.com/o/r/issues/2", "t2", "b", "carol", []string{"masanami"}, nil),
		},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	if item := findIngestItem(res, "o/r#1"); item == nil || item.Result != IngestOutcomeCreated {
		t.Fatalf("o/r#1 = %+v, want created (AC-32: assignee のいない Issue)", item)
	}
	if item := findIngestItem(res, "o/r#2"); item != nil {
		t.Fatalf("o/r#2 = %+v, want not ingested (assignee あり)", item)
	}
	repo := findRepoResult(res, "s", "o/r")
	if repo == nil || repo.Excluded != 1 {
		t.Fatalf("excluded = %+v, want 1", repo)
	}
}

// AC-33: self_assignees の解決に失敗しても、対応のある課題のポリシーの状態は
// 再判定されない（前回 in_policy の課題の Issue に他人を assign しておいても
// in_policy のまま）。
func TestIngest_OmittedSelfAssignees_ResolutionFailure_DoesNotReevaluateExistingBindings(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := mustCreateChallenge(t, s, "already bound")
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), validBindingInput("o/r#9")); err != nil {
		t.Fatalf("insertSourceBinding() error = %v", err)
	}

	src := excludeOthersSource("s", []string{"o/r"}, nil)
	up := &fakeUpstream{loginErr: errors.New("gh api user failed"), issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#9", "https://github.com/o/r/issues/9", "t", "b", "carol", []string{"eve"}, nil)},
	}}
	runIngestForTest(t, s, []SourceEntry{src}, up)

	sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || sb == nil || sb.PolicyState != policyStateInPolicy {
		t.Fatalf("source_binding = %+v, %v, want policy_state=in_policy のまま (AC-33)", sb, err)
	}
}

// --- AC-34〜AC-48: 冪等な作成（対応の無い、ポリシーに合う Issue から課題を作る） ---

func TestIngest_CreatesUnclassifiedChallengeFromNewMatchingIssue(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("harness-repo-issues", []string{"masanami/flywheel"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"masanami/flywheel": {issueFixture(
			"masanami/flywheel#49",
			"https://github.com/masanami/flywheel/issues/49",
			"issue title", "issue body\nline 2", "octocat", nil, nil,
		)},
	}}

	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "masanami/flywheel#49")
	if item == nil || item.Result != IngestOutcomeCreated {
		t.Fatalf("item = %+v, want created", item)
	}

	detail, err := s.GetChallenge(context.Background(), item.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}

	// AC-34: 状態は未分類。
	if detail.Status != StatusUnclassified {
		t.Errorf("Status = %q, want %q (AC-34)", detail.Status, StatusUnclassified)
	}
	// AC-35: タイトルは Issue のタイトル。
	if detail.Title != "issue title" {
		t.Errorf("Title = %q, want %q (AC-35)", detail.Title, "issue title")
	}
	// AC-36: 説明は本文と1バイトも違わない。
	if detail.Description != "issue body\nline 2" {
		t.Errorf("Description = %q, want exact body (AC-36)", detail.Description)
	}
	// AC-37: 完了条件は空。
	if detail.DoneCriteria != "" {
		t.Errorf("DoneCriteria = %q, want empty (AC-37)", detail.DoneCriteria)
	}
	// AC-38: 優先度は未設定。
	if detail.Priority != nil {
		t.Errorf("Priority = %v, want nil (AC-38)", detail.Priority)
	}
	// AC-41: 起票者は Issue の作成者の login。
	if detail.Reporter != "octocat" {
		t.Errorf("Reporter = %q, want %q (AC-41)", detail.Reporter, "octocat")
	}

	sb := detail.SourceBinding
	if sb == nil {
		t.Fatalf("SourceBinding = nil, want non-nil")
	}
	// AC-42: source_binding に取り込み元の id。
	if sb.SourceID != "harness-repo-issues" {
		t.Errorf("SourceID = %q, want %q (AC-42)", sb.SourceID, "harness-repo-issues")
	}
	// AC-43: source_binding に外部キー。
	if sb.ExternalKey != "masanami/flywheel#49" {
		t.Errorf("ExternalKey = %q, want %q (AC-43)", sb.ExternalKey, "masanami/flywheel#49")
	}
	// AC-44: url は html_url。
	if sb.URL != "https://github.com/masanami/flywheel/issues/49" {
		t.Errorf("URL = %q, want the html_url (AC-44)", sb.URL)
	}
	// AC-45: fingerprint は "2:<12桁>"。
	wantFP := Fingerprint("issue body\nline 2")
	if sb.Fingerprint != wantFP {
		t.Errorf("Fingerprint = %q, want %q (AC-45)", sb.Fingerprint, wantFP)
	}
	// AC-46: upstream_state は open。
	if sb.UpstreamState != "open" {
		t.Errorf("UpstreamState = %q, want open (AC-46)", sb.UpstreamState)
	}
	// AC-47: policy_state は in_policy。
	if sb.PolicyState != "in_policy" {
		t.Errorf("PolicyState = %q, want in_policy (AC-47)", sb.PolicyState)
	}
}

// AC-39・AC-40: 緊急度は urgency_labels の完全一致。異なる緊急度へ写るラベルが
// 複数あれば未設定。
func TestIngest_UrgencyFromLabels(t *testing.T) {
	urgencyLabels := map[string]string{
		"priority:high": "高", "priority:medium": "中", "priority:low": "低", "P0": "高",
	}
	cases := []struct {
		name   string
		labels []string
		want   *Urgency
	}{
		{"高", []string{"priority:high"}, urgencyPtr(UrgencyHigh)},
		{"中", []string{"priority:medium"}, urgencyPtr(UrgencyMedium)},
		{"低", []string{"priority:low"}, urgencyPtr(UrgencyLow)},
		{"一致なし", []string{"bug"}, nil},
		{"異なる緊急度へ写るラベルが2つ", []string{"priority:high", "priority:low"}, nil},
		{"同じ緊急度へ写るラベルが2つ", []string{"priority:high", "P0"}, urgencyPtr(UrgencyHigh)},
		{"大文字小文字が違うラベルは一致しない", []string{"Priority:High"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			src := SourceEntry{ID: "s", Type: SourceTypeGitHubIssue, Repos: []string{"o/r"}, UrgencyLabels: urgencyLabels}
			up := &fakeUpstream{issues: map[string][]UpstreamIssue{
				"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", "b", "carol", nil, tc.labels)},
			}}
			res := runIngestForTest(t, s, []SourceEntry{src}, up)
			item := findIngestItem(res, "o/r#1")
			if item == nil {
				t.Fatalf("item = nil, want created")
			}
			detail, err := s.GetChallenge(context.Background(), item.ChallengeID)
			if err != nil {
				t.Fatalf("GetChallenge() error = %v", err)
			}
			if !urgencyEqual(detail.Urgency, tc.want) {
				t.Errorf("Urgency = %v, want %v", detail.Urgency, tc.want)
			}
		})
	}
}

func urgencyPtr(u Urgency) *Urgency { return &u }

// AC-48: create で作った課題の show（GetChallenge）は source_binding に null
// （nil）を出力する。
func TestGetChallenge_CreateChallengeHasNilSourceBinding(t *testing.T) {
	s := newStoreForTest(t)
	ch := mustCreateChallenge(t, s, "no source")
	detail, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.SourceBinding != nil {
		t.Errorf("SourceBinding = %+v, want nil (AC-48)", detail.SourceBinding)
	}
}

// --- AC-87: 作業ログ ---

func TestIngest_RecordsIngestCreateActivity(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", "b", "carol", nil, nil)},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil {
		t.Fatalf("item = nil, want created")
	}

	activities, err := s.ListActivities(context.Background(), &item.ChallengeID)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	if len(activities) != 1 {
		t.Fatalf("len(activities) = %d, want 1", len(activities))
	}
	a := activities[0]
	if a.Action != "ingest_create" || a.Actor != "alice" || a.Channel != "cli" || a.Verification != "none" || a.Before != nil {
		t.Errorf("activity = %+v, want ingest_create by alice/cli/none with before=null (AC-87)", a)
	}
	var after map[string]any
	if err := json.Unmarshal(a.After, &after); err != nil {
		t.Fatalf("unmarshal after: %v", err)
	}
	// after は課題の人間記入欄と external_key で、version は載せない（M1 の
	// create と同じ規則）。
	wantAfter := map[string]any{
		"title": "t", "description": "b", "done_criteria": "", "urgency": nil,
		"status": "unclassified", "reporter": "carol", "external_key": "o/r#1",
	}
	if len(after) != len(wantAfter) {
		t.Errorf("after = %+v, want exactly %+v", after, wantAfter)
	}
	for k, v := range wantAfter {
		if got, ok := after[k]; !ok || got != v {
			t.Errorf("after[%q] = %#v, want %#v", k, got, v)
		}
	}
	if _, ok := after["version"]; ok {
		t.Errorf("after has version, want none (AC-87: M1 の create と同じ規則)")
	}
}

// --- 骨格の設計を固定する追加のテスト（変異注入で検出力を確かめる対象） ---

// 取得（ListOpenIssues）の間は、ストアの書き込みロックを保持していない。
// fakeUpstream の ListOpenIssues をブロックしている間に、別の *Store からの
// CreateChallenge が store_busy にならずに成功することで検証する。
func TestIngest_DoesNotHoldWriteLockWhileFetching(t *testing.T) {
	fixedActor(t, "alice")
	ws := t.TempDir()
	if _, err := Init(ws); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	storeA, err := OpenWorkspace(ws)
	if err != nil {
		t.Fatalf("OpenWorkspace() A error = %v", err)
	}
	defer func() { _ = storeA.Close() }()
	storeB, err := OpenWorkspace(ws)
	if err != nil {
		t.Fatalf("OpenWorkspace() B error = %v", err)
	}
	defer func() { _ = storeB.Close() }()

	entered := make(chan struct{})
	release := make(chan struct{})
	var once, releaseOnce sync.Once
	releaseFetch := func() { releaseOnce.Do(func() { close(release) }) }
	up := &fakeUpstream{
		issues: map[string][]UpstreamIssue{"o/r": {}},
		onListOpenIssues: func(string) {
			once.Do(func() { close(entered) })
			<-release
		},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
		_, _ = storeA.Ingest(context.Background(), ChannelCLI, IngestInput{Sources: []SourceEntry{src}, Upstream: up})
	}()
	// 失敗経路（t.Fatal）でも取得を解放し、Ingest の終了を待ってから後始末する。
	t.Cleanup(func() {
		releaseFetch()
		<-done
	})

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for fetch to start")
	}

	// 取得がストアの書き込みロックを保持していれば、この CreateChallenge は
	// busy_timeout（テスト用の短縮値ではなく本番の 5000ms）を待つことになる。
	// 保持していなければ即座に成功するはずなので、余裕を持った上限で検証する。
	createDone := make(chan error, 1)
	go func() {
		_, err := storeB.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "concurrent"})
		createDone <- err
	}()

	select {
	case err := <-createDone:
		if err != nil {
			t.Fatalf("CreateChallenge() during fetch error = %v, want nil (fetch はストアの書き込みロックを保持しない)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CreateChallenge() blocked while ListOpenIssues was in flight; fetch must not hold the write lock")
	}

	releaseFetch()
	<-done
}

// 1 つの Issue の反映で失敗を注入しても、それより前に反映した Issue の課題は
// 残る（1 Issue = 1 トランザクションであることの検証）。
func TestIngest_FailureOnOneIssueDoesNotRollBackEarlierIssues(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	// insertActivity は external_key を直接は知らないため、2件目の ingest_create
	// 呼び出しだけを数えて狙い撃ちで失敗させる。
	failInjected := errors.New("injected activity failure")
	callCount := 0
	s.insertActivity = func(tx *sql.Tx, row activityRow) error {
		if row.Action != "ingest_create" {
			return defaultInsertActivity(tx, row)
		}
		callCount++
		if callCount == 2 {
			return failInjected
		}
		return defaultInsertActivity(tx, row)
	}

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {
			issueFixture("o/r#1", "https://github.com/o/r/issues/1", "first", "b1", "carol", nil, nil),
			issueFixture("o/r#2", "https://github.com/o/r/issues/2", "second", "b2", "carol", nil, nil),
			issueFixture("o/r#3", "https://github.com/o/r/issues/3", "third", "b3", "carol", nil, nil),
		},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)

	first := findIngestItem(res, "o/r#1")
	if first == nil || first.Result != IngestOutcomeCreated {
		t.Fatalf("o/r#1 = %+v, want created (前に反映した Issue は残る)", first)
	}
	second := findIngestItem(res, "o/r#2")
	if second == nil || second.Result != IngestOutcomeFailed {
		t.Fatalf("o/r#2 = %+v, want failed (注入した失敗)", second)
	}
	third := findIngestItem(res, "o/r#3")
	if third == nil || third.Result != IngestOutcomeCreated {
		t.Fatalf("o/r#3 = %+v, want created (失敗した Issue の後も続行する)", third)
	}

	if sb, err := s.getSourceBindingByExternalKey(context.Background(), "o/r#2"); err != nil || sb != nil {
		t.Fatalf("source_binding(o/r#2) = %+v, %v, want nil, nil（失敗したトランザクションは何も残さない）", sb, err)
	}
	all, err := s.ListChallenges(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListChallenges() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("課題の件数 = %d, want 2（1件目・3件目だけが残る）", len(all))
	}
}

// 対応が既にある Issue は、ポリシーに合っても合わなくても、課題・対応・作業ログを
// 変えず、items にも excluded にも数えない（#56 の骨格。更新は #57、ポリシーの
// 状態は #58）。self_assignees の解決に失敗した場合も同じ（AC-33 の分岐）。
func TestIngest_BoundIssuesAreNeitherCreatedNorExcluded(t *testing.T) {
	cases := []struct {
		name     string
		src      SourceEntry
		loginErr error
	}{
		{"self_assignees を明示", selfOnlySource("s", []string{"o/r"}, []string{"masanami"}), nil},
		{"self_assignees の解決に失敗", selfOnlySource("s", []string{"o/r"}, nil), errors.New("gh api user failed")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			inPolicy := mustCreateChallenge(t, s, "bound, in policy")
			outOfPolicy := mustCreateChallenge(t, s, "bound, out of policy")
			for id, key := range map[string]string{inPolicy.ID: "o/r#1", outOfPolicy.ID: "o/r#2"} {
				if _, err := createSourceBindingForTest(s, id, time.Now(), validBindingInput(key)); err != nil {
					t.Fatalf("insertSourceBinding(%s) error = %v", key, err)
				}
			}
			before, err := s.ListActivities(context.Background(), nil)
			if err != nil {
				t.Fatalf("ListActivities() error = %v", err)
			}

			up := &fakeUpstream{loginErr: tc.loginErr, issues: map[string][]UpstreamIssue{
				"o/r": {
					issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t1", "b1", "carol", []string{"masanami"}, nil),
					issueFixture("o/r#2", "https://github.com/o/r/issues/2", "t2", "b2", "carol", []string{"eve"}, nil),
				},
			}}
			res := runIngestForTest(t, s, []SourceEntry{tc.src}, up)

			repo := findRepoResult(res, "s", "o/r")
			if repo == nil || len(repo.Items) != 0 || repo.Excluded != 0 {
				t.Fatalf("repo = %+v, want 0 items and excluded=0 (対応のある Issue は新しい Issue ではない)", repo)
			}
			all, err := s.ListChallenges(context.Background(), ListOptions{})
			if err != nil || len(all) != 2 {
				t.Fatalf("ListChallenges() = %d, %v, want 2 (課題を作らない)", len(all), err)
			}
			after, err := s.ListActivities(context.Background(), nil)
			if err != nil || len(after) != len(before) {
				t.Fatalf("activities = %d, %v, want %d (作業ログを変えない)", len(after), err, len(before))
			}
			for _, key := range []string{"o/r#1", "o/r#2"} {
				sb, err := s.getSourceBindingByExternalKey(context.Background(), key)
				if err != nil || sb == nil || sb.PolicyState != policyStateInPolicy || sb.Fingerprint != "2:abc" {
					t.Fatalf("source_binding(%s) = %+v, %v, want unchanged", key, sb, err)
				}
			}
		})
	}
}

// actor（OS のログインユーザー名）を解決できない環境では、上流へ接続せずに
// ErrActorUnavailable で終わり、何も作らない（M1 の書き込みコマンドと同じ）。
func TestIngest_ActorUnavailable_FailsBeforeFetching(t *testing.T) {
	s := newStoreForTest(t)
	withActorSource(t, actorSourceFuncs{
		userCurrent: func() (*user.User, error) { return nil, errors.New("no user") },
		getenv:      fakeGetenv(map[string]string{}),
	})
	src := excludeOthersSource("s", []string{"o/r"}, nil)
	up := &fakeUpstream{login: "masanami", issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", "b", "carol", nil, nil)},
	}}
	_, err := s.Ingest(context.Background(), ChannelCLI, IngestInput{Sources: []SourceEntry{src}, Upstream: up})
	if !errors.Is(err, ErrActorUnavailable) {
		t.Fatalf("Ingest() error = %v, want ErrActorUnavailable", err)
	}
	if up.listCalls != 0 || up.loginCalls != 0 {
		t.Fatalf("upstream calls = list %d / login %d, want 0 (取得の前に終える)", up.listCalls, up.loginCalls)
	}
	all, err := s.ListChallenges(context.Background(), ListOptions{})
	if err != nil || len(all) != 0 {
		t.Fatalf("ListChallenges() = %d, %v, want 0", len(all), err)
	}
}

// ctx が取り消されたら、残りの取得・反映をせずに error を返す（Issue ごとの
// failed に薄めない）。
func TestIngest_ContextCanceled_ReturnsError(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ctx, cancel := context.WithCancel(context.Background())
	up := &fakeUpstream{
		issues: map[string][]UpstreamIssue{
			"o/r1": {issueFixture("o/r1#1", "https://github.com/o/r1/issues/1", "t", "b", "carol", nil, nil)},
			"o/r2": {issueFixture("o/r2#1", "https://github.com/o/r2/issues/1", "t", "b", "carol", nil, nil)},
		},
		onListOpenIssues: func(string) { cancel() },
	}
	src := excludeOthersSource("s", []string{"o/r1", "o/r2"}, []string{"masanami"})
	_, err := s.Ingest(ctx, ChannelCLI, IngestInput{Sources: []SourceEntry{src}, Upstream: up})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Ingest() error = %v, want context.Canceled", err)
	}
	if up.listCalls != 1 {
		t.Fatalf("ListOpenIssues called %d times, want 1 (取り消し後は取得しない)", up.listCalls)
	}
	all, err := s.ListChallenges(context.Background(), ListOptions{})
	if err != nil || len(all) != 0 {
		t.Fatalf("ListChallenges() = %d, %v, want 0", len(all), err)
	}
}

// result の閉集合は仕様の列挙（§IF / API `ingest` の JSON 出力）と一致する。
func TestIngestOutcomeValues_MatchSpec(t *testing.T) {
	want := []string{"created", "updated", "unchanged", "skipped_done", "fingerprint_unknown_version", "failed"}
	got := IngestOutcomeValues()
	if len(got) != len(want) {
		t.Fatalf("IngestOutcomeValues() = %v, want %v", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("IngestOutcomeValues()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

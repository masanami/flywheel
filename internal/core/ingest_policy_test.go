package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// このファイルは #58 の「ポリシーに合わなくなった課題」（AC-76〜AC-79）と、
// 「作業ログと版」のうち複数の契機が1回の反映で同時に起きる場合（AC-89〜AC-92）を
// 検証する。上流の close の確かめは ingest_close_test.go が担当する。

// bindInPolicyChallenge は externalKey に対応する未分類の課題を作り、
// upstream_state=open・policy_state=in_policy の source_binding を、fingerprint が
// body と一致する形で結び付ける（人間記入欄の置き換え=ingest_update を起こさず、
// ポリシー・上流の状態の変化だけを検証できるようにするため）。
func bindInPolicyChallenge(t *testing.T, s *Store, title, externalKey, body string) *Challenge {
	t.Helper()
	ch := mustCreateChallenge(t, s, title)
	in := validBindingInput(externalKey)
	in.Fingerprint = Fingerprint(body)
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest(%s) error = %v", externalKey, err)
	}
	return ch
}

// --- AC-76: selfAssignees に無い人が assign されると out_of_policy になる ---

func TestIngest_Policy_AssigneeOutsideSelfAssigneesBecomesOutOfPolicy(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	body := "body"
	ch := bindInPolicyChallenge(t, s, "bound", "o/r#1", body)

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", []string{"eve"}, nil)},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.PolicyState != "out_of_policy" {
		t.Fatalf("item = %+v, want policy_state=out_of_policy (AC-76)", item)
	}

	sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || sb == nil || sb.PolicyState != policyStateOutOfPolicy {
		t.Fatalf("source_binding = %+v, %v, want policy_state=out_of_policy", sb, err)
	}
}

// --- AC-77: self-only で assignee がいなくなると out_of_policy になる ---

func TestIngest_Policy_SelfOnlyBecomesOutOfPolicyWhenUnassigned(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	body := "body"
	bindInPolicyChallenge(t, s, "bound", "o/r#1", body)

	src := selfOnlySource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", nil, nil)},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.PolicyState != "out_of_policy" {
		t.Fatalf("item = %+v, want policy_state=out_of_policy (AC-77)", item)
	}
}

// --- AC-78: out_of_policy がポリシーに合う状態に戻れば in_policy に戻る ---

func TestIngest_Policy_OutOfPolicyReturnsToInPolicy(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	body := "body"
	ch := mustCreateChallenge(t, s, "bound")
	in := validBindingInput("o/r#1")
	in.Fingerprint = Fingerprint(body)
	in.PolicyState = policyStateOutOfPolicy
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest() error = %v", err)
	}

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", []string{"masanami"}, nil)},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.PolicyState != "in_policy" {
		t.Fatalf("item = %+v, want policy_state=in_policy (AC-78)", item)
	}
}

// --- AC-79: out_of_policy になっても課題は削除されず、状態も変わらない
// （未分類・着手中のそれぞれ） ---

func TestIngest_Policy_OutOfPolicyDoesNotDeleteOrChangeStatus(t *testing.T) {
	for _, status := range []Status{StatusUnclassified, StatusInProgress} {
		t.Run(string(status), func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			body := "body"
			ch := bindInPolicyChallenge(t, s, "bound", "o/r#1", body)
			cid := mustParseChallengeID(t, ch.ID)
			setChallengeStatus(t, s, cid, status)

			src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
			up := &fakeUpstream{issues: map[string][]UpstreamIssue{
				"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", []string{"eve"}, nil)},
			}}
			runIngestForTest(t, s, []SourceEntry{src}, up)

			detail, err := s.GetChallenge(context.Background(), ch.ID)
			if err != nil || detail.Status != status {
				t.Fatalf("Status = %v, %v, want unchanged %q (AC-79)", detail, err, status)
			}
			if detail.SourceBinding == nil || detail.SourceBinding.PolicyState != "out_of_policy" {
				t.Errorf("SourceBinding = %+v, want policy_state=out_of_policy", detail.SourceBinding)
			}
			all, err := s.ListChallenges(context.Background(), ListOptions{})
			if err != nil || len(all) != 1 {
				t.Fatalf("ListChallenges() = %d, %v, want 1（削除されない）", len(all), err)
			}
		})
	}
}

// --- selfAssignees の解決に失敗した回はポリシーを再判定しない（AC-33 と同じ趣旨） ---

func TestIngest_Policy_NotReevaluatedWhenSelfAssigneesUnresolved(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	body := "body"
	bindInPolicyChallenge(t, s, "bound", "o/r#1", body)

	src := selfOnlySource("s", []string{"o/r"}, nil) // self_assignees 省略・CurrentLogin が失敗
	up := &fakeUpstream{
		loginErr: errors.New("gh api user failed"),
		issues: map[string][]UpstreamIssue{
			"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", []string{"eve"}, nil)},
		},
	}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.PolicyState != "in_policy" {
		t.Fatalf("item = %+v, want policy_state=in_policy のまま（AC-33 と同じ趣旨）", item)
	}
}

// --- AC-89〜92: 反映で複数の契機が同時に起きても版は1回だけ増え、各エントリの
// after.version はどれも増やした後の同じ版 ---

// reopen（upstream_state_change）とポリシーの再判定（policy_state_change）が
// 同時に起きる場合。
func TestIngest_Policy_ReopenAndPolicyChangeInOneReflectionShareVersion(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	body := "body"
	ch := mustCreateChallenge(t, s, "bound")
	in := validBindingInput("o/r#1")
	in.Fingerprint = Fingerprint(body)
	in.UpstreamState = upstreamStateClosed // reopen の契機
	in.PolicyState = policyStateInPolicy
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest() error = %v", err)
	}
	before, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		// assignee が eve（selfAssignees に無い）: ポリシーの再判定の契機。
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", []string{"eve"}, nil)},
	}}
	runIngestForTest(t, s, []SourceEntry{src}, up)

	after, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	// AC-91: 2つの契機が同時に起きても版は1だけ増える。
	if after.Version != before.Version+1 {
		t.Fatalf("Version = %d, want %d (AC-91)", after.Version, before.Version+1)
	}
	if after.SourceBinding.UpstreamState != "open" || after.SourceBinding.PolicyState != "out_of_policy" {
		t.Fatalf("SourceBinding = %+v, want upstream_state=open policy_state=out_of_policy", after.SourceBinding)
	}

	// 1件目は mustCreateChallenge の create。2・3件目が今回の反映
	// （upstream_state_change → policy_state_change の順。§作業ログと版）。
	activities, err := s.ListActivities(context.Background(), &after.ID)
	if err != nil || len(activities) != 3 {
		t.Fatalf("ListActivities() = %d, %v, want 3", len(activities), err)
	}
	if activities[1].Action != "upstream_state_change" || activities[2].Action != "policy_state_change" {
		t.Fatalf("actions = [%q, %q], want [upstream_state_change, policy_state_change]（順序。§作業ログと版）", activities[1].Action, activities[2].Action)
	}
	// AC-89・AC-90: before・after がそれぞれの状態を持つ（before に version は無い）。
	assertStateChangeActivity(t, activities[1], "upstream_state_change", "upstream_state", "closed", "open", after.Version)
	assertStateChangeActivity(t, activities[2], "policy_state_change", "policy_state", "in_policy", "out_of_policy", after.Version)
	// AC-92: 2つのエントリの after.version はどちらも増やした後の同じ版。
	for _, a := range activities[1:] {
		var afterField map[string]any
		if err := json.Unmarshal(a.After, &afterField); err != nil {
			t.Fatalf("unmarshal after: %v", err)
		}
		gotVersion, ok := afterField["version"].(float64)
		if !ok || int(gotVersion) != after.Version {
			t.Errorf("activity %q after.version = %#v, want %d (AC-92)", a.Action, afterField["version"], after.Version)
		}
	}
}

// 人間記入欄の更新（ingest_update）と reopen（upstream_state_change）が同時に
// 起きる場合も、版は1回だけ増え、記録の順は ingest_update → upstream_state_change。
func TestIngest_Policy_HumanFieldUpdateAndReopenInOneReflectionShareVersion(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := mustCreateChallenge(t, s, "bound")
	in := validBindingInput("o/r#1")
	in.Fingerprint = Fingerprint("old body")
	in.UpstreamState = upstreamStateMissing // reopen の契機
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest() error = %v", err)
	}
	before, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", "new body", "carol", nil, nil)},
	}}
	runIngestForTest(t, s, []SourceEntry{src}, up)

	after, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Version != before.Version+1 {
		t.Fatalf("Version = %d, want %d (AC-91)", after.Version, before.Version+1)
	}
	if after.Description != "new body" || after.SourceBinding.UpstreamState != "open" {
		t.Fatalf("after = %+v, want description=new body upstream_state=open", after)
	}

	activities, err := s.ListActivities(context.Background(), &after.ID)
	if err != nil || len(activities) != 3 {
		t.Fatalf("ListActivities() = %d, %v, want 3", len(activities), err)
	}
	if activities[1].Action != "ingest_update" || activities[2].Action != "upstream_state_change" {
		t.Fatalf("actions = [%q, %q], want [ingest_update, upstream_state_change]", activities[1].Action, activities[2].Action)
	}
	for _, a := range activities[1:] {
		var afterField map[string]any
		if err := json.Unmarshal(a.After, &afterField); err != nil {
			t.Fatalf("unmarshal after: %v", err)
		}
		gotVersion, ok := afterField["version"].(float64)
		if !ok || int(gotVersion) != after.Version {
			t.Errorf("activity %q after.version = %#v, want %d (AC-92)", a.Action, afterField["version"], after.Version)
		}
	}
}

// assertStateChangeActivity は upstream_state_change・policy_state_change の
// エントリが、before に {key: wantBefore}（version なし）、after に
// {key: wantAfter, version: wantVersion} だけを持つことを確かめる（AC-89・AC-90・
// §作業ログ「before・after はそれぞれの状態。after は version を含む」）。
func assertStateChangeActivity(t *testing.T, a Activity, wantAction, key, wantBefore, wantAfter string, wantVersion int) {
	t.Helper()
	if a.Action != wantAction {
		t.Fatalf("action = %q, want %q", a.Action, wantAction)
	}
	var before, after map[string]any
	if err := json.Unmarshal(a.Before, &before); err != nil {
		t.Fatalf("%s: unmarshal before: %v", wantAction, err)
	}
	if err := json.Unmarshal(a.After, &after); err != nil {
		t.Fatalf("%s: unmarshal after: %v", wantAction, err)
	}
	if len(before) != 1 || before[key] != wantBefore {
		t.Errorf("%s before = %v, want {%q: %q} only", wantAction, before, key, wantBefore)
	}
	v, ok := after["version"].(float64)
	if len(after) != 2 || after[key] != wantAfter || !ok || int(v) != wantVersion {
		t.Errorf("%s after = %v, want {%q: %q, version: %d}", wantAction, after, key, wantAfter, wantVersion)
	}
}

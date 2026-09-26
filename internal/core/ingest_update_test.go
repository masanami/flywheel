package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// このファイルは #57（冪等な更新）の受入基準（AC-49〜AC-61・AC-88・AC-92 相当）を
// 検証する。#56 が作った骨格（ingest.go の ingestOneIssue の「対応が既にある」
// 分岐・並行した取り込みに先を越された分岐）に、fingerprint の3分岐と
// ingest_update の記録を足す。上流の close の確かめ・ポリシーの状態の再判定
// （#58）は対象外。

// --- AC-49: fingerprint が一致すれば何も変えない ---

func TestIngest_UnchangedFingerprintLeavesEverythingUntouched(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	body := "same body\nline 2"
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t1", body, "carol", nil, nil)},
	}}
	first := runIngestForTest(t, s, []SourceEntry{src}, up)
	created := findIngestItem(first, "o/r#1")
	if created == nil || created.Result != IngestOutcomeCreated {
		t.Fatalf("first ingest = %+v, want created", created)
	}

	beforeDetail, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	beforeActivities, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}

	// AC-49: 上流が変わらないまま2回目を実行しても unchanged。
	second := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(second, "o/r#1")
	if item == nil || item.Result != IngestOutcomeUnchanged {
		t.Fatalf("second ingest item = %+v, want unchanged (AC-49)", item)
	}
	if item.ChallengeID != created.ChallengeID {
		t.Errorf("ChallengeID = %q, want %q (新しい課題を作らない)", item.ChallengeID, created.ChallengeID)
	}
	// unchanged 等の要素にも UpstreamState・PolicyState を読み直した値で入れる。
	if item.UpstreamState != "open" || item.PolicyState != "in_policy" {
		t.Errorf("item = %+v, want upstream_state=open policy_state=in_policy", item)
	}

	afterDetail, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if afterDetail.Challenge != beforeDetail.Challenge {
		t.Errorf("Challenge changed = %+v, want unchanged %+v (AC-49)", afterDetail.Challenge, beforeDetail.Challenge)
	}
	if *afterDetail.SourceBinding != *beforeDetail.SourceBinding {
		t.Errorf("SourceBinding changed = %+v, want unchanged %+v (AC-49)", *afterDetail.SourceBinding, *beforeDetail.SourceBinding)
	}
	afterActivities, err := s.ListActivities(context.Background(), nil)
	if err != nil || len(afterActivities) != len(beforeActivities) {
		t.Fatalf("activities = %d, %v, want %d unchanged (AC-49)", len(afterActivities), err, len(beforeActivities))
	}
}

// AC-56: タイトルだけが変わった Issue は、fingerprint の入力が本文だけなので
// 変わらない（unchanged のまま）。
func TestIngest_TitleOnlyChangeIsUnchanged(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	body := "stable body"
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "old title", body, "carol", nil, nil)},
	}}
	created := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
	if created == nil || created.Result != IngestOutcomeCreated {
		t.Fatalf("first ingest = %+v, want created", created)
	}

	up.issues["o/r"][0] = issueFixture("o/r#1", "https://github.com/o/r/issues/1", "new title", body, "carol", nil, nil)
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeUnchanged {
		t.Fatalf("item = %+v, want unchanged (AC-56)", item)
	}

	detail, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.Title != "old title" {
		t.Errorf("Title = %q, want unchanged %q (AC-56)", detail.Title, "old title")
	}
}

// --- AC-50〜AC-53: fingerprint が違えば人間記入欄と fingerprint を置き換えるが、
// 起票者・完了条件・優先度・状態・計画は変えない ---

func TestIngest_UpdateReplacesTitleDescriptionUrgencyFingerprintButKeepsClassificationAndPlan(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	urgencyLabels := map[string]string{"priority:high": "高", "priority:low": "低"}
	src := SourceEntry{ID: "s", Type: SourceTypeGitHubIssue, Repos: []string{"o/r"}, SelfAssignees: []string{"masanami"}, UrgencyLabels: urgencyLabels}
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "old title", "old body", "carol", nil, []string{"priority:low"})},
	}}
	created := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
	if created == nil || created.Result != IngestOutcomeCreated {
		t.Fatalf("first ingest = %+v, want created", created)
	}

	// 分類済で計画を持つ課題にする（起票者・完了条件・優先度・状態・計画が
	// 変わらないことを検証するための前提）。完了条件は空でない値にしておく
	// （self-review 指摘: 空文字列のままだと、UPDATE が done_criteria を
	// 空文字列で上書きする退行が起きてもこのテストは検出できない）。
	doneCriteria := "done when merged"
	if _, err := s.EditChallenge(context.Background(), ChannelCLI, created.ChallengeID, EditInput{DoneCriteria: &doneCriteria}); err != nil {
		t.Fatalf("EditChallenge() error = %v", err)
	}
	if _, err := s.ClassifyChallenge(context.Background(), ChannelCLI, created.ChallengeID, ClassifyInput{Priority: "P1"}); err != nil {
		t.Fatalf("ClassifyChallenge() error = %v", err)
	}
	cid := mustParseChallengeID(t, created.ChallengeID)
	insertPlanRow(t, s, cid, 1, "plan body")

	beforeDetail, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	beforeFP := beforeDetail.SourceBinding.Fingerprint

	up.issues["o/r"][0] = issueFixture("o/r#1", "https://github.com/o/r/issues/1", "new title", "new body", "dave", nil, []string{"priority:high"})
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeUpdated {
		t.Fatalf("item = %+v, want updated", item)
	}

	afterDetail, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	// AC-50〜AC-52: タイトル・説明・緊急度が上流の値に置き換わる。
	if afterDetail.Title != "new title" {
		t.Errorf("Title = %q, want %q (AC-50)", afterDetail.Title, "new title")
	}
	if afterDetail.Description != "new body" {
		t.Errorf("Description = %q, want %q (AC-51)", afterDetail.Description, "new body")
	}
	if afterDetail.Urgency == nil || *afterDetail.Urgency != UrgencyHigh {
		t.Errorf("Urgency = %v, want %v (AC-52)", afterDetail.Urgency, UrgencyHigh)
	}
	// AC-53: 起票者・完了条件・優先度・状態・計画は変わらない。
	if afterDetail.Reporter != "carol" {
		t.Errorf("Reporter = %q, want unchanged %q (AC-53)", afterDetail.Reporter, "carol")
	}
	if afterDetail.DoneCriteria != beforeDetail.DoneCriteria {
		t.Errorf("DoneCriteria = %q, want unchanged %q (AC-53)", afterDetail.DoneCriteria, beforeDetail.DoneCriteria)
	}
	if afterDetail.Priority == nil || *afterDetail.Priority != PriorityP1 {
		t.Errorf("Priority = %v, want unchanged %v (AC-53)", afterDetail.Priority, PriorityP1)
	}
	if afterDetail.Status != beforeDetail.Status {
		t.Errorf("Status = %q, want unchanged %q (AC-53)", afterDetail.Status, beforeDetail.Status)
	}
	if len(afterDetail.Plans) != 1 || afterDetail.Plans[0] != beforeDetail.Plans[0] {
		t.Errorf("Plans = %+v, want unchanged %+v (AC-53)", afterDetail.Plans, beforeDetail.Plans)
	}
	// AC-92: 課題の版を1増やす。
	if afterDetail.Version != beforeDetail.Version+1 {
		t.Errorf("Version = %d, want %d (AC-92)", afterDetail.Version, beforeDetail.Version+1)
	}
	// fingerprint が新しい本文の値になる（AC-45 相当・fingerprint の更新）。
	wantFP := Fingerprint("new body")
	if afterDetail.SourceBinding.Fingerprint != wantFP || afterDetail.SourceBinding.Fingerprint == beforeFP {
		t.Errorf("Fingerprint = %q, want %q (!= %q)", afterDetail.SourceBinding.Fingerprint, wantFP, beforeFP)
	}
}

// --- AC-54: 承認・保留・不可逆操作の記録は変わらない ---

func TestIngest_UpdateKeepsApprovalsHoldsAndOperations(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "old title", "old body", "carol", nil, nil)},
	}}
	created := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
	if created == nil || created.Result != IngestOutcomeCreated {
		t.Fatalf("first ingest = %+v, want created", created)
	}
	cid := mustParseChallengeID(t, created.ChallengeID)
	insertFullChallengeFixtures(t, s, cid)

	beforeDetail, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}

	up.issues["o/r"][0] = issueFixture("o/r#1", "https://github.com/o/r/issues/1", "new title", "new body", "carol", nil, nil)
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeUpdated {
		t.Fatalf("item = %+v, want updated", item)
	}

	afterDetail, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if len(afterDetail.Approvals) != len(beforeDetail.Approvals) || afterDetail.Approvals[0] != beforeDetail.Approvals[0] {
		t.Errorf("Approvals = %+v, want unchanged %+v (AC-54)", afterDetail.Approvals, beforeDetail.Approvals)
	}
	// Hold は Answer・AnsweredAt・AnsweredBy がポインタなので、再読込のたびに別の
	// アドレスが割り当たる。値で比較する（アドレスの比較は常に不一致になり誤検出
	// するため）。
	if len(afterDetail.Holds) != len(beforeDetail.Holds) || !holdEqual(afterDetail.Holds[0], beforeDetail.Holds[0]) {
		t.Errorf("Holds = %+v, want unchanged %+v (AC-54)", afterDetail.Holds, beforeDetail.Holds)
	}
	if len(afterDetail.Operations) != len(beforeDetail.Operations) || afterDetail.Operations[0] != beforeDetail.Operations[0] {
		t.Errorf("Operations = %+v, want unchanged %+v (AC-54)", afterDetail.Operations, beforeDetail.Operations)
	}
}

// holdEqual は Hold を値で比較する（Answer・AnsweredAt・AnsweredBy はポインタ
// フィールドなので、GetChallenge の再読込のたびに別のアドレスへ割り当たり、
// == によるポインタ比較は常に不一致になる）。
func holdEqual(a, b Hold) bool {
	if a.Question != b.Question || a.FromStatus != b.FromStatus || !a.RaisedAt.Equal(b.RaisedAt) {
		return false
	}
	if (a.Answer == nil) != (b.Answer == nil) || (a.Answer != nil && *a.Answer != *b.Answer) {
		return false
	}
	if (a.AnsweredBy == nil) != (b.AnsweredBy == nil) || (a.AnsweredBy != nil && *a.AnsweredBy != *b.AnsweredBy) {
		return false
	}
	if (a.AnsweredAt == nil) != (b.AnsweredAt == nil) || (a.AnsweredAt != nil && !a.AnsweredAt.Equal(*b.AnsweredAt)) {
		return false
	}
	return true
}

// --- AC-57: 記録された fingerprint の版が2以外なら fail-closed ---

func TestIngest_UnknownFingerprintVersionLeavesEverythingUntouched(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := mustCreateChallenge(t, s, "legacy fp")
	in := validBindingInput("o/r#1")
	in.Fingerprint = "1:aaaaaaaaaaaa" // 版1（現行の古い版）。
	if _, err := createSourceBindingForTest(s, ch.ID, s.currentTime(), in); err != nil {
		t.Fatalf("insertSourceBinding() error = %v", err)
	}
	beforeActivities, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "new title", "new body", "carol", nil, nil)},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeFingerprintUnknownVersion {
		t.Fatalf("item = %+v, want fingerprint_unknown_version (AC-57)", item)
	}

	detail, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.Title != "legacy fp" {
		t.Errorf("Title = %q, want unchanged (AC-57)", detail.Title)
	}
	if detail.SourceBinding.Fingerprint != "1:aaaaaaaaaaaa" {
		t.Errorf("Fingerprint = %q, want unchanged (AC-57)", detail.SourceBinding.Fingerprint)
	}
	if detail.Version != 1 {
		t.Errorf("Version = %d, want unchanged 1 (AC-57 は版を増やさない)", detail.Version)
	}
	afterActivities, err := s.ListActivities(context.Background(), nil)
	if err != nil || len(afterActivities) != len(beforeActivities) {
		t.Fatalf("activities = %d, %v, want %d unchanged (AC-57)", len(afterActivities), err, len(beforeActivities))
	}
}

// --- AC-58: 完了した課題は最優先でスキップする ---

func TestIngest_DoneChallengeIsSkipped(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	insertChallengeWithID(t, s, 1, "done challenge", StatusDone)
	in := validBindingInput("o/r#1")
	if _, err := createSourceBindingForTest(s, "C-1", s.currentTime(), in); err != nil {
		t.Fatalf("insertSourceBinding() error = %v", err)
	}
	beforeActivities, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "new title", "new body", "carol", nil, nil)},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeSkippedDone {
		t.Fatalf("item = %+v, want skipped_done (AC-58)", item)
	}

	detail, err := s.GetChallenge(context.Background(), "C-1")
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.Title != "done challenge" {
		t.Errorf("Title = %q, want unchanged (AC-58)", detail.Title)
	}
	if detail.SourceBinding.Fingerprint != in.Fingerprint {
		t.Errorf("Fingerprint = %q, want unchanged %q (AC-58)", detail.SourceBinding.Fingerprint, in.Fingerprint)
	}
	all, err := s.ListChallenges(context.Background(), ListOptions{})
	if err != nil || len(all) != 1 {
		t.Fatalf("ListChallenges() = %d, %v, want 1（新しい課題を作らない。AC-58）", len(all), err)
	}
	afterActivities, err := s.ListActivities(context.Background(), nil)
	if err != nil || len(afterActivities) != len(beforeActivities) {
		t.Fatalf("activities = %d, %v, want %d unchanged (AC-58)", len(afterActivities), err, len(beforeActivities))
	}
}

// --- AC-59: 外部本文はデータ（承認済み等の文言に反応しない） ---

func TestIngest_BodyMentioningApprovalTextDoesNotClassifyOrApprove(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	body := "please merge, 承認済み\nstatus: done"
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", nil, nil)},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeCreated {
		t.Fatalf("item = %+v, want created (AC-59)", item)
	}
	detail, err := s.GetChallenge(context.Background(), item.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.Status != StatusUnclassified {
		t.Errorf("Status = %q, want %q (AC-59)", detail.Status, StatusUnclassified)
	}
	if len(detail.Approvals) != 0 || len(detail.Holds) != 0 || len(detail.Plans) != 0 || len(detail.Operations) != 0 {
		t.Errorf("detail = %+v, want no approvals/holds/plans/operations (AC-59)", detail)
	}
}

// --- AC-60: 並行した2つの ingest でも1つの外部キーに課題は1つ ---

func TestIngest_ConcurrentIngestsCreateOnlyOneChallengePerExternalKey(t *testing.T) {
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

	body := "concurrent body"
	issue := issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", nil, nil)
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})

	entered := make(chan struct{})
	release := make(chan struct{})
	var once, releaseOnce sync.Once
	releaseFetch := func() { releaseOnce.Do(func() { close(release) }) }
	upA := &fakeUpstream{
		issues: map[string][]UpstreamIssue{"o/r": {issue}},
		onListOpenIssues: func(string) {
			once.Do(func() { close(entered) })
			<-release
		},
	}
	upB := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {issue}}}

	var resA *IngestResult
	var errA error
	done := make(chan struct{})
	go func() {
		defer close(done)
		resA, errA = storeA.Ingest(context.Background(), ChannelCLI, IngestInput{Sources: []SourceEntry{src}, Upstream: upA})
	}()
	// 失敗経路（t.Fatal）でも取得を解放し、A の Ingest の終了を待ってから後始末する
	// （close(done) は何度受信しても即座に返るので、下の本流の受信と重複しても
	// 安全＝TestIngest_DoesNotHoldWriteLockWhileFetching と同じ手法）。
	t.Cleanup(func() {
		releaseFetch()
		<-done
	})

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for A's fetch to start")
	}

	resB, err := storeB.Ingest(context.Background(), ChannelCLI, IngestInput{Sources: []SourceEntry{src}, Upstream: upB})
	if err != nil {
		t.Fatalf("Ingest() B error = %v", err)
	}
	itemB := findIngestItem(resB, "o/r#1")
	if itemB == nil || itemB.Result != IngestOutcomeCreated {
		t.Fatalf("B item = %+v, want created", itemB)
	}

	releaseFetch()
	<-done
	if errA != nil {
		t.Fatalf("Ingest() A error = %v", errA)
	}
	itemA := findIngestItem(resA, "o/r#1")
	if itemA == nil {
		t.Fatalf("A item = nil, want non-nil (対応が既にある Issue として反映される)")
	}
	// AC-60: A は B に先を越されるので、created ではなく冪等な更新（fingerprint が
	// 同じ本文なので unchanged）に回る。
	if itemA.Result != IngestOutcomeUnchanged {
		t.Fatalf("A item = %+v, want unchanged (先を越された分岐は冪等な更新へ回る)", itemA)
	}

	all, err := storeA.ListChallenges(context.Background(), ListOptions{})
	if err != nil || len(all) != 1 {
		t.Fatalf("ListChallenges() = %d, %v, want 1（1つの外部キーに課題は1つ。AC-60）", len(all), err)
	}
}

// --- AC-61: 1 Issue の反映中の失敗は他の Issue に影響しない（更新の分岐） ---
//
// self-review 指摘で訂正: 旧版は 2 件（o/r#1・o/r#2）で先頭に失敗を注入して
// おり、「それより前に反映した Issue は残る」を実際には検証できていなかった
// （失敗より前に反映された Issue が1件も無いため、全件ロールバックする実装でも
// このテストは通ってしまう）。#56 の TestIngest_FailureOnOneIssueDoesNotRollBackEarlierIssues
// と同じく、3 件の真ん中（2 件目）に失敗を注入し、失敗より前（1件目）・
// 失敗そのもの（2件目）・失敗より後（3件目）の 3 つを区別して検証する。
func TestIngest_FailureDuringUpdateDoesNotLeaveChangesOrAffectOtherIssues(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {
			issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t1", "b1", "carol", nil, nil),
			issueFixture("o/r#2", "https://github.com/o/r/issues/2", "t2", "b2", "carol", nil, nil),
			issueFixture("o/r#3", "https://github.com/o/r/issues/3", "t3", "b3", "carol", nil, nil),
		},
	}}
	first := runIngestForTest(t, s, []SourceEntry{src}, up)
	item1 := findIngestItem(first, "o/r#1")
	item2 := findIngestItem(first, "o/r#2")
	item3 := findIngestItem(first, "o/r#3")
	if item1 == nil || item1.Result != IngestOutcomeCreated ||
		item2 == nil || item2.Result != IngestOutcomeCreated ||
		item3 == nil || item3.Result != IngestOutcomeCreated {
		t.Fatalf("first ingest = %+v, %+v, %+v, want all created", item1, item2, item3)
	}
	before1, err := s.GetChallenge(context.Background(), item1.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	before3, err := s.GetChallenge(context.Background(), item3.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}

	// 2 件目（真ん中）の ingest_update だけを狙い撃ちで失敗させる。
	failInjected := errors.New("injected ingest_update failure")
	callCount := 0
	s.insertActivity = func(tx *sql.Tx, row activityRow) error {
		if row.Action != "ingest_update" {
			return defaultInsertActivity(tx, row)
		}
		callCount++
		if callCount == 2 {
			return failInjected
		}
		return defaultInsertActivity(tx, row)
	}

	up.issues["o/r"][0] = issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t1-new", "b1-new", "carol", nil, nil)
	up.issues["o/r"][1] = issueFixture("o/r#2", "https://github.com/o/r/issues/2", "t2-new", "b2-new", "carol", nil, nil)
	up.issues["o/r"][2] = issueFixture("o/r#3", "https://github.com/o/r/issues/3", "t3-new", "b3-new", "carol", nil, nil)
	res := runIngestForTest(t, s, []SourceEntry{src}, up)

	got1 := findIngestItem(res, "o/r#1")
	if got1 == nil || got1.Result != IngestOutcomeUpdated {
		t.Fatalf("o/r#1 = %+v, want updated (失敗より前に反映した Issue。AC-61)", got1)
	}
	got2 := findIngestItem(res, "o/r#2")
	if got2 == nil || got2.Result != IngestOutcomeFailed {
		t.Fatalf("o/r#2 = %+v, want failed (注入した失敗。AC-61)", got2)
	}
	got3 := findIngestItem(res, "o/r#3")
	if got3 == nil || got3.Result != IngestOutcomeUpdated {
		t.Fatalf("o/r#3 = %+v, want updated (失敗した Issue の後も続行する。AC-61)", got3)
	}

	// 失敗より前（1件目）は残る（ロールバックされない）。
	detail1, err := s.GetChallenge(context.Background(), item1.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail1.Title != "t1-new" || detail1.Description != "b1-new" {
		t.Errorf("challenge 1 = %+v, want updated (失敗より前に反映した Issue は残る。AC-61)", detail1.Challenge)
	}
	if detail1.Version != before1.Version+1 {
		t.Errorf("challenge 1 version = %d, want %d (AC-61)", detail1.Version, before1.Version+1)
	}

	// 失敗そのもの（2件目）は課題・対応・作業ログのどれも残らない。
	detail2, err := s.GetChallenge(context.Background(), item2.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail2.Title != "t2" || detail2.Description != "b2" {
		t.Errorf("challenge 2 = %+v, want unchanged (失敗したトランザクションは何も残さない。AC-61)", detail2.Challenge)
	}
	if detail2.SourceBinding.Fingerprint != Fingerprint("b2") {
		t.Errorf("binding 2 fingerprint = %q, want unchanged %q (AC-61)", detail2.SourceBinding.Fingerprint, Fingerprint("b2"))
	}

	// 失敗より後（3件目）も続けて反映される。
	detail3, err := s.GetChallenge(context.Background(), item3.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail3.Title != "t3-new" || detail3.Description != "b3-new" {
		t.Errorf("challenge 3 = %+v, want updated (失敗した Issue の後も続行する。AC-61)", detail3.Challenge)
	}
	if detail3.Version != before3.Version+1 {
		t.Errorf("challenge 3 version = %d, want %d (AC-61)", detail3.Version, before3.Version+1)
	}
}

// --- AC-88: ingest_update の作業ログ ---

func TestIngest_RecordsIngestUpdateActivityWithChangedFieldsAndVersion(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "old title", "old body", "carol", nil, nil)},
	}}
	created := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
	if created == nil || created.Result != IngestOutcomeCreated {
		t.Fatalf("first ingest = %+v, want created", created)
	}

	// タイトルは変えず、本文だけ変える（変わった項目だけを記録することの検証）。
	up.issues["o/r"][0] = issueFixture("o/r#1", "https://github.com/o/r/issues/1", "old title", "new body", "carol", nil, nil)
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeUpdated {
		t.Fatalf("item = %+v, want updated", item)
	}

	activities, err := s.ListActivities(context.Background(), &item.ChallengeID)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	// 1件目は ingest_create、2件目が今回の ingest_update。
	if len(activities) != 2 {
		t.Fatalf("len(activities) = %d, want 2", len(activities))
	}
	a := activities[1]
	if a.Action != "ingest_update" || a.Actor != "alice" || a.Channel != "cli" || a.Verification != "none" {
		t.Fatalf("activity = %+v, want ingest_update by alice/cli/none (AC-88)", a)
	}
	var before, after map[string]any
	if err := json.Unmarshal(a.Before, &before); err != nil {
		t.Fatalf("unmarshal before: %v", err)
	}
	if err := json.Unmarshal(a.After, &after); err != nil {
		t.Fatalf("unmarshal after: %v", err)
	}
	// タイトルは変わっていないので before/after に載らない。
	if _, ok := before["title"]; ok {
		t.Errorf("before has title, want absent (変わっていない項目は載せない)")
	}
	wantBefore := map[string]any{"description": "old body", "fingerprint": Fingerprint("old body")}
	wantAfterNoVersion := map[string]any{"description": "new body", "fingerprint": Fingerprint("new body")}
	if len(before) != len(wantBefore) {
		t.Errorf("before = %+v, want exactly %+v", before, wantBefore)
	}
	for k, v := range wantBefore {
		if got, ok := before[k]; !ok || got != v {
			t.Errorf("before[%q] = %#v, want %#v", k, got, v)
		}
	}
	if _, ok := before["version"]; ok {
		t.Errorf("before has version, want none (M1 H14)")
	}
	if len(after) != len(wantAfterNoVersion)+1 {
		t.Errorf("after = %+v, want exactly %+v plus version", after, wantAfterNoVersion)
	}
	for k, v := range wantAfterNoVersion {
		if got, ok := after[k]; !ok || got != v {
			t.Errorf("after[%q] = %#v, want %#v", k, got, v)
		}
	}
	gotVersion, ok := after["version"].(float64)
	if !ok || int(gotVersion) != 2 {
		t.Errorf("after[version] = %#v, want 2", after["version"])
	}
}

// 上流の緊急度ラベルが外れると、緊急度は未設定に戻り、ingest_update の after に
// null で載る（未設定の列は null で載せる＝M1 の H16）。
func TestIngest_UpdateClearsUrgencyWhenLabelRemoved(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := SourceEntry{ID: "s", Type: SourceTypeGitHubIssue, Repos: []string{"o/r"}, SelfAssignees: []string{"masanami"}, UrgencyLabels: map[string]string{"priority:high": "高"}}
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", "old body", "carol", nil, []string{"priority:high"})},
	}}
	created := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
	if created == nil || created.Result != IngestOutcomeCreated {
		t.Fatalf("first ingest = %+v, want created", created)
	}

	up.issues["o/r"][0] = issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", "new body", "carol", nil, nil)
	item := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
	if item == nil || item.Result != IngestOutcomeUpdated {
		t.Fatalf("item = %+v, want updated", item)
	}
	detail, err := s.GetChallenge(context.Background(), item.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.Urgency != nil {
		t.Errorf("Urgency = %v, want nil", *detail.Urgency)
	}

	activities, err := s.ListActivities(context.Background(), &item.ChallengeID)
	if err != nil || len(activities) != 2 {
		t.Fatalf("ListActivities() = %d, %v, want 2", len(activities), err)
	}
	var before, after map[string]any
	if err := json.Unmarshal(activities[1].Before, &before); err != nil {
		t.Fatalf("unmarshal before: %v", err)
	}
	if err := json.Unmarshal(activities[1].After, &after); err != nil {
		t.Fatalf("unmarshal after: %v", err)
	}
	if got := before["urgency"]; got != "高" {
		t.Errorf("before[urgency] = %#v, want 高", got)
	}
	if got, ok := after["urgency"]; !ok || got != nil {
		t.Errorf("after[urgency] = %#v (present=%v), want null", got, ok)
	}
}

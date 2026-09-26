package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

// このファイルは #58 の「上流の close の検出」（AC-67〜AC-74）を検証する。
// 一覧の取得に成功したリポジトリで、①で読んだ対応のうち open の一覧に現れ
// なかったもの（upstream_state が open・未完了）を GetIssue で1件ずつ確かめる
// （checkUnseenOpenBindings・confirmOpenListAbsence）。ポリシーの再判定・reopen
// （reconcileBoundIssue 側）は ingest_policy_test.go が担当する。

// bindOpenChallenge は externalKey に対応する未分類の課題を作り、
// upstream_state=open・policy_state=in_policy の source_binding を結び付ける
// （fingerprint などは validBindingInput の既定値）。
func bindOpenChallenge(t *testing.T, s *Store, title, externalKey string) *Challenge {
	t.Helper()
	ch := mustCreateChallenge(t, s, title)
	in := validBindingInput(externalKey)
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest(%s) error = %v", externalKey, err)
	}
	return ch
}

// --- AC-67: closed ---

func TestIngest_CloseDetection_MissingFromListAndGetIssueClosedMarksClosed(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindOpenChallenge(t, s, "bound", "o/r#1")

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{
		issues: map[string][]UpstreamIssue{"o/r": {}}, // 一覧には現れない
		getIssue: map[string]UpstreamIssue{
			"o/r#1": {ExternalKey: "o/r#1", State: "closed"},
		},
	}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.UpstreamState != "closed" {
		t.Fatalf("item = %+v, want upstream_state=closed (AC-67)", item)
	}
	if item.Result != IngestOutcomeUnchanged {
		t.Errorf("Result = %q, want unchanged（result は人間記入欄についての結果）", item.Result)
	}

	sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || sb == nil || sb.UpstreamState != upstreamStateClosed {
		t.Fatalf("source_binding = %+v, %v, want upstream_state=closed", sb, err)
	}
	if len(up.getIssueCalls) != 1 || up.getIssueCalls[0] != "o/r#1" {
		t.Fatalf("GetIssue calls = %v, want [\"o/r#1\"]", up.getIssueCalls)
	}
	assertCloseCheckRecorded(t, s, ch, "closed")
}

// assertCloseCheckRecorded は close の確かめで upstream_state が open から want に
// 変わったとき、課題の版が 1 だけ増え、最後の作業ログが upstream_state_change
// （before open・after want と増やした後の版。AC-89・AC-91）であることを確かめる。
func assertCloseCheckRecorded(t *testing.T, s *Store, ch *Challenge, want string) {
	t.Helper()
	detail, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.Version != ch.Version+1 {
		t.Fatalf("Version = %d, want %d（版は 1 だけ増える。AC-91）", detail.Version, ch.Version+1)
	}
	activities, err := s.ListActivities(context.Background(), &detail.ID)
	if err != nil || len(activities) == 0 {
		t.Fatalf("ListActivities() = %v, %v", activities, err)
	}
	assertStateChangeActivity(t, activities[len(activities)-1], "upstream_state_change", "upstream_state", "open", want, detail.Version)
}

// --- AC-68: missing（404・410 の両方） ---
// 404・410 を ErrUpstreamIssueNotFound へ写すのは adapter の責務で、その写像は
// internal/adapters/github のテスト（410 は TestGetIssue_410_HTTP2_…）が担保する。
// core の層ではどちらも同じ sentinel になるので、ここでは sentinel から missing へ
// の写像を確かめる（2 つのサブテストは受入基準との対応を示すための名前）。

func TestIngest_CloseDetection_NotFoundStatusesMarkMissing(t *testing.T) {
	for _, status := range []string{"404", "410"} {
		t.Run(status, func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			ch := bindOpenChallenge(t, s, "bound", "o/r#1")

			src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
			up := &fakeUpstream{
				issues:      map[string][]UpstreamIssue{"o/r": {}},
				getIssueErr: map[string]error{"o/r#1": ErrUpstreamIssueNotFound},
			}
			res := runIngestForTest(t, s, []SourceEntry{src}, up)
			item := findIngestItem(res, "o/r#1")
			if item == nil || item.UpstreamState != "missing" {
				t.Fatalf("item = %+v, want upstream_state=missing (AC-68)", item)
			}

			sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
			if err != nil || sb == nil || sb.UpstreamState != upstreamStateMissing {
				t.Fatalf("source_binding = %+v, %v, want upstream_state=missing", sb, err)
			}
			assertCloseCheckRecorded(t, s, ch, "missing")
		})
	}
}

// --- AC-69: 404・410 以外の失敗は変えず failed ---

func TestIngest_CloseDetection_OtherFailureLeavesOpenAndMarksFailed(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindOpenChallenge(t, s, "bound", "o/r#1")

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{
		issues:      map[string][]UpstreamIssue{"o/r": {}},
		getIssueErr: map[string]error{"o/r#1": errors.New("network error")},
	}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeFailed || item.UpstreamState != "open" {
		t.Fatalf("item = %+v, want result=failed upstream_state=open (AC-69)", item)
	}
	if item.Error == nil || *item.Error == "" {
		t.Errorf("Error = %v, want non-empty failure summary", item.Error)
	}

	sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || sb == nil || sb.UpstreamState != upstreamStateOpen {
		t.Fatalf("source_binding = %+v, %v, want upstream_state unchanged (open)", sb, err)
	}
	detail, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil || detail.Version != 1 {
		t.Fatalf("Version = %v, %v, want 1（変更していない）", detail, err)
	}
}

// --- AC-70: GetIssue が open を返せば変わらない ---

func TestIngest_CloseDetection_StillOpenLeavesUnchanged(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindOpenChallenge(t, s, "bound", "o/r#1")

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{
		issues:   map[string][]UpstreamIssue{"o/r": {}},
		getIssue: map[string]UpstreamIssue{"o/r#1": {ExternalKey: "o/r#1", State: "open"}},
	}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.UpstreamState != "open" || item.Result != IngestOutcomeUnchanged {
		t.Fatalf("item = %+v, want upstream_state=open result=unchanged (AC-70)", item)
	}

	detail, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil || detail.Version != 1 {
		t.Fatalf("Version = %v, %v, want 1（変更していない）", detail, err)
	}
}

// --- AC-71: closed／missing が open の一覧に現れたら reopen する ---

func TestIngest_CloseDetection_ReappearingInOpenListReopens(t *testing.T) {
	for _, initial := range []upstreamState{upstreamStateClosed, upstreamStateMissing} {
		t.Run(string(initial), func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			ch := mustCreateChallenge(t, s, "bound")
			body := "same body"
			in := validBindingInput("o/r#1")
			in.Fingerprint = Fingerprint(body)
			in.UpstreamState = initial
			if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
				t.Fatalf("createSourceBindingForTest() error = %v", err)
			}

			src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
			up := &fakeUpstream{issues: map[string][]UpstreamIssue{
				"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", nil, nil)},
			}}
			res := runIngestForTest(t, s, []SourceEntry{src}, up)
			item := findIngestItem(res, "o/r#1")
			if item == nil || item.UpstreamState != "open" {
				t.Fatalf("item = %+v, want upstream_state=open (AC-71, from %s)", item, initial)
			}

			sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
			if err != nil || sb == nil || sb.UpstreamState != upstreamStateOpen {
				t.Fatalf("source_binding = %+v, %v, want upstream_state=open", sb, err)
			}
			detail, err := s.GetChallenge(context.Background(), ch.ID)
			if err != nil || detail.Version != 2 {
				t.Fatalf("Version = %v, %v, want 2 (reopen は版を1増やす)", detail, err)
			}

			// 1件目は mustCreateChallenge の create（CreateChallenge）、2件目が
			// 今回の reopen（upstream_state_change）。
			activities, err := s.ListActivities(context.Background(), &item.ChallengeID)
			if err != nil || len(activities) != 2 {
				t.Fatalf("ListActivities() = %d, %v, want 2", len(activities), err)
			}
			if activities[1].Action != "upstream_state_change" {
				t.Errorf("Action = %q, want upstream_state_change", activities[1].Action)
			}
		})
	}
}

// --- AC-73: 上流の状態が変わっても課題の状態は変わらない（未分類・着手中・
// 完了確認待ちのそれぞれ） ---

func TestIngest_CloseDetection_DoesNotChangeChallengeStatus(t *testing.T) {
	statuses := []Status{StatusUnclassified, StatusInProgress, StatusAwaitingCompletionApproval}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			ch := bindOpenChallenge(t, s, "bound", "o/r#1")
			cid := mustParseChallengeID(t, ch.ID)
			setChallengeStatus(t, s, cid, status)

			src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
			up := &fakeUpstream{
				issues:   map[string][]UpstreamIssue{"o/r": {}},
				getIssue: map[string]UpstreamIssue{"o/r#1": {ExternalKey: "o/r#1", State: "closed"}},
			}
			runIngestForTest(t, s, []SourceEntry{src}, up)

			detail, err := s.GetChallenge(context.Background(), ch.ID)
			if err != nil || detail.Status != status {
				t.Fatalf("Status = %v, %v, want unchanged %q (AC-73)", detail, err, status)
			}
			if detail.SourceBinding == nil || detail.SourceBinding.UpstreamState != "closed" {
				t.Errorf("SourceBinding = %+v, want upstream_state=closed", detail.SourceBinding)
			}
		})
	}
}

// --- AC-74: 完了した課題の Issue が一覧から消えても GetIssue を呼ばず、対応の記録も変わらない ---

func TestIngest_CloseDetection_DoneChallengeSkipsGetIssueAndBindingUnchanged(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindOpenChallenge(t, s, "bound", "o/r#1")
	cid := mustParseChallengeID(t, ch.ID)
	setChallengeStatus(t, s, cid, StatusDone)

	before, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("getSourceBindingByChallengeID() error = %v", err)
	}

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {}}} // GetIssue の固定応答は与えない
	res := runIngestForTest(t, s, []SourceEntry{src}, up)

	if item := findIngestItem(res, "o/r#1"); item != nil {
		t.Errorf("item = %+v, want no item emitted for a done challenge", item)
	}
	if len(up.getIssueCalls) != 0 {
		t.Fatalf("GetIssue calls = %v, want none (AC-74)", up.getIssueCalls)
	}

	after, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || *after != *before {
		t.Fatalf("source_binding changed = %+v, want unchanged %+v (AC-74)", after, before)
	}
}

// --- 宣言から外したリポジトリ・取り込み元の対応は取得も変更もしない ---

func TestIngest_CloseDetection_UndeclaredRepoIsNotChecked(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	bindOpenChallenge(t, s, "bound", "o/other#1")

	// この回の宣言は "o/r" だけで、"o/other" は含まれない。
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{"o/r": {}}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)

	if item := findIngestItem(res, "o/other#1"); item != nil {
		t.Errorf("item = %+v, want none (宣言に無いリポジトリ)", item)
	}
	if len(up.getIssueCalls) != 0 {
		t.Fatalf("GetIssue calls = %v, want none", up.getIssueCalls)
	}
}

// --- 一覧の取得に失敗したリポジトリの課題は変わらない ---

func TestIngest_CloseDetection_ListFetchFailureSkipsCloseCheck(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	bindOpenChallenge(t, s, "bound", "o/r#1")

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{errs: map[string]error{"o/r": errors.New("list failed")}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)

	repo := findRepoResult(res, "s", "o/r")
	if repo == nil || repo.Error == nil {
		t.Fatalf("repo = %+v, want error != nil", repo)
	}
	if len(repo.Items) != 0 {
		t.Errorf("Items = %+v, want empty (close の確かめをしない)", repo.Items)
	}
	if len(up.getIssueCalls) != 0 {
		t.Fatalf("GetIssue calls = %v, want none", up.getIssueCalls)
	}
}

// --- 候補は Issue 番号の昇順で確かめる ---

func TestIngest_CloseDetection_CandidatesAreCheckedInAscendingIssueNumberOrder(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	bindOpenChallenge(t, s, "third", "o/r#30")
	bindOpenChallenge(t, s, "first", "o/r#3")
	bindOpenChallenge(t, s, "second", "o/r#12")

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{
		issues: map[string][]UpstreamIssue{"o/r": {}},
		getIssue: map[string]UpstreamIssue{
			"o/r#3":  {ExternalKey: "o/r#3", State: "open"},
			"o/r#12": {ExternalKey: "o/r#12", State: "open"},
			"o/r#30": {ExternalKey: "o/r#30", State: "open"},
		},
	}
	runIngestForTest(t, s, []SourceEntry{src}, up)

	want := []string{"o/r#3", "o/r#12", "o/r#30"}
	if len(up.getIssueCalls) != len(want) {
		t.Fatalf("GetIssue calls = %v, want %v", up.getIssueCalls, want)
	}
	for i, k := range want {
		if up.getIssueCalls[i] != k {
			t.Errorf("GetIssue calls[%d] = %q, want %q (%v)", i, up.getIssueCalls[i], k, up.getIssueCalls)
		}
	}
}

// --- repo の一致は external_key の repo 部分と大文字小文字を無視して比べる ---

func TestIngest_CloseDetection_RepoMatchIsCaseInsensitive(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	// external_key は API の正規の表記（大文字を含む）で記録されている想定。
	bindOpenChallenge(t, s, "bound", "Owner/Repo#1")

	// 宣言（.flywheel/sources.json）の表記は小文字。
	src := excludeOthersSource("s", []string{"owner/repo"}, []string{"masanami"})
	up := &fakeUpstream{
		issues:   map[string][]UpstreamIssue{"owner/repo": {}},
		getIssue: map[string]UpstreamIssue{"Owner/Repo#1": {ExternalKey: "Owner/Repo#1", State: "closed"}},
	}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "Owner/Repo#1")
	if item == nil || item.UpstreamState != "closed" {
		t.Fatalf("item = %+v, want upstream_state=closed", item)
	}
}

// --- AC-72: 上流の状態が closed の完了した課題の Issue が open の一覧に現れても、
// 対応の記録は変わらない（reopen もポリシーの再判定もしない。版・作業ログも
// 変えない）。assignee を selfAssignees に無い人にして、ポリシーの再判定の契機も
// 同時に作る。 ---

func TestIngest_CloseDetection_DoneClosedChallengeReappearingIsNotReopened(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := mustCreateChallenge(t, s, "bound")
	in := validBindingInput("o/r#1")
	in.UpstreamState = upstreamStateClosed
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest() error = %v", err)
	}
	setChallengeStatus(t, s, mustParseChallengeID(t, ch.ID), StatusDone)

	ctx := context.Background()
	beforeSB, err := s.getSourceBindingByChallengeID(ctx, ch.ID)
	if err != nil {
		t.Fatalf("getSourceBindingByChallengeID() error = %v", err)
	}
	beforeCh, err := s.GetChallenge(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	beforeActs, err := s.ListActivities(ctx, &beforeCh.ID)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", "changed body", "carol", []string{"eve"}, nil)},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)

	if item := findIngestItem(res, "o/r#1"); item == nil || item.Result != IngestOutcomeSkippedDone || item.UpstreamState != "closed" || item.PolicyState != "in_policy" {
		t.Fatalf("item = %+v, want skipped_done with upstream_state=closed policy_state=in_policy (AC-72)", item)
	}
	afterSB, err := s.getSourceBindingByChallengeID(ctx, ch.ID)
	if err != nil || *afterSB != *beforeSB {
		t.Fatalf("source_binding = %+v, %v, want unchanged %+v (AC-72)", afterSB, err, beforeSB)
	}
	afterCh, err := s.GetChallenge(ctx, ch.ID)
	if err != nil || afterCh.Version != beforeCh.Version {
		t.Fatalf("Version = %+v, %v, want unchanged %d", afterCh, err, beforeCh.Version)
	}
	afterActs, err := s.ListActivities(ctx, &afterCh.ID)
	if err != nil || len(afterActs) != len(beforeActs) {
		t.Fatalf("activities = %d, %v, want unchanged %d", len(afterActs), err, len(beforeActs))
	}
	if len(up.getIssueCalls) != 0 {
		t.Errorf("GetIssue calls = %v, want none", up.getIssueCalls)
	}
}

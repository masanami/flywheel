package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// このファイルは #72（親要件チケット #51 §上流の更新の観測と既読）の
// 受入基準 AC-111〜AC-133・AC-146〜150・AC-160 を検証する（core 側）。
// mark-read（AC-134〜145）は markread_test.go、スキーマ版 2→3 の既定値
// （AC-151〜159）は schema_v3_observation_test.go が担当する。

// observedIssue は issueFixture に観測値（comments・updated_at）を足す。
func observedIssue(externalKey, url, title, body, reporter string, assignees, labels []string, comments int, updatedAt string) UpstreamIssue {
	issue := issueFixture(externalKey, url, title, body, reporter, assignees, labels)
	issue.Comments = comments
	issue.UpdatedAt = updatedAt
	return issue
}

// --- AC-111〜114: 取り込みで作られた課題の観測値・読んだ時点の値 ---

func TestIngest_CreatedChallenge_HasObservationFields(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", "b", "carol", nil, nil, 3, "2026-09-25T08:00:00Z")},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeCreated {
		t.Fatalf("item = %+v, want created", item)
	}
	detail, err := s.GetChallenge(context.Background(), item.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	sb := detail.SourceBinding
	if sb == nil {
		t.Fatalf("SourceBinding = nil, want non-nil")
	}
	// AC-111: comments_count は Issue の comments。
	if sb.CommentsCount != 3 {
		t.Errorf("CommentsCount = %d, want 3 (AC-111)", sb.CommentsCount)
	}
	// AC-112: upstream_updated_at は Issue の updated_at。
	if sb.UpstreamUpdatedAt != "2026-09-25T08:00:00Z" {
		t.Errorf("UpstreamUpdatedAt = %q, want %q (AC-112)", sb.UpstreamUpdatedAt, "2026-09-25T08:00:00Z")
	}
	// AC-113: read_comments_count は 0。
	if sb.ReadCommentsCount != 0 {
		t.Errorf("ReadCommentsCount = %d, want 0 (AC-113)", sb.ReadCommentsCount)
	}
	// AC-114: read_upstream_updated_at は作成時の upstream_updated_at と同じ値。
	if sb.ReadUpstreamUpdatedAt != sb.UpstreamUpdatedAt {
		t.Errorf("ReadUpstreamUpdatedAt = %q, want %q (AC-114)", sb.ReadUpstreamUpdatedAt, sb.UpstreamUpdatedAt)
	}
	// item（ingest --json の要素）にも反映後の観測値が入る（AC-131・AC-132）。
	if item.CommentsCount != 3 || item.UpstreamUpdatedAt != "2026-09-25T08:00:00Z" {
		t.Errorf("item = %+v, want comments_count=3 upstream_updated_at=2026-09-25T08:00:00Z", item)
	}
}

// --- AC-115・AC-116: 作成直後の未読の種類 ---

func TestIngest_CreatedChallenge_UnreadKindsRightAfterCreation(t *testing.T) {
	cases := []struct {
		name          string
		comments      int
		wantCommented bool
	}{
		{"コメント1件以上", 1, true},
		{"コメント0件", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
			up := &fakeUpstream{issues: map[string][]UpstreamIssue{
				"o/r": {observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", "b", "carol", nil, nil, tc.comments, "2026-09-25T08:00:00Z")},
			}}
			res := runIngestForTest(t, s, []SourceEntry{src}, up)
			item := findIngestItem(res, "o/r#1")
			if item == nil {
				t.Fatalf("item = nil, want created")
			}
			ov, err := s.GetOverview(context.Background())
			if err != nil {
				t.Fatalf("GetOverview() error = %v", err)
			}
			d := findDiscrepancy(ov.Discrepancies, item.ChallengeID)
			hasCommented := d != nil && kindsContain(d.Kinds, DiscrepancyKindUpstreamCommented)
			if hasCommented != tc.wantCommented {
				t.Errorf("upstream_commented present = %v, want %v (AC-115. discrepancy=%+v)", hasCommented, tc.wantCommented, d)
			}
			// AC-116: 作成直後は upstream_updated を含まない（読んだ時点の更新日時
			// を作成時の観測値と同じにするため）。
			if d != nil && kindsContain(d.Kinds, DiscrepancyKindUpstreamUpdated) {
				t.Errorf("discrepancy = %+v, must not contain upstream_updated right after creation (AC-116)", d)
			}
			// item.Unread（nil も可。JSON への変換時に [] へ正規化するのは
			// internal/cli の責務）は同じ判定を共有するので、上と同じ結果になる。
			hasCommentedInItem := kindsContain(item.Unread, DiscrepancyKindUpstreamCommented)
			if hasCommentedInItem != tc.wantCommented {
				t.Errorf("item.Unread = %v, want upstream_commented=%v (AC-115・AC-133)", item.Unread, tc.wantCommented)
			}
		})
	}
}

func kindsContain(kinds []DiscrepancyKind, want DiscrepancyKind) bool {
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

// --- AC-117〜120: 本文が変わらず観測値だけが変わった取り込み ---

func TestIngest_ObservationOnlyChange_UpdatesValuesAndStaysUnchanged(t *testing.T) {
	t.Run("コメント数だけ増える", func(t *testing.T) {
		s := newStoreForTest(t)
		fixedActor(t, "alice")
		src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
		body := "stable body"
		up := &fakeUpstream{issues: map[string][]UpstreamIssue{
			"o/r": {observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", nil, nil, 1, "2026-09-25T08:00:00Z")},
		}}
		created := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
		if created == nil || created.Result != IngestOutcomeCreated {
			t.Fatalf("first ingest = %+v, want created", created)
		}

		up.issues["o/r"][0] = observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", nil, nil, 4, "2026-09-25T08:00:00Z")
		item := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
		if item == nil {
			t.Fatalf("item = nil")
		}
		// AC-117: comments_count が新しい値になる。
		if item.CommentsCount != 4 {
			t.Errorf("CommentsCount = %d, want 4 (AC-117)", item.CommentsCount)
		}
		// AC-118: result は unchanged。
		if item.Result != IngestOutcomeUnchanged {
			t.Errorf("Result = %q, want unchanged (AC-118)", item.Result)
		}
	})

	t.Run("updated_at だけ変わる", func(t *testing.T) {
		s := newStoreForTest(t)
		fixedActor(t, "alice")
		src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
		body := "stable body"
		up := &fakeUpstream{issues: map[string][]UpstreamIssue{
			"o/r": {observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", nil, nil, 1, "2026-09-25T08:00:00Z")},
		}}
		created := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
		if created == nil || created.Result != IngestOutcomeCreated {
			t.Fatalf("first ingest = %+v, want created", created)
		}

		up.issues["o/r"][0] = observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", nil, nil, 1, "2026-09-26T09:00:00Z")
		item := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
		if item == nil {
			t.Fatalf("item = nil")
		}
		// AC-119: upstream_updated_at が新しい値になる。
		if item.UpstreamUpdatedAt != "2026-09-26T09:00:00Z" {
			t.Errorf("UpstreamUpdatedAt = %q, want %q (AC-119)", item.UpstreamUpdatedAt, "2026-09-26T09:00:00Z")
		}
		// AC-120: result は unchanged。
		if item.Result != IngestOutcomeUnchanged {
			t.Errorf("Result = %q, want unchanged (AC-120)", item.Result)
		}
	})
}

// --- AC-121: 観測値だけが変わっても人間記入欄と fingerprint は変わらない ---

func TestIngest_ObservationOnlyChange_DoesNotTouchHumanFieldsOrFingerprint(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	body := "stable body"
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {observedIssue("o/r#1", "https://github.com/o/r/issues/1", "old title", body, "carol", nil, nil, 1, "2026-09-25T08:00:00Z")},
	}}
	created := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
	if created == nil {
		t.Fatalf("first ingest = nil")
	}
	before, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}

	up.issues["o/r"][0] = observedIssue("o/r#1", "https://github.com/o/r/issues/1", "old title", body, "carol", nil, nil, 5, "2026-09-30T00:00:00Z")
	runIngestForTest(t, s, []SourceEntry{src}, up)

	after, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Title != before.Title || after.Description != before.Description {
		t.Errorf("human fields changed: before=%+v after=%+v (AC-121)", before.Challenge, after.Challenge)
	}
	if after.SourceBinding.Fingerprint != before.SourceBinding.Fingerprint {
		t.Errorf("Fingerprint changed = %q, want unchanged %q (AC-121)", after.SourceBinding.Fingerprint, before.SourceBinding.Fingerprint)
	}
}

// --- AC-122・AC-123: 観測値の置き換えは読んだ時点の値を変えない ---

func TestIngest_ObservationReplace_DoesNotChangeReadValues(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	body := "stable body"
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", nil, nil, 1, "2026-09-25T08:00:00Z")},
	}}
	created := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
	if created == nil {
		t.Fatalf("first ingest = nil")
	}

	// 1回目の取り込みの直後: 読んだ時点のコメント数は 0、更新日時は作成時の値。
	sb1, err := s.getSourceBindingByChallengeID(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("getSourceBindingByChallengeID() error = %v", err)
	}
	wantReadComments, wantReadUpdatedAt := sb1.ReadCommentsCount, sb1.ReadUpstreamUpdatedAt

	// コメント数・updated_at が増えた状態で 2 回、取り込みを繰り返す。
	for i, c := range []int{3, 6} {
		up.issues["o/r"][0] = observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", nil, nil, c, "2026-09-2"+string(rune('6'+i))+"T00:00:00Z")
		runIngestForTest(t, s, []SourceEntry{src}, up)
	}

	sb2, err := s.getSourceBindingByChallengeID(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("getSourceBindingByChallengeID() error = %v", err)
	}
	// AC-122: read_comments_count は変わらない。
	if sb2.ReadCommentsCount != wantReadComments {
		t.Errorf("ReadCommentsCount = %d, want unchanged %d (AC-122)", sb2.ReadCommentsCount, wantReadComments)
	}
	// AC-123: read_upstream_updated_at は変わらない。
	if sb2.ReadUpstreamUpdatedAt != wantReadUpdatedAt {
		t.Errorf("ReadUpstreamUpdatedAt = %q, want unchanged %q (AC-123)", sb2.ReadUpstreamUpdatedAt, wantReadUpdatedAt)
	}
	// 未読の更新（upstream_commented・upstream_updated）は、上書きが繰り返されても
	// 消えずに残る（QH10 の帰結）。
	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	d := findDiscrepancy(ov.Discrepancies, created.ChallengeID)
	if d == nil || !kindsContain(d.Kinds, DiscrepancyKindUpstreamCommented) || !kindsContain(d.Kinds, DiscrepancyKindUpstreamUpdated) {
		t.Errorf("discrepancy = %+v, want both upstream_commented and upstream_updated to remain", d)
	}
}

// --- AC-124・AC-125: unreadKinds の判定条件（純粋関数の直接検証） ---

func TestUnreadKinds_CommentsCountThreshold(t *testing.T) {
	cases := []struct {
		name           string
		comments, read int
		wantCommented  bool
	}{
		{"大きい", 3, 1, true},
		{"等しい", 2, 2, false},
		{"小さい（コメントの削除）", 1, 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unreadKinds(tc.comments, tc.read, "same", "same")
			has := false
			for _, k := range got {
				if k == DiscrepancyKindUpstreamCommented {
					has = true
				}
			}
			if has != tc.wantCommented {
				t.Errorf("unreadKinds(%d, %d, ...) commented = %v, want %v (AC-124)", tc.comments, tc.read, has, tc.wantCommented)
			}
		})
	}
}

func TestUnreadKinds_UpdatedAtDiffers(t *testing.T) {
	cases := []struct {
		name        string
		upstream    string
		read        string
		wantUpdated bool
	}{
		{"同じ", "2026-09-25T00:00:00Z", "2026-09-25T00:00:00Z", false},
		{"違う", "2026-09-26T00:00:00Z", "2026-09-25T00:00:00Z", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unreadKinds(0, 0, tc.upstream, tc.read)
			has := false
			for _, k := range got {
				if k == DiscrepancyKindUpstreamUpdated {
					has = true
				}
			}
			if has != tc.wantUpdated {
				t.Errorf("unreadKinds(..., %q, %q) updated = %v, want %v (AC-125)", tc.upstream, tc.read, has, tc.wantUpdated)
			}
		})
	}
}

// --- AC-126・AC-127: 完了した課題の観測値は変わらない ---

func TestIngest_DoneChallenge_ObservationUnchanged(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", "b", "carol", nil, nil, 1, "2026-09-25T08:00:00Z")},
	}}
	created := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
	if created == nil {
		t.Fatalf("first ingest = nil")
	}
	cid := mustParseChallengeID(t, created.ChallengeID)
	setChallengeStatus(t, s, cid, StatusDone)

	before, err := s.getSourceBindingByChallengeID(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("getSourceBindingByChallengeID() error = %v", err)
	}

	// AC-126・AC-127: コメント数・updated_at の両方が変わった Issue でも、
	// 完了した課題の対応の記録は変わらない。
	up.issues["o/r"][0] = observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", "b", "carol", nil, nil, 9, "2026-12-31T00:00:00Z")
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeSkippedDone {
		t.Fatalf("item = %+v, want skipped_done", item)
	}

	after, err := s.getSourceBindingByChallengeID(context.Background(), created.ChallengeID)
	if err != nil || *after != *before {
		t.Fatalf("source_binding changed = %+v, %v, want unchanged %+v (AC-126・AC-127)", after, err, before)
	}
}

// --- AC-128: 一覧の取得に失敗したリポジトリの課題は観測値が変わらない ---

func TestIngest_ListFetchFailure_ObservationUnchanged(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := mustCreateChallenge(t, s, "bound")
	in := validBindingInput("o/r#1")
	in.CommentsCount = 1
	in.UpstreamUpdatedAt = "2026-09-25T08:00:00Z"
	in.ReadUpstreamUpdatedAt = "2026-09-25T08:00:00Z"
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest() error = %v", err)
	}
	before, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("getSourceBindingByChallengeID() error = %v", err)
	}

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{errs: map[string]error{"o/r": errAny("list failed")}}
	runIngestForTest(t, s, []SourceEntry{src}, up)

	after, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || *after != *before {
		t.Fatalf("source_binding changed = %+v, %v, want unchanged %+v (AC-128)", after, err, before)
	}
}

// --- AC-129・AC-130: close の確かめの応答から観測値を置き換える ---

func TestIngest_CloseDetection_ClosedResponseUpdatesObservation(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindOpenChallenge(t, s, "bound", "o/r#1")

	closedIssue := observedIssue("o/r#1", "", "", "", "", nil, nil, 7, "2026-09-27T00:00:00Z")
	closedIssue.State = "closed"
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{
		issues:   map[string][]UpstreamIssue{"o/r": {}},
		getIssue: map[string]UpstreamIssue{"o/r#1": closedIssue},
	}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.UpstreamState != "closed" {
		t.Fatalf("item = %+v, want upstream_state=closed", item)
	}
	// AC-129: comments が comments_count になる。
	if item.CommentsCount != 7 {
		t.Errorf("CommentsCount = %d, want 7 (AC-129)", item.CommentsCount)
	}
	// AC-130: updated_at が upstream_updated_at になる。
	if item.UpstreamUpdatedAt != "2026-09-27T00:00:00Z" {
		t.Errorf("UpstreamUpdatedAt = %q, want %q (AC-130)", item.UpstreamUpdatedAt, "2026-09-27T00:00:00Z")
	}

	sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || sb.CommentsCount != 7 || sb.UpstreamUpdatedAt != "2026-09-27T00:00:00Z" {
		t.Fatalf("source_binding = %+v, %v, want comments_count=7 upstream_updated_at=2026-09-27T00:00:00Z", sb, err)
	}
}

// bindOpenChallengeWithObservation は bindOpenChallenge と同じだが、観測値・
// 読んだ時点の値を 0・"" 以外の値で作る（close の確かめの各分岐が観測値を
// 「変えない」ことと「初期値のまま変わらない」ことを区別して検証するため。
// self-review 指摘: 既定値のまま検証すると、観測値の更新ロジックを
// 誤って missing・failed の分岐にも適用する退行を見逃す）。
func bindOpenChallengeWithObservation(t *testing.T, s *Store, title, externalKey string, commentsCount int, upstreamUpdatedAt string) *Challenge {
	t.Helper()
	ch := mustCreateChallenge(t, s, title)
	in := validBindingInput(externalKey)
	in.CommentsCount = commentsCount
	in.UpstreamUpdatedAt = upstreamUpdatedAt
	in.ReadCommentsCount = 0
	in.ReadUpstreamUpdatedAt = upstreamUpdatedAt
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest(%s) error = %v", externalKey, err)
	}
	return ch
}

// close の確かめで GetIssue が成功し open のままだった（デフォルト分岐）場合も、
// 観測値は上流の応答の comments・updated_at で置き換わる（§上流の更新の観測と
// 既読）。self-review 指摘: この分岐だけ observation の更新テストが無く、
// computeObservationChange の呼び出しを消しても検出できなかった。
func TestIngest_CloseDetection_StillOpenResponseUpdatesObservation(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindOpenChallengeWithObservation(t, s, "bound", "o/r#1", 1, "2026-09-20T00:00:00Z")

	openIssue := observedIssue("o/r#1", "", "", "", "", nil, nil, 5, "2026-09-27T00:00:00Z")
	openIssue.State = "open"
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{
		issues:   map[string][]UpstreamIssue{"o/r": {}},
		getIssue: map[string]UpstreamIssue{"o/r#1": openIssue},
	}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.UpstreamState != "open" {
		t.Fatalf("item = %+v, want upstream_state=open (still open)", item)
	}
	if item.CommentsCount != 5 || item.UpstreamUpdatedAt != "2026-09-27T00:00:00Z" {
		t.Errorf("item = %+v, want comments_count=5 upstream_updated_at=2026-09-27T00:00:00Z", item)
	}
	sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || sb.CommentsCount != 5 || sb.UpstreamUpdatedAt != "2026-09-27T00:00:00Z" {
		t.Fatalf("source_binding = %+v, %v, want comments_count=5 upstream_updated_at=2026-09-27T00:00:00Z", sb, err)
	}
}

// close の確かめで GetIssue が 404/410（missing）になったときは、応答の本文が
// 無いので観測値を変えない（§上流の更新の観測と既読【仮定】）。self-review
// 指摘: 既存の missing のテストは観測値が既定値（0・""）のままの binding しか
// 使っておらず、「変えない」と「初期値のまま」を区別できていなかった。
func TestIngest_CloseDetection_MissingResponse_ObservationUnchanged(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindOpenChallengeWithObservation(t, s, "bound", "o/r#1", 3, "2026-09-20T00:00:00Z")

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{
		issues:      map[string][]UpstreamIssue{"o/r": {}},
		getIssueErr: map[string]error{"o/r#1": ErrUpstreamIssueNotFound},
	}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.UpstreamState != "missing" {
		t.Fatalf("item = %+v, want upstream_state=missing", item)
	}
	if item.CommentsCount != 3 || item.UpstreamUpdatedAt != "2026-09-20T00:00:00Z" {
		t.Errorf("item = %+v, want comments_count/upstream_updated_at unchanged (3 / 2026-09-20T00:00:00Z)", item)
	}
	sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || sb.CommentsCount != 3 || sb.UpstreamUpdatedAt != "2026-09-20T00:00:00Z" {
		t.Fatalf("source_binding = %+v, %v, want comments_count/upstream_updated_at unchanged (3 / 2026-09-20T00:00:00Z)", sb, err)
	}
}

// close の確かめで GetIssue が 404/410 以外で失敗したときも、観測値を変えない。
func TestIngest_CloseDetection_OtherFailure_ObservationUnchanged(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindOpenChallengeWithObservation(t, s, "bound", "o/r#1", 3, "2026-09-20T00:00:00Z")

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{
		issues:      map[string][]UpstreamIssue{"o/r": {}},
		getIssueErr: map[string]error{"o/r#1": errAny("network error")},
	}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeFailed {
		t.Fatalf("item = %+v, want result=failed", item)
	}
	if item.CommentsCount != 3 || item.UpstreamUpdatedAt != "2026-09-20T00:00:00Z" {
		t.Errorf("item = %+v, want comments_count/upstream_updated_at unchanged (3 / 2026-09-20T00:00:00Z)", item)
	}
	sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || sb.CommentsCount != 3 || sb.UpstreamUpdatedAt != "2026-09-20T00:00:00Z" {
		t.Fatalf("source_binding = %+v, %v, want comments_count/upstream_updated_at unchanged (3 / 2026-09-20T00:00:00Z)", sb, err)
	}
}

// --- AC-146・AC-147: 観測値だけが変わった取り込みは upstream_observation_change
// を記録し、版を 1 増やす ---

func TestIngest_ObservationOnlyChange_RecordsActivityAndBumpsVersion(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	body := "stable body"
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", nil, nil, 1, "2026-09-25T08:00:00Z")},
	}}
	created := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
	if created == nil {
		t.Fatalf("first ingest = nil")
	}
	before, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}

	up.issues["o/r"][0] = observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", body, "carol", nil, nil, 4, "2026-09-25T08:00:00Z")
	runIngestForTest(t, s, []SourceEntry{src}, up)

	after, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	// AC-147: 版を 1 だけ増やす。
	if after.Version != before.Version+1 {
		t.Errorf("Version = %d, want %d (AC-147)", after.Version, before.Version+1)
	}

	activities, err := s.ListActivities(context.Background(), &created.ChallengeID)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	last := activities[len(activities)-1]
	// AC-146: upstream_observation_change を記録する。
	if last.Action != "upstream_observation_change" {
		t.Fatalf("Action = %q, want upstream_observation_change (AC-146)", last.Action)
	}
	var before2, after2 map[string]any
	if err := json.Unmarshal(last.Before, &before2); err != nil {
		t.Fatalf("unmarshal before: %v", err)
	}
	if err := json.Unmarshal(last.After, &after2); err != nil {
		t.Fatalf("unmarshal after: %v", err)
	}
	if before2["comments_count"] != 1.0 || after2["comments_count"] != 4.0 {
		t.Errorf("before/after comments_count = %v/%v, want 1/4 (AC-146)", before2["comments_count"], after2["comments_count"])
	}
	gotVersion, ok := after2["version"].(float64)
	if !ok || int(gotVersion) != after.Version {
		t.Errorf("after[version] = %#v, want %d", after2["version"], after.Version)
	}
}

// --- AC-148・AC-149: 本文と観測値が同時に変わっても版は 1 だけ増え、両方の
// エントリの after.version が同じ ---

func TestIngest_BodyAndObservationChangeTogether_SingleVersionBump(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", "old body", "carol", nil, nil, 1, "2026-09-25T08:00:00Z")},
	}}
	created := findIngestItem(runIngestForTest(t, s, []SourceEntry{src}, up), "o/r#1")
	if created == nil {
		t.Fatalf("first ingest = nil")
	}
	before, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}

	up.issues["o/r"][0] = observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", "new body", "carol", nil, nil, 5, "2026-09-26T08:00:00Z")
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeUpdated {
		t.Fatalf("item = %+v, want updated", item)
	}

	after, err := s.GetChallenge(context.Background(), created.ChallengeID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	// AC-148: 版は 1 だけ増える。
	if after.Version != before.Version+1 {
		t.Errorf("Version = %d, want %d (AC-148)", after.Version, before.Version+1)
	}

	activities, err := s.ListActivities(context.Background(), &created.ChallengeID)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	last2 := activities[len(activities)-2:]
	if last2[0].Action != "ingest_update" || last2[1].Action != "upstream_observation_change" {
		t.Fatalf("actions = %q, %q, want ingest_update, upstream_observation_change", last2[0].Action, last2[1].Action)
	}
	var a1, a2 map[string]any
	if err := json.Unmarshal(last2[0].After, &a1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := json.Unmarshal(last2[1].After, &a2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// AC-149: 2 つのエントリの after.version はどちらも同じ（増やした後の版）。
	if a1["version"] != a2["version"] || int(a1["version"].(float64)) != after.Version {
		t.Errorf("after.version mismatch: ingest_update=%v upstream_observation_change=%v, want both %d (AC-149)", a1["version"], a2["version"], after.Version)
	}
}

// --- AC-160: fingerprint の版が未知でも、観測値の更新は行う ---

func TestIngest_UnknownFingerprintVersion_ObservationStillUpdates(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := mustCreateChallenge(t, s, "legacy fp")
	in := validBindingInput("o/r#1")
	in.Fingerprint = "1:aaaaaaaaaaaa"
	in.CommentsCount = 1
	in.UpstreamUpdatedAt = "2026-09-25T08:00:00Z"
	in.ReadUpstreamUpdatedAt = "2026-09-25T08:00:00Z"
	if _, err := createSourceBindingForTest(s, ch.ID, s.currentTime(), in); err != nil {
		t.Fatalf("insertSourceBinding() error = %v", err)
	}

	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {observedIssue("o/r#1", "https://github.com/o/r/issues/1", "t", "new body", "carol", nil, nil, 4, "2026-09-25T08:00:00Z")},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil || item.Result != IngestOutcomeFingerprintUnknownVersion {
		t.Fatalf("item = %+v, want fingerprint_unknown_version", item)
	}
	if item.CommentsCount != 4 {
		t.Errorf("CommentsCount = %d, want 4 (AC-160)", item.CommentsCount)
	}
	sb, err := s.getSourceBindingByChallengeID(context.Background(), ch.ID)
	if err != nil || sb.Fingerprint != "1:aaaaaaaaaaaa" {
		t.Fatalf("source_binding = %+v, %v, want fingerprint unchanged (fail-closed still holds for human fields)", sb, err)
	}
}

// errAny is a tiny helper to avoid importing errors just for a literal.
type errAny string

func (e errAny) Error() string { return string(e) }

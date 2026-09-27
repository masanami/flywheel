package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// このファイルは #72（親要件チケット #51 §上流の更新の観測と既読）の
// 受入基準 AC-134〜AC-145 を検証する（core.Store.MarkRead）。

// bindChallengeWithUnreadUpdate は、未読の更新（comments_count > read_comments_count・
// upstream_updated_at != read_upstream_updated_at）がある対応を持つ課題を作る。
func bindChallengeWithUnreadUpdate(t *testing.T, s *Store, title string) *Challenge {
	t.Helper()
	ch := mustCreateChallenge(t, s, title)
	in := validBindingInput("o/r#1")
	in.CommentsCount = 3
	in.UpstreamUpdatedAt = "2026-09-25T08:00:00Z"
	in.ReadCommentsCount = 0
	in.ReadUpstreamUpdatedAt = "2026-09-24T00:00:00Z"
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest() error = %v", err)
	}
	return ch
}

// --- AC-134・AC-135: mark-read は読んだ時点の値を現在の観測値に合わせる ---

func TestMarkRead_SetsReadValuesToCurrentObservation(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindChallengeWithUnreadUpdate(t, s, "unread")

	res, err := s.MarkRead(context.Background(), ChannelCLI, ch.ID)
	if err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}
	if !res.Changed {
		t.Fatalf("Changed = false, want true")
	}
	// AC-134: read_comments_count が comments_count と同じ値になる。
	if res.SourceBinding.ReadCommentsCount != res.SourceBinding.CommentsCount {
		t.Errorf("ReadCommentsCount = %d, want %d (AC-134)", res.SourceBinding.ReadCommentsCount, res.SourceBinding.CommentsCount)
	}
	// AC-135: read_upstream_updated_at が upstream_updated_at と同じ値になる。
	if res.SourceBinding.ReadUpstreamUpdatedAt != res.SourceBinding.UpstreamUpdatedAt {
		t.Errorf("ReadUpstreamUpdatedAt = %q, want %q (AC-135)", res.SourceBinding.ReadUpstreamUpdatedAt, res.SourceBinding.UpstreamUpdatedAt)
	}

	// 未読の更新が消える（§食い違いの表示）。
	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if d := findDiscrepancy(ov.Discrepancies, ch.ID); d != nil {
		t.Errorf("discrepancy = %+v, want none after mark-read (AC-136)", d)
	}
}

// --- AC-139・AC-140: 作業ログに upstream_read が残る ---

func TestMarkRead_RecordsUpstreamReadActivity(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindChallengeWithUnreadUpdate(t, s, "unread")

	if _, err := s.MarkRead(context.Background(), ChannelCLI, ch.ID); err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}

	activities, err := s.ListActivities(context.Background(), &ch.ID)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	last := activities[len(activities)-1]
	// AC-139: action=upstream_read・actor=OS のログインユーザー名・経路=cli・
	// 本人確認の方式=none。
	if last.Action != "upstream_read" || last.Actor != "alice" || last.Channel != "cli" || last.Verification != "none" {
		t.Fatalf("activity = %+v, want upstream_read by alice/cli/none (AC-139)", last)
	}
	var before, after map[string]any
	if err := json.Unmarshal(last.Before, &before); err != nil {
		t.Fatalf("unmarshal before: %v", err)
	}
	if err := json.Unmarshal(last.After, &after); err != nil {
		t.Fatalf("unmarshal after: %v", err)
	}
	// AC-140: before・after は変わった read_comments_count・read_upstream_updated_at を持つ。
	if before["read_comments_count"] != 0.0 || after["read_comments_count"] != 3.0 {
		t.Errorf("before/after read_comments_count = %v/%v, want 0/3 (AC-140)", before["read_comments_count"], after["read_comments_count"])
	}
	if before["read_upstream_updated_at"] != "2026-09-24T00:00:00Z" || after["read_upstream_updated_at"] != "2026-09-25T08:00:00Z" {
		t.Errorf("before/after read_upstream_updated_at = %v/%v, want 2026-09-24T00:00:00Z/2026-09-25T08:00:00Z (AC-140)", before["read_upstream_updated_at"], after["read_upstream_updated_at"])
	}
}

// --- AC-141: mark-read で課題の版が 1 増える ---

func TestMarkRead_BumpsVersionByOne(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindChallengeWithUnreadUpdate(t, s, "unread")

	before, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if _, err := s.MarkRead(context.Background(), ChannelCLI, ch.ID); err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}
	after, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Version != before.Version+1 {
		t.Errorf("Version = %d, want %d (AC-141)", after.Version, before.Version+1)
	}
}

// --- AC-142: 読んだ時点の値が観測値とすべて同じ課題への mark-read は何も変えずに成功する ---

func TestMarkRead_NoUnreadUpdate_NoOpSuccess(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := mustCreateChallenge(t, s, "already read")
	in := validBindingInput("o/r#1")
	in.CommentsCount = 2
	in.ReadCommentsCount = 2
	in.UpstreamUpdatedAt = "2026-09-25T00:00:00Z"
	in.ReadUpstreamUpdatedAt = "2026-09-25T00:00:00Z"
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest() error = %v", err)
	}
	beforeChallenge, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	beforeActivities, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}

	res, err := s.MarkRead(context.Background(), ChannelCLI, ch.ID)
	if err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}
	if res.Changed {
		t.Errorf("Changed = true, want false (AC-142)")
	}

	afterChallenge, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if afterChallenge.Challenge != beforeChallenge.Challenge {
		t.Errorf("challenge changed = %+v, want unchanged %+v (AC-142)", afterChallenge.Challenge, beforeChallenge.Challenge)
	}
	if *afterChallenge.SourceBinding != *beforeChallenge.SourceBinding {
		t.Errorf("source_binding changed = %+v, want unchanged %+v (AC-142)", *afterChallenge.SourceBinding, *beforeChallenge.SourceBinding)
	}
	afterActivities, err := s.ListActivities(context.Background(), nil)
	if err != nil || len(afterActivities) != len(beforeActivities) {
		t.Fatalf("activities = %d, %v, want %d unchanged (AC-142)", len(afterActivities), err, len(beforeActivities))
	}
}

// AC-134・AC-135 と AC-142 の境界（2026-09-27 オーナー決定・PR #74）:
// コメントの削除で comments_count が read_comments_count を下回っただけ
// （更新日時は同じ）の課題は、未読の更新（upstream_commented）は無いが、
// 読んだ時点の値が観測値と違う。mark-read は AC-134・AC-135 を優先して
// 観測値へそろえ、changed=true・upstream_read・版 +1 の通常の既読と同じに
// 扱う（読んだ時点の件数が大きいまま残ると、その後に足されたコメントに
// upstream_commented が出ないため）。
func TestMarkRead_CommentCountDecreasedOnly_AlignsReadValuesToObservation(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := mustCreateChallenge(t, s, "comment deleted")
	in := validBindingInput("o/r#1")
	in.CommentsCount = 1     // コメントが削除され件数が減った。
	in.ReadCommentsCount = 3 // 削除前に読んだ時点の件数の方が大きい。
	in.UpstreamUpdatedAt = "2026-09-25T00:00:00Z"
	in.ReadUpstreamUpdatedAt = "2026-09-25T00:00:00Z"
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest() error = %v", err)
	}
	// 前提: 未読の更新は無い（削除では upstream_commented を出さない）。
	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if d := findDiscrepancy(ov.Discrepancies, ch.ID); d != nil {
		t.Fatalf("precondition: discrepancy = %+v, want none", d)
	}
	beforeChallenge, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}

	res, err := s.MarkRead(context.Background(), ChannelCLI, ch.ID)
	if err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}
	if !res.Changed {
		t.Errorf("Changed = false, want true (読んだ時点の値が観測値と違えばそろえる)")
	}
	if res.SourceBinding.ReadCommentsCount != 1 {
		t.Errorf("ReadCommentsCount = %d, want 1 (AC-134)", res.SourceBinding.ReadCommentsCount)
	}
	if res.SourceBinding.ReadUpstreamUpdatedAt != "2026-09-25T00:00:00Z" {
		t.Errorf("ReadUpstreamUpdatedAt = %q, want unchanged 2026-09-25T00:00:00Z", res.SourceBinding.ReadUpstreamUpdatedAt)
	}

	afterChallenge, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if afterChallenge.SourceBinding.ReadCommentsCount != 1 {
		t.Errorf("stored ReadCommentsCount = %d, want 1", afterChallenge.SourceBinding.ReadCommentsCount)
	}
	if afterChallenge.Version != beforeChallenge.Version+1 {
		t.Errorf("Version = %d, want %d (AC-141 と同じ扱い)", afterChallenge.Version, beforeChallenge.Version+1)
	}

	activities, err := s.ListActivities(context.Background(), &ch.ID)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	last := activities[len(activities)-1]
	if last.Action != "upstream_read" || last.Actor != "alice" || last.Channel != "cli" || last.Verification != "none" {
		t.Fatalf("activity = %+v, want upstream_read by alice/cli/none", last)
	}
	var before, after map[string]any
	if err := json.Unmarshal(last.Before, &before); err != nil {
		t.Fatalf("unmarshal before: %v", err)
	}
	if err := json.Unmarshal(last.After, &after); err != nil {
		t.Fatalf("unmarshal after: %v", err)
	}
	// 変わった項目だけを載せる（read_upstream_updated_at は変わっていない）。
	if before["read_comments_count"] != 3.0 || after["read_comments_count"] != 1.0 {
		t.Errorf("before/after read_comments_count = %v/%v, want 3/1", before["read_comments_count"], after["read_comments_count"])
	}
	if _, ok := after["read_upstream_updated_at"]; ok {
		t.Errorf("after has read_upstream_updated_at = %v, want absent (unchanged)", after["read_upstream_updated_at"])
	}
	if after["version"] != float64(beforeChallenge.Version+1) {
		t.Errorf("after.version = %v, want %d", after["version"], beforeChallenge.Version+1)
	}

	// そろえた後に足されたコメントは upstream_commented として出る（決定の理由）。
	newComments := 2
	if _, _, err := updateSourceBindingForTest(s, ch.ID, time.Now(), updateSourceBindingInput{CommentsCount: &newComments}); err != nil {
		t.Fatalf("updateSourceBindingForTest() error = %v", err)
	}
	ov, err = s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	d := findDiscrepancy(ov.Discrepancies, ch.ID)
	if d == nil || len(d.Kinds) != 1 || d.Kinds[0] != DiscrepancyKindUpstreamCommented {
		t.Errorf("discrepancy after a new comment = %+v, want [upstream_commented]", d)
	}
}

// 読んだ時点の更新日時だけが観測値と違う課題（コメント数は同じ）への mark-read は、
// 更新日時だけをそろえ、変わった read_upstream_updated_at だけを作業ログに載せる
// （AC-134・AC-135。判定がコメント数だけの比較に退行しないことの検証）。
func TestMarkRead_UpdatedAtDiffersOnly_AlignsReadUpdatedAt(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := mustCreateChallenge(t, s, "labels changed")
	in := validBindingInput("o/r#1")
	in.CommentsCount = 2
	in.ReadCommentsCount = 2
	in.UpstreamUpdatedAt = "2026-09-26T00:00:00Z"
	in.ReadUpstreamUpdatedAt = "2026-09-25T00:00:00Z"
	if _, err := createSourceBindingForTest(s, ch.ID, time.Now(), in); err != nil {
		t.Fatalf("createSourceBindingForTest() error = %v", err)
	}

	res, err := s.MarkRead(context.Background(), ChannelCLI, ch.ID)
	if err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}
	if !res.Changed {
		t.Fatalf("Changed = false, want true")
	}
	if res.SourceBinding.ReadUpstreamUpdatedAt != "2026-09-26T00:00:00Z" || res.SourceBinding.ReadCommentsCount != 2 {
		t.Errorf("read values = %d/%q, want 2/2026-09-26T00:00:00Z", res.SourceBinding.ReadCommentsCount, res.SourceBinding.ReadUpstreamUpdatedAt)
	}
	activities, err := s.ListActivities(context.Background(), &ch.ID)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	last := activities[len(activities)-1]
	if last.Action != "upstream_read" {
		t.Fatalf("last action = %q, want upstream_read", last.Action)
	}
	var after map[string]any
	if err := json.Unmarshal(last.After, &after); err != nil {
		t.Fatalf("unmarshal after: %v", err)
	}
	if _, ok := after["read_comments_count"]; ok {
		t.Errorf("after has read_comments_count = %v, want absent (unchanged)", after["read_comments_count"])
	}
	if after["read_upstream_updated_at"] != "2026-09-26T00:00:00Z" {
		t.Errorf("after.read_upstream_updated_at = %v, want 2026-09-26T00:00:00Z", after["read_upstream_updated_at"])
	}
}

// --- AC-143: 対応の無い課題への mark-read は validation_failed ---

func TestMarkRead_NoBinding_ValidationFailed(t *testing.T) {
	s := newStoreForTest(t)
	ch := mustCreateChallenge(t, s, "no binding")

	_, err := s.MarkRead(context.Background(), ChannelCLI, ch.ID)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("MarkRead() error = %v, want ErrValidation (AC-143)", err)
	}
}

// --- AC-144: 完了した課題への mark-read は terminal_state ---

func TestMarkRead_DoneChallenge_TerminalState(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindChallengeWithUnreadUpdate(t, s, "done")
	cid := mustParseChallengeID(t, ch.ID)
	setChallengeStatus(t, s, cid, StatusDone)

	_, err := s.MarkRead(context.Background(), ChannelCLI, ch.ID)
	if !errors.Is(err, ErrTerminalState) {
		t.Fatalf("MarkRead() error = %v, want ErrTerminalState (AC-144)", err)
	}
}

// --- AC-145: 無い ID への mark-read は not_found ---

func TestMarkRead_UnknownID_NotFound(t *testing.T) {
	s := newStoreForTest(t)
	for _, id := range []string{"C-999", "not-an-id"} {
		if _, err := s.MarkRead(context.Background(), ChannelCLI, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("MarkRead(%q) error = %v, want ErrNotFound (AC-145)", id, err)
		}
	}
}

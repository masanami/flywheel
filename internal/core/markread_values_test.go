package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// このファイルは #85（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §J2 計画・決定 M3P14）が M2 の `mark-read` の core の API に足す「読んだ時点の
// 値を渡す形」（Store.markReadWithValues）を検証する。CLI の `mark-read`
// （ストアの観測値でそろえる）は変えない。

func TestMarkReadWithValues_SetsGivenValuesNotStoreObservation(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	// ストアの観測値は (コメント 3 件・2026-09-25T08:00:00Z)。J2 の直前の取得は
	// それより古い (2 件・2026-09-24T12:00:00Z) を得ていた、という状況。
	ch := bindChallengeWithUnreadUpdate(t, s, "given values")

	res, err := s.markReadWithValues(context.Background(), ChannelInvoker, ch.ID, markReadOptions{
		Values: &readValues{CommentsCount: 2, UpstreamUpdatedAt: "2026-09-24T12:00:00Z"},
	})
	if err != nil {
		t.Fatalf("markReadWithValues() error = %v", err)
	}
	if !res.Changed {
		t.Fatalf("Changed = false, want true")
	}
	if res.SourceBinding.ReadCommentsCount != 2 || res.SourceBinding.ReadUpstreamUpdatedAt != "2026-09-24T12:00:00Z" {
		t.Errorf("read values = (%d, %q), want (2, 2026-09-24T12:00:00Z): the given values, not the observation",
			res.SourceBinding.ReadCommentsCount, res.SourceBinding.ReadUpstreamUpdatedAt)
	}
	// 観測値は変えない。
	if res.SourceBinding.CommentsCount != 3 || res.SourceBinding.UpstreamUpdatedAt != "2026-09-25T08:00:00Z" {
		t.Errorf("observation = (%d, %q), want it unchanged (3, 2026-09-25T08:00:00Z)",
			res.SourceBinding.CommentsCount, res.SourceBinding.UpstreamUpdatedAt)
	}
}

func TestMarkReadWithValues_ActivityCarriesChannelAndRunID(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindChallengeWithUnreadUpdate(t, s, "run id")
	runInt := insertFakeRunForTest(t, s, ch.ID, JudgmentJ2)

	if _, err := s.markReadWithValues(context.Background(), ChannelInvoker, ch.ID, markReadOptions{
		RunID:  formatRunID(runInt),
		Values: &readValues{CommentsCount: 3, UpstreamUpdatedAt: "2026-09-25T08:00:00Z"},
	}); err != nil {
		t.Fatalf("markReadWithValues() error = %v", err)
	}

	var action, channel, verification string
	var runID sql.NullInt64
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT action, channel, verification, run_id FROM activity WHERE action = 'upstream_read' ORDER BY id DESC LIMIT 1`).
			Scan(&action, &channel, &verification, &runID)
	})
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if action != "upstream_read" || channel != string(ChannelInvoker) || verification != string(VerificationNone) {
		t.Errorf("activity = (%q, %q, %q), want (upstream_read, invoker, none)", action, channel, verification)
	}
	if !runID.Valid || runID.Int64 != runInt {
		t.Errorf("activity.run_id = %v, want %d", runID, runInt)
	}
}

func TestMarkReadWithValues_SameAsCurrentReadValuesIsNoOp(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindChallengeWithUnreadUpdate(t, s, "noop")
	if _, err := s.markReadWithValues(context.Background(), ChannelInvoker, ch.ID, markReadOptions{
		Values: &readValues{CommentsCount: 1, UpstreamUpdatedAt: "2026-09-25T00:00:00Z"},
	}); err != nil {
		t.Fatalf("first markReadWithValues() error = %v", err)
	}
	before, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}

	res, err := s.markReadWithValues(context.Background(), ChannelInvoker, ch.ID, markReadOptions{
		Values: &readValues{CommentsCount: 1, UpstreamUpdatedAt: "2026-09-25T00:00:00Z"},
	})
	if err != nil {
		t.Fatalf("second markReadWithValues() error = %v", err)
	}
	if res.Changed {
		t.Errorf("Changed = true, want false when the read values are already the given values")
	}
	after, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	if after.Version != before.Version {
		t.Errorf("version changed %d -> %d on a no-op", before.Version, after.Version)
	}
}

func TestMarkReadWithValues_RejectsNegativeCountAndBadRunID(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindChallengeWithUnreadUpdate(t, s, "validation")

	_, err := s.markReadWithValues(context.Background(), ChannelInvoker, ch.ID, markReadOptions{
		Values: &readValues{CommentsCount: -1, UpstreamUpdatedAt: "x"},
	})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("negative comments count: error = %v, want ErrValidation", err)
	}
	_, err = s.markReadWithValues(context.Background(), ChannelInvoker, ch.ID, markReadOptions{
		RunID:  "not-a-run-id",
		Values: &readValues{CommentsCount: 1, UpstreamUpdatedAt: "x"},
	})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("malformed run id: error = %v, want ErrValidation", err)
	}
}

func TestMarkReadWithValues_NilValuesBehavesLikeMarkRead(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindChallengeWithUnreadUpdate(t, s, "nil values")

	res, err := s.markReadWithValues(context.Background(), ChannelCLI, ch.ID, markReadOptions{})
	if err != nil {
		t.Fatalf("markReadWithValues() error = %v", err)
	}
	if res.SourceBinding.ReadCommentsCount != res.SourceBinding.CommentsCount {
		t.Errorf("ReadCommentsCount = %d, want the observed %d", res.SourceBinding.ReadCommentsCount, res.SourceBinding.CommentsCount)
	}
}

// 読んだ時点の値・原因の run を渡せるのは経路 invoker だけ（人間の経路は、ストアの
// 観測値でそろえる MarkRead だけ）。
func TestMarkReadWithValues_ValuesAndRunIDRequireTheInvokerChannel(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindChallengeWithUnreadUpdate(t, s, "channel guard")
	runInt := insertFakeRunForTest(t, s, ch.ID, JudgmentJ2)

	_, err := s.markReadWithValues(context.Background(), ChannelCLI, ch.ID, markReadOptions{
		Values: &readValues{CommentsCount: 3, UpstreamUpdatedAt: "2026-09-25T08:00:00Z"},
	})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("values via the cli channel: error = %v, want ErrValidation", err)
	}
	_, err = s.markReadWithValues(context.Background(), ChannelCLI, ch.ID, markReadOptions{RunID: formatRunID(runInt)})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("run id via the cli channel: error = %v, want ErrValidation", err)
	}
	after, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SourceBinding.ReadCommentsCount != 0 {
		t.Errorf("a rejected call changed the read values: %+v", after.SourceBinding)
	}
}

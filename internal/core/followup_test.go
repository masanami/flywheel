package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

// 区切り S2b のフォローアップの core の口（docs/features/core-redesign.md の「フォローアップ」の受入基準）を、
// 実ストアで検証する。

func newFollowupStore(t *testing.T) *Store {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	return s
}

func fuChallenge(t *testing.T, s *Store, title string) *Challenge {
	t.Helper()
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: title})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	return c
}

func fuCreate(t *testing.T, s *Store, challengeID, title, detail string) *Followup {
	t.Helper()
	f, err := s.CreateFollowup(context.Background(), ChannelCLI, FollowupInput{ChallengeID: challengeID, Title: title, Detail: detail, Source: FollowupSourceLearn})
	if err != nil {
		t.Fatalf("CreateFollowup: %v", err)
	}
	return f
}

func fuGet(t *testing.T, s *Store, id string) Followup {
	t.Helper()
	all, err := s.ListFollowups(context.Background(), FollowupFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range all {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("followup %s not found", id)
	return Followup{}
}

func fuActs(t *testing.T, s *Store, entity, action string) []Activity {
	t.Helper()
	acts, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []Activity
	for _, a := range acts {
		if a.Entity == entity && a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

func fuAtt(t *testing.T, target string) Attestation { return verifiedAttestationForTest(t, target) }

func TestCreateFollowup_WritesOpenRowAndCreateActivity(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	c := fuChallenge(t, s, "c")
	f := fuCreate(t, s, c.ID, "確かめる", "細かい説明")
	if f.ID != "F-1" || f.Status != FollowupStatusOpen || f.Source != FollowupSourceLearn || f.Title != "確かめる" || f.Detail != "細かい説明" {
		t.Errorf("followup = %+v", f)
	}
	if f.ChallengeID == nil || *f.ChallengeID != c.ID || f.Note != nil || f.Reason != nil || f.PromotedChallengeID != nil {
		t.Errorf("followup refs = %+v", f)
	}
	acts := fuActs(t, s, "followup", "create")
	if len(acts) != 1 || acts[0].EntityID != "F-1" {
		t.Fatalf("create activities = %+v", acts)
	}
	// 課題に属さないもの・説明の無いもの。
	g := fuCreate(t, s, "", "属さない", "")
	if g.ChallengeID != nil || g.Detail != "" {
		t.Errorf("unattached followup = %+v", g)
	}
}

func TestCreateFollowup_RejectsInvalidInputWithoutWriting(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	c := fuChallenge(t, s, "c")
	for name, in := range map[string]FollowupInput{
		"empty title":     {ChallengeID: c.ID, Title: " ", Source: FollowupSourceLearn},
		"multiline title": {ChallengeID: c.ID, Title: "a\nb", Source: FollowupSourceLearn},
		"other source":    {ChallengeID: c.ID, Title: "a", Source: "manual"},
	} {
		if _, err := s.CreateFollowup(context.Background(), ChannelCLI, in); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
	if _, err := s.CreateFollowup(context.Background(), ChannelCLI, FollowupInput{ChallengeID: "C-999", Title: "a", Source: FollowupSourceLearn}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown challenge: err = %v, want ErrNotFound", err)
	}
	if got, _ := s.ListFollowups(context.Background(), FollowupFilter{}); len(got) != 0 {
		t.Errorf("rows written: %+v", got)
	}
}

func TestListFollowups_FiltersByStatusAndChallenge(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	a, b := fuChallenge(t, s, "a"), fuChallenge(t, s, "b")
	f1 := fuCreate(t, s, a.ID, "1", "")
	fuCreate(t, s, b.ID, "2", "")
	fuCreate(t, s, "", "3", "")
	if _, err := s.CompleteFollowups(context.Background(), ChannelCLI, []string{f1.ID}, "ok"); err != nil {
		t.Fatal(err)
	}
	ids := func(f FollowupFilter) []string {
		got, err := s.ListFollowups(context.Background(), f)
		if err != nil {
			t.Fatalf("ListFollowups(%+v): %v", f, err)
		}
		var out []string
		for _, x := range got {
			out = append(out, x.ID)
		}
		return out
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}
	eq(ids(FollowupFilter{}), "F-1", "F-2", "F-3")
	eq(ids(FollowupFilter{Status: "open"}), "F-2", "F-3")
	eq(ids(FollowupFilter{Status: "done"}), "F-1")
	eq(ids(FollowupFilter{Status: "dropped"}))
	eq(ids(FollowupFilter{ChallengeID: b.ID}), "F-2")
	if _, err := s.ListFollowups(context.Background(), FollowupFilter{Status: "bogus"}); !errors.Is(err, ErrValidation) {
		t.Errorf("bogus status: %v", err)
	}
	if _, err := s.ListFollowups(context.Background(), FollowupFilter{ChallengeID: "C-99"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown challenge: %v", err)
	}
}

func TestCompleteFollowups_RequiresNoteAndIsAllOrNothing(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	f1 := fuCreate(t, s, "", "1", "")
	f2 := fuCreate(t, s, "", "2", "")
	ctx := context.Background()
	if _, err := s.CompleteFollowups(ctx, ChannelCLI, []string{f1.ID}, " "); !errors.Is(err, ErrValidation) {
		t.Errorf("empty note: %v", err)
	}
	if _, err := s.CompleteFollowups(ctx, ChannelCLI, []string{f1.ID, "F-99"}, "n"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing id: %v", err)
	}
	if fuGet(t, s, f1.ID).Status != FollowupStatusOpen {
		t.Error("f1 changed although another id was missing")
	}
	if _, err := s.CompleteFollowups(ctx, ChannelCLI, []string{f1.ID}, "n"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteFollowups(ctx, ChannelCLI, []string{f2.ID, f1.ID}, "n"); !errors.Is(err, ErrValidation) {
		t.Errorf("already done: %v", err)
	}
	if fuGet(t, s, f2.ID).Status != FollowupStatusOpen {
		t.Error("f2 changed although f1 was not open")
	}
	if _, err := s.CompleteFollowups(ctx, ChannelCLI, []string{f2.ID, f2.ID}, "n"); !errors.Is(err, ErrValidation) {
		t.Errorf("duplicate id: %v", err)
	}
	done, err := s.CompleteFollowups(ctx, ChannelCLI, []string{f2.ID}, "確かめた")
	if err != nil || len(done) != 1 || done[0].Status != FollowupStatusDone || done[0].Note == nil || *done[0].Note != "確かめた" {
		t.Fatalf("done = %+v, err = %v", done, err)
	}
	acts := fuActs(t, s, "followup", ActivityActionFollowupDone)
	if len(acts) != 2 || acts[1].Actor != "alice" || acts[1].Channel != string(ChannelCLI) || acts[1].Verification != string(VerificationNone) {
		t.Errorf("followup_done activities = %+v", acts)
	}
}

func TestDropFollowups_NeedsAttestationOverSortedTargetAndRecordsEvidence(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	ctx := context.Background()
	f1, f2, f3 := fuCreate(t, s, "", "1", ""), fuCreate(t, s, "", "2", ""), fuCreate(t, s, "", "3", "")
	target, ok := FollowupAttestationTarget([]string{f3.ID, f1.ID})
	if !ok || target != "F-1,F-3" {
		t.Fatalf("target = %q, %v", target, ok)
	}
	if one, _ := FollowupAttestationTarget([]string{f2.ID}); one != "F-2" {
		t.Errorf("single target = %q", one)
	}
	// 別の対象向けの本人確認・画面の経路・ゼロ値は受けない。
	if _, err := s.DropFollowups(ctx, []string{f1.ID, f3.ID}, "r", fuAtt(t, "F-3,F-1")); !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("unsorted target: %v", err)
	}
	if _, err := s.DropFollowups(ctx, []string{f1.ID}, "r", Attestation{}); !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("zero attestation: %v", err)
	}
	ui, err := Verify(ChannelUI, &fakeVerifier{method: VerificationNone, actor: "alice"}, "s", f1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DropFollowups(ctx, []string{f1.ID}, "r", ui); !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("ui none: %v", err)
	}
	if _, err := s.DropFollowups(ctx, []string{f1.ID, f3.ID}, " ", fuAtt(t, target)); !errors.Is(err, ErrValidation) {
		t.Errorf("empty reason: %v", err)
	}
	if _, err := s.DropFollowups(ctx, []string{f1.ID, "F-99"}, "r", fuAtt(t, "F-1,F-99")); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if fuGet(t, s, f1.ID).Status != FollowupStatusOpen {
		t.Fatal("something changed before all checks passed")
	}
	if got := len(fuActs(t, s, "followup", ActivityActionFollowupDrop)); got != 0 {
		t.Fatalf("drop activities written by rejected calls: %d", got)
	}
	got, err := s.DropFollowups(ctx, []string{f3.ID, f1.ID}, "要らない", relayedAttestation(t, target, "sess", "quote"))
	if err != nil || len(got) != 2 {
		t.Fatalf("drop: %+v, %v", got, err)
	}
	for _, f := range got {
		if f.Status != FollowupStatusDropped || f.Reason == nil || *f.Reason != "要らない" {
			t.Errorf("dropped = %+v", f)
		}
	}
	if fuGet(t, s, f2.ID).Status != FollowupStatusOpen {
		t.Error("unlisted followup changed")
	}
	acts := fuActs(t, s, "followup", ActivityActionFollowupDrop)
	if len(acts) != 2 || acts[0].Verification != string(VerificationRelayed) {
		t.Fatalf("drop activities = %+v", acts)
	}
	var after map[string]any
	if err := json.Unmarshal(acts[0].After, &after); err != nil || after["relay_session"] != "sess" || after["relay_quote"] != "quote" || after["reason"] != "要らない" {
		t.Errorf("after = %v, %v", after, err)
	}
	// 捨てたものは、もう捨てられない。
	if _, err := s.DropFollowups(ctx, []string{f1.ID}, "r", fuAtt(t, f1.ID)); !errors.Is(err, ErrValidation) {
		t.Errorf("drop dropped: %v", err)
	}
}

func TestPromoteFollowup_MakesUnclassifiedChallengeAndKeepsFollowupOpen(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	ctx := context.Background()
	parent := fuChallenge(t, s, "parent")
	attached := fuCreate(t, s, parent.ID, "属する", "説明あり")
	loose := fuCreate(t, s, "", "属さない", "")

	c, f, err := promoteViaPlan(ctx, s, attached.ID, fuAtt(t, attached.ID))
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if c.Status != StatusUnclassified || c.Title != "属する" || c.Description != "説明あり" || c.DoneCriteria != "" || c.Reporter != "alice" {
		t.Errorf("challenge = %+v", c)
	}
	if f.Status != FollowupStatusOpen || f.PromotedChallengeID == nil || *f.PromotedChallengeID != c.ID {
		t.Errorf("followup = %+v", f)
	}
	var origin, followupOf, desc *string
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT origin, followup_of, description FROM challenge WHERE id = ?`, mustParseChallengeID(t, c.ID)).Scan(&origin, &followupOf, &desc)
	}); err != nil || origin == nil || *origin != "followup" || followupOf == nil || *followupOf != parent.ID {
		t.Errorf("origin = %v, followup_of = %v, err = %v", origin, followupOf, err)
	}

	c2, _, err := promoteViaPlan(ctx, s, loose.ID, relayedAttestation(t, loose.ID, "s", "q"))
	if err != nil {
		t.Fatal(err)
	}
	if c2.Description != "" {
		t.Errorf("description = %q, want empty", c2.Description)
	}
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT origin, followup_of FROM challenge WHERE id = ?`, mustParseChallengeID(t, c2.ID)).Scan(&origin, &followupOf)
	}); err != nil || *origin != "manual" || followupOf != nil {
		t.Errorf("origin = %v, followup_of = %v, err = %v", *origin, followupOf, err)
	}

	// 作業ログ: followup_promote と、作った課題の create。
	if got := fuActs(t, s, "followup", ActivityActionFollowupPromote); len(got) != 2 {
		t.Errorf("followup_promote activities = %d", len(got))
	}
	var created int
	for _, a := range fuActs(t, s, "challenge", "create") {
		if a.EntityID == c.ID || a.EntityID == c2.ID {
			created++
		}
	}
	if created != 2 {
		t.Errorf("create activities for promoted challenges = %d", created)
	}
}

func TestPromoteFollowup_RejectsBadStatesAndAttestations(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	ctx := context.Background()
	f := fuCreate(t, s, "", "x", "")
	if _, _, err := promoteViaPlan(ctx, s, "F-99", fuAtt(t, "F-99")); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, _, err := promoteViaPlan(ctx, s, f.ID, fuAtt(t, "F-2")); !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("other target: %v", err)
	}
	ui, _ := Verify(ChannelUI, &fakeVerifier{method: VerificationNone, actor: "alice"}, "s", f.ID)
	if _, _, err := promoteViaPlan(ctx, s, f.ID, ui); !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("ui: %v", err)
	}
	var n int
	_ = s.db.Read(ctx, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT COUNT(*) FROM challenge`).Scan(&n) })
	if n != 0 {
		t.Fatalf("rejected promotions made %d challenges", n)
	}
	c, _, err := promoteViaPlan(ctx, s, f.ID, fuAtt(t, f.ID))
	if err != nil {
		t.Fatal(err)
	}
	// 上げた先が取り下げでないうちは、上げ直せない。
	if _, _, err := promoteViaPlan(ctx, s, f.ID, fuAtt(t, f.ID)); !errors.Is(err, ErrValidation) {
		t.Errorf("re-promote while alive: %v", err)
	}
	// 取り下げになれば、新しい課題に置き換えて上げ直せる。
	setChallengeStatus(t, s, mustParseChallengeID(t, c.ID), StatusWithdrawn)
	c2, f2, err := promoteViaPlan(ctx, s, f.ID, fuAtt(t, f.ID))
	if err != nil {
		t.Fatalf("re-promote after withdraw: %v", err)
	}
	if c2.ID == c.ID || f2.PromotedChallengeID == nil || *f2.PromotedChallengeID != c2.ID || f2.Status != FollowupStatusOpen {
		t.Errorf("c2 = %s, followup = %+v", c2.ID, f2)
	}
	// 済み・捨てたものは上げられない。
	h := fuCreate(t, s, "", "z", "")
	if _, err := s.DropFollowups(ctx, []string{h.ID}, "r", fuAtt(t, h.ID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := promoteViaPlan(ctx, s, h.ID, fuAtt(t, h.ID)); !errors.Is(err, ErrValidation) {
		t.Errorf("promote dropped: %v", err)
	}
	g := fuCreate(t, s, "", "y", "")
	if _, err := s.CompleteFollowups(ctx, ChannelCLI, []string{g.ID}, "n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := promoteViaPlan(ctx, s, g.ID, fuAtt(t, g.ID)); !errors.Is(err, ErrValidation) {
		t.Errorf("promote done: %v", err)
	}
}

func TestPromotedChallengeCompletion_CompletesOpenFollowupsOnT13AndT14(t *testing.T) {
	t.Parallel()
	for _, holdRelease := range []bool{false, true} {
		name := "T13"
		if holdRelease {
			name = "T14"
		}
		t.Run(name, func(t *testing.T) {
			s := newFollowupStore(t)
			ctx := context.Background()
			f := fuCreate(t, s, "", "上げる", "")
			dropped := fuCreate(t, s, "", "捨てた", "")
			c, _, err := promoteViaPlan(ctx, s, f.ID, fuAtt(t, f.ID))
			if err != nil {
				t.Fatal(err)
			}
			// 手で捨てたフォローアップも同じ課題を上げた先にしてある（捨てたまま残ることの確認）。
			if _, err := s.DropFollowups(ctx, []string{dropped.ID}, "r", fuAtt(t, dropped.ID)); err != nil {
				t.Fatal(err)
			}
			cid := mustParseChallengeID(t, c.ID)
			if err := s.db.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE followup SET promoted_challenge_id = ? WHERE id = 2`, cid)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			complete := func() {
				t.Helper()
				setChallengeStatus(t, s, cid, StatusAwaitingCompletionApproval)
				prev, err := s.PrepareApproval(ctx, c.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := s.ExecuteApproval(ctx, ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved, HoldRelease: holdRelease}, fuAtt(t, c.ID)); err != nil {
					t.Fatalf("ExecuteApproval: %v", err)
				}
			}
			before := fuGet(t, s, f.ID)
			if before.Status != FollowupStatusOpen {
				t.Fatalf("before = %+v", before)
			}
			complete()
			got := fuGet(t, s, f.ID)
			if got.Status != FollowupStatusDone || got.Note == nil || *got.Note != "上げた先の課題 "+c.ID+" が完了した" {
				t.Errorf("after completion = %+v", got)
			}
			if d := fuGet(t, s, dropped.ID); d.Status != FollowupStatusDropped {
				t.Errorf("dropped followup = %+v", d)
			}
			if n := len(fuActs(t, s, "followup", ActivityActionFollowupDone)); n != 1 {
				t.Fatalf("followup_done = %d, want 1", n)
			}
			// 反映のやり直し（T18）の後の再完了では、記録が増えない。
			complete()
			if n := len(fuActs(t, s, "followup", ActivityActionFollowupDone)); n != 1 {
				t.Errorf("followup_done after re-completion = %d, want 1", n)
			}
		})
	}
}

func TestPromotedChallengeWithdrawal_LeavesFollowupOpen(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	ctx := context.Background()
	f := fuCreate(t, s, "", "x", "")
	c, _, err := promoteViaPlan(ctx, s, f.ID, fuAtt(t, f.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteWithdraw(ctx, WithdrawRequest{ChallengeID: c.ID, ExpectedVersion: c.Version, Reason: "いらない"}, fuAtt(t, c.ID)); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if got := fuGet(t, s, f.ID); got.Status != FollowupStatusOpen || got.PromotedChallengeID == nil {
		t.Errorf("followup = %+v", got)
	}
	if n := len(fuActs(t, s, "followup", ActivityActionFollowupDone)); n != 0 {
		t.Errorf("followup_done = %d", n)
	}
}

func TestCountOpenFollowupsToTriage_ExcludesPromotedAndAliveOnes(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	ctx := context.Background()
	a, b, c := fuCreate(t, s, "", "a", ""), fuCreate(t, s, "", "b", ""), fuCreate(t, s, "", "c", "")
	fuCreate(t, s, "", "d", "")
	count := func() int {
		n, err := s.CountOpenFollowupsToTriage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 4 {
		t.Fatalf("count = %d, want 4", count())
	}
	ca, _, err := promoteViaPlan(ctx, s, a.ID, fuAtt(t, a.ID))
	if err != nil {
		t.Fatal(err)
	}
	if count() != 3 {
		t.Errorf("after promote = %d, want 3", count())
	}
	// 上げた先が取り下げになれば、また数える。
	setChallengeStatus(t, s, mustParseChallengeID(t, ca.ID), StatusWithdrawn)
	if count() != 4 {
		t.Errorf("after withdraw = %d, want 4", count())
	}
	if _, err := s.CompleteFollowups(ctx, ChannelCLI, []string{b.ID}, "n"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DropFollowups(ctx, []string{c.ID}, "r", fuAtt(t, c.ID)); err != nil {
		t.Fatal(err)
	}
	if count() != 2 {
		t.Errorf("after done/drop = %d, want 2", count())
	}
	// 一覧は状態だけで数える（上げてあっても未のうち）。
	if _, _, err := promoteViaPlan(ctx, s, a.ID, fuAtt(t, a.ID)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListFollowups(ctx, FollowupFilter{Status: "open"}); len(got) != 2 {
		t.Errorf("open list = %d, want 2", len(got))
	}
}

func TestListActivities_ForChallengeIncludesItsFollowupRecords(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	a, b := fuChallenge(t, s, "a"), fuChallenge(t, s, "b")
	fa := fuCreate(t, s, a.ID, "a の", "")
	fuCreate(t, s, b.ID, "b の", "")
	if _, err := s.CompleteFollowups(context.Background(), ChannelCLI, []string{fa.ID}, "n"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range activitiesFor(t, s, a.ID) {
		if x.Entity == "followup" {
			got = append(got, x.EntityID+":"+x.Action)
		}
	}
	if len(got) != 2 || got[0] != "F-1:create" || got[1] != "F-1:followup_done" {
		t.Errorf("followup activities of a = %v", got)
	}
}

// #774: 捨てる前の読み取りだけの検査（本人確認を求める前に終える）は、DropFollowups と同じ規則・同じ順で
// 拒み、通るときは番号の小さい順で返し、ストアを変えない。
func TestPrepareFollowupDrop_SameRulesAndOrderAsDropAndReadOnly(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	c := fuChallenge(t, s, "c")
	f1 := fuCreate(t, s, c.ID, "one", "")
	f2 := fuCreate(t, s, c.ID, "two", "")
	done := fuCreate(t, s, c.ID, "done", "")
	if _, err := s.CompleteFollowups(context.Background(), ChannelCLI, []string{done.ID}, "確かめた"); err != nil {
		t.Fatal(err)
	}
	before := len(fuActs(t, s, "followup", ActivityActionFollowupDrop))
	ctx := context.Background()

	// 通るとき: 指定の順に依らず、番号の小さい順。
	got, err := s.PrepareFollowupDrop(ctx, []string{f2.ID, f1.ID}, "不要")
	if err != nil || len(got) != 2 || got[0].ID != f1.ID || got[1].ID != f2.ID {
		t.Fatalf("PrepareFollowupDrop = %+v, %v; want [%s %s]", got, err, f1.ID, f2.ID)
	}
	// 拒むとき（DropFollowups と同じエラー）。
	cases := []struct {
		name   string
		ids    []string
		reason string
		want   error
	}{
		{"存在しない ID", []string{f1.ID, "F-999"}, "不要", ErrNotFound},
		{"形の違う ID", []string{"X-1"}, "不要", ErrNotFound},
		{"未でない", []string{f1.ID, done.ID}, "不要", ErrValidation},
		{"理由が空", []string{f1.ID}, "  ", ErrValidation},
		{"理由が空で、存在しない ID もある（理由が先）", []string{"F-999"}, "", ErrValidation},
	}
	for _, tc := range cases {
		if _, err := s.PrepareFollowupDrop(ctx, tc.ids, tc.reason); !errors.Is(err, tc.want) {
			t.Errorf("%s: PrepareFollowupDrop error = %v, want %v", tc.name, err, tc.want)
		}
		// 同じ入力を DropFollowups に渡しても、同じ種類のエラーになる。
		target, _ := FollowupAttestationTarget(tc.ids)
		if _, err := s.DropFollowups(ctx, tc.ids, tc.reason, fuAtt(t, target)); !errors.Is(err, tc.want) {
			t.Errorf("%s: DropFollowups error = %v, want %v (same as the prepare check)", tc.name, err, tc.want)
		}
	}
	// 読み取りだけ: どのフォローアップも変わらず、記録も増えない。
	for _, id := range []string{f1.ID, f2.ID} {
		if st := fuGet(t, s, id).Status; st != FollowupStatusOpen {
			t.Errorf("%s status = %s, want open", id, st)
		}
	}
	if n := len(fuActs(t, s, "followup", ActivityActionFollowupDrop)); n != before {
		t.Errorf("followup_drop activities %d → %d, want unchanged", before, n)
	}
}

// #774: 課題に上げる前の読み取りだけの検査は、PromoteFollowup と同じ規則で拒み、ストアを変えない。
func TestPrepareFollowupPromote_SameRulesAsPromoteAndReadOnly(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	c := fuChallenge(t, s, "c")
	open := fuCreate(t, s, c.ID, "open", "")
	done := fuCreate(t, s, c.ID, "done", "")
	if _, err := s.CompleteFollowups(context.Background(), ChannelCLI, []string{done.ID}, "確かめた"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	challenges := func() int {
		cs, err := s.ListChallenges(ctx, ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return len(cs)
	}
	before := challenges()
	if f, err := s.PrepareFollowupPromote(ctx, open.ID, FollowupPromoteOptions{}, nil); err != nil || f.Followup.ID != open.ID {
		t.Fatalf("PrepareFollowupPromote(open) = %+v, %v", f, err)
	}
	if _, err := s.PrepareFollowupPromote(ctx, done.ID, FollowupPromoteOptions{}, nil); !errors.Is(err, ErrValidation) {
		t.Errorf("PrepareFollowupPromote(done) error = %v, want ErrValidation", err)
	}
	if _, err := s.PrepareFollowupPromote(ctx, "F-999", FollowupPromoteOptions{}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("PrepareFollowupPromote(missing) error = %v, want ErrNotFound", err)
	}
	if got := challenges(); got != before {
		t.Errorf("challenges %d → %d, want unchanged (prepare is read-only)", before, got)
	}
	// 上げてある（上げた先が取り下げでない）ものは、上げ直せない。
	if _, _, err := promoteViaPlan(ctx, s, open.ID, fuAtt(t, open.ID)); err != nil {
		t.Fatalf("PromoteFollowup: %v", err)
	}
	if _, err := s.PrepareFollowupPromote(ctx, open.ID, FollowupPromoteOptions{}, nil); !errors.Is(err, ErrValidation) {
		t.Errorf("PrepareFollowupPromote(already promoted) error = %v, want ErrValidation", err)
	}
}

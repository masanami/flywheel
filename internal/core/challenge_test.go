package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os/user"
	"testing"
	"time"
)

// fixedActor は resolveActor が常に name を返すように actorSource を固定する
// （新規に発行される課題の reporter・作業ログの actor を予測可能にする）。
func fixedActor(t *testing.T, name string) {
	t.Helper()
	withActorSource(t, actorSourceFuncs{
		userCurrent: func() (*user.User, error) { return &user.User{Username: name}, nil },
		getenv:      fakeGetenv(map[string]string{}),
	})
}

func strPtr(s string) *string { return &s }

func TestCreateChallenge_SetsUnclassifiedStatusVersionAndID(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	if c.ID != "C-1" {
		t.Errorf("ID = %q, want %q", c.ID, "C-1")
	}
	if c.Status != StatusUnclassified {
		t.Errorf("Status = %q, want %q", c.Status, StatusUnclassified)
	}
	if c.Version != 1 {
		t.Errorf("Version = %d, want 1", c.Version)
	}
	if c.Reporter != "alice" {
		t.Errorf("Reporter = %q, want %q", c.Reporter, "alice")
	}
	if c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		t.Error("CreatedAt/UpdatedAt should not be zero")
	}
	if !c.CreatedAt.Equal(c.UpdatedAt) {
		t.Errorf("CreatedAt(%v) != UpdatedAt(%v) on create", c.CreatedAt, c.UpdatedAt)
	}
}

func TestCreateChallenge_IDsAreSequentialAcrossCalls(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	first, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "a"})
	if err != nil {
		t.Fatalf("CreateChallenge() 1 error = %v", err)
	}
	second, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "b"})
	if err != nil {
		t.Fatalf("CreateChallenge() 2 error = %v", err)
	}
	if first.ID != "C-1" || second.ID != "C-2" {
		t.Fatalf("IDs = %q, %q, want C-1, C-2", first.ID, second.ID)
	}
}

func TestCreateChallenge_TitleIsStoredVerbatimNotTrimmed(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "  padded  "})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	if c.Title != "  padded  " {
		t.Fatalf("Title = %q, want the value untrimmed", c.Title)
	}
}

func TestCreateChallenge_EmptyOrWhitespaceTitleIsValidationErrorAndCreatesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	for _, title := range []string{"", "   ", "\t\n"} {
		before := countChallenges(t, s)
		_, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: title})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("title=%q: err = %v, want ErrValidation", title, err)
		}
		if after := countChallenges(t, s); after != before {
			t.Errorf("title=%q: challenge count changed: before=%d after=%d", title, before, after)
		}
	}
}

func TestCreateChallenge_InvalidUrgencyIsValidationErrorAndCreatesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	before := countChallenges(t, s)
	bogus := "urgent"
	_, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t", Urgency: &bogus})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if after := countChallenges(t, s); after != before {
		t.Fatalf("challenge count changed: before=%d after=%d", before, after)
	}
}

func TestCreateChallenge_ValidUrgencyIsStored(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	high := string(UrgencyHigh)
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t", Urgency: &high})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	if c.Urgency == nil || *c.Urgency != UrgencyHigh {
		t.Fatalf("Urgency = %v, want %q", c.Urgency, UrgencyHigh)
	}
}

func TestCreateChallenge_RecordsActivityLogCreateEntryWithVerificationNone(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t", Description: "d", DoneCriteria: "dc"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}

	activities, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	if len(activities) != 1 {
		t.Fatalf("len(activities) = %d, want 1", len(activities))
	}
	a := activities[0]
	if a.Actor != "alice" {
		t.Errorf("Actor = %q, want %q", a.Actor, "alice")
	}
	if a.Channel != string(ChannelCLI) {
		t.Errorf("Channel = %q, want %q", a.Channel, ChannelCLI)
	}
	if a.Verification != string(VerificationNone) {
		t.Errorf("Verification = %q, want %q", a.Verification, VerificationNone)
	}
	if a.Entity != "challenge" {
		t.Errorf("Entity = %q, want %q", a.Entity, "challenge")
	}
	if a.EntityID != c.ID {
		t.Errorf("EntityID = %q, want %q", a.EntityID, c.ID)
	}
	if a.Action != "create" {
		t.Errorf("Action = %q, want %q", a.Action, "create")
	}
	if a.Before != nil {
		t.Errorf("Before = %s, want nil (create has no prior state)", a.Before)
	}
	var after map[string]any
	if err := json.Unmarshal(a.After, &after); err != nil {
		t.Fatalf("unmarshal After: %v", err)
	}
	if after["title"] != "t" || after["description"] != "d" || after["done_criteria"] != "dc" {
		t.Errorf("After = %+v, missing expected human-entry fields", after)
	}
	if after["status"] != string(StatusUnclassified) {
		t.Errorf("After[status] = %v, want %q", after["status"], StatusUnclassified)
	}
	if after["reporter"] != "alice" {
		t.Errorf("After[reporter] = %v, want %q", after["reporter"], "alice")
	}
}

// AC-68: 作業ログへの書き込みを失敗させると、対象の変更も残らない。
func TestCreateChallenge_ActivityInsertFailureRollsBackChallengeCreation(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	s.insertActivity = func(_ *sql.Tx, _ activityRow) error {
		return errors.New("boom: injected activity insert failure")
	}

	before := countChallenges(t, s)
	_, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err == nil {
		t.Fatal("CreateChallenge() error = nil, want an error from the injected failure")
	}
	if after := countChallenges(t, s); after != before {
		t.Fatalf("challenge count changed despite injected activity failure: before=%d after=%d", before, after)
	}
}

func TestGetChallenge_ReturnsErrNotFoundForMissingID(t *testing.T) {
	s := newStoreForTest(t)
	_, err := s.GetChallenge(context.Background(), "C-999")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGetChallenge_ReturnsErrNotFoundForMalformedID(t *testing.T) {
	s := newStoreForTest(t)
	for _, id := range []string{"foo", "OP-1", "C-0", "C-01", "C--1", "C-"} {
		if _, err := s.GetChallenge(context.Background(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("id=%q: err = %v, want ErrNotFound", id, err)
		}
	}
}

func TestGetChallenge_IncludesPlansApprovalsHoldsAndOperations(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	cid, _ := parseChallengeID(c.ID)
	insertFullChallengeFixtures(t, s, cid)

	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}

	if len(detail.Plans) != 2 {
		t.Fatalf("len(Plans) = %d, want 2", len(detail.Plans))
	}
	if detail.Plans[0].Version != 1 || detail.Plans[1].Version != 2 {
		t.Errorf("Plans versions = %d, %d, want 1, 2 (ascending)", detail.Plans[0].Version, detail.Plans[1].Version)
	}

	if len(detail.Approvals) != 1 {
		t.Fatalf("len(Approvals) = %d, want 1", len(detail.Approvals))
	}
	ap := detail.Approvals[0]
	if ap.Kind != ApprovalKindPlan || ap.Decision != ApprovalDecisionApproved {
		t.Errorf("Approval = %+v, want kind=plan decision=approved", ap)
	}
	if ap.OperationID != nil {
		t.Errorf("Approval.OperationID = %v, want nil", *ap.OperationID)
	}

	if len(detail.Holds) != 1 {
		t.Fatalf("len(Holds) = %d, want 1", len(detail.Holds))
	}
	h := detail.Holds[0]
	if h.Question != "why?" || h.FromStatus != StatusInProgress {
		t.Errorf("Hold = %+v, want question=why? from_status=in_progress", h)
	}
	if h.Answer == nil || *h.Answer != "because" {
		t.Errorf("Hold.Answer = %v, want %q", h.Answer, "because")
	}

	if len(detail.Operations) != 1 {
		t.Fatalf("len(Operations) = %d, want 1", len(detail.Operations))
	}
	op := detail.Operations[0]
	if op.ID != "OP-1" || op.ChallengeID != c.ID || op.Kind != OperationKindRelease {
		t.Errorf("Operation = %+v, want id=OP-1 challenge_id=%s kind=release", op, c.ID)
	}
}

// insertFullChallengeFixtures は id の課題に対して、計画2版・承認1件・保留1件
// （回答済み）・不可逆操作1件を直接 SQL で挿入する（#9 は plan/approve/hold/op add
// の書き込み API を持たないため、show の読み取りをテスト用フィクスチャで検証する）。
func insertFullChallengeFixtures(t *testing.T, s *Store, challengeID int64) {
	t.Helper()
	now := "2026-09-22T00:00:00.000Z"
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at) VALUES (?, 1, 'v1', ?)`, challengeID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at) VALUES (?, 2, 'v2', ?)`, challengeID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, reason, decided_at)
			 VALUES (?, NULL, 'plan', 'approved', 1, 'alice', 'cli', 'tty_confirm', NULL, ?)`, challengeID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by)
			 VALUES (?, 'why?', 'in_progress', ?, 'because', ?, 'alice')`, challengeID, now, now); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at)
			 VALUES (?, 'release', 'ship it', NULL, 'pending', 1, ?)`, challengeID, now); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("insertFullChallengeFixtures: %v", err)
	}
}

func TestListChallenges_OrdersByIDAscending(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	for _, title := range []string{"a", "b", "c"} {
		if _, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: title}); err != nil {
			t.Fatalf("CreateChallenge(%q) error = %v", title, err)
		}
	}

	list, err := s.ListChallenges(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListChallenges() error = %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len(list) = %d, want 3", len(list))
	}
	for i, want := range []string{"C-1", "C-2", "C-3"} {
		if list[i].ID != want {
			t.Errorf("list[%d].ID = %q, want %q", i, list[i].ID, want)
		}
	}
}

func TestListChallenges_FiltersByStatus(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c1, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "a"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	if _, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "b"}); err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	cid, _ := parseChallengeID(c1.ID)
	setChallengeStatus(t, s, cid, StatusClassified)

	for _, status := range []string{"unclassified", "未分類"} {
		list, err := s.ListChallenges(context.Background(), ListOptions{Status: strPtr(status)})
		if err != nil {
			t.Fatalf("status=%q: ListChallenges() error = %v", status, err)
		}
		if len(list) != 1 {
			t.Fatalf("status=%q: len(list) = %d, want 1", status, len(list))
		}
		if list[0].Title != "b" {
			t.Fatalf("status=%q: list[0].Title = %q, want %q", status, list[0].Title, "b")
		}
	}
}

func TestListChallenges_InvalidStatusIsValidationError(t *testing.T) {
	s := newStoreForTest(t)
	for _, status := range []string{"計画承認待ち（未分類 → … → 完了）", "レビュー待ち", "bogus"} {
		_, err := s.ListChallenges(context.Background(), ListOptions{Status: strPtr(status)})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("status=%q: err = %v, want ErrValidation", status, err)
		}
	}
}

func TestListChallenges_EmptyResultIsNonNilEmptySlice(t *testing.T) {
	s := newStoreForTest(t)
	list, err := s.ListChallenges(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListChallenges() error = %v", err)
	}
	if list == nil {
		t.Fatal("list is nil, want a non-nil empty slice")
	}
	if len(list) != 0 {
		t.Fatalf("len(list) = %d, want 0", len(list))
	}
}

// setChallengeStatus はテスト用フィクスチャとして課題の状態を直接書き換える
// （#9 は状態遷移コマンドを持たないため、list --status のフィルタ検証・done 状態の
// edit 拒否の検証に使う）。
func setChallengeStatus(t *testing.T, s *Store, id int64, status Status) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = ? WHERE id = ?`, string(status), id)
		return err
	}); err != nil {
		t.Fatalf("setChallengeStatus: %v", err)
	}
}

func TestEditChallenge_ChangesFieldsAndBumpsVersion(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t", Description: "d", DoneCriteria: "dc"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}

	edited, err := s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{Title: strPtr("t2")})
	if err != nil {
		t.Fatalf("EditChallenge() error = %v", err)
	}
	if edited.Title != "t2" {
		t.Errorf("Title = %q, want %q", edited.Title, "t2")
	}
	if edited.Version != 2 {
		t.Errorf("Version = %d, want 2", edited.Version)
	}
	if !edited.UpdatedAt.After(c.UpdatedAt) && !edited.UpdatedAt.Equal(c.UpdatedAt) {
		t.Errorf("UpdatedAt did not advance: before=%v after=%v", c.UpdatedAt, edited.UpdatedAt)
	}
	if edited.Description != "d" || edited.DoneCriteria != "dc" {
		t.Errorf("unrelated fields changed: description=%q done_criteria=%q", edited.Description, edited.DoneCriteria)
	}
}

// レビュー指摘: 「UpdatedAt が巻き戻っていない」ことしか検査しない緩い assertion
// では、EditChallenge が updated_at の更新自体を落としても検出できない
// （no-op edit のテストが Equal を求めるのと対称的に、値を変える edit は
// 厳密に「新しい時刻に進む」ことを固定する）。Store.now フックで時刻を
// 固定・進行させ、UpdatedAt がその注入した時刻に一致することを直接検証する。
func TestEditChallenge_UpdatedAtAdvancesToTheInjectedEditTime(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	createTime := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return createTime }
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}

	editTime := createTime.Add(1 * time.Hour)
	s.now = func() time.Time { return editTime }
	edited, err := s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{Title: strPtr("t2")})
	if err != nil {
		t.Fatalf("EditChallenge() error = %v", err)
	}
	if !edited.UpdatedAt.Equal(editTime) {
		t.Fatalf("UpdatedAt = %v, want %v (the injected clock's edit-time value)", edited.UpdatedAt, editTime)
	}
	if !edited.CreatedAt.Equal(createTime) {
		t.Fatalf("CreatedAt = %v, want %v (unchanged from create)", edited.CreatedAt, createTime)
	}
}

func TestEditChallenge_AllFieldsIncludingUrgency(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}

	edited, err := s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{
		Description:  strPtr("d2"),
		DoneCriteria: strPtr("dc2"),
		Urgency:      strPtr(string(UrgencyLow)),
	})
	if err != nil {
		t.Fatalf("EditChallenge() error = %v", err)
	}
	if edited.Description != "d2" || edited.DoneCriteria != "dc2" {
		t.Errorf("fields not updated: %+v", edited)
	}
	if edited.Urgency == nil || *edited.Urgency != UrgencyLow {
		t.Errorf("Urgency = %v, want %q", edited.Urgency, UrgencyLow)
	}
}

func TestEditChallenge_InvalidUrgencyIsValidationErrorAndChangesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}

	_, err = s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{Urgency: strPtr("bogus")})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}

	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Version != 1 {
		t.Fatalf("Version = %d, want 1 (unchanged)", after.Version)
	}
}

// レビュー指摘: EditChallenge がタイトルの必須検証を欠いていたため、
// `edit --title ""`（または空白のみ）が成功し、CreateChallenge が保証する
// 「タイトルは必須」という不変条件を edit 経由で壊せてしまっていた。
func TestEditChallenge_EmptyOrWhitespaceTitleIsValidationErrorAndChangesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}

	for _, title := range []string{"", "   ", "\t\n"} {
		_, err := s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{Title: strPtr(title)})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("title=%q: err = %v, want ErrValidation", title, err)
		}
	}

	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Title != "t" || after.Version != 1 {
		t.Fatalf("challenge changed despite validation failure: title=%q version=%d", after.Title, after.Version)
	}
}

func TestEditChallenge_NotFoundForMissingOrMalformedID(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	for _, id := range []string{"C-999", "OP-1", "foo"} {
		_, err := s.EditChallenge(context.Background(), ChannelCLI, id, EditInput{Title: strPtr("x")})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("id=%q: err = %v, want ErrNotFound", id, err)
		}
	}
}

func TestEditChallenge_TerminalStateForDoneChallenge(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	cid, _ := parseChallengeID(c.ID)
	setChallengeStatus(t, s, cid, StatusDone)

	_, err = s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{Title: strPtr("x")})
	if !errors.Is(err, ErrTerminalState) {
		t.Fatalf("err = %v, want ErrTerminalState", err)
	}

	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Title != "t" {
		t.Fatalf("Title changed to %q despite terminal state", after.Title)
	}
}

// AC-66: 値を変えない edit は終了コード0で終わり、作業ログにエントリを追加しない。
func TestEditChallenge_NoopWhenValueUnchangedDoesNotBumpVersionOrLog(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}

	edited, err := s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{Title: strPtr("t")})
	if err != nil {
		t.Fatalf("EditChallenge() error = %v", err)
	}
	if edited.Version != 1 {
		t.Fatalf("Version = %d, want 1 (no-op should not bump)", edited.Version)
	}
	if !edited.UpdatedAt.Equal(c.UpdatedAt) {
		t.Fatalf("UpdatedAt changed on no-op edit: before=%v after=%v", c.UpdatedAt, edited.UpdatedAt)
	}

	activities, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	if len(activities) != 1 {
		t.Fatalf("len(activities) = %d, want 1 (only the create entry, no edit entry)", len(activities))
	}
}

func TestEditChallenge_RecordsOnlyChangedFieldsInActivityLog(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t", Description: "d"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}

	if _, err := s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{Title: strPtr("t2")}); err != nil {
		t.Fatalf("EditChallenge() error = %v", err)
	}

	activities, err := s.ListActivities(context.Background(), strPtr(c.ID))
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	if len(activities) != 2 {
		t.Fatalf("len(activities) = %d, want 2 (create + edit)", len(activities))
	}
	edit := activities[1]
	if edit.Action != "edit" {
		t.Fatalf("Action = %q, want %q", edit.Action, "edit")
	}
	var before, after map[string]any
	if err := json.Unmarshal(edit.Before, &before); err != nil {
		t.Fatalf("unmarshal Before: %v", err)
	}
	if err := json.Unmarshal(edit.After, &after); err != nil {
		t.Fatalf("unmarshal After: %v", err)
	}
	if _, ok := before["description"]; ok {
		t.Errorf("Before contains unchanged field description: %+v", before)
	}
	if before["title"] != "t" || after["title"] != "t2" {
		t.Errorf("Before/After title = %v/%v, want t/t2", before["title"], after["title"])
	}
	if after["version"] != float64(2) {
		t.Errorf("After[version] = %v, want 2", after["version"])
	}
}

// AC-68: 作業ログへの書き込みを失敗させると、対象の変更も残らない（edit 版）。
func TestEditChallenge_ActivityInsertFailureRollsBackEdit(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}

	s.insertActivity = func(_ *sql.Tx, _ activityRow) error {
		return errors.New("boom: injected activity insert failure")
	}

	_, err = s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{Title: strPtr("t2")})
	if err == nil {
		t.Fatal("EditChallenge() error = nil, want an error from the injected failure")
	}

	s.insertActivity = nil
	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Title != "t" || after.Version != 1 {
		t.Fatalf("edit was not rolled back: title=%q version=%d", after.Title, after.Version)
	}
}

func TestListActivities_OrdersOldestFirst(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	if _, err := s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{Title: strPtr("t2")}); err != nil {
		t.Fatalf("EditChallenge() error = %v", err)
	}
	if _, err := s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{Title: strPtr("t3")}); err != nil {
		t.Fatalf("EditChallenge() error = %v", err)
	}

	activities, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	if len(activities) != 3 {
		t.Fatalf("len(activities) = %d, want 3", len(activities))
	}
	wantActions := []string{"create", "edit", "edit"}
	for i, want := range wantActions {
		if activities[i].Action != want {
			t.Errorf("activities[%d].Action = %q, want %q", i, activities[i].Action, want)
		}
	}
}

func TestListActivities_FiltersByChallengeID(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c1, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "a"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	c2, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "b"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	if _, err := s.EditChallenge(context.Background(), ChannelCLI, c1.ID, EditInput{Title: strPtr("a2")}); err != nil {
		t.Fatalf("EditChallenge() error = %v", err)
	}

	activities, err := s.ListActivities(context.Background(), strPtr(c2.ID))
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	if len(activities) != 1 {
		t.Fatalf("len(activities) = %d, want 1", len(activities))
	}
	if activities[0].EntityID != c2.ID {
		t.Fatalf("EntityID = %q, want %q", activities[0].EntityID, c2.ID)
	}
}

// #13: log <C-ID> は、その課題自身のエントリだけでなく、その課題が持つ
// 不可逆操作（operation）のエントリ（op add・単独の承認・差し戻し）も含む。
// operation は challenge_id で従属するエンティティであり、他の課題の
// operation のエントリは含めない。
func TestListActivities_FiltersByChallengeIDIncludesItsOperations(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c1, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "a"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	c2, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "b"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	op1, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: c1.ID, Kind: "release", Summary: "s1"})
	if err != nil {
		t.Fatalf("CreateOperation() c1 error = %v", err)
	}
	if _, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: c2.ID, Kind: "release", Summary: "s2"}); err != nil {
		t.Fatalf("CreateOperation() c2 error = %v", err)
	}

	activities, err := s.ListActivities(context.Background(), strPtr(c1.ID))
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	// create(challenge) + op_add(operation) の2件。c2 の operation は含まない。
	if len(activities) != 2 {
		t.Fatalf("len(activities) = %d, want 2: %+v", len(activities), activities)
	}
	var sawChallengeCreate, sawOperationAdd bool
	for _, a := range activities {
		switch {
		case a.Entity == "challenge" && a.EntityID == c1.ID && a.Action == "create":
			sawChallengeCreate = true
		case a.Entity == "operation" && a.EntityID == op1.ID && a.Action == "op_add":
			sawOperationAdd = true
		default:
			t.Errorf("unexpected activity entry: %+v", a)
		}
	}
	if !sawChallengeCreate || !sawOperationAdd {
		t.Errorf("activities = %+v, want both a challenge create entry and an operation op_add entry", activities)
	}
}

func TestListActivities_NotFoundForMissingOrMalformedChallengeID(t *testing.T) {
	s := newStoreForTest(t)
	for _, id := range []string{"C-999", "OP-1", "foo"} {
		_, err := s.ListActivities(context.Background(), strPtr(id))
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("id=%q: err = %v, want ErrNotFound", id, err)
		}
	}
}

package core

// 分けるかの保留への回答（split・single）と、保留を開いたままの取り下げを、実ストアで検証する
// （docs/features/request-to-plan-s3.md）。AC の番号は親の Issue の受入基準の並び順。

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type sliceAnswerFixture struct {
	*j10Fixture
	// id は分けるかの保留で人間対応待ちの元の課題、key はその取り込み元の外部キー。
	id, key string
	cid     int64
}

// newSliceAnswerFixture は、優先度 P1・緊急度 高・起票者 reporter1 の取り込んだ課題を、区切り 3 つ（2 は 1 の後、3 は 1・2 の後）の
// 分ける案で、分けるかの保留にする。
func newSliceAnswerFixture(t *testing.T) *sliceAnswerFixture {
	t.Helper()
	f := newJ10Fixture(t)
	id := f.pending(t)
	cid := mustChallengeInternalID(t, id)
	f.write(t, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET reporter = 'reporter1' WHERE id = ?`, cid)
		return err
	})
	f.judge(t, applySplitOutput(t), nil)
	if got := f.splitState(t, id); got != SliceSplitStateAwaitingOwner {
		t.Fatalf("slice_split.state = %q, want awaiting_owner", got)
	}
	return &sliceAnswerFixture{j10Fixture: f, id: id, key: "o/app#" + id[2:], cid: cid}
}

func (f *sliceAnswerFixture) answer(t *testing.T, option, note string) (*Challenge, *Hold, error) {
	t.Helper()
	p, err := f.s.PrepareAnswerOption(context.Background(), f.id, note, option, false)
	if err != nil {
		t.Fatalf("PrepareAnswerOption: %v", err)
	}
	return f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: p.Version, Option: option, Answer: note},
		relayedAttestation(t, f.id, "sess", "分けて"))
}

func (f *sliceAnswerFixture) mustAnswer(t *testing.T, option, note string) (*Challenge, *Hold) {
	t.Helper()
	c, h, err := f.answer(t, option, note)
	if err != nil {
		t.Fatalf("answer %s: %v", option, err)
	}
	return c, h
}

func (f *sliceAnswerFixture) evidencePath() string {
	return filepath.Join(f.s.Workspace(), filepath.FromSlash(sliceSplitEvidencePath(f.cid)))
}

func (f *sliceAnswerFixture) wantEvidenceGone(t *testing.T) {
	t.Helper()
	if _, err := os.Lstat(f.evidencePath()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("evidence still exists: %v", err)
	}
}

// created は split で作った 3 つの区切りの課題の詳細（区切りの順）。
func (f *sliceAnswerFixture) created(t *testing.T) []*ChallengeDetail {
	t.Helper()
	d := f.detail(t, f.id)
	var out []*ChallengeDetail
	for _, sl := range d.SliceSplit.Slices {
		if sl.ChallengeID == "" {
			t.Fatalf("slice %q has no challenge_id", sl.Title)
		}
		out = append(out, f.detail(t, sl.ChallengeID))
	}
	return out
}

// AC-104〜115・118・119・121〜124。
func TestSliceSplitAnswer_SplitCreatesTheRequirementAndTheSliceChallenges(t *testing.T) {
	t.Parallel()
	f := newSliceAnswerFixture(t)
	original := f.detail(t, f.id)
	c, _ := f.mustAnswer(t, "split", "")

	d := f.detail(t, f.id)
	r := d.Requirement
	if r == nil || f.count(t, `SELECT COUNT(*) FROM requirement`) != 1 { // AC-104
		t.Fatalf("requirement = %+v", r)
	}
	if r.Origin != RequirementOriginIssue || r.ExternalKey == nil || *r.ExternalKey != f.key ||
		r.URL == nil || *r.URL != "https://github.com/o/app/issues/"+f.id[2:] || r.Title != original.Title ||
		r.OriginChallengeID == nil || *r.OriginChallengeID != f.id ||
		r.EvidencePath != ".flywheel/requirements/split-"+f.id+".md" || r.ResolvedAt != nil { // AC-104
		t.Errorf("requirement = %+v", r.Requirement)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM challenge WHERE origin = 'split'`); n != 3 { // AC-105・107
		t.Errorf("split challenges = %d, want 3", n)
	}
	for i, got := range f.created(t) {
		n := []string{"1", "2", "3"}[i]
		if got.Title != "TITLE-"+n || got.Description != "SUMMARY-"+n || got.DoneCriteria != "SCOPE-"+n { // AC-106
			t.Errorf("slice %s = %q %q %q", n, got.Title, got.Description, got.DoneCriteria)
		}
		if got.Status != StatusClassified { // AC-108
			t.Errorf("slice %s status = %s", n, got.Status)
		}
		if got.Priority == nil || *got.Priority != Priority("P1") { // AC-109
			t.Errorf("slice %s priority = %v", n, got.Priority)
		}
		if got.Urgency == nil || *got.Urgency != Urgency("高") { // AC-110
			t.Errorf("slice %s urgency = %v", n, got.Urgency)
		}
		if got.Reporter != "reporter1" { // AC-111
			t.Errorf("slice %s reporter = %q", n, got.Reporter)
		}
		if got.Headline == nil || *got.Headline != "HEADLINE-"+n { // AC-112
			t.Errorf("slice %s headline = %v", n, got.Headline)
		}
		if got.Context == nil || *got.Context != "CONTEXT-"+n { // AC-113
			t.Errorf("slice %s context = %v", n, got.Context)
		}
		if got.Requirement == nil || got.Requirement.ID != r.ID { // AC-114
			t.Errorf("slice %s requirement = %+v", n, got.Requirement)
		}
		if got.SourceBinding != nil { // AC-115
			t.Errorf("slice %s source binding = %+v", n, got.SourceBinding)
		}
		if got.SliceSplit != nil {
			t.Errorf("slice %s slice_split = %+v", n, got.SliceSplit)
		}
	}
	if c.Status != StatusWithdrawn || d.Status != StatusWithdrawn { // AC-118
		t.Errorf("original status = %s / %s", c.Status, d.Status)
	}
	if c.Version != d.Version {
		t.Errorf("returned version = %d, stored %d", c.Version, d.Version)
	}
	if d.SourceBinding == nil || d.SourceBinding.ExternalKey != f.key { // AC-119
		t.Errorf("source binding = %+v", d.SourceBinding)
	}
	withdraws := f.activities(t, "withdraw")
	if len(withdraws) != 1 || withdraws[0]["reason"] != "区切りの課題に分けた（要求 "+r.ID+"）" { // AC-121・129
		t.Errorf("withdraw activities = %v", withdraws)
	}
	if d.SliceSplit.State != SliceSplitStateSplit { // AC-122
		t.Errorf("slice_split.state = %s", d.SliceSplit.State)
	}
	var wantIDs, gotIDs []string
	for _, ch := range r.Challenges {
		wantIDs = append(wantIDs, ch.ID)
	}
	for _, sl := range d.SliceSplit.Slices {
		gotIDs = append(gotIDs, sl.ChallengeID)
	}
	if !slices.Equal(gotIDs, wantIDs) { // AC-123（ID の昇順は区切りの順）
		t.Errorf("slices[].challenge_id = %v, want %v", gotIDs, wantIDs)
	}
	if d.SliceSplit.HoldID != nil || d.Holds[0].Open() { // AC-124
		t.Errorf("hold_id = %v, hold = %+v", d.SliceSplit.HoldID, d.Holds[0])
	}
	if f.count(t, `SELECT COUNT(*) FROM slice_split WHERE requirement_id IS NOT NULL AND proposal LIKE '%NOTE-A%'`) != 1 {
		t.Errorf("slice_split must keep the evidence of the proposal and point at the requirement")
	}
	if _, err := os.Stat(f.evidencePath()); err != nil {
		t.Errorf("the evidence of the requirement must remain: %v", err)
	}
}

// AC-116・117。
func TestSliceSplitAnswer_SplitMakesTheDependencies(t *testing.T) {
	t.Parallel()
	f := newSliceAnswerFixture(t)
	f.mustAnswer(t, "split", "")
	got := f.created(t)
	for i, want := range [][]string{nil, {got[0].ID}, {got[0].ID, got[1].ID}} {
		cid := mustChallengeInternalID(t, got[i].ID)
		if n := f.count(t, `SELECT COUNT(*) FROM challenge_dependency WHERE challenge_id = ?`, cid); n != len(want) {
			t.Errorf("slice %d dependencies = %d, want %d", i+1, n, len(want))
		}
		for _, dep := range want {
			if n := f.count(t, `SELECT COUNT(*) FROM challenge_dependency WHERE challenge_id = ? AND depends_on_id = ?`, cid, mustChallengeInternalID(t, dep)); n != 1 {
				t.Errorf("slice %d must depend on %s", i+1, dep)
			}
		}
	}
	if n := len(f.activities(t, ActivityActionDepAdd)); n != 2 {
		t.Errorf("dep_add activities = %d, want 2", n)
	}
}

// AC-120。
func TestSliceSplitAnswer_SplitIssueIsNotIngestableAgain(t *testing.T) {
	t.Parallel()
	f := newSliceAnswerFixture(t)
	f.mustAnswer(t, "split", "")
	source := excludeOthersSource("src", []string{"o/app"}, []string{"masanami"})
	// 同じ上流の一覧に、課題になっていない Issue も置く（一覧が空だから出ない、ではないことを確かめる）。
	got := listIngestable(t, f.s, &fakeUpstream{issues: map[string][]UpstreamIssue{"o/app": {ingIssue(f.key, "", "masanami"), ingIssue("o/app#999", "", "masanami")}}}, source)
	if keys := ingKeys(got); !slices.Equal(keys, []string{"o/app#999"}) {
		t.Errorf("ingestable = %v (error %v), want only o/app#999", keys, got.Error)
	}
}

// AC-125〜130。
func TestSliceSplitAnswer_SplitRecordsTheActivities(t *testing.T) {
	t.Parallel()
	f := newSliceAnswerFixture(t)
	before := len(f.activities(t, "create"))
	f.mustAnswer(t, "split", "")
	req := f.detail(t, f.id).Requirement.ID
	if n := len(f.activities(t, ActivityActionRequirementCreate)); n != 1 { // AC-125
		t.Errorf("requirement_create = %d", n)
	}
	creates := f.activities(t, "create")[before:]
	if len(creates) != 3 { // AC-126
		t.Fatalf("create activities = %d, want 3", len(creates))
	}
	for _, a := range creates {
		if a["origin"] != "split" || a["requirement_id"] != req { // AC-127・128
			t.Errorf("create after = %v", a)
		}
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'withdraw' AND entity = 'challenge' AND entity_id = ?`, f.cid); n != 1 { // AC-129
		t.Errorf("withdraw = %d", n)
	}
	if n := len(f.activities(t, ActivityActionSliceSplitDone)); n != 1 { // AC-130
		t.Errorf("slice_split_done = %d", n)
	}
}

// wantSplitNotApplied は split の回答が何も書いていないことを確かめる（AC-132〜135・137・138）。
func (f *sliceAnswerFixture) wantSplitNotApplied(t *testing.T) {
	t.Helper()
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM requirement`:                      0,
		`SELECT COUNT(*) FROM challenge`:                        1,
		`SELECT COUNT(*) FROM challenge_dependency`:             0,
		`SELECT COUNT(*) FROM activity WHERE action='withdraw'`: 0,
		`SELECT COUNT(*) FROM activity WHERE action='answer'`:   0,
	} {
		if n := f.count(t, query); n != want {
			t.Errorf("%s = %d, want %d", query, n, want)
		}
	}
	d := f.detail(t, f.id)
	if d.Status != StatusAwaitingHuman || !d.Holds[0].Open() {
		t.Errorf("original = %s hold = %+v", d.Status, d.Holds[0])
	}
	if d.SliceSplit.State != SliceSplitStateAwaitingOwner || d.SliceSplit.HoldID == nil {
		t.Errorf("slice_split = %+v", d.SliceSplit)
	}
	if _, err := os.Stat(f.evidencePath()); err != nil {
		t.Errorf("evidence must remain: %v", err)
	}
}

// AC-131〜135。2 件目の区切りの課題の作成を、ストアのトリガーで失敗させる。
func TestSliceSplitAnswer_SplitRollsBackWhenAWriteFails(t *testing.T) {
	t.Parallel()
	f := newSliceAnswerFixture(t)
	f.write(t, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER inject_failure BEFORE INSERT ON challenge WHEN NEW.title = 'TITLE-2' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		return err
	})
	if _, _, err := f.answer(t, "split", ""); err == nil { // AC-131
		t.Fatal("answer must fail")
	}
	f.wantSplitNotApplied(t)
}

// AC-136〜138。
func TestSliceSplitAnswer_SplitIsConflictWhileADelegationIsUnfinished(t *testing.T) {
	t.Parallel()
	for name, arrange := range map[string]func(t *testing.T, f *sliceAnswerFixture){
		"delegate run": func(t *testing.T, f *sliceAnswerFixture) {
			runID := insertActiveFakeRunForTest(t, f.s, f.id, judgmentJ1)
			f.write(t, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE run SET kind = 'delegate', judgment = NULL WHERE id = ?`, runID)
				return err
			})
		},
		"contract run": func(t *testing.T, f *sliceAnswerFixture) {
			f.write(t, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, max_budget_usd, started_at, observed_at)
					SELECT ?, 'c', 'x', MAX(id), 'working', '', 0, '2026-11-28T00:00:00.000Z', '2026-11-28T00:00:00.000Z' FROM run`, f.cid)
				return err
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSliceAnswerFixture(t)
			arrange(t, f)
			if _, _, err := f.answer(t, "split", ""); !errors.Is(err, ErrConflict) {
				t.Fatalf("err = %v, want ErrConflict", err)
			}
			f.wantSplitNotApplied(t)
		})
	}
}

// AC-139・140。
func TestSliceSplitAnswer_SplitRejectsTheApprovedOperation(t *testing.T) {
	t.Parallel()
	f := newSliceAnswerFixture(t)
	f.write(t, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at) VALUES (?, 'release', 'r', 'ref', 'approved', 1, '2026-11-28T00:00:00.000Z')`, f.cid)
		return err
	})
	f.mustAnswer(t, "split", "") // AC-139
	ops := f.detail(t, f.id).Operations
	if len(ops) != 1 || ops[0].State != OperationStateRejected { // AC-140
		t.Errorf("operations = %+v", ops)
	}
}

// AC-141・143・146。回答の本人確認だけで成立し（取り下げの本人確認を受けない経路 ui でも成立する）、添え書きは保留の回答の文に残る。
func TestSliceSplitAnswer_SplitNeedsOnlyTheAnswerVerification(t *testing.T) {
	t.Parallel()
	t.Run("relayed with a note", func(t *testing.T) {
		f := newSliceAnswerFixture(t)
		_, h := f.mustAnswer(t, "split", "NOTE")
		want := "この案で分ける\nNOTE"
		if got := f.detail(t, f.id).Holds[0].Answer; got == nil || *got != want || h.Answer == nil || *h.Answer != want { // AC-146
			t.Errorf("answer = %v", got)
		}
	})
	t.Run("ui", func(t *testing.T) {
		f := newSliceAnswerFixture(t)
		hold := f.detail(t, f.id).Holds[0]
		c, h, err := f.s.ExecuteAnswer(context.Background(),
			AnswerRequest{ChallengeID: f.id, Option: "split", HoldID: hold.HoldID, HoldCheck: hold.Check}, uiAttestationForTest(t, f.id))
		if err != nil || c.Status != StatusWithdrawn { // AC-141
			t.Fatalf("answer = %+v, %v", c, err)
		}
		if h.AnsweredChannel == nil || *h.AnsweredChannel != "ui" { // AC-143
			t.Errorf("channel = %v", h.AnsweredChannel)
		}
	})
}

// AC-144。画面の経路は、取り下げそのものを受けない。分けるかの保留でない保留の、識別が split の選択肢は、取り下げを起こさない。
func TestSliceSplitAnswer_ScreenStillRefusesOtherWithdrawals(t *testing.T) {
	t.Parallel()
	f := newSliceAnswerFixture(t)
	prev, err := f.s.PrepareWithdraw(context.Background(), f.id, "r")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ExecuteWithdraw(context.Background(), WithdrawRequest{ChallengeID: f.id, ExpectedVersion: prev.Version, Reason: "r"}, uiAttestationForTest(t, f.id))
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("ui withdraw = %v, want ErrVerificationRejected", err)
	}
	// 記録の hold_id を外すと、同じ選択肢を持つ保留は分けるかの保留ではない。
	hold := f.detail(t, f.id).Holds[0]
	f.write(t, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE slice_split SET hold_id = NULL WHERE challenge_id = ?`, f.cid)
		return err
	})
	c, _, err := f.s.ExecuteAnswer(context.Background(),
		AnswerRequest{ChallengeID: f.id, Option: "split", HoldID: hold.HoldID, HoldCheck: hold.Check}, uiAttestationForTest(t, f.id))
	if err != nil || c.Status != StatusClassified {
		t.Fatalf("answer = %+v, %v", c, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM requirement`); n != 0 {
		t.Errorf("requirements = %d", n)
	}
}

// AC-145・147〜152。
func TestSliceSplitAnswer_Single(t *testing.T) {
	t.Parallel()
	f := newSliceAnswerFixture(t)
	hold := f.detail(t, f.id).Holds[0]
	if _, _, err := f.s.ExecuteAnswer(context.Background(),
		AnswerRequest{ChallengeID: f.id, Option: "single", HoldID: hold.HoldID, HoldCheck: hold.Check}, uiAttestationForTest(t, f.id)); err != nil { // AC-145
		t.Fatal(err)
	}
	d := f.detail(t, f.id)
	if d.SliceSplit.State != SliceSplitStateSingle || d.SliceSplit.HoldID != nil { // AC-147・148
		t.Errorf("slice_split = %+v", d.SliceSplit)
	}
	if d.Status != StatusClassified { // AC-149
		t.Errorf("status = %s", d.Status)
	}
	var targets []int64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		targets, _, err = selectJ2AutoTargets(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(targets, f.cid) { // AC-150
		t.Errorf("plan targets = %v, want %d", targets, f.cid)
	}
	var byOwner int
	for _, a := range f.activities(t, ActivityActionSliceSplitSingle) {
		if a["by"] == "owner" {
			byOwner++
		}
	}
	if byOwner != 1 { // AC-151
		t.Errorf("slice_split_single by owner = %d", byOwner)
	}
	f.wantEvidenceGone(t) // AC-152
	if n := len(f.activities(t, ActivityActionRequirementEvidenceCleanupFailed)); n != 0 {
		t.Errorf("cleanup failed activities = %d", n)
	}
}

// AC-153。
func TestSliceSplitAnswer_SingleSucceedsWhenTheEvidenceCannotBeRemoved(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root は読み取りだけのディレクトリでも消せる")
	}
	f := newSliceAnswerFixture(t)
	dir := filepath.Dir(f.evidencePath())
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	c, _ := f.mustAnswer(t, "single", "")
	if c.Status != StatusClassified {
		t.Errorf("status = %s", c.Status)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = ? AND entity = 'challenge' AND entity_id = ?`,
		ActivityActionRequirementEvidenceCleanupFailed, f.cid); n != 1 {
		t.Errorf("cleanup failed activities = %d, want 1", n)
	}
}

// AC-154〜156。
func TestWithdraw_WhileTheSliceSplitHoldIsOpen_RemovesTheEvidence(t *testing.T) {
	t.Parallel()
	f := newSliceAnswerFixture(t)
	if _, err := withdrawForTest(t, f.s, f.id, "やめる"); err != nil {
		t.Fatal(err)
	}
	f.wantEvidenceGone(t) // AC-154
	d := f.detail(t, f.id)
	if d.SliceSplit.State != SliceSplitStateAwaitingOwner || d.SliceSplit.HoldID != nil { // AC-155・156
		t.Errorf("slice_split = %+v", d.SliceSplit)
	}
	if n := len(f.activities(t, ActivityActionRequirementEvidenceCleanupFailed)); n != 0 {
		t.Errorf("cleanup failed activities = %d", n)
	}
}

// 証跡がすでに無ければ、取り下げは何も残さない。
func TestWithdraw_WhileTheSliceSplitHoldIsOpen_MissingEvidenceIsNotAnError(t *testing.T) {
	t.Parallel()
	f := newSliceAnswerFixture(t)
	if err := os.Remove(f.evidencePath()); err != nil {
		t.Fatal(err)
	}
	if _, err := withdrawForTest(t, f.s, f.id, "やめる"); err != nil {
		t.Fatal(err)
	}
	if n := len(f.activities(t, ActivityActionRequirementEvidenceCleanupFailed)); n != 0 {
		t.Errorf("cleanup failed activities = %d", n)
	}
}

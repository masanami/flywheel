package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// このファイルは、親が直接行った作業を完了として記録する道（課題 #452）の core の振る舞いを確かめる。
// 保留の種類（parent_direct）・遷移 T17・成果物の参照・自動の対象からの除外・承認後の完了・
// status と関係グラフの区分を、実ストアで固定する。

func holdAsParentDirect(t *testing.T, s *Store, from Status) *Challenge {
	t.Helper()
	c := createAndAdvance(t, s, from)
	h, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "親が直接行う", Kind: string(HoldKindParentDirect)})
	if err != nil {
		t.Fatalf("HoldChallenge(parent_direct) error = %v", err)
	}
	if h.Status != StatusAwaitingHuman {
		t.Fatalf("status after hold = %q, want awaiting_human", h.Status)
	}
	return h
}

func completeDirectInput(refs ...string) CompleteDirectInput {
	return CompleteDirectInput{Artifacts: refs}
}

// --- 遷移表（T17） ---

func TestTable_T17_CompleteDirectFromAwaitingHuman(t *testing.T) {
	tr, ok := Lookup(StatusAwaitingHuman, OpCompleteDirect)
	if !ok || tr.ID != "T17" || tr.Target.Fixed != StatusAwaitingCompletionApproval || tr.Target.ReturnToPrecedingHoldStatus || tr.RequiresVerification {
		t.Fatalf("Lookup(awaiting_human, complete_direct) = %+v, ok=%v, want T17 -> awaiting_completion_approval without verification", tr, ok)
	}
	for _, e := range StatusVocabulary {
		if e.Code == StatusAwaitingHuman {
			continue
		}
		if _, ok := Lookup(e.Code, OpCompleteDirect); ok {
			t.Errorf("Lookup(%q, complete_direct) is defined, want only awaiting_human", e.Code)
		}
	}
}

// --- 保留の種類 ---

func TestHoldChallenge_KindDefaultsToHumanAndAcceptsParentDirect(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	a := createAndAdvance(t, s, StatusClassified)
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, a.ID, HoldInput{Question: "q"}); err != nil {
		t.Fatalf("HoldChallenge(default) error = %v", err)
	}
	b := holdAsParentDirect(t, s, StatusClassified)

	da, _ := s.GetChallenge(context.Background(), a.ID)
	db, _ := s.GetChallenge(context.Background(), b.ID)
	if len(da.Holds) != 1 || da.Holds[0].Kind != HoldKindHuman {
		t.Errorf("default hold kind = %+v, want human", da.Holds)
	}
	if len(db.Holds) != 1 || db.Holds[0].Kind != HoldKindParentDirect {
		t.Errorf("parent_direct hold kind = %+v, want parent_direct", db.Holds)
	}
}

func TestHoldChallenge_UnknownKindIsValidationErrorAndChangesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusClassified)
	_, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "q", Kind: "somebody"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	d, _ := s.GetChallenge(context.Background(), c.ID)
	if d.Status != StatusClassified || d.Version != 1 || len(d.Holds) != 0 {
		t.Errorf("challenge changed despite the rejection: %s v%d holds=%d", d.Status, d.Version, len(d.Holds))
	}
}

// 回答（T12）は従来どおり、種類が親が直接行うの保留からも保留の前の状態へ戻る。
func TestAnswer_ReturnsFromParentDirectHoldToPrecedingStatus(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdAsParentDirect(t, s, StatusClassified)
	res := dispatchOperation(t, s, c.ID, OpAnswer)
	if res.err != nil || res.status != StatusClassified {
		t.Fatalf("answer = %+v, want back to classified", res)
	}
}

// --- CompleteDirect ---

func TestCompleteDirect_MovesToAwaitingCompletionApprovalRecordsArtifactsAndClosesHold(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdAsParentDirect(t, s, StatusClassified)
	note := "案 B を実際に動かして確かめた"

	got, dc, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, CompleteDirectInput{
		Artifacts: []string{"https://github.com/o/r/issues/337#issuecomment-1", "  o/r#12  "},
		Note:      &note,
	})
	if err != nil {
		t.Fatalf("CompleteDirect() error = %v", err)
	}
	if got.Status != StatusAwaitingCompletionApproval || got.Version != c.Version+1 {
		t.Errorf("challenge = %s v%d, want awaiting_completion_approval v%d", got.Status, got.Version, c.Version+1)
	}
	if len(dc.Artifacts) != 2 || dc.Artifacts[0] != "https://github.com/o/r/issues/337#issuecomment-1" || dc.Artifacts[1] != "o/r#12" {
		t.Errorf("artifacts = %q (trimmed, in the given order)", dc.Artifacts)
	}
	if dc.Note == nil || *dc.Note != note {
		t.Errorf("note = %v, want %q", dc.Note, note)
	}

	d, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Holds) != 1 || d.Holds[0].Open() || d.Holds[0].Answer != nil || d.Holds[0].ClosedAt == nil {
		t.Errorf("hold = %+v, want closed without an answer", d.Holds)
	}
	if len(d.DirectCompletions) != 1 || len(d.DirectCompletions[0].Artifacts) != 2 {
		t.Errorf("detail direct completions = %+v", d.DirectCompletions)
	}
	var found bool
	for _, a := range activitiesFor(t, s, c.ID) {
		if a.Action == string(OpCompleteDirect) {
			found = true
			if a.Channel != string(ChannelCLI) || a.Verification != string(VerificationNone) {
				t.Errorf("activity channel/verification = %s/%s, want cli/none", a.Channel, a.Verification)
			}
		}
	}
	if !found {
		t.Error("no complete_direct activity entry")
	}
	runs, err := s.ListRuns(context.Background(), RunListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("runs = %d, want 0 (J5 must not be started)", len(runs))
	}
}

func TestCompleteDirect_NoteIsOptional(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdAsParentDirect(t, s, StatusUnclassified)
	_, dc, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1"))
	if err != nil {
		t.Fatalf("CompleteDirect() error = %v", err)
	}
	if dc.Note != nil {
		t.Errorf("note = %v, want nil", *dc.Note)
	}
}

func TestCompleteDirect_RejectsWithoutAnArtifactAndChangesNothing(t *testing.T) {
	for name, in := range map[string]CompleteDirectInput{
		"none":       {},
		"blank only": {Artifacts: []string{"  ", ""}},
		"one blank":  {Artifacts: []string{"https://example.com/1", " "}},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := holdAsParentDirect(t, s, StatusClassified)
			_, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, in)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("err = %v, want ErrValidation", err)
			}
			d, _ := s.GetChallenge(context.Background(), c.ID)
			if d.Status != StatusAwaitingHuman || d.Version != c.Version || !d.Holds[0].Open() || len(d.DirectCompletions) != 0 {
				t.Errorf("challenge changed despite the rejection: %s v%d", d.Status, d.Version)
			}
		})
	}
}

func TestCompleteDirect_RejectsAHumanHold(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusClassified)
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "オーナーに聞く"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1"))
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
	d, _ := s.GetChallenge(context.Background(), c.ID)
	if d.Status != StatusAwaitingHuman || !d.Holds[0].Open() || len(d.DirectCompletions) != 0 {
		t.Errorf("challenge changed despite the rejection: %s", d.Status)
	}
}

// 種類は「いま開いている保留」で決める。以前の保留が親が直接行うでも、いまの保留が人の対応を待つなら使えない。
func TestCompleteDirect_UsesTheOpenHoldNotAnEarlierOne(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdAsParentDirect(t, s, StatusClassified)
	if res := dispatchOperation(t, s, c.ID, OpAnswer); res.err != nil {
		t.Fatal(res.err)
	}
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "今度は人に聞く"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1"))
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

func TestCompleteDirect_RejectsEveryOtherStatus(t *testing.T) {
	for _, e := range StatusVocabulary {
		if e.Code == StatusAwaitingHuman {
			continue
		}
		t.Run(string(e.Code), func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := createAndAdvance(t, s, e.Code)
			_, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1"))
			want := ErrInvalidTransition
			if IsTerminal(Table, StatusVocabulary, e.Code) {
				want = ErrTerminalState
			}
			if !errors.Is(err, want) {
				t.Errorf("err = %v, want %v", err, want)
			}
			d, _ := s.GetChallenge(context.Background(), c.ID)
			if d.Status != e.Code || d.Version != 1 || len(d.DirectCompletions) != 0 {
				t.Errorf("challenge changed despite the rejection: %s v%d", d.Status, d.Version)
			}
		})
	}
}

func TestCompleteDirect_UnknownChallengeIsNotFound(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	for _, id := range []string{"C-99", "nonsense"} {
		if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, id, completeDirectInput("https://example.com/1")); !errors.Is(err, ErrNotFound) {
			t.Errorf("CompleteDirect(%q) err = %v, want ErrNotFound", id, err)
		}
	}
}

// --- 自動の計画・委譲・検証の対象にならない ---

func autoTargetsForTest(t *testing.T, s *Store) (j1, j2, deleg, j5 []int64) {
	t.Helper()
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		if j1, _, err = selectJ1AutoTargets(context.Background(), tx); err != nil {
			return err
		}
		if j2, _, err = selectJ2AutoTargets(context.Background(), tx); err != nil {
			return err
		}
		if deleg, _, err = selectDelegationTargets(context.Background(), tx); err != nil {
			return err
		}
		j5, _, err = selectJ5AutoTargets(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatalf("select targets: %v", err)
	}
	return
}

func TestParentDirectChallenge_IsNeverAnAutoTargetWhileHeldNorAfterCompleteDirect(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	// 分類済から保留に置いた課題（回答で戻せば J2 の対象になる状態）。
	c := holdAsParentDirect(t, s, StatusClassified)
	// 比較用: 保留にしていない分類済の課題は J2 の対象になる（対象の選び方が働いていることの確認）。
	other := createAndAdvance(t, s, StatusClassified)

	j1, j2, deleg, j5 := autoTargetsForTest(t, s)
	otherID, _ := parseChallengeID(other.ID)
	cid, _ := parseChallengeID(c.ID)
	if len(j2) != 1 || j2[0] != otherID {
		t.Fatalf("J2 targets while held = %v, want only %d", j2, otherID)
	}
	for name, ids := range map[string][]int64{"J1": j1, "delegation": deleg, "J5": j5} {
		for _, id := range ids {
			if id == cid {
				t.Errorf("%s targets (held) include %d", name, cid)
			}
		}
	}

	if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1")); err != nil {
		t.Fatal(err)
	}
	j1, j2, deleg, j5 = autoTargetsForTest(t, s)
	for name, ids := range map[string][]int64{"J1": j1, "J2": j2, "delegation": deleg, "J5": j5} {
		for _, id := range ids {
			if id == cid {
				t.Errorf("%s targets (awaiting completion approval) include %d", name, cid)
			}
		}
	}
}

// --- 完了の承認と、承認後の完了 ---

func approveCompletionForTest(t *testing.T, s *Store, id string) {
	t.Helper()
	prev, err := s.PrepareApproval(context.Background(), id)
	if err != nil {
		t.Fatalf("PrepareApproval: %v", err)
	}
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: id, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved, HoldRelease: true,
	}, verifiedAttestationForTest(t, id)); err != nil {
		t.Fatalf("ExecuteApproval: %v", err)
	}
}

func TestApproval_SummaryCarriesTheDirectCompletionArtifactsAlongsideDoneCriteria(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t", DoneCriteria: "結果が Issue に書かれている"})
	if err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(c.ID)
	setChallengeStatus(t, s, cid, StatusClassified)
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "親が直接行う", Kind: "parent_direct"}); err != nil {
		t.Fatal(err)
	}
	note := "メモ"
	if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, CompleteDirectInput{Artifacts: []string{"https://example.com/a", "o/r#3"}, Note: &note}); err != nil {
		t.Fatal(err)
	}

	p, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != ApprovalKindCompletion || p.DoneCriteria != "結果が Issue に書かれている" {
		t.Fatalf("preview kind/done criteria = %v/%q", p.Kind, p.DoneCriteria)
	}
	if p.DirectCompletion == nil || len(p.DirectCompletion.Artifacts) != 2 || p.DirectCompletion.Artifacts[1] != "o/r#3" ||
		p.DirectCompletion.Note == nil || *p.DirectCompletion.Note != "メモ" {
		t.Errorf("preview direct completion = %+v", p.DirectCompletion)
	}
}

func TestApproval_OrdinaryCompletionHasNoDirectCompletion(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingCompletionApproval)
	p, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.DirectCompletion != nil {
		t.Errorf("DirectCompletion = %+v, want nil", p.DirectCompletion)
	}
}

// 差し戻されて別の道（検証の met＝T8）で再び完了確認待ちに来たとき、前の直接行った記録を承認の根拠として見せない。
// 親が直接した完了の差し戻しは「親が直接やる」保留へ戻る（#481）ので、回答で保留の前の状態へ戻してから進める。
func TestApproval_StaleDirectCompletionIsNotShownAfterRejectionAndReentry(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdAsParentDirect(t, s, StatusInProgress)
	if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1")); err != nil {
		t.Fatal(err)
	}
	if res := dispatchOperation(t, s, c.ID, OpReject); res.err != nil || res.status != StatusAwaitingHuman {
		t.Fatalf("reject = %+v, want awaiting_human", res)
	}
	if res := dispatchOperation(t, s, c.ID, OpAnswer); res.err != nil || res.status != StatusInProgress {
		t.Fatalf("answer = %+v, want in_progress", res)
	}
	if _, err := s.SubmitChallenge(context.Background(), ChannelCLI, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyChallenge(context.Background(), ChannelCLI, c.ID, VerifyInput{Result: "met"}); err != nil {
		t.Fatal(err)
	}
	p, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.DirectCompletion != nil {
		t.Errorf("DirectCompletion = %+v after re-entry through verify, want nil", p.DirectCompletion)
	}
	if completionDirectForTest(t, s, c.ID) {
		t.Error("completion_direct = true after entering through T8, want false")
	}
	// 記録そのものは残る（詳細には出る）。
	d, _ := s.GetChallenge(context.Background(), c.ID)
	if len(d.DirectCompletions) != 1 {
		t.Errorf("detail direct completions = %d, want 1", len(d.DirectCompletions))
	}
}

func TestApproval_AfterApprovalItIsAnOrdinaryCompletionCountedAndMarkedDirect(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdAsParentDirect(t, s, StatusClassified)
	if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1")); err != nil {
		t.Fatal(err)
	}
	approveCompletionForTest(t, s, c.ID)

	d, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusDone {
		t.Fatalf("status = %q, want done", d.Status)
	}
	if len(d.DirectCompletions) != 1 || d.DirectCompletions[0].Artifacts[0] != "https://example.com/1" {
		t.Errorf("direct completion mark after approval = %+v", d.DirectCompletions)
	}

	// 普段の完了と同じに数える（完了の承認を受けた課題として metrics の対象になり、完了の承認の発生が残る）。
	m, err := s.Metrics(context.Background(), MetricsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if m.TargetChallenges != 1 {
		t.Errorf("metrics target challenges = %d, want 1", m.TargetChallenges)
	}
	if m.Occurrences.ByKind[string(OccurrenceKindCompletionApproved)] != 1 {
		t.Errorf("completion_approved occurrences = %v, want 1", m.Occurrences.ByKind)
	}
}

// --- J6 の入力 ---

func TestJ6Input_CarriesTheDirectMarkAndArtifactsInsteadOfRuns(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdAsParentDirect(t, s, StatusClassified)
	note := "親が手で確かめた"
	if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, CompleteDirectInput{Artifacts: []string{"https://example.com/result", "o/r#9"}, Note: &note}); err != nil {
		t.Fatal(err)
	}
	approveCompletionForTest(t, s, c.ID)

	cid, _ := parseChallengeID(c.ID)
	jc, err := s.loadJ6Context(context.Background(), cid)
	if err != nil {
		t.Fatal(err)
	}
	var section *JudgmentDataSection
	for _, sec := range prepareJ6(jc, "") {
		if sec.Label == j6DirectCompletionLabel {
			sec := sec
			section = &sec
		}
	}
	if section == nil {
		t.Fatalf("J6 input has no %q section", j6DirectCompletionLabel)
	}
	for _, want := range []string{"親が直接行った", "https://example.com/result", "o/r#9", "親が手で確かめた"} {
		if !strings.Contains(section.Content, want) {
			t.Errorf("section lacks %q:\n%s", want, section.Content)
		}
	}
}

func TestJ6Input_OrdinaryChallengeHasNoDirectCompletionSection(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusDone)
	cid, _ := parseChallengeID(c.ID)
	jc, err := s.loadJ6Context(context.Background(), cid)
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range prepareJ6(jc, "") {
		if sec.Label == j6DirectCompletionLabel {
			t.Errorf("unexpected %q section for an ordinary challenge", sec.Label)
		}
	}
}

// --- status・カード・関係グラフ ---

func TestOverview_ParentDirectHoldIsParentAgentsTurnNotNeedsHuman(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	direct := holdAsParentDirect(t, s, StatusClassified)
	human := createAndAdvance(t, s, StatusClassified)
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, human.ID, HoldInput{Question: "聞く"}); err != nil {
		t.Fatal(err)
	}

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := func(cs []Challenge) []string {
		out := []string{}
		for _, c := range cs {
			out = append(out, c.ID)
		}
		return out
	}
	if got := ids(ov.ParentAgentChallenges); len(got) != 1 || got[0] != direct.ID {
		t.Errorf("ParentAgentChallenges = %v, want [%s]", got, direct.ID)
	}
	if got := ids(ov.NeedsHumanChallenges); len(got) != 1 || got[0] != human.ID {
		t.Errorf("NeedsHumanChallenges = %v, want [%s]", got, human.ID)
	}
	for _, c := range ov.ParentAgentChallenges {
		if c.Status != StatusAwaitingHuman {
			t.Errorf("status of %s = %q, want awaiting_human (no new state)", c.ID, c.Status)
		}
	}
}

func TestOverview_ParentDirectChallengeLeavesParentAgentSectionWhenCompletedDirectly(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdAsParentDirect(t, s, StatusClassified)
	if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1")); err != nil {
		t.Fatal(err)
	}
	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.ParentAgentChallenges) != 0 {
		t.Errorf("ParentAgentChallenges = %v, want none", ov.ParentAgentChallenges)
	}
	if len(ov.NeedsHumanChallenges) != 1 || ov.NeedsHumanChallenges[0].Status != StatusAwaitingCompletionApproval {
		t.Errorf("NeedsHumanChallenges = %v, want the awaiting_completion_approval challenge", ov.NeedsHumanChallenges)
	}
}

func TestListUnansweredHolds_ExcludesParentDirectHolds(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	holdAsParentDirect(t, s, StatusClassified)
	human := createAndAdvance(t, s, StatusClassified)
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, human.ID, HoldInput{Question: "聞く"}); err != nil {
		t.Fatal(err)
	}
	hs, err := s.ListUnansweredHolds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 1 || hs[0].ChallengeID != human.ID {
		t.Errorf("unanswered holds = %+v, want only %s", hs, human.ID)
	}
}

func TestCard_ParentDirectHoldHasNoAnswerQuestionAction(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	direct := holdAsParentDirect(t, s, StatusClassified)
	human := createAndAdvance(t, s, StatusClassified)
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, human.ID, HoldInput{Question: "聞く"}); err != nil {
		t.Fatal(err)
	}
	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cd, err := s.DeriveCard(context.Background(), mustChallenge(t, s, direct.ID), ov, s.currentTime())
	if err != nil {
		t.Fatal(err)
	}
	if len(cd.NextHumanActions) != 0 || cd.State != StatusAwaitingHuman {
		t.Errorf("direct card = state %q actions %v, want awaiting_human with no actions", cd.State, cd.NextHumanActions)
	}
	ch, err := s.DeriveCard(context.Background(), mustChallenge(t, s, human.ID), ov, s.currentTime())
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.NextHumanActions) != 1 || ch.NextHumanActions[0] != CardActionAnswerQuestion {
		t.Errorf("human card actions = %v, want [answer_question]", ch.NextHumanActions)
	}
}

func TestChallengeGraph_ParentDirectHoldIsParentAgentCategory(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	direct := holdAsParentDirect(t, s, StatusClassified)
	human := createAndAdvance(t, s, StatusClassified)
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, human.ID, HoldInput{Question: "聞く"}); err != nil {
		t.Fatal(err)
	}
	done := createAndAdvance(t, s, StatusDone)
	withdrawn := createAndAdvance(t, s, StatusWithdrawn)
	_ = createAndAdvance(t, s, StatusClassified) // 関係を持たない課題

	g, err := s.ChallengeGraph(context.Background(), s.currentTime())
	if err != nil {
		t.Fatal(err)
	}
	cat := map[string]GraphCategory{}
	state := map[string]Status{}
	for _, n := range g.Nodes {
		cat[n.ID], state[n.ID] = n.Category, n.State
	}
	if cat[direct.ID] != GraphCategoryParentAgent || state[direct.ID] != StatusAwaitingHuman {
		t.Errorf("direct node = %s/%s, want parent_agent/awaiting_human", cat[direct.ID], state[direct.ID])
	}
	if cat[human.ID] != GraphCategoryHuman {
		t.Errorf("human-hold node category = %s, want human", cat[human.ID])
	}
	if cat[done.ID] != GraphCategoryDone || cat[withdrawn.ID] != GraphCategoryWithdrawn {
		t.Errorf("done/withdrawn categories = %s/%s", cat[done.ID], cat[withdrawn.ID])
	}
}

// 親が直接行う保留の課題に、ほかの人間の操作待ち（不可逆操作の承認など）があれば、区分は人間の操作待ちが先に当たる。
func TestChallengeGraph_HumanActionOutranksParentAgent(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	direct := holdAsParentDirect(t, s, StatusClassified)
	if _, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: direct.ID, Kind: "other", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	g, err := s.ChallengeGraph(context.Background(), s.currentTime())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range g.Nodes {
		if n.ID == direct.ID {
			found = true
			if n.Category != GraphCategoryHuman {
				t.Errorf("category = %s, want human (a pending operation needs the owner)", n.Category)
			}
		}
	}
	if !found {
		t.Fatalf("node %s missing from the graph", direct.ID)
	}
}

func TestGraphCategoryValues_IncludesParentAgent(t *testing.T) {
	var found bool
	for _, c := range GraphCategoryValues() {
		found = found || c == GraphCategoryParentAgent
	}
	if !found {
		t.Errorf("GraphCategoryValues() = %v lacks parent_agent", GraphCategoryValues())
	}
}

func mustChallenge(t *testing.T, s *Store, id string) Challenge {
	t.Helper()
	d, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge(%s): %v", id, err)
	}
	return d.Challenge
}

// --- 親が直接した完了の差し戻し（T15 の 2 つの戻り先。#481） ---

// completionDirectForTest は challenge.completion_direct（いまの完了確認待ちが T17 から来たか）を読む。
func completionDirectForTest(t *testing.T, s *Store, id string) bool {
	t.Helper()
	cid, ok := parseChallengeID(id)
	if !ok {
		t.Fatalf("bad id %q", id)
	}
	var v int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT completion_direct FROM challenge WHERE id = ?`, cid).Scan(&v)
	}); err != nil {
		t.Fatalf("read completion_direct: %v", err)
	}
	if v != 0 && v != 1 {
		t.Fatalf("completion_direct = %d, want 0 or 1", v)
	}
	return v == 1
}

// rejectAfterForTest は課題の最後の reject の作業ログの after を返す（無ければ nil）。
func rejectAfterForTest(t *testing.T, s *Store, id string) map[string]any {
	t.Helper()
	var out map[string]any
	for _, a := range activitiesFor(t, s, id) {
		if a.Action != string(OpReject) {
			continue
		}
		out = map[string]any{}
		if err := json.Unmarshal(a.After, &out); err != nil {
			t.Fatalf("reject activity after is not JSON: %v", err)
		}
	}
	return out
}

// rejectWithReasonForTest は完了確認待ちの課題を理由つきで差し戻す（本人確認つきの 2 段階をまとめたもの）。
func rejectWithReasonForTest(t *testing.T, s *Store, id, reason string) *Challenge {
	t.Helper()
	prev, err := s.PrepareRejection(context.Background(), id, reason)
	if err != nil {
		t.Fatalf("PrepareRejection: %v", err)
	}
	c, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: id, ExpectedVersion: prev.Version, Decision: ApprovalDecisionRejected, Reason: &reason,
	}, verifiedAttestationForTest(t, id))
	if err != nil {
		t.Fatalf("ExecuteApproval(reject): %v", err)
	}
	return c
}

func TestTable_T15_HasADirectCompletionTargetOfAwaitingHuman(t *testing.T) {
	tr, ok := Lookup(StatusAwaitingCompletionApproval, OpReject)
	if !ok || tr.ID != "T15" || !tr.RequiresVerification {
		t.Fatalf("Lookup(awaiting_completion_approval, reject) = %+v, ok=%v", tr, ok)
	}
	plain, err := tr.Target.ResolveForCompletionDirect(Table, NoStatus, false)
	if err != nil || plain != StatusInProgress {
		t.Errorf("ResolveForCompletionDirect(false) = %q, %v, want in_progress", plain, err)
	}
	direct, err := tr.Target.ResolveForCompletionDirect(Table, NoStatus, true)
	if err != nil || direct != StatusAwaitingHuman {
		t.Errorf("ResolveForCompletionDirect(true) = %q, %v, want awaiting_human", direct, err)
	}
	// 通常の Resolve は従来どおり（既存の呼び出し元が見る遷移先は変わらない）。
	if got, err := tr.Target.Resolve(Table, NoStatus); err != nil || got != StatusInProgress {
		t.Errorf("Resolve = %q, %v, want in_progress", got, err)
	}
	// ほかの行は、真偽に関わらず従来の遷移先。
	for _, row := range Table {
		if row.ID == "T15" {
			continue
		}
		if row.Target.IfCompletionDirect != NoStatus {
			t.Errorf("%s has IfCompletionDirect = %q, want only T15", row.ID, row.Target.IfCompletionDirect)
		}
	}
}

// ① 親が直接した完了の差し戻しは、着手中ではなく「親が直接やる」保留（人間対応待ち）へ戻る。保留を立てた元の状態に
// よらず同じで（②）、戻した保留の from_status は元の保留の from_status、問いの文は差し戻しの理由（③）。
func TestReject_OfADirectCompletionReturnsToAParentDirectHold(t *testing.T) {
	for _, from := range []Status{StatusUnclassified, StatusClassified, StatusInProgress, StatusVerifying} {
		t.Run(string(from), func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := holdAsParentDirect(t, s, from)
			if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1")); err != nil {
				t.Fatal(err)
			}
			reason := "成果物が完了条件を満たしていない。\n3 点目を直してやり直す"

			got := rejectWithReasonForTest(t, s, c.ID, reason)
			if got.Status != StatusAwaitingHuman {
				t.Fatalf("status after reject = %q, want awaiting_human", got.Status)
			}
			d, err := s.GetChallenge(context.Background(), c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if d.Status != StatusAwaitingHuman || d.Version != got.Version {
				t.Errorf("stored challenge = %s v%d, returned v%d", d.Status, d.Version, got.Version)
			}
			if len(d.Holds) != 2 {
				t.Fatalf("holds = %d, want 2 (the closed original and the new one)", len(d.Holds))
			}
			if d.Holds[0].Open() || d.Holds[0].Answer != nil {
				t.Errorf("original hold = %+v, want still closed without an answer", d.Holds[0])
			}
			h := d.Holds[1]
			if !h.Open() || h.Kind != HoldKindParentDirect || h.Question != reason || h.FromStatus != from || h.Answer != nil {
				t.Errorf("new hold = open %v kind %q question %q from %q, want open parent_direct %q from %q", h.Open(), h.Kind, h.Question, h.FromStatus, reason, from)
			}

			// 親のエージェントが動く番（status の parent_agent 区分）で、周の自動の対象には選ばれない。
			ov, err := s.GetOverview(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(ov.ParentAgentChallenges) != 1 || ov.ParentAgentChallenges[0].ID != c.ID {
				t.Errorf("ParentAgentChallenges = %v, want [%s]", ov.ParentAgentChallenges, c.ID)
			}
			cid, _ := parseChallengeID(c.ID)
			j1, j2, deleg, j5 := autoTargetsForTest(t, s)
			for name, ids := range map[string][]int64{"J1": j1, "J2": j2, "delegation": deleg, "J5": j5} {
				for _, id := range ids {
					if id == cid {
						t.Errorf("%s targets include the rejected direct completion %d", name, cid)
					}
				}
			}
			if completionDirectForTest(t, s, c.ID) {
				t.Error("completion_direct = true after leaving awaiting_completion_approval, want false")
			}

			// 作業ログの reject の after は、戻り先の保留の種類を持つ。
			after := rejectAfterForTest(t, s, c.ID)
			if after == nil || after["hold_kind"] != string(HoldKindParentDirect) || after["status"] != string(StatusAwaitingHuman) || after["reason"] != reason {
				t.Errorf("reject activity after = %v, want status awaiting_human, hold_kind parent_direct and the reason", after)
			}
		})
	}
}

// ④ 差し戻した保留から、親が再び complete-direct で完了確認待ちへ進められ、承認の要約には新しい記録が出る。
// もう一度差し戻しても、同じ元の状態の保留へ戻る。
func TestReject_DirectCompletionCanBeCompletedAgainAndRejectedAgain(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdAsParentDirect(t, s, StatusClassified)
	if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/first")); err != nil {
		t.Fatal(err)
	}
	rejectWithReasonForTest(t, s, c.ID, "やり直し")

	got, dc, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/second"))
	if err != nil {
		t.Fatalf("CompleteDirect() after reject error = %v", err)
	}
	if got.Status != StatusAwaitingCompletionApproval || len(dc.Artifacts) != 1 || dc.Artifacts[0] != "https://example.com/second" {
		t.Fatalf("second completion = %s %+v", got.Status, dc)
	}
	p, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.DirectCompletion == nil || len(p.DirectCompletion.Artifacts) != 1 || p.DirectCompletion.Artifacts[0] != "https://example.com/second" {
		t.Errorf("preview direct completion = %+v, want the second record", p.DirectCompletion)
	}
	d, _ := s.GetChallenge(context.Background(), c.ID)
	if len(d.DirectCompletions) != 2 || len(d.Holds) != 2 || d.Holds[1].Open() {
		t.Errorf("direct completions = %d, holds = %+v, want 2 completions and the second hold closed", len(d.DirectCompletions), d.Holds)
	}

	// 2 回目の完了を差し戻しても、元の状態（分類済）を保った保留へ戻る。
	rejectWithReasonForTest(t, s, c.ID, "もう一度")
	d, _ = s.GetChallenge(context.Background(), c.ID)
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 3 || !d.Holds[2].Open() || d.Holds[2].FromStatus != StatusClassified || d.Holds[2].Question != "もう一度" {
		t.Errorf("after the second reject: %s holds=%+v", d.Status, d.Holds)
	}
	// 回答すれば、保留の前の状態（分類済）へ戻る。
	if res := dispatchOperation(t, s, c.ID, OpAnswer); res.err != nil || res.status != StatusClassified {
		t.Errorf("answer = %+v, want back to classified", res)
	}
}

// ⑤ 通常の完了（T8 由来）の差し戻しは従来どおり着手中へ戻り、保留は立たない。
func TestReject_OfAnOrdinaryCompletionStillReturnsToInProgressWithoutAHold(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusVerifying)
	if _, err := s.VerifyChallenge(context.Background(), ChannelCLI, c.ID, VerifyInput{Result: "met"}); err != nil {
		t.Fatal(err)
	}
	if completionDirectForTest(t, s, c.ID) {
		t.Fatal("completion_direct = true after T8, want false")
	}
	got := rejectWithReasonForTest(t, s, c.ID, "足りない")
	if got.Status != StatusInProgress {
		t.Fatalf("status after reject = %q, want in_progress", got.Status)
	}
	d, _ := s.GetChallenge(context.Background(), c.ID)
	if len(d.Holds) != 0 {
		t.Errorf("holds = %+v, want none", d.Holds)
	}
	after := rejectAfterForTest(t, s, c.ID)
	if after == nil {
		t.Fatal("no reject activity entry")
	}
	if _, ok := after["hold_kind"]; ok {
		t.Errorf("reject activity after has hold_kind: %v", after)
	}
}

// 別の課題が direct 由来でも、通常の課題の差し戻しには影響しない（値は課題ごと）。
func TestReject_DirectFlagIsPerChallenge(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	direct := holdAsParentDirect(t, s, StatusClassified)
	if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, direct.ID, completeDirectInput("https://example.com/1")); err != nil {
		t.Fatal(err)
	}
	plain := createAndAdvance(t, s, StatusAwaitingCompletionApproval)
	if res := dispatchOperation(t, s, plain.ID, OpReject); res.err != nil || res.status != StatusInProgress {
		t.Fatalf("plain reject = %+v, want in_progress", res)
	}
	if !completionDirectForTest(t, s, direct.ID) {
		t.Error("the direct challenge lost its flag")
	}
}

// ⑥ 値は T17 で真、ほかのすべての遷移で偽。完了確認待ちを出るとき（T13・T14・T15・T16）に消える。
func TestCompletionDirect_IsTrueOnlyAfterT17AndClearedWhenLeaving(t *testing.T) {
	enter := func(t *testing.T, s *Store) *Challenge {
		t.Helper()
		c := holdAsParentDirect(t, s, StatusClassified)
		if completionDirectForTest(t, s, c.ID) {
			t.Fatal("completion_direct = true while only held")
		}
		if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1")); err != nil {
			t.Fatal(err)
		}
		if !completionDirectForTest(t, s, c.ID) {
			t.Fatal("completion_direct = false after T17, want true")
		}
		return c
	}
	for name, leave := range map[string]func(t *testing.T, s *Store, id string){
		"T13 approve":              func(t *testing.T, s *Store, id string) { dispatchOperation(t, s, id, OpApprove) },
		"T14 approve hold-release": func(t *testing.T, s *Store, id string) { dispatchOperation(t, s, id, OpApproveHoldRelease) },
		"T15 reject":               func(t *testing.T, s *Store, id string) { dispatchOperation(t, s, id, OpReject) },
		"T16 withdraw":             func(t *testing.T, s *Store, id string) { dispatchOperation(t, s, id, OpWithdraw) },
	} {
		t.Run(name, func(t *testing.T) {
			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := enter(t, s)
			leave(t, s, c.ID)
			d, _ := s.GetChallenge(context.Background(), c.ID)
			if d.Status == StatusAwaitingCompletionApproval {
				t.Fatalf("still in awaiting_completion_approval")
			}
			if completionDirectForTest(t, s, c.ID) {
				t.Errorf("completion_direct = true after leaving through %s (now %s), want false", name, d.Status)
			}
		})
	}

	t.Run("T8 sets false", func(t *testing.T) {
		s := newStoreForTest(t)
		fixedActor(t, "alice")
		c := createAndAdvance(t, s, StatusVerifying)
		if _, err := s.VerifyChallenge(context.Background(), ChannelCLI, c.ID, VerifyInput{Result: "met"}); err != nil {
			t.Fatal(err)
		}
		if completionDirectForTest(t, s, c.ID) {
			t.Error("completion_direct = true after T8, want false")
		}
	})

	// 値は遷移の外の書き込み（edit）で変わらない。
	t.Run("edit keeps the value", func(t *testing.T) {
		s := newStoreForTest(t)
		fixedActor(t, "alice")
		c := enter(t, s)
		title := "題名を直した"
		if _, err := s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{Title: &title}); err != nil {
			t.Fatal(err)
		}
		if !completionDirectForTest(t, s, c.ID) {
			t.Error("edit cleared completion_direct")
		}
		p, err := s.PrepareApproval(context.Background(), c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if p.DirectCompletion == nil {
			t.Error("preview lost the direct completion after an edit")
		}
	})
}

// 承認の要約が「いまの完了確認待ちが T17 から来たか」を見るのは、作業ログではなく課題の記録（#481）。
// 作業ログの行が無くても（取り込んだ・手で直したストア）、値が真なら記録を見せる。
func TestApproval_DirectCompletionFollowsTheRecordNotTheActivityLog(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdAsParentDirect(t, s, StatusClassified)
	if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1")); err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(c.ID)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = 'complete_direct'`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.DirectCompletion == nil {
		t.Error("DirectCompletion = nil, want the record (the flag is true)")
	}
}

// ストアの書き出し・再構築（M5 S1）で値が往復する。
func TestExportImport_CarriesTheCompletionDirectFlag(t *testing.T) {
	src, _ := newWorkspaceStore(t)
	fixedActor(t, "alice")
	c := holdAsParentDirect(t, src, StatusClassified)
	if _, _, err := src.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1")); err != nil {
		t.Fatal(err)
	}
	dst, _ := newWorkspaceStore(t)
	if _, err := dst.Import(context.Background(), mustExport(t, src)); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !completionDirectForTest(t, dst, c.ID) {
		t.Error("completion_direct = false after the round trip, want true")
	}
	p, err := dst.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.DirectCompletion == nil {
		t.Error("preview has no direct completion after the round trip")
	}
	// 差し戻しも、再構築したストアで同じ戻り先になる。
	got := rejectWithReasonForTest(t, dst, c.ID, "やり直し")
	if got.Status != StatusAwaitingHuman {
		t.Errorf("status after reject in the imported store = %q, want awaiting_human", got.Status)
	}
}

// 計画承認待ちの課題で、親が直接行う保留を立て、完了として記録し、差し戻されたら親が直接行う保留へ戻る
// （#519）。承認を待っている計画は一連の流れを通して残る。
func TestPlanApproval_ParentDirectHoldCompleteDirectAndRejectKeepThePlan(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	cid, _ := parseChallengeID(c.ID)
	insertPlanRow(t, s, cid, 1, "plan body")
	planRows := func() int {
		d, err := s.GetChallenge(context.Background(), c.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(d.Plans)
	}

	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "親が直接行う", Kind: string(HoldKindParentDirect)}); err != nil {
		t.Fatalf("hold: %v", err)
	}
	got, dc, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1"))
	if err != nil {
		t.Fatalf("CompleteDirect: %v", err)
	}
	if got.Status != StatusAwaitingCompletionApproval || len(dc.Artifacts) != 1 {
		t.Fatalf("status=%q artifacts=%v", got.Status, dc.Artifacts)
	}

	// 差し戻すと、子に任され直さず親が直接行う保留へ戻る。
	rej := rejectWithReasonForTest(t, s, c.ID, "やり直す")
	if rej.Status != StatusAwaitingHuman {
		t.Fatalf("status after reject = %q, want awaiting_human", rej.Status)
	}
	d, _ := s.GetChallenge(context.Background(), c.ID)
	open := d.Holds[len(d.Holds)-1]
	if !open.Open() || open.Kind != HoldKindParentDirect || open.FromStatus != StatusAwaitingPlanApproval {
		t.Fatalf("open hold = %+v, want an open parent_direct hold from awaiting_plan_approval", open)
	}
	if planRows() != 1 {
		t.Errorf("plan rows = %d, want 1 (the pending plan must be kept)", planRows())
	}
}

// 計画承認待ちから立てた保留を回答で解くと、計画承認待ちへ戻り、承認待ちの計画は残る。
func TestPlanApproval_AnswerReturnsToPlanApprovalAndKeepsThePlan(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	cid, _ := parseChallengeID(c.ID)
	insertPlanRow(t, s, cid, 1, "plan body")
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "親が直接行う", Kind: string(HoldKindParentDirect)}); err != nil {
		t.Fatal(err)
	}
	if res := dispatchOperation(t, s, c.ID, OpAnswer); res.err != nil {
		t.Fatalf("answer: %v", res.err)
	}
	d, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusAwaitingPlanApproval || len(d.Plans) != 1 {
		t.Fatalf("status=%q plans=%d, want awaiting_plan_approval with 1 plan", d.Status, len(d.Plans))
	}
}

// 計画承認待ちから親が直接行う保留を立てた課題を作る（承認待ちの計画を 1 版持つ）。
func holdFromPlanApprovalForTest(t *testing.T, s *Store) *Challenge {
	t.Helper()
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "plan body")
	h, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "親が直接行う", Kind: string(HoldKindParentDirect)})
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	return h
}

// 計画承認待ちから立てた親が直接行う保留のあいだは、計画の承認（T5）は invalid_transition で断られ、
// 状態・計画・作業ログが変わらない（#519）。
func TestPlanApproval_ParentDirectHoldBlocksPlanApprovalAndChangesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdFromPlanApprovalForTest(t, s)
	before, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	actsBefore := len(activitiesFor(t, s, c.ID))

	if _, err := s.PrepareApproval(context.Background(), c.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("PrepareApproval while held: err = %v, want ErrInvalidTransition", err)
	}
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: before.Version, Decision: ApprovalDecisionApproved,
	}, verifiedAttestationForTest(t, c.ID)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ExecuteApproval while held: err = %v, want ErrInvalidTransition", err)
	}

	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusAwaitingHuman || after.Version != before.Version || len(after.Plans) != 1 || len(after.Approvals) != 0 {
		t.Fatalf("after = status %q version %d plans %d approvals %d, want unchanged (awaiting_human, v%d, 1 plan, 0 approvals)",
			after.Status, after.Version, len(after.Plans), len(after.Approvals), before.Version)
	}
	if got := len(activitiesFor(t, s, c.ID)); got != actsBefore {
		t.Errorf("activities = %d, want %d (unchanged)", got, actsBefore)
	}
}

// 計画承認待ちから立てた親が直接行う保留のあいだも、親が直接行った完了を差し戻して保留へ戻った後も、
// 委譲と自動の計画（J2）の対象に選ばれない（選び方は変えず、既存の関数で確かめる。#519）。
// 保留中と差し戻し後の課題は、委譲の対象になりうる承認済みで spec つきの計画を持たせ、状態だけが
// 外れる理由になるようにする。
func TestPlanApproval_ParentDirectHoldIsNotADelegationOrJ2TargetBeforeAndAfterReject(t *testing.T) {
	f := newDelegateFixture(t)
	s := f.s
	fixedActor(t, "alice")
	ctx := context.Background()
	// 承認済みで spec つきの計画 v1 を持つ課題を計画承認待ちへ置き（計画 v1 の承認の後に作り直した計画を
	// 待っている形）、そこから親が直接行う保留を立てる。
	holdFromPlanApprovalWithApprovedSpec := func(title string) string {
		t.Helper()
		id := f.newInProgress(t, title, "P1", planSpec(nil))
		cid, _ := parseChallengeID(id)
		if err := s.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE challenge SET status = 'awaiting_plan_approval', version = version + 1 WHERE id = ?`, cid)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		insertPlanRowForTest(t, s, id, 2, "plan v2")
		if _, err := s.HoldChallenge(ctx, ChannelCLI, id, HoldInput{Question: "親が直接行う", Kind: string(HoldKindParentDirect)}); err != nil {
			t.Fatalf("hold: %v", err)
		}
		return id
	}
	held := holdFromPlanApprovalWithApprovedSpec("held")
	rejected := holdFromPlanApprovalWithApprovedSpec("rejected")
	if _, _, err := s.CompleteDirect(ctx, ChannelCLI, rejected, completeDirectInput("https://example.com/1")); err != nil {
		t.Fatal(err)
	}
	if r := rejectWithReasonForTest(t, s, rejected, "やり直す"); r.Status != StatusAwaitingHuman {
		t.Fatalf("status after reject = %q, want awaiting_human", r.Status)
	}
	// 比較用（選び方が働いていることの確認）: 保留にしていない分類済の課題は J2 の対象になり、
	// 同じ形の計画を持つ着手中の課題は委譲の対象になる。
	other := createAndAdvance(t, s, StatusClassified)
	otherID, _ := parseChallengeID(other.ID)
	inProgress := f.newInProgress(t, "in-progress", "P1", planSpec(nil))
	inProgressID, _ := parseChallengeID(inProgress)

	_, j2, deleg, _ := autoTargetsForTest(t, s)
	if len(j2) != 1 || j2[0] != otherID {
		t.Fatalf("J2 targets = %v, want only %d", j2, otherID)
	}
	if len(deleg) != 1 || deleg[0] != inProgressID {
		t.Fatalf("delegation targets = %v, want only %d", deleg, inProgressID)
	}
	for _, id := range []string{held, rejected} {
		cid, _ := parseChallengeID(id)
		for name, ids := range map[string][]int64{"J2": j2, "delegation": deleg} {
			for _, got := range ids {
				if got == cid {
					t.Errorf("%s targets include %s", name, id)
				}
			}
		}
	}
}

// 計画承認待ちから立てた親が直接行う保留のあいだは計画の承認の依頼（承認の要約・カードの approve_plan）が
// 出ず、回答で計画承認待ちへ戻ると再び出て、承認（T5）すると保留の前から残っていた計画のまま着手中へ進む（#519）。
func TestPlanApproval_AnswerRestoresTheApprovalRequestAndApprovalProceedsWithTheKeptPlan(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdFromPlanApprovalForTest(t, s)
	hasApprovePlan := func() bool {
		t.Helper()
		cur, err := s.GetChallenge(context.Background(), c.ID)
		if err != nil {
			t.Fatal(err)
		}
		card, err := s.DeriveCard(context.Background(), cur.Challenge, &Overview{}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range card.NextHumanActions {
			if a == CardActionApprovePlan {
				return true
			}
		}
		return false
	}
	if hasApprovePlan() {
		t.Fatal("card offers approve_plan while held, want none")
	}

	if res := dispatchOperation(t, s, c.ID, OpAnswer); res.err != nil {
		t.Fatalf("answer: %v", res.err)
	}
	if !hasApprovePlan() {
		t.Fatal("card does not offer approve_plan after the answer")
	}
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval after the answer: %v", err)
	}
	if prev.Kind != ApprovalKindPlan || prev.PlanVersion != 1 || prev.PlanBody != "plan body" {
		t.Fatalf("preview = kind %q plan v%d %q, want the kept plan v1", prev.Kind, prev.PlanVersion, prev.PlanBody)
	}
	got, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved, ReleaseHeads: headsForAll(prev),
	}, verifiedAttestationForTest(t, c.ID))
	if err != nil {
		t.Fatalf("ExecuteApproval: %v", err)
	}
	if got.Status != StatusInProgress {
		t.Fatalf("status after plan approval = %q, want in_progress", got.Status)
	}
	d, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Plans) != 1 || d.Plans[0].Version != 1 {
		t.Errorf("plans = %+v, want only the kept v1", d.Plans)
	}
}

// 計画承認待ちから来て計画が残っている課題が、親が直接行った完了で完了確認待ちにあるとき、完了の承認の要約は
// 計画を持たず（計画は計画の承認のときだけ）、親が直接行った完了の記録を持つ（#519。承認画面の API の形への
// 変換は internal/view の TestFromApprovalPreview_CompletionWithDirectCompletionHasNoPlan が確かめる）。
func TestPlanApproval_CompletionSummaryAfterCompleteDirectHasDirectCompletionAndNoPlan(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdFromPlanApprovalForTest(t, s)
	if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1")); err != nil {
		t.Fatal(err)
	}
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev.Kind != ApprovalKindCompletion || prev.PlanVersion != 0 || prev.PlanBody != "" {
		t.Errorf("preview = kind %q plan v%d %q, want completion without a plan", prev.Kind, prev.PlanVersion, prev.PlanBody)
	}
	if prev.DirectCompletion == nil || len(prev.DirectCompletion.Artifacts) != 1 || prev.DirectCompletion.Artifacts[0] != "https://example.com/1" {
		t.Errorf("DirectCompletion = %+v, want the recorded artifact https://example.com/1", prev.DirectCompletion)
	}
}

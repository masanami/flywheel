package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
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

// 差し戻されて別の道で再び完了確認待ちに来たとき、前の直接行った記録を承認の根拠として見せない。
func TestApproval_StaleDirectCompletionIsNotShownAfterRejectionAndReentry(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := holdAsParentDirect(t, s, StatusClassified)
	if _, _, err := s.CompleteDirect(context.Background(), ChannelCLI, c.ID, completeDirectInput("https://example.com/1")); err != nil {
		t.Fatal(err)
	}
	if res := dispatchOperation(t, s, c.ID, OpReject); res.err != nil || res.status != StatusInProgress {
		t.Fatalf("reject = %+v, want in_progress", res)
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

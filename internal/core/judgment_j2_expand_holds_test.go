package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// 区切り S4c-1 の 7: 足す依頼から立つ 2 つの保留（task_add_over_limit・task_add_question）。実ストアと偽の判断の IF で確かめる。

// overLimitOutput は、見込み 10 + 5 = 15 が上限 14 を超える add の出力。
func overLimitOutput() []byte {
	return []byte(expandAddOutput(func(ts []map[string]any) {
		ts[0]["budget_impl_usd"], ts[0]["budget_review_usd"] = 3.0, 2.0
	}))
}

func (f *paFixture) lastHold(t *testing.T) (Hold, string) {
	t.Helper()
	d := f.detail(t, f.id)
	if len(d.Holds) == 0 {
		t.Fatal("no hold")
	}
	var reason string
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		var r sql.NullString
		if err := tx.QueryRow(`SELECT reason FROM hold ORDER BY id DESC LIMIT 1`).Scan(&r); err != nil {
			return err
		}
		reason = r.String
		return nil
	})
	return d.Holds[len(d.Holds)-1], reason
}

func TestExpandHold_AddOverTheLimitHoldsTheChallengeAndWaitsForTheOwner(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: overLimitOutput()})

	if n := len(taskRows(t, f.s, f.cid())); n != 2 {
		t.Errorf("tasks = %d, want 2 (no task row is made)", n)
	}
	d := f.detail(t, f.id)
	if d.Status != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", d.Status)
	}
	h, reason := f.lastHold(t)
	if reason != string(holdReasonTaskAddOverLimit) || !h.Open() || h.FromStatus != StatusInProgress {
		t.Errorf("hold = %+v reason %q, want an open task_add_over_limit hold from in_progress", h, reason)
	}
	wantLastHoldScopeNull(t, f.s) // AC-6
	adds := f.additions(t)
	if len(adds) != 1 || adds[0].State != TaskAdditionAwaitingOwner || adds[0].HoldID == nil || adds[0].Spec == nil {
		t.Fatalf("additions = %+v, want one awaiting_owner with spec and hold", adds)
	}
	var holdInt int64
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM hold ORDER BY id DESC LIMIT 1`).Scan(&holdInt)
	})
	if *adds[0].HoldID != holdInt {
		t.Errorf("hold_id = %d, want %d", *adds[0].HoldID, holdInt)
	}
	if adds[0].TotalAfterUSD == nil || *adds[0].TotalAfterUSD != 15 || adds[0].LimitUSD == nil || *adds[0].LimitUSD != 14 {
		t.Errorf("recorded total %v limit %v, want 15 / 14", adds[0].TotalAfterUSD, adds[0].LimitUSD)
	}
}

func TestExpandHold_OverLimitQuestionStatesTheAmountsAndTheReplanConstraint(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: overLimitOutput()})
	h, _ := f.lastHold(t)
	for _, want := range []string{"続き c", "実装 $3", "レビュー対応 $2", "足した後の見込み: $15", "上限: $14", "超える額: $1", "動いている作業が終わるまで、計画の作り直しは選べない"} {
		if !strings.Contains(h.Question, want) {
			t.Errorf("question lacks %q:\n%s", want, h.Question)
		}
	}
}

func TestExpandHold_OverLimitOptionsAreAddSkipReplanAndAnAnswerWithoutOneIsRefused(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: overLimitOutput()})
	h, _ := f.lastHold(t)
	var ids []string
	for _, o := range h.Options {
		ids = append(ids, o.ID)
		if o.ID == "add" && !strings.Contains(o.Description, "$15") {
			t.Errorf("add description %q lacks the raised limit $15", o.Description)
		}
	}
	if strings.Join(ids, ",") != "add,skip,replan" || !h.OptionRequired {
		t.Errorf("options = %v required %v, want add,skip,replan and required", ids, h.OptionRequired)
	}
}

func TestExpandHold_AskHoldsWithTheQuestionAndKeepsTheAdditionPending(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"ask","question":"どちらの方針で足しますか"}`)})
	if st := f.detail(t, f.id).Status; st != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", st)
	}
	h, reason := f.lastHold(t)
	if reason != string(holdReasonTaskAddQuestion) || h.Question != "どちらの方針で足しますか" || len(h.Options) != 0 || h.OptionRequired {
		t.Errorf("hold = %+v reason %q, want the plain question without options", h, reason)
	}
	wantLastHoldScopeNull(t, f.s) // AC-4
	adds := f.additions(t)
	if len(adds) != 1 || adds[0].State != TaskAdditionPending || adds[0].HoldID == nil {
		t.Fatalf("additions = %+v, want pending with hold_id", adds)
	}
	if n := len(taskRows(t, f.s, f.cid())); n != 2 {
		t.Errorf("tasks = %d, want 2", n)
	}
}

func TestExpandHold_AskWithOptionsMakesAnOptionQuestion(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"ask","question":"どちら","options":["A案","B案"]}`)})
	h, _ := f.lastHold(t)
	if len(h.Options) != 2 || h.Options[0].Label != "A案" || h.Options[1].Label != "B案" || h.OptionRequired {
		t.Errorf("options = %+v required %v, want 2 labelled options, not required", h.Options, h.OptionRequired)
	}
}

func TestExpandHold_ABadOptionLabelDropsTheOptionsButKeepsTheQuestion(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"ask","question":"どちら","options":["A案",""]}`)})
	h, reason := f.lastHold(t)
	if reason != string(holdReasonTaskAddQuestion) || len(h.Options) != 0 {
		t.Errorf("hold = %+v reason %q, want a plain question hold", h, reason)
	}
}

func TestExpandHold_AnInvalidAddOutputStillMakesNoHold(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(expandAddOutput(func(ts []map[string]any) { ts[0]["repo"] = "other" }))})
	if st := f.detail(t, f.id).Status; st != StatusInProgress {
		t.Errorf("status = %s, want in_progress", st)
	}
	if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionPending || adds[0].HoldID != nil {
		t.Errorf("additions = %+v, want the untouched pending one", adds)
	}
}

// 不変条件: オーナー待ちの足す依頼は、同じ課題の保留を指す。
func TestTaskAddition_AwaitingOwnerNeedsAHoldOfItsOwnChallenge(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	ctx := context.Background()
	ta, err := s.CreateTaskAddition(ctx, ChannelCLI, id, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: tasks[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTaskAdditionState(ctx, ChannelCLI, ta.ID, TaskAdditionAwaitingOwner, TaskAdditionUpdate{}); !errors.Is(err, ErrValidation) {
		t.Errorf("no hold: err = %v, want ErrValidation", err)
	}
	other, err := s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "別", Description: "d", DoneCriteria: "c"})
	if err != nil {
		t.Fatal(err)
	}
	ocid, _ := parseChallengeID(other.ID)
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (?, 'q', 'in_progress', '2026-11-01T00:00:00.000Z')`, ocid)
		return err
	})
	hold := int64(1)
	if _, err := s.SetTaskAdditionState(ctx, ChannelCLI, ta.ID, TaskAdditionAwaitingOwner, TaskAdditionUpdate{HoldID: &hold}); !errors.Is(err, ErrValidation) {
		t.Errorf("another challenge's hold: err = %v, want ErrValidation", err)
	}
}

// 判定していない足す依頼の「足した後の見込み」「上限」は null（nil）。判定した後は額。
func TestTaskAddition_UnjudgedValuesAreNilAndJudgedValuesAreAmounts(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"ask","question":"どちら"}`)})
	adds := f.additions(t)
	if len(adds) != 1 || adds[0].State != TaskAdditionPending || adds[0].TotalAfterUSD != nil || adds[0].LimitUSD != nil {
		t.Fatalf("additions = %+v, want a pending one with nil total/limit", adds)
	}
	g := expandDoneFixture(t)
	g.expand(t, &expandRecorder{out: overLimitOutput()})
	a := g.additions(t)[0]
	if a.TotalAfterUSD == nil || *a.TotalAfterUSD != 15 || a.LimitUSD == nil || *a.LimitUSD != 14 {
		t.Errorf("judged addition = %+v, want 15 / 14", a)
	}
}

// answerAsk は、開いている task_add_question の保留へ回答する（option が空なら文で答える）。
func (f *paFixture) answerAsk(t *testing.T, option, text string) {
	t.Helper()
	ctx := context.Background()
	prev, err := f.s.PrepareAnswerOption(ctx, f.id, text, option, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ExecuteAnswer(ctx, AnswerRequest{ChallengeID: f.id, ExpectedVersion: prev.Version, Answer: text, Option: option, Agent: f.agent, Conn: f.conn},
		relayedAttestation(t, f.id, "sess", "quote")); err != nil {
		t.Fatal(err)
	}
}

func holdRecordSection(t *testing.T, inv *expandRecorder) string {
	t.Helper()
	if inv.count() != 1 {
		t.Fatalf("judgment calls = %d, want 1", inv.count())
	}
	c := inv.calls[0]
	if len(c.Sections) == 0 || c.Sections[0].Label != callShapeSectionLabel || c.Sections[0].Content != callShapeExpand {
		t.Fatalf("first section = %+v, want the add call shape", c.Sections)
	}
	for _, s := range c.Sections {
		if s.Label == labelHoldRecord {
			return s.Content
		}
	}
	return ""
}

func TestExpandHold_AnsweredQuestionIsPassedToTheNextAddCall(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"ask","question":"どちらの方針で足しますか"}`)})
	f.answerAsk(t, "", "B 方針で足してください")
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	got := holdRecordSection(t, inv)
	if !strings.Contains(got, "問い: どちらの方針で足しますか") || !strings.Contains(got, "回答: B 方針で足してください") {
		t.Errorf("hold record section = %q, want the question and the answer", got)
	}
}

func TestExpandHold_ChosenOptionLabelIsPassedToTheNextAddCall(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"ask","question":"どちら","options":["A案","B案"]}`)})
	f.answerAsk(t, "o2", "")
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	if got := holdRecordSection(t, inv); !strings.Contains(got, "回答: B案") {
		t.Errorf("hold record section = %q, want the chosen label B案", got)
	}
}

func TestExpandHold_NoHoldRecordSectionWithoutAnAnsweredQuestion(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	if got := holdRecordSection(t, inv); got != "" {
		t.Errorf("hold record section = %q, want none", got)
	}
}

// 候補を選んだ後に課題の状態が変わっていたら、足す依頼は作られない（作る書き込みの中で確かめ直す）。
func TestCreateExpandAdditions_RecheckInTheWriteTransaction(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	var cands []expandAdditionCandidate
	readForTest(t, f.s, func(tx *sql.Tx) error {
		var err error
		cands, err = selectExpandAdditionCandidates(context.Background(), tx, f.agent)
		return err
	})
	if len(cands) != 1 {
		t.Fatalf("candidates = %v, want 1", cands)
	}
	f.setChallengeStatus(t, StatusVerifying) // 選んだ後に変わる
	n, err := f.s.createExpandAdditionsFrom(context.Background(), f.agent, cands)
	if err != nil || n != 0 {
		t.Fatalf("created = %d err %v, want 0", n, err)
	}
	if adds := f.additions(t); len(adds) != 0 {
		t.Errorf("additions = %+v, want none", adds)
	}
}

func TestExpandHold_AllAnsweredQuestionsOfTheAdditionArePassed(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"ask","question":"最初の問い"}`)})
	f.answerAsk(t, "", "最初の答え")
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"ask","question":"次の問い"}`)})
	f.answerAsk(t, "", "次の答え")
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	got := holdRecordSection(t, inv)
	if !strings.Contains(got, "最初の問い") || !strings.Contains(got, "最初の答え") || !strings.Contains(got, "次の問い") || !strings.Contains(got, "回答: 次の答え") {
		t.Errorf("hold record section = %q, want both rounds", got)
	}
}

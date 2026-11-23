package core

// このファイルは、区切り S4d-2 の T1 の、経路 ui（画面のボタン）の回答の限り・選択肢ごとの「ボタンで選べるか」・保留の照合の値を、
// 実ストアで検証する（AC-1〜25）。承認の経路の決まり（AC-26〜33）は verification_ui_test.go と followup_ui_test.go。

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// uiAnswerHold は経路 ui（本人確認 none）で、保留 h に、読んだときの照合の値で答える。
func uiAnswerHold(t *testing.T, s *Store, id string, h Hold, option, note string, replan bool) (*Challenge, *Hold, error) {
	t.Helper()
	return s.ExecuteAnswer(context.Background(), AnswerRequest{
		ChallengeID: id, Answer: note, Option: option, Replan: replan, HoldID: h.HoldID, HoldCheck: h.Check,
	}, uiAttestationForTest(t, id))
}

func holdsOf(t *testing.T, s *Store, id string) []Hold {
	t.Helper()
	d, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d.Holds
}

func wantOpenHold(t *testing.T, s *Store, id string, i int) {
	t.Helper()
	if h := holdsOf(t, s, id)[i]; !h.Open() {
		t.Errorf("hold %s = %+v, want still open", h.HoldID, h)
	}
}

// AC-1・2・5: 選択肢の識別で答えると成立し、経路は ui・本人確認は none、回答の文は選んだ選択肢の見出しだけ。
func TestUIAnswer_OptionAnswersWithTheLabelOnlyAndRecordsUINone(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	h := holdsOf(t, s, id)[0]
	_, out, err := uiAnswerHold(t, s, id, h, "add", "", false)
	if err != nil {
		t.Fatal(err)
	}
	got := holdsOf(t, s, id)[0]
	if got.Open() || got.Answer == nil || *got.Answer != "足す" || got.AnswerOption == nil || *got.AnswerOption != "add" {
		t.Errorf("hold = %+v", got)
	}
	if got.AnsweredChannel == nil || *got.AnsweredChannel != "ui" || got.AnsweredVerification == nil || *got.AnsweredVerification != "none" {
		t.Errorf("channel/verification = %v/%v, want ui/none", got.AnsweredChannel, got.AnsweredVerification)
	}
	if out.AnsweredChannel == nil || *out.AnsweredChannel != "ui" {
		t.Errorf("returned hold = %+v", out)
	}
}

// AC-3・4・6・11: 文だけ・添え書き・計画を作り直す指定・保留に無い識別は検証の誤りで断り、保留は開いたまま。
func TestUIAnswer_RefusesSentenceNoteReplanAndUnknownOptionAsValidation(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	h := holdsOf(t, s, id)[0]
	cases := []struct {
		name, option, note string
		replan             bool
	}{
		{"sentence only", "", "これで", false},
		{"note with an option", "add", "ただし", false},
		{"replan with an option", "add", "", true},
		{"replan only", "", "", true},
		{"unknown option", "zzz", "", false},
	}
	for _, c := range cases {
		if _, _, err := uiAnswerHold(t, s, id, h, c.option, c.note, c.replan); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", c.name, err)
		}
		wantOpenHold(t, s, id, 0)
	}
	// 保留の識別と照合の値を付けない回答も、検証の誤り。
	if _, _, err := s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: id, Option: "add"}, uiAttestationForTest(t, id)); !errors.Is(err, ErrValidation) {
		t.Errorf("no hold id: err = %v, want ErrValidation", err)
	}
	wantOpenHold(t, s, id, 0)
}

// AC-10: 選択肢を持たない保留は、本人確認の誤り（経路で受けない操作）で断る。
func TestUIAnswer_HoldWithoutOptionsIsRefusedAsNotAcceptedFromTheScreen(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, HoldOptionsInput{})
	h := holdsOf(t, s, id)[0]
	if _, _, err := uiAnswerHold(t, s, id, h, "add", "", false); !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("err = %v, want ErrVerificationRejected", err)
	}
	wantOpenHold(t, s, id, 0)
}

// AC-7・8・9・12・13・14: 足す依頼の保留の「足す」「計画を作り直す」は画面で選べず（選択肢ごとの印も偽）、「足さずに進める」は選べる。
func TestUIAnswer_OverLimitHoldRefusesAddAndReplanButAcceptsSkip(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	h, _ := f.lastHold(t)
	byID := map[string]bool{}
	for i, o := range h.Options {
		byID[o.ID] = h.OptionButtons[i]
	}
	if want := map[string]bool{"add": false, "skip": true, "replan": false}; !reflect.DeepEqual(byID, want) { // AC-12・13・14
		t.Errorf("buttons = %v, want %v", byID, want)
	}
	limit, status := f.limitMicros(t), f.detail(t, f.id).Status
	for _, opt := range []string{taskAddOptionAdd, taskAddOptionReplan} {
		_, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{
			ChallengeID: f.id, Option: opt, HoldID: h.HoldID, HoldCheck: h.Check, Agent: f.agent, Conn: f.conn,
		}, uiAttestationForTest(t, f.id))
		if !errors.Is(err, ErrVerificationRejected) { // AC-7・8
			t.Errorf("%s: err = %v, want ErrVerificationRejected", opt, err)
		}
		if f.limitMicros(t) != limit || f.detail(t, f.id).Status != status || f.taskByKey(t, "c") != nil {
			t.Errorf("%s: something changed (limit %d status %s)", opt, f.limitMicros(t), f.detail(t, f.id).Status)
		}
		if got, _ := f.lastHold(t); !got.Open() || got.Question != h.Question {
			t.Errorf("%s: hold = %+v, want untouched and open", opt, got)
		}
	}
	c, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{
		ChallengeID: f.id, Option: taskAddOptionSkip, HoldID: h.HoldID, HoldCheck: h.Check, Agent: f.agent, Conn: f.conn,
	}, uiAttestationForTest(t, f.id))
	if err != nil || c.Status != StatusInProgress { // AC-9
		t.Fatalf("skip: %v, %v", c, err)
	}
	if adds := f.additions(t); adds[0].State != TaskAdditionDeclined {
		t.Errorf("addition = %s, want declined", adds[0].State)
	}
}

// AC-15・16・17: ボタンで選べないのは、足す依頼の保留（上限を超える）の add・replan だけ。ほかの保留（委譲先の問い・スーパーバイザーが置き換えた選択肢・
// 足す依頼の中身の問い）の選択肢は、識別が add・replan でもすべて真。
func TestHoldOptionButtonOK_OnlyTheOverLimitAddAndReplanAreFalse(t *testing.T) {
	t.Parallel()
	for _, reason := range []holdReason{"", holdReasonTaskAddQuestion, holdReasonFactsUnmet, holdReason("supervisor_replaced")} {
		for _, id := range []string{"o1", "add", "skip", "replan"} {
			if !holdOptionButtonOK(reason, id) {
				t.Errorf("reason %q option %q = false, want true", reason, id)
			}
		}
	}
	for id, want := range map[string]bool{"add": false, "replan": false, "skip": true, "o1": true} {
		if got := holdOptionButtonOK(holdReasonTaskAddOverLimit, id); got != want {
			t.Errorf("over limit %q = %v, want %v", id, got, want)
		}
	}
	// 読んだ保留の印は、選択肢と同じ並び。
	s, id := optionHoldFixture(t, answerOptsFixture)
	if h := holdsOf(t, s, id)[0]; !reflect.DeepEqual(h.OptionButtons, []bool{true, true}) {
		t.Errorf("buttons = %v, want [true true]", h.OptionButtons)
	}
}

// AC-18・19・21: 問いの文か選択肢が変わると照合の値が変わり、読んだときの値で答えると、競合で断り、保留は開いたまま。
func TestUIAnswer_CheckValueChangesWithTheQuestionAndOptionsAndAStaleOneConflicts(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	read := holdsOf(t, s, id)[0]
	q := "新しい問い"
	if _, err := s.EditHoldText(context.Background(), ChannelCLI, id, HoldTextEdit{Question: &q}); err != nil {
		t.Fatal(err)
	}
	afterQ := holdsOf(t, s, id)[0]
	if afterQ.Check == read.Check { // AC-18
		t.Error("check did not change with the question")
	}
	if _, err := s.EditHoldText(context.Background(), ChannelCLI, id, HoldTextEdit{Descriptions: map[string]string{"add": "別の説明"}}); err != nil {
		t.Fatal(err)
	}
	afterO := holdsOf(t, s, id)[0]
	if afterO.Check == afterQ.Check { // AC-19
		t.Error("check did not change with the options")
	}
	for _, stale := range []Hold{read, afterQ} { // AC-21
		if _, _, err := uiAnswerHold(t, s, id, stale, "add", "", false); !errors.Is(err, ErrConflict) {
			t.Errorf("stale check: err = %v, want ErrConflict", err)
		}
		wantOpenHold(t, s, id, 0)
	}
	if _, _, err := uiAnswerHold(t, s, id, afterO, "add", "", false); err != nil {
		t.Errorf("current check: %v", err)
	}
}

// AC-20・23: 同じ課題のほかの保留が立っても照合の値は変わらず、指した保留だけを閉じる。
func TestUIAnswer_TwoOpenHoldsCheckIsStableAndOnlyTheNamedHoldCloses(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	h1 := holdsOf(t, f.s, f.id)[0]
	if err := f.overlay(f.taskB, HoldOptionsInput{Scope: HoldScopeTask, Options: answerOptsFixture.Options}); err != nil {
		t.Fatal(err)
	}
	hs := holdsOf(t, f.s, f.id)
	if len(hs) != 2 || hs[0].Check != h1.Check { // AC-20
		t.Fatalf("holds = %+v, want H-1's check unchanged", hs)
	}
	if _, _, err := uiAnswerHold(t, f.s, f.id, hs[1], "add", "", false); err != nil { // AC-23
		t.Fatal(err)
	}
	hs = holdsOf(t, f.s, f.id)
	if !hs[0].Open() || hs[1].Open() {
		t.Errorf("open: H-1=%v H-2=%v, want true/false", hs[0].Open(), hs[1].Open())
	}
	if c, _ := f.s.GetChallenge(context.Background(), f.id); c.Status != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human (H-1 still open)", c.Status)
	}
}

// AC-22: 閉じた保留への回答は、競合で断る。ほかの課題の保留・無い保留は ErrNotFound。
func TestUIAnswer_ClosedHoldConflictsAndForeignHoldIsNotFound(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	h := holdsOf(t, s, id)[0]
	if _, _, err := uiAnswerHold(t, s, id, h, "add", "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := uiAnswerHold(t, s, id, h, "add", "", false); !errors.Is(err, ErrConflict) {
		t.Errorf("closed hold: err = %v, want ErrConflict", err)
	}
	ghost := h
	ghost.HoldID = "H-999"
	if _, _, err := uiAnswerHold(t, s, id, ghost, "add", "", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such hold: err = %v, want ErrNotFound", err)
	}
}

// AC-24: 経路 cli（relayed）は、文だけの回答・足す依頼の「足す」を、いまと同じに受ける（経路 ui の限りは効かない）。
func TestAnswer_CLIKeepsAcceptingASentenceAndTheOverLimitAdd(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	if _, h, err := answerWith(t, s, id, "自由な文", "", false); err != nil || h.Answer == nil || *h.Answer != "自由な文" {
		t.Fatalf("sentence: %v %+v", err, h)
	}
	f := overLimitHeld(t)
	if c, err := f.answerHold(t, taskAddOptionAdd); err != nil || c.Status != StatusInProgress {
		t.Fatalf("add: %v %v", c, err)
	}
}

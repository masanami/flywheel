package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// S4b-1 の 2: 選択肢で答える回答・保留の書き換え。実ストアで検証する。

var answerOptsFixture = HoldOptionsInput{Options: []HoldOption{
	{ID: "add", Label: "足す", Description: "説明 A"}, {ID: "skip", Label: "足さない"},
}}

func optionHoldFixture(t *testing.T, in HoldOptionsInput) (*Store, string) {
	t.Helper()
	fixedActor(t, "alice")
	s := newStoreForTest(t)
	c := createAndAdvance(t, s, StatusUnclassified)
	_, err := s.transition(context.Background(), ChannelCLI, c.ID, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		_, err := insertHoldWithOptions(ctx, tc, "どうする？", HoldKindHuman, tc.current.Status, nil, in)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, c.ID
}

func answerWith(t *testing.T, s *Store, id, answer, option string, replan bool) (*Challenge, *Hold, error) {
	t.Helper()
	prev, err := s.PrepareAnswerOption(context.Background(), id, answer, option, replan)
	if err != nil {
		return nil, nil, err
	}
	return s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: id, ExpectedVersion: prev.Version, Answer: answer, Option: option, Replan: replan},
		relayedAttestation(t, id, "sess", "quote"))
}

func TestAnswerOption_LabelBecomesAnswerAndIdIsRecorded(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	before, _ := s.GetChallenge(context.Background(), id)
	c, h, err := answerWith(t, s, id, "", "add", false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusUnclassified || c.Version != before.Version+1 {
		t.Errorf("status=%s version=%d", c.Status, c.Version)
	}
	if h.Answer == nil || *h.Answer != "足す" || h.AnswerOption == nil || *h.AnswerOption != "add" {
		t.Errorf("hold = %+v", h)
	}
	d, _ := s.GetChallenge(context.Background(), id)
	got := d.Holds[0]
	if *got.Answer != "足す" || got.AnswerOption == nil || *got.AnswerOption != "add" {
		t.Errorf("stored hold = %+v", got)
	}
	if after := lastAfter(t, s, id); after["answer"] != "足す" || after["answer_option"] != "add" {
		t.Errorf("after = %+v", after)
	}
}

func TestAnswerOption_AnswerTextIsAnAnnotation(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	_, h, err := answerWith(t, s, id, "理由は X", "skip", false)
	if err != nil {
		t.Fatal(err)
	}
	if *h.Answer != "足さない\n理由は X" || *h.AnswerOption != "skip" {
		t.Errorf("hold = %q %v", *h.Answer, *h.AnswerOption)
	}
}

func TestAnswerOption_AnswerOnlyLeavesOptionNull(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	_, h, err := answerWith(t, s, id, "自由な回答", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if *h.Answer != "自由な回答" || h.AnswerOption != nil {
		t.Errorf("hold = %+v", h)
	}
}

func TestAnswerOption_InvalidIsRefusedWithoutWritingAndHoldStaysOpen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		in     HoldOptionsInput
		answer string
		option string
		replan bool
	}{
		{"unknown id", answerOptsFixture, "", "nope", false},
		{"hold without options", HoldOptionsInput{}, "x", "add", false},
		{"option with replan on a hold that has the option", answerOptsFixture, "", "add", true},
		{"option with replan and annotation", answerOptsFixture, "理由", "skip", true},
		{"option with replan on a hold without options", HoldOptionsInput{}, "", "add", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, id := optionHoldFixture(t, tc.in)
			before, _ := s.GetChallenge(context.Background(), id)
			// 確かめ（本人確認の前）で断る。
			if _, err := s.PrepareAnswerOption(context.Background(), id, tc.answer, tc.option, tc.replan); !errors.Is(err, ErrValidation) {
				t.Errorf("Prepare err = %v, want ErrValidation", err)
			}
			if tc.replan {
				// 作り直しの指定との組が理由で断られる（選択肢が無い・識別が無いからではない）。
				if _, err := s.PrepareAnswerOption(context.Background(), id, tc.answer, tc.option, true); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
					t.Errorf("Prepare err = %v, want the --option/--replan refusal", err)
				}
			}
			_, _, err := s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: id, ExpectedVersion: before.Version, Answer: tc.answer, Option: tc.option, Replan: tc.replan},
				relayedAttestation(t, id, "sess", "quote"))
			if !errors.Is(err, ErrValidation) || (tc.replan && !strings.Contains(err.Error(), "cannot be combined")) {
				t.Errorf("Execute err = %v, want ErrValidation (the --option/--replan refusal when replan)", err)
			}
			after, _ := s.GetChallenge(context.Background(), id)
			if after.Version != before.Version || after.Status != StatusAwaitingHuman || !after.Holds[0].Open() {
				t.Errorf("state changed: %+v", after.Challenge)
			}
		})
	}
}

func TestAnswerOption_RequiredHoldRefusesAnswerWithoutOptionAndListsOptions(t *testing.T) {
	t.Parallel()
	in := answerOptsFixture
	in.Required = true
	s, id := optionHoldFixture(t, in)
	_, _, err := answerWith(t, s, id, "自由な回答", "", false)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "add (足す)") || !strings.Contains(err.Error(), "skip (足さない)") {
		t.Fatalf("err = %v, want ErrValidation listing the options", err)
	}
	d, _ := s.GetChallenge(context.Background(), id)
	if !d.Holds[0].Open() {
		t.Error("hold is not open")
	}
	// ExecuteAnswer でも断る（確かめを通らない呼び出し）。
	if _, _, err := s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: id, ExpectedVersion: d.Version, Answer: "x"}, relayedAttestation(t, id, "s", "q")); !errors.Is(err, ErrValidation) {
		t.Errorf("Execute err = %v", err)
	}
	if _, _, err := answerWith(t, s, id, "", "skip", false); err != nil {
		t.Errorf("answer with option: %v", err)
	}
}

func TestPrepareAnswerOption_ReturnsResolvedLabel(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	p, err := s.PrepareAnswerOption(context.Background(), id, "", "skip", false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Option != "skip" || p.OptionLabel != "足さない" || p.Answer != "" {
		t.Errorf("preview = %+v", p)
	}
}

func TestAnswerOption_UnattestedAnswerIsStillRejected(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	d, _ := s.GetChallenge(context.Background(), id)
	_, _, err := s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: id, ExpectedVersion: d.Version, Option: "add"}, Attestation{})
	if !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("err = %v, want ErrVerificationRejected", err)
	}
}

func TestAnswerOption_UserSeesChosenIdInsideTheSameWriteAndFailureRollsBack(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	var seen []string
	s.answerOptionUse = func(_ context.Context, _ *transitionCtx, _ int64, optionID string) error {
		seen = append(seen, optionID)
		return nil
	}
	if _, _, err := answerWith(t, s, id, "", "add", false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, []string{"add"}) {
		t.Errorf("seen = %v", seen)
	}

	s2, id2 := optionHoldFixture(t, answerOptsFixture)
	boom := errors.New("boom")
	s2.answerOptionUse = func(context.Context, *transitionCtx, int64, string) error { return boom }
	if _, _, err := answerWith(t, s2, id2, "", "add", false); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	d, _ := s2.GetChallenge(context.Background(), id2)
	if !d.Holds[0].Open() || d.Holds[0].AnswerOption != nil {
		t.Errorf("answer was not rolled back: %+v", d.Holds[0])
	}

	// 自由な回答では使う側は呼ばれない。
	s3, id3 := optionHoldFixture(t, answerOptsFixture)
	called := false
	s3.answerOptionUse = func(context.Context, *transitionCtx, int64, string) error { called = true; return nil }
	if _, _, err := answerWith(t, s3, id3, "free", "", false); err != nil || called {
		t.Errorf("err=%v called=%v", err, called)
	}
}

func TestEditHoldText_RewritesQuestionAndDescriptionsAndBumpsVersionOnce(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	before, _ := s.GetChallenge(context.Background(), id)
	q := "新しい問い"
	c, err := s.EditHoldText(context.Background(), ChannelCLI, id, HoldTextEdit{Question: &q, Descriptions: map[string]string{"add": "新説明", "skip": "説明 B"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != before.Version+1 {
		t.Errorf("version = %d, want %d", c.Version, before.Version+1)
	}
	d, _ := s.GetChallenge(context.Background(), id)
	h := d.Holds[0]
	want := []HoldOption{{ID: "add", Label: "足す", Description: "新説明"}, {ID: "skip", Label: "足さない", Description: "説明 B"}}
	if h.Question != q || !reflect.DeepEqual(h.Options, want) {
		t.Errorf("hold = %q %+v", h.Question, h.Options)
	}
	if a := lastAfter(t, s, id); fmt.Sprint(a["version"]) != fmt.Sprint(before.Version+1) {
		t.Errorf("after = %+v", a)
	}
}

func TestEditHoldText_RefusesBadInputAndClosedHold(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	empty := " "
	if _, err := s.EditHoldText(context.Background(), ChannelCLI, id, HoldTextEdit{Question: &empty}); !errors.Is(err, ErrValidation) {
		t.Errorf("empty question: %v", err)
	}
	if _, err := s.EditHoldText(context.Background(), ChannelCLI, id, HoldTextEdit{Descriptions: map[string]string{"zzz": "x"}}); !errors.Is(err, ErrValidation) {
		t.Errorf("unknown option: %v", err)
	}
	d, _ := s.GetChallenge(context.Background(), id)
	if d.Holds[0].Question != "どうする？" || !reflect.DeepEqual(d.Holds[0].Options, answerOptsFixture.Options) {
		t.Errorf("hold changed: %+v", d.Holds[0])
	}
	if _, _, err := answerWith(t, s, id, "", "add", false); err != nil {
		t.Fatal(err)
	}
	q := "x"
	if _, err := s.EditHoldText(context.Background(), ChannelCLI, id, HoldTextEdit{Question: &q}); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("answered: %v", err)
	}
}

func TestEditHoldText_CalledInsideTheAnswerWriteRewritesTheOpenHoldAndAdvancesVersionOnce(t *testing.T) {
	t.Parallel()
	s, id := optionHoldFixture(t, answerOptsFixture)
	before, _ := s.GetChallenge(context.Background(), id)
	s.answerOptionUse = func(ctx context.Context, tc *transitionCtx, holdID int64, _ string) error {
		q := "書き換えた問い"
		_, err := editHoldText(ctx, tc.tx, tc.rec, tc.id, holdID, HoldTextEdit{Question: &q, Descriptions: map[string]string{"add": "x"}}, nil)
		return err
	}
	c, _, err := answerWith(t, s, id, "", "add", false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != before.Version+1 {
		t.Errorf("version = %d, want %d", c.Version, before.Version+1)
	}
	d, _ := s.GetChallenge(context.Background(), id)
	h := d.Holds[0]
	want := []HoldOption{{ID: "add", Label: "足す", Description: "x"}, {ID: "skip", Label: "足さない"}}
	if h.Question != "書き換えた問い" || !reflect.DeepEqual(h.Options, want) || *h.Answer != "足す" {
		t.Errorf("hold = %q %+v %v", h.Question, h.Options, *h.Answer)
	}
}

// 見出しの制御文字は、誤りの文では見える形に直す。保存した見出しと回答の文は変えない。
func TestAnswerOption_ErrorEscapesControlCharsInOptionListButKeepsStoredLabel(t *testing.T) {
	t.Parallel()
	const label = "足す\x1b[31m\t赤\u202e"
	const shown = `足す\x1b[31m\t赤\u202e`
	in := HoldOptionsInput{Options: []HoldOption{{ID: "add", Label: label}, {ID: "skip", Label: "足さない"}}}
	check := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("err = %v, want ErrValidation", err)
		}
		msg := err.Error()
		if !strings.Contains(msg, "add ("+shown+")") {
			t.Errorf("message = %q, want escaped label %q", msg, shown)
		}
		if strings.ContainsAny(msg, "\x1b\t\u202e") {
			t.Errorf("message = %q still has control characters", msg)
		}
	}
	t.Run("unknown id", func(t *testing.T) {
		t.Parallel()
		s, id := optionHoldFixture(t, in)
		_, _, err := answerWith(t, s, id, "", "nope", false)
		check(t, err)
		d, _ := s.GetChallenge(context.Background(), id)
		if !d.Holds[0].Open() || d.Holds[0].Options[0].Label != label {
			t.Errorf("hold = %+v", d.Holds[0])
		}
	})
	t.Run("required hold without option", func(t *testing.T) {
		t.Parallel()
		req := in
		req.Required = true
		s, id := optionHoldFixture(t, req)
		_, _, err := answerWith(t, s, id, "自由な回答", "", false)
		check(t, err)
		// 選べば、回答の文は保存した見出しそのまま。
		_, h, err := answerWith(t, s, id, "", "add", false)
		if err != nil || *h.Answer != label {
			t.Errorf("answer = %v, err = %v, want the stored label unchanged", h, err)
		}
	})
}

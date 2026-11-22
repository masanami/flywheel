package core

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// S4b-1 の 1: 保留の選択肢（AC-9〜AC-11・AC-45〜AC-47）。実ストアで検証する。

func holdWithOptions(t *testing.T, s *Store, in HoldOptionsInput) (string, error) {
	t.Helper()
	c := mustCreateChallenge(t, s, "選択肢つきの保留")
	_, err := s.transition(context.Background(), ChannelCLI, c.ID, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		_, err := insertHoldWithOptions(ctx, tc, "どうする？", HoldKindHuman, tc.current.Status, nil, in)
		return err
	})
	return c.ID, err
}

func loadHoldsOf(t *testing.T, s *Store, id string) []Hold {
	t.Helper()
	c, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return c.Holds
}

func TestHoldOptions_CallerIDsAreKeptAndMissingIDsGetOrdinal(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	id, err := holdWithOptions(t, s, HoldOptionsInput{Options: []HoldOption{
		{ID: "add", Label: "足す", Description: "説明"}, {Label: "足さない"}, {ID: "x_1-z", Label: "別"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h := loadHoldsOf(t, s, id)
	if len(h) != 1 {
		t.Fatalf("holds = %d, want 1", len(h))
	}
	want := []HoldOption{{ID: "add", Label: "足す", Description: "説明"}, {ID: "o2", Label: "足さない"}, {ID: "x_1-z", Label: "別"}}
	if !reflect.DeepEqual(h[0].Options, want) {
		t.Errorf("Options = %+v, want %+v", h[0].Options, want)
	}
	if h[0].AnswerOption != nil || h[0].OptionRequired {
		t.Errorf("AnswerOption=%v OptionRequired=%v, want nil, false", h[0].AnswerOption, h[0].OptionRequired)
	}
}

func TestHoldOptions_RequiredIsStoredAndZeroOptionsAreAllowed(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	id, err := holdWithOptions(t, s, HoldOptionsInput{Required: true})
	if err != nil {
		t.Fatal(err)
	}
	h := loadHoldsOf(t, s, id)[0]
	if h.Options != nil || !h.OptionRequired {
		t.Errorf("Options=%v OptionRequired=%v, want nil, true", h.Options, h.OptionRequired)
	}
}

func TestHoldOptions_ExistingHoldCreatorsLeaveOptionsEmpty(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	c := mustCreateChallenge(t, s, "既存の入口")
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "q"}); err != nil {
		t.Fatal(err)
	}
	h := loadHoldsOf(t, s, c.ID)[0]
	if h.Options != nil || h.AnswerOption != nil || h.OptionRequired {
		t.Errorf("hold = %+v, want no options", h)
	}
}

func TestHoldOptions_InvalidOptionsAreRefusedAndCreateNoHold(t *testing.T) {
	t.Parallel()
	cases := map[string][]HoldOption{
		"duplicate_id":        {{ID: "a", Label: "x"}, {ID: "a", Label: "y"}},
		"duplicate_with_auto": {{Label: "x"}, {ID: "o1", Label: "y"}},
		"uppercase":           {{ID: "Add", Label: "x"}},
		"33_chars":            {{ID: strings.Repeat("a", 33), Label: "x"}},
		"bad_char":            {{ID: "a b", Label: "x"}},
		"empty_label":         {{ID: "a", Label: " "}},
		"multiline_label":     {{ID: "a", Label: "x\ny"}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _ := newWorkspaceStore(t)
			id, err := holdWithOptions(t, s, HoldOptionsInput{Options: opts})
			if !errors.Is(err, ErrHoldOptionInvalid) {
				t.Fatalf("err = %v, want ErrHoldOptionInvalid", err)
			}
			if errors.Is(err, ErrValidation) {
				t.Errorf("err must not be a user-input ErrValidation")
			}
			if n := len(loadHoldsOf(t, s, id)); n != 0 {
				t.Errorf("holds = %d, want 0", n)
			}
		})
	}
	t.Run("32_chars_is_accepted", func(t *testing.T) {
		t.Parallel()
		s, _ := newWorkspaceStore(t)
		if _, err := holdWithOptions(t, s, HoldOptionsInput{Options: []HoldOption{{ID: strings.Repeat("a", 32), Label: "x"}}}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHoldOptions_AnswerOptionColumnIsReadIntoHold(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	id, err := holdWithOptions(t, s, HoldOptionsInput{Options: []HoldOption{{ID: "add", Label: "足す"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE hold SET answer_option = 'add'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h := loadHoldsOf(t, s, id)[0]
	if h.AnswerOption == nil || *h.AnswerOption != "add" {
		t.Errorf("AnswerOption = %v, want add", h.AnswerOption)
	}
}

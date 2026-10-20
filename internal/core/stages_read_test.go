package core

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

// card.headline・card.context は challenge の列と同じ値を返す。
func TestDeriveCard_HeadlineAndContextFollowTheChallengeColumns(t *testing.T) {
	s, c := newChallengeForStages(t)
	card, err := s.DeriveCard(context.Background(), *c, &Overview{}, c.CreatedAt)
	if err != nil || card.Headline != nil || card.Context != nil {
		t.Fatalf("unset: %+v %v", card, err)
	}
	got, err := edit(t, s, c.ID, EditInput{Headline: strPtr("一言"), Context: strPtr("位置")})
	if err != nil {
		t.Fatal(err)
	}
	card, err = s.DeriveCard(context.Background(), *got, &Overview{}, got.UpdatedAt)
	if err != nil || card.Headline == nil || *card.Headline != "一言" || card.Context == nil || *card.Context != "位置" {
		t.Errorf("set: %+v %v", card, err)
	}
}

// ①の要約は show と同じ段階の欄と論点の回答を持ち、pairs は core.Pairs の結果。
func TestPrepareApproval_CarriesStagesIssuesAnswerAndPairs(t *testing.T) {
	s, c := newChallengeForStages(t)
	cid, _ := parseChallengeID(c.ID)
	if _, err := edit(t, s, c.ID, EditInput{
		Issues: strPtr(mustJSON(t, issuesN(3))), Expected: strPtr(mustJSON(t, expectedN(2))), Actual: strPtr(mustJSON(t, actualN(1))),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer) VALUES (?, 'q', 'unclassified', '2026-10-19T00:00:00.000Z', '決定')`, cid)
		if err != nil {
			return err
		}
		hid, _ := res.LastInsertId()
		_, err = tx.Exec(`UPDATE challenge SET issues_hold_id = ? WHERE id = ?`, hid, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	setChallengeStatus(t, s, cid, StatusAwaitingCompletionApproval)

	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prev.StageFields, d.StageFields) {
		t.Errorf("stages = %+v, want %+v", prev.StageFields, d.StageFields)
	}
	if prev.IssuesAnswer == nil || *prev.IssuesAnswer != "決定" || d.IssuesAnswer == nil || *d.IssuesAnswer != "決定" {
		t.Errorf("answers = %v / %v", prev.IssuesAnswer, d.IssuesAnswer)
	}
	if want := Pairs(expectedN(2), actualN(1)); !reflect.DeepEqual(prev.Pairs, want) || len(prev.Pairs) != 2 || prev.Pairs[1].Actual != nil {
		t.Errorf("pairs = %+v", prev.Pairs)
	}
}

func TestPrepareApproval_NoStagesGivesEmptyPairsAndNilAnswer(t *testing.T) {
	s, c := newChallengeForStages(t)
	cid, _ := parseChallengeID(c.ID)
	setChallengeStatus(t, s, cid, StatusAwaitingCompletionApproval)
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev.Pairs == nil || len(prev.Pairs) != 0 || prev.IssuesAnswer != nil || prev.Issues != nil {
		t.Errorf("preview = %+v", prev)
	}
}

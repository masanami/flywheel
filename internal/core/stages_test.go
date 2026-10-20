package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func validIssue() StageIssue {
	ex := "例"
	return StageIssue{Question: "q", Options: []StageOption{{Label: "a", Example: &ex}, {Label: "b"}}, Recommended: 1}
}

func issuesN(n int) []StageIssue {
	out := make([]StageIssue, n)
	for i := range out {
		out[i] = validIssue()
	}
	return out
}

func expectedN(n int) []StageExpected {
	out := make([]StageExpected, n)
	for i := range out {
		out[i] = StageExpected{Kind: ExpectedKindUI, Scene: "s", Before: "b", After: "a"}
	}
	return out
}

func actualN(n int) []StageActual {
	out := make([]StageActual, n)
	for i := range out {
		out[i] = StageActual{Scene: "s", After: "a"}
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNormalizeIssues_CountsAndShape(t *testing.T) {
	for n, wantOK := range map[int]bool{0: true, 1: false, 2: false, 3: true, 4: true, 5: true, 6: false} {
		got, err := NormalizeIssues(issuesN(n))
		if (err == nil) != wantOK {
			t.Errorf("issues n=%d: err = %v, wantOK %v", n, err, wantOK)
		}
		if err != nil && !errors.Is(err, ErrValidation) {
			t.Errorf("issues n=%d: err = %v, want ErrValidation", n, err)
		}
		if n == 0 && got != nil {
			t.Errorf("0 issues = %v, want nil (NULL)", got)
		}
	}
	for n, wantOK := range map[int]bool{1: false, 2: true, 5: true, 6: false} {
		is := issuesN(3)
		is[1].Options = make([]StageOption, n)
		for i := range is[1].Options {
			is[1].Options[i] = StageOption{Label: "x"}
		}
		is[1].Recommended = 1
		if _, err := NormalizeIssues(is); (err == nil) != wantOK {
			t.Errorf("options n=%d: err = %v, wantOK %v", n, err, wantOK)
		}
	}
	for rec, wantOK := range map[int]bool{0: false, 1: true, 2: true, 3: false, -1: false} {
		is := issuesN(3)
		is[0].Recommended = rec
		if _, err := NormalizeIssues(is); (err == nil) != wantOK {
			t.Errorf("recommended=%d: err = %v, wantOK %v", rec, err, wantOK)
		}
	}
	blank := "  "
	is := issuesN(3)
	is[0].Options[0].Example = &blank
	if _, err := NormalizeIssues(is); err == nil {
		t.Error("blank example accepted")
	}
	is = issuesN(3)
	is[2].Question = " \n"
	if _, err := NormalizeIssues(is); err == nil {
		t.Error("blank question accepted")
	}
}

func TestNormalizeExpectedActualNonGoals(t *testing.T) {
	for n, wantOK := range map[int]bool{0: true, 1: true, 3: true, 4: false} {
		if _, err := NormalizeExpected(expectedN(n)); (err == nil) != wantOK {
			t.Errorf("expected n=%d: err = %v", n, err)
		}
	}
	e := expectedN(1)
	e[0].Kind = "web"
	if _, err := NormalizeExpected(e); err == nil {
		t.Error("kind outside the closed set accepted")
	}
	e = expectedN(1)
	e[0].Before = " "
	if _, err := NormalizeExpected(e); err == nil {
		t.Error("blank before accepted")
	}
	for n, wantOK := range map[int]bool{0: true, 3: true, 4: false} {
		if _, err := NormalizeActual(actualN(n)); (err == nil) != wantOK {
			t.Errorf("actual n=%d: err = %v", n, err)
		}
	}
	for n, wantOK := range map[int]bool{0: true, 10: true, 11: false} {
		g := make([]string, n)
		for i := range g {
			g[i] = "x"
		}
		got, err := NormalizeNonGoals(g)
		if (err == nil) != wantOK {
			t.Errorf("non_goals n=%d: err = %v", n, err)
		}
		if n == 0 && got != nil {
			t.Errorf("0 non_goals = %v, want nil", got)
		}
	}
	if _, err := NormalizeNonGoals([]string{" "}); err == nil {
		t.Error("blank non_goal accepted")
	}
}

func TestStageLengthLimits(t *testing.T) {
	// 文字数は Unicode のコードポイント（日本語 100 文字は 100 文字）。
	if err := CheckHeadline(strings.Repeat("あ", 100)); err != nil {
		t.Errorf("100 chars: %v", err)
	}
	if err := CheckHeadline(strings.Repeat("あ", 101)); err == nil {
		t.Error("101 chars accepted")
	}
	if err := CheckHeadline("  "); err == nil {
		t.Error("blank headline accepted")
	}
	if err := CheckContext(strings.Repeat("あ", 1000)); err != nil {
		t.Errorf("1000 chars: %v", err)
	}
	if err := CheckContext(strings.Repeat("あ", 1001)); err == nil {
		t.Error("1001 chars accepted")
	}
	// 図はバイト。
	if err := CheckFigure(strings.Repeat("a", 16384)); err != nil {
		t.Errorf("16384 bytes: %v", err)
	}
	if err := CheckFigure(strings.Repeat("a", 16385)); err == nil {
		t.Error("16385 bytes accepted")
	}
	if err := CheckFigure(strings.Repeat("あ", 5462)); err == nil { // 16386 bytes
		t.Error("multibyte figure over the byte limit accepted")
	}
	ng := []string{strings.Repeat("あ", 2000)}
	if _, err := NormalizeNonGoals(ng); err != nil {
		t.Errorf("2000 chars non_goal: %v", err)
	}
	if _, err := NormalizeNonGoals([]string{strings.Repeat("あ", 2001)}); err == nil {
		t.Error("2001 chars non_goal accepted")
	}
}

func TestParseStageJSON(t *testing.T) {
	if got, err := ParseIssuesJSON("[]"); err != nil || got != nil {
		t.Errorf("[] = %v, %v; want nil, nil", got, err)
	}
	if got, err := ParseIssuesJSON(mustJSON(t, issuesN(3))); err != nil || len(got) != 3 {
		t.Errorf("valid = %v, %v", got, err)
	}
	for name, text := range map[string]string{
		"null":          "null",
		"object":        "{}",
		"garbage":       "[",
		"unknown key":   `[{"kind":"ui","scene":"s","before":"b","after":"a","extra":1}]`,
		"trailing data": `[] []`,
	} {
		if _, err := ParseExpectedJSON(text); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
	if _, err := ParseNonGoalsJSON(`["a", 1]`); err == nil {
		t.Error("non-string element accepted")
	}
	if got, err := ParseActualJSON(`[{"scene":"s","after":"a"}]`); err != nil || len(got) != 1 {
		t.Errorf("actual = %v, %v", got, err)
	}
}

func TestPairs(t *testing.T) {
	if got := Pairs(nil, nil); got == nil || len(got) != 0 {
		t.Errorf("Pairs(nil,nil) = %#v, want empty non-nil", got)
	}
	e, a := expectedN(2), actualN(1)
	e[0].After, e[1].After = "e1", "e2"
	a[0].After = "a1"
	got := Pairs(e, a)
	if len(got) != 2 || got[0].Expected.After != "e1" || got[0].Actual.After != "a1" || got[1].Expected.After != "e2" || got[1].Actual != nil {
		t.Errorf("fewer actual: %+v", got)
	}
	got = Pairs(e[:1], append(actualN(2), StageActual{}))
	if len(got) != 3 || got[1].Expected != nil || got[2].Expected != nil || got[0].Expected == nil {
		t.Errorf("fewer expected: %+v", got)
	}
	got = Pairs(nil, actualN(1))
	if len(got) != 1 || got[0].Expected != nil || got[0].Actual == nil {
		t.Errorf("only actual: %+v", got)
	}
	if PairActualMissingMark != "実物なし" {
		t.Errorf("mark = %q", PairActualMissingMark)
	}
}

func TestNormalizeRejectedFields(t *testing.T) {
	got, ok := NormalizeRejectedFields([]string{"figure", "issues", "figure", " expected "})
	if !ok || !reflect.DeepEqual(got, []RejectedField{RejectedFieldIssues, RejectedFieldExpected, RejectedFieldFigure}) {
		t.Errorf("got %v, %v; want closed-set order without duplicates", got, ok)
	}
	if got, ok := NormalizeRejectedFields(nil); !ok || got != nil {
		t.Errorf("nil = %v, %v", got, ok)
	}
	if _, ok := NormalizeRejectedFields([]string{"title"}); ok {
		t.Error("value outside the closed set accepted")
	}
	if want := []RejectedField{"issues", "expected", "non_goals", "figure"}; !reflect.DeepEqual(rejectedFieldValues, want) {
		t.Errorf("closed set = %v, want %v", rejectedFieldValues, want)
	}
}

func newChallengeForStages(t *testing.T) (*Store, *Challenge) {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func edit(t *testing.T, s *Store, id string, in EditInput) (*Challenge, error) {
	t.Helper()
	return s.EditChallenge(context.Background(), ChannelCLI, id, in)
}

func lastActivity(t *testing.T, s *Store, id string) Activity {
	t.Helper()
	acts, err := s.ListActivities(context.Background(), &id)
	if err != nil || len(acts) == 0 {
		t.Fatalf("ListActivities: %v %v", acts, err)
	}
	return acts[len(acts)-1]
}

func TestEditChallenge_WritesEachStageFieldAndRoundTrips(t *testing.T) {
	s, c := newChallengeForStages(t)
	issues := issuesN(3)
	in := EditInput{
		Headline: strPtr("一言"),
		Context:  strPtr("位置"),
		Issues:   strPtr(mustJSON(t, issues)),
		Expected: strPtr(mustJSON(t, expectedN(2))),
		Actual:   strPtr(mustJSON(t, actualN(1))),
		NonGoals: strPtr(`["x","y"]`),
		Figure:   strPtr("graph TD; A-->B"),
	}
	got, err := edit(t, s, c.ID, in)
	if err != nil {
		t.Fatalf("EditChallenge: %v", err)
	}
	if got.Version != 2 {
		t.Errorf("version = %d, want 2", got.Version)
	}
	d, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]StageFields{"returned": got.StageFields, "reloaded": d.StageFields} {
		if got.Headline == nil || *got.Headline != "一言" || got.Context == nil || *got.Context != "位置" ||
			!reflect.DeepEqual(got.Issues, issues) || !reflect.DeepEqual(got.Expected, expectedN(2)) ||
			!reflect.DeepEqual(got.Actual, actualN(1)) || !reflect.DeepEqual(got.NonGoals, []string{"x", "y"}) ||
			got.Figure == nil || *got.Figure != "graph TD; A-->B" {
			t.Errorf("%s stage fields = %+v", name, got)
		}
	}
	// 作業ログの before/after は変えた列の名前をキーにし、JSON の列は解釈した値で載せる。
	act := lastActivity(t, s, c.ID)
	var after map[string]any
	if err := json.Unmarshal(act.After, &after); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"headline", "context", "issues", "expected", "actual", "non_goals", "figure", "version"} {
		if _, ok := after[k]; !ok {
			t.Errorf("after lacks %q: %v", k, after)
		}
	}
	if _, ok := after["issues"].([]any); !ok {
		t.Errorf("after.issues = %T, want an interpreted array", after["issues"])
	}
	var before map[string]any
	if err := json.Unmarshal(act.Before, &before); err != nil {
		t.Fatal(err)
	}
	if v, ok := before["headline"]; !ok || v != nil {
		t.Errorf("before.headline = %v (present %v), want null", v, ok)
	}
}

func TestEditChallenge_EmptyStringClearsToNull(t *testing.T) {
	s, c := newChallengeForStages(t)
	if _, err := edit(t, s, c.ID, EditInput{Headline: strPtr("h"), Context: strPtr("c"), Issues: strPtr(mustJSON(t, issuesN(3))),
		Expected: strPtr(mustJSON(t, expectedN(1))), Actual: strPtr(mustJSON(t, actualN(1))), NonGoals: strPtr(`["a"]`), Figure: strPtr("f")}); err != nil {
		t.Fatal(err)
	}
	got, err := edit(t, s, c.ID, EditInput{Headline: strPtr(""), Context: strPtr(""), Issues: strPtr(""), Expected: strPtr(""),
		Actual: strPtr(""), NonGoals: strPtr(""), Figure: strPtr("")})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.StageFields, StageFields{}) {
		t.Errorf("after clearing = %+v, want all NULL", got.StageFields)
	}
	// 要素 0 個の配列も NULL として書く（[] を保存しない）。
	if _, err := edit(t, s, c.ID, EditInput{NonGoals: strPtr(`["a"]`)}); err != nil {
		t.Fatal(err)
	}
	got, err = edit(t, s, c.ID, EditInput{NonGoals: strPtr(`[]`)})
	if err != nil || got.NonGoals != nil {
		t.Fatalf("[] = %v, %v; want NULL", got, err)
	}
	var raw sql.NullString
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT non_goals FROM challenge`).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	if raw.Valid {
		t.Errorf("stored non_goals = %q, want NULL", raw.String)
	}
}

func TestEditChallenge_InvalidStageValueIsValidationAndChangesNothing(t *testing.T) {
	s, c := newChallengeForStages(t)
	before := lastActivity(t, s, c.ID)
	for name, in := range map[string]EditInput{
		"headline blank":   {Headline: strPtr("  ")},
		"headline long":    {Headline: strPtr(strings.Repeat("a", 101))},
		"context long":     {Context: strPtr(strings.Repeat("a", 1001))},
		"figure long":      {Figure: strPtr(strings.Repeat("a", 16385))},
		"issues 2":         {Issues: strPtr(mustJSON(t, issuesN(2)))},
		"expected 4":       {Expected: strPtr(mustJSON(t, expectedN(4)))},
		"actual 4":         {Actual: strPtr(mustJSON(t, actualN(4)))},
		"non_goals 11":     {NonGoals: strPtr(mustJSON(t, make11()))},
		"non_goals shape":  {NonGoals: strPtr(`{"a":1}`)},
		"with valid title": {Title: strPtr("new"), Figure: strPtr(" ")},
	} {
		if _, err := edit(t, s, c.ID, in); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
	got, _ := s.GetChallenge(context.Background(), c.ID)
	if got.Version != 1 || got.Title != "t" || lastActivity(t, s, c.ID).At != before.At {
		t.Errorf("challenge changed: %+v", got.Challenge)
	}
}

func make11() []string {
	out := make([]string, 11)
	for i := range out {
		out[i] = "x"
	}
	return out
}

func TestEditChallenge_ValidationComesBeforeExistenceAndTerminal(t *testing.T) {
	s, _ := newChallengeForStages(t)
	if _, err := edit(t, s, "C-999", EditInput{Headline: strPtr("  ")}); !errors.Is(err, ErrValidation) {
		t.Errorf("missing id + invalid value: %v, want ErrValidation", err)
	}
	if _, err := edit(t, s, "C-999", EditInput{Headline: strPtr("h")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing id: %v, want ErrNotFound", err)
	}
	done := createAndAdvance(t, s, StatusDone)
	if _, err := edit(t, s, done.ID, EditInput{Headline: strPtr("h")}); !errors.Is(err, ErrTerminalState) {
		t.Errorf("done: %v, want ErrTerminalState", err)
	}
}

func TestEditChallenge_SameStageValueIsNoOp(t *testing.T) {
	s, c := newChallengeForStages(t)
	in := EditInput{Headline: strPtr("h"), Issues: strPtr(mustJSON(t, issuesN(3)))}
	first, err := edit(t, s, c.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	n := activityCount(t, s, c.ID)
	again, err := edit(t, s, c.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if again.Version != first.Version || activityCount(t, s, c.ID) != n {
		t.Errorf("same value rewrote: version %d→%d, activities %d→%d", first.Version, again.Version, n, activityCount(t, s, c.ID))
	}
	// 変わらない欄と変わる欄を同時に指定したら、変わる欄だけが作業ログに載る。
	if _, err := edit(t, s, c.ID, EditInput{Headline: strPtr("h"), Figure: strPtr("f")}); err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	_ = json.Unmarshal(lastActivity(t, s, c.ID).After, &after)
	if _, ok := after["headline"]; ok {
		t.Errorf("unchanged headline logged: %v", after)
	}
	if after["figure"] != "f" {
		t.Errorf("after = %v", after)
	}
}

func activityCount(t *testing.T, s *Store, id string) int {
	t.Helper()
	acts, err := s.ListActivities(context.Background(), &id)
	if err != nil {
		t.Fatal(err)
	}
	return len(acts)
}

func TestEditChallenge_ChangingIssuesClearsIssuesHoldAndAnswer(t *testing.T) {
	s, c := newChallengeForStages(t)
	if _, err := edit(t, s, c.ID, EditInput{Issues: strPtr(mustJSON(t, issuesN(3)))}); err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(c.ID)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by)
			VALUES (?, 'q', 'unclassified', '2026-10-19T00:00:00.000Z', '答え', '2026-10-19T00:00:01.000Z', 'alice')`, cid)
		if err != nil {
			return err
		}
		hid, _ := res.LastInsertId()
		_, err = tx.Exec(`UPDATE challenge SET issues_hold_id = ? WHERE id = ?`, hid, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.IssuesHoldID == nil || d.IssuesAnswer == nil || *d.IssuesAnswer != "答え" {
		t.Fatalf("IssuesHoldID = %v, IssuesAnswer = %v; want the hold's answer", d.IssuesHoldID, d.IssuesAnswer)
	}

	// issues を同じ値で指定しても参照は残る。
	if _, err := edit(t, s, c.ID, EditInput{Issues: strPtr(mustJSON(t, issuesN(3)))}); err != nil {
		t.Fatal(err)
	}
	d, _ = s.GetChallenge(context.Background(), c.ID)
	if d.IssuesHoldID == nil || d.IssuesAnswer == nil {
		t.Errorf("unchanged issues cleared the reference: %v %v", d.IssuesHoldID, d.IssuesAnswer)
	}

	// 変えたら参照も回答も外れる。
	if _, err := edit(t, s, c.ID, EditInput{Issues: strPtr(mustJSON(t, issuesN(4)))}); err != nil {
		t.Fatal(err)
	}
	d, _ = s.GetChallenge(context.Background(), c.ID)
	if d.IssuesHoldID != nil || d.IssuesAnswer != nil {
		t.Errorf("after changing issues: hold %v answer %v, want nil", d.IssuesHoldID, d.IssuesAnswer)
	}
	var before, after map[string]any
	act := lastActivity(t, s, c.ID)
	_ = json.Unmarshal(act.Before, &before)
	_ = json.Unmarshal(act.After, &after)
	if before["issues_hold_id"] == nil || after["issues_hold_id"] != nil {
		t.Errorf("activity before/after issues_hold_id = %v / %v", before["issues_hold_id"], after["issues_hold_id"])
	}
}

func TestIssuesAnswer_UnansweredHoldIsNil(t *testing.T) {
	s, c := newChallengeForStages(t)
	cid, _ := parseChallengeID(c.ID)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (?, 'q', 'unclassified', '2026-10-19T00:00:00.000Z')`, cid)
		if err != nil {
			return err
		}
		hid, _ := res.LastInsertId()
		_, err = tx.Exec(`UPDATE challenge SET issues_hold_id = ? WHERE id = ?`, hid, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil || d.IssuesHoldID == nil || d.IssuesAnswer != nil {
		t.Errorf("hold %v answer %v err %v; want hold set and answer nil", d.IssuesHoldID, d.IssuesAnswer, err)
	}
}

// 遷移の写像が段階の欄を、課題の遷移・作業ログと同じトランザクションで書ける。
func TestRunTransition_WritesStageFieldsWithTheTransition(t *testing.T) {
	s, c := newChallengeForStages(t)
	h := "一言"
	got, err := s.transition(context.Background(), ChannelCLI, c.ID, OpClassify, func(_ context.Context, tc *transitionCtx) error {
		p := PriorityP1
		tc.priority = &p
		tc.patchStages(StagePatch{Headline: StageSet(&h), Expected: StageSet(expectedN(2))})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusClassified || got.Headline == nil || *got.Headline != h || len(got.Expected) != 2 {
		t.Errorf("returned = %+v", got)
	}
	d, _ := s.GetChallenge(context.Background(), c.ID)
	if d.Headline == nil || *d.Headline != h || len(d.Expected) != 2 || d.Status != StatusClassified {
		t.Errorf("stored = %+v", d.Challenge)
	}
	var after map[string]any
	_ = json.Unmarshal(lastActivity(t, s, c.ID).After, &after)
	if after["headline"] != h || after["status"] != string(StatusClassified) {
		t.Errorf("after = %v", after)
	}
	if _, ok := after["expected"].([]any); !ok {
		t.Errorf("after.expected = %T, want an interpreted array", after["expected"])
	}

	// apply が失敗すれば欄も遷移も書かれない。
	boom := errors.New("boom")
	_, err = s.transition(context.Background(), ChannelCLI, c.ID, OpPlan, func(_ context.Context, tc *transitionCtx) error {
		tc.patchStages(StagePatch{Figure: StageSet(strPtr("f"))})
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	d, _ = s.GetChallenge(context.Background(), c.ID)
	if d.Figure != nil {
		t.Errorf("figure written despite the failure: %q", *d.Figure)
	}
}

func TestRunTransition_WithoutPatchLeavesStageFieldsAlone(t *testing.T) {
	s, c := newChallengeForStages(t)
	if _, err := edit(t, s, c.ID, EditInput{Headline: strPtr("h"), Issues: strPtr(mustJSON(t, issuesN(3)))}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClassifyChallenge(context.Background(), ChannelCLI, c.ID, ClassifyInput{Priority: "P0"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Headline == nil || *got.Headline != "h" || len(got.Issues) != 3 {
		t.Errorf("stage fields lost by an ordinary transition: %+v", got.StageFields)
	}
}

func TestExecuteApproval_RecordsRejectedFieldsInClosedSetOrder(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	reason := "ずれ"
	prev, err := s.PrepareRejection(context.Background(), c.ID, reason)
	if err != nil {
		t.Fatal(err)
	}
	att := verifiedAttestationForTest(t, c.ID)

	// 閉集合の外の値は何も書かず ErrValidation。
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version,
		Decision: ApprovalDecisionRejected, Reason: &reason, RejectedFields: []string{"title"}}, att); !errors.Is(err, ErrValidation) {
		t.Fatalf("outside the closed set: %v", err)
	}
	_, a, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version,
		Decision: ApprovalDecisionRejected, Reason: &reason, RejectedFields: []string{"figure", "expected", "figure"}}, att)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"expected", "figure"}
	if !reflect.DeepEqual(a.RejectedFields, want) {
		t.Errorf("returned = %v, want %v", a.RejectedFields, want)
	}
	d, _ := s.GetChallenge(context.Background(), c.ID)
	if len(d.Approvals) != 1 || !reflect.DeepEqual(d.Approvals[0].RejectedFields, want) {
		t.Errorf("stored approvals = %+v", d.Approvals)
	}
	var after map[string]any
	_ = json.Unmarshal(lastActivity(t, s, c.ID).After, &after)
	if got, _ := json.Marshal(after["rejected_fields"]); string(got) != `["expected","figure"]` {
		t.Errorf("after.rejected_fields = %s", got)
	}
}

func TestExecuteApproval_ApprovalHasNullRejectedFieldsAndRejectsFields(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	att := verifiedAttestationForTest(t, c.ID)
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 承認に欄を付けると ErrValidation。
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version,
		Decision: ApprovalDecisionApproved, RejectedFields: []string{"figure"}}, att); !errors.Is(err, ErrValidation) {
		t.Fatalf("approve with fields: %v", err)
	}
	_, a, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version,
		Decision: ApprovalDecisionApproved}, att)
	if err != nil || a.RejectedFields != nil {
		t.Fatalf("approve: %v %v", a, err)
	}
	d, _ := s.GetChallenge(context.Background(), c.ID)
	if d.Approvals[0].RejectedFields != nil {
		t.Errorf("stored = %v, want nil", d.Approvals[0].RejectedFields)
	}
}

func TestPatchStages_CalledTwiceKeepsOriginalBefore(t *testing.T) {
	s, c := newChallengeForStages(t)
	if _, err := edit(t, s, c.ID, EditInput{Issues: strPtr(mustJSON(t, issuesN(3)))}); err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(c.ID)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		for i := 0; i < 9; i++ {
			if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (?, 'q', 'unclassified', '2026-10-19T00:00:00.000Z')`, cid); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`UPDATE challenge SET issues_hold_id = 5 WHERE id = ?`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	newID := int64(9)
	if _, err := s.transition(context.Background(), ChannelCLI, c.ID, OpClassify, func(_ context.Context, tc *transitionCtx) error {
		p := PriorityP1
		tc.priority = &p
		tc.patchStages(StagePatch{Issues: StageSet(issuesN(4))})
		tc.patchStages(StagePatch{IssuesHoldID: StageSet(&newID)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	act := lastActivity(t, s, c.ID)
	_ = json.Unmarshal(act.Before, &before)
	_ = json.Unmarshal(act.After, &after)
	if before["issues_hold_id"] != float64(5) || after["issues_hold_id"] != float64(9) {
		t.Errorf("before/after issues_hold_id = %v / %v, want 5 / 9", before["issues_hold_id"], after["issues_hold_id"])
	}
}

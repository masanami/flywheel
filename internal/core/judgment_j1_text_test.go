package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// J1 の出力の「一言で」「位置づけ」（S4。AC-265〜276・M4P35）のテスト。

func TestJ1OutputSchema_HasHeadlineAndContext(t *testing.T) { // AC-265
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(j1OutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"headline", "context"} {
		if _, ok := schema.Properties[k]; !ok {
			t.Errorf("schema lacks %q", k)
		}
	}
}

func TestValidateJ1Output_MineRequiresHeadlineAndContext(t *testing.T) { // AC-266・267
	ok := func(h, c *string) bool {
		_, ok := validateJ1Output(j1Output(t, j1RawOutput{Verdict: "mine", Priority: strPtr("P1"), Reason: "r", Headline: h, Context: c}))
		return ok
	}
	if !ok(strPtr("h"), strPtr("c")) {
		t.Error("valid pair rejected")
	}
	if !ok(strPtr(strings.Repeat("あ", 100)), strPtr(strings.Repeat("い", 1000))) {
		t.Error("at-limit pair (in code points) rejected")
	}
	for name, c := range map[string]struct{ h, c *string }{
		"headline missing": {nil, strPtr("c")},
		"context missing":  {strPtr("h"), nil},
		"headline blank":   {strPtr(" \n"), strPtr("c")},
		"context blank":    {strPtr("h"), strPtr("　")},
		"headline long":    {strPtr(strings.Repeat("あ", 101)), strPtr("c")},
		"context long":     {strPtr("h"), strPtr(strings.Repeat("い", 1001))},
	} {
		if ok(c.h, c.c) {
			t.Errorf("%s: want invalid", name)
		}
	}
}

func TestValidateJ1Output_NonMineDropsBadTextButKeepsVerdict(t *testing.T) { // AC-268〜275
	for _, verdict := range []string{"not_mine", "uncertain"} {
		raw := j1RawOutput{Verdict: verdict, Reason: "r", Question: strPtr("q?"),
			Headline: strPtr("  "), Context: strPtr(strings.Repeat("い", 1001))}
		v, ok := validateJ1Output(j1Output(t, raw))
		if !ok || v.Headline != nil || v.Context != nil {
			t.Errorf("%s bad text: ok=%v v=%+v, want ok with nil texts", verdict, ok, v)
		}
		raw.Headline, raw.Context = strPtr("h"), strPtr("c")
		v, ok = validateJ1Output(j1Output(t, raw))
		if !ok || v.Headline == nil || *v.Headline != "h" || *v.Context != "c" {
			t.Errorf("%s good text: ok=%v v=%+v", verdict, ok, v)
		}
	}
}

func runJ1ForText(t *testing.T, s *Store, id string, raw j1RawOutput) *J1AutoResult {
	t.Helper()
	decl := newAgentDeclForJ1Test(t, s, "pos")
	cycleID := beginJ1Cycle(t, s, 300)
	res, err := s.ClassifyAutoJ1(context.Background(), J1AutoInput{ChallengeID: &id, AgentDecl: decl, Invoker: succeededInvoker(j1Output(t, raw)), CycleID: cycleID})
	if err != nil {
		t.Fatalf("ClassifyAutoJ1: %v", err)
	}
	return res
}

func newUnclassified(t *testing.T, s *Store) string {
	t.Helper()
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return ch.ID
}

func lastClassifyActivity(t *testing.T, s *Store, id string) (action string, after map[string]any, count int) {
	t.Helper()
	cid := mustChallengeInternalID(t, id)
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM activity WHERE entity='challenge' AND entity_id=?`, cid).Scan(&count); err != nil {
			return err
		}
		var raw string
		if err := tx.QueryRowContext(context.Background(), `SELECT action, after FROM activity WHERE entity='challenge' AND entity_id=? ORDER BY id DESC LIMIT 1`, cid).Scan(&action, &raw); err != nil {
			return err
		}
		return json.Unmarshal([]byte(raw), &after)
	})
	if err != nil {
		t.Fatal(err)
	}
	return action, after, count
}

func TestClassifyAutoJ1_Mine_WritesTextAndLogsAfter(t *testing.T) { // AC-266 写像・AC-276
	s := newStoreForTest(t)
	id := newUnclassified(t, s)
	runJ1ForText(t, s, id, j1RawOutput{Verdict: "mine", Priority: strPtr("P1"), Reason: "r", Headline: strPtr("一言で"), Context: strPtr("位置づけ")})
	got, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Headline == nil || *got.Headline != "一言で" || got.Context == nil || *got.Context != "位置づけ" || got.Status != StatusClassified {
		t.Fatalf("challenge = %+v", got.Challenge)
	}
	action, after, _ := lastClassifyActivity(t, s, id)
	if action != "classify" || after["headline"] != "一言で" || after["context"] != "位置づけ" {
		t.Errorf("activity action=%q after=%v", action, after)
	}
}

func TestClassifyAutoJ1_MineBadText_InvalidOutputLeavesColumns(t *testing.T) { // AC-266・267
	s := newStoreForTest(t)
	id := newUnclassified(t, s)
	res := runJ1ForText(t, s, id, j1RawOutput{Verdict: "mine", Priority: strPtr("P1"), Reason: "r", Headline: strPtr("h")})
	if res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("Result = %q", res.Items[0].Result)
	}
	assertStoredRunResultForTest(t, s, res.Items[0].RunID, RunResultInvalidOutput)
	got, _ := s.GetChallenge(context.Background(), id)
	if got.Status != StatusUnclassified || got.Version != 1 || got.Headline != nil || got.Context != nil {
		t.Errorf("challenge changed: %+v", got.Challenge)
	}
}

func TestClassifyAutoJ1_Uncertain_WritesValidTextOnly(t *testing.T) { // AC-268〜275
	s := newStoreForTest(t)
	id := newUnclassified(t, s)
	res := runJ1ForText(t, s, id, j1RawOutput{Verdict: "uncertain", Question: strPtr("q?"), Reason: "r", Headline: strPtr("一言で"), Context: strPtr(" ")})
	if res.Items[0].Outcome != "uncertain" || res.Items[0].Result != RunResultSucceeded {
		t.Fatalf("item = %+v", res.Items[0])
	}
	got, _ := s.GetChallenge(context.Background(), id)
	if got.Status != StatusAwaitingHuman || got.Headline == nil || *got.Headline != "一言で" || got.Context != nil {
		t.Errorf("challenge = %+v", got.Challenge)
	}
	_, after, _ := lastClassifyActivity(t, s, id)
	if after["headline"] != "一言で" {
		t.Errorf("after = %v", after)
	}
}

func TestClassifyAutoJ1_NotMine_WritesTextWithoutTransition(t *testing.T) { // 起票時の決定・AC-276
	s := newStoreForTest(t)
	id := newUnclassified(t, s)
	res := runJ1ForText(t, s, id, j1RawOutput{Verdict: "not_mine", Reason: "r", Headline: strPtr("一言で"), Context: strPtr("位置づけ")})
	if res.Items[0].Outcome != "not_mine" || res.Items[0].Status != nil || res.Items[0].Result != RunResultSucceeded {
		t.Fatalf("item = %+v", res.Items[0])
	}
	got, _ := s.GetChallenge(context.Background(), id)
	if got.Status != StatusUnclassified || got.Version != 2 || *got.Headline != "一言で" || *got.Context != "位置づけ" {
		t.Fatalf("challenge = %+v", got.Challenge)
	}
	action, after, _ := lastClassifyActivity(t, s, id)
	if action != "classify" || after["headline"] != "一言で" || after["context"] != "位置づけ" || after["version"] != float64(2) {
		t.Errorf("activity action=%q after=%v", action, after)
	}
	if _, has := after["status"]; has {
		t.Errorf("status must not change: %v", after)
	}
	row := queryLastActivityForTest(t, s, id)
	if row.channel != string(ChannelInvoker) || row.runID == nil {
		t.Errorf("row = %+v", row)
	}
}

func TestClassifyAutoJ1_NotMine_BadOrNoText_WritesNothing(t *testing.T) { // AC-268〜275
	for name, raw := range map[string]j1RawOutput{
		"none":  {Verdict: "not_mine", Reason: "r"},
		"blank": {Verdict: "not_mine", Reason: "r", Headline: strPtr(" "), Context: strPtr("")},
		"long":  {Verdict: "not_mine", Reason: "r", Headline: strPtr(strings.Repeat("あ", 101))},
	} {
		s := newStoreForTest(t)
		id := newUnclassified(t, s)
		_, _, before := lastClassifyActivity(t, s, id)
		res := runJ1ForText(t, s, id, raw)
		if res.Items[0].Result != RunResultSucceeded {
			t.Errorf("%s: result = %q, want succeeded", name, res.Items[0].Result)
		}
		got, _ := s.GetChallenge(context.Background(), id)
		_, _, after := lastClassifyActivity(t, s, id)
		if got.Version != 1 || got.Headline != nil || got.Context != nil || after != before {
			t.Errorf("%s: changed: %+v activities %d->%d", name, got.Challenge, before, after)
		}
	}
}

func TestJ1NotMine_TextWriteKeepsExclusionUntilOtherVersionChange(t *testing.T) {
	s := newStoreForTest(t)
	id := newUnclassified(t, s)
	runJ1ForText(t, s, id, j1RawOutput{Verdict: "not_mine", Reason: "r", Headline: strPtr("一言で"), Context: strPtr("位置づけ")})
	cid := mustChallengeInternalID(t, id)
	if containsInt64(listJ1AutoTargetsForTest(t, s), cid) {
		t.Fatal("not_mine with a text write must stay excluded from the next auto targets")
	}
	desc := "changed"
	if _, err := s.EditChallenge(context.Background(), ChannelCLI, id, EditInput{Description: &desc}); err != nil {
		t.Fatal(err)
	}
	if !containsInt64(listJ1AutoTargetsForTest(t, s), cid) {
		t.Fatal("a later edit must put it back into the auto targets")
	}
}

func TestApplyJ1NotMine_ChallengeChangedSinceRun_WritesNothing(t *testing.T) {
	s := newStoreForTest(t)
	id := newUnclassified(t, s)
	runID := insertFakeRunForTest(t, s, id, judgmentJ1) // 起動時の版 1
	desc := "edited while running"
	if _, err := s.EditChallenge(context.Background(), ChannelCLI, id, EditInput{Description: &desc}); err != nil {
		t.Fatal(err)
	}
	mapped, err := s.applyJ1NotMine(context.Background(), formatRunID(runID), id, strPtr("h"), strPtr("c"))
	if err != nil || mapped {
		t.Fatalf("mapped=%v err=%v, want not mapped", mapped, err)
	}
	got, _ := s.GetChallenge(context.Background(), id)
	if got.Version != 2 || got.Headline != nil {
		t.Errorf("challenge = %+v", got.Challenge)
	}
}

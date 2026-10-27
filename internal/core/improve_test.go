package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// M4.5 S4 AC-320・321・325・331・334・337・342・346〜352・356〜361: J8 の起動と写像。
// core は実ストア、claude は偽の invoker。

type j8Fixture struct {
	s     *Store
	agent *AgentDeclaration
	inv   *fakeJudgmentInvoker
	cid   int64
	calls int
}

func newJ8Fixture(t *testing.T) *j8Fixture {
	t.Helper()
	fixedActor(t, "alice")
	f := &j8Fixture{s: newStoreForTest(t), agent: defaultAgentDeclaration(), inv: &fakeJudgmentInvoker{}}
	f.agent.ImproveThreshold = 10
	f.cid = mChallenge(t, f.s, "c")
	f.inv.onInvoke = func(JudgmentLaunchInput) { f.calls++ }
	f.out(nil)
	return f
}

func j8Proposal(layer, target string, ids ...string) map[string]any {
	return map[string]any{
		"layer": layer, "target": target, "cause": "context", "occurrence_ids": ids, "metric": "rejection_rate",
		"expected": "却下が減る", "action": "add", "title": "題名 " + target, "done_criteria": "完了条件 " + target,
	}
}

// out は偽の claude が返す提案を決める（nil は空の提案）。
func (f *j8Fixture) out(props []any) {
	if props == nil {
		props = []any{}
	}
	b, _ := json.Marshal(map[string]any{"proposals": props})
	f.inv.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: b, ReportedTotalCostUSD: float64Ptr(0.5)}
}

func (f *j8Fixture) occ(t *testing.T, n int, r j8Row) []int64 {
	t.Helper()
	var ids []int64
	for i := 0; i < n; i++ {
		ids = append(ids, j8Occ(t, f.s, f.cid, r))
	}
	return ids
}

func (f *j8Fixture) auto(t *testing.T) *ImproveJ8Result {
	t.Helper()
	c, err := f.s.BeginCycle(context.Background(), BeginCycleInput{Trigger: "run", BudgetUSD: 100})
	if err != nil {
		t.Fatal(err)
	}
	return f.run(t, ImproveJ8Input{CycleID: c.ID})
}

func (f *j8Fixture) manual(t *testing.T) *ImproveJ8Result {
	t.Helper()
	return f.run(t, ImproveJ8Input{Manual: true})
}

func (f *j8Fixture) run(t *testing.T, in ImproveJ8Input) *ImproveJ8Result {
	t.Helper()
	res, err := f.try(in)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (f *j8Fixture) try(in ImproveJ8Input) (*ImproveJ8Result, error) {
	in.AgentDecl, in.Invoker = f.agent, f.inv
	return f.s.ImproveJ8(context.Background(), in)
}

func (f *j8Fixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *j8Fixture) handled(t *testing.T, id int64) string {
	t.Helper()
	var h sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT handled FROM occurrence WHERE id = ?`, id).Scan(&h)
	}); err != nil {
		t.Fatal(err)
	}
	return h.String
}

func (f *j8Fixture) handledAll(t *testing.T) string {
	t.Helper()
	var out []string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, COALESCE(handled, '-') FROM occurrence ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id int64
			var h string
			if err := rows.Scan(&id, &h); err != nil {
				return err
			}
			out = append(out, formatOccurrenceID(id)+"="+h)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, ",")
}

func (f *j8Fixture) challengeField(t *testing.T, id string, col string) string {
	t.Helper()
	n, _ := parseChallengeID(id)
	var v sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT CAST(`+col+` AS TEXT) FROM challenge WHERE id = ?`, n).Scan(&v)
	}); err != nil {
		t.Fatal(err)
	}
	return v.String
}

func TestImproveJ8_AutoCallsOnlyAtThreshold_AC320_321(t *testing.T) {
	f := newJ8Fixture(t)
	f.occ(t, 9, j8Row{})
	if res := f.auto(t); res.Called || f.calls != 0 || res.Triggering != 9 {
		t.Fatalf("9 occurrences: res = %+v calls = %d", res, f.calls)
	}
	f.occ(t, 1, j8Row{})
	if res := f.auto(t); !res.Called || f.calls != 1 || res.Triggering != 10 {
		t.Fatalf("10 occurrences: res = %+v calls = %d", res, f.calls)
	}
}

func TestImproveJ8_ManualCallsAtOneOrMore_AC325(t *testing.T) {
	f := newJ8Fixture(t)
	if res := f.manual(t); res.Called || f.calls != 0 {
		t.Fatalf("0 occurrences: res = %+v calls = %d", res, f.calls)
	}
	f.occ(t, 1, j8Row{})
	if res := f.manual(t); !res.Called || f.calls != 1 {
		t.Fatalf("1 occurrence: res = %+v calls = %d", res, f.calls)
	}
}

func TestImproveJ8_ValidationErrors(t *testing.T) {
	f := newJ8Fixture(t)
	for name, in := range map[string]ImproveJ8Input{
		"auto without a cycle": {},
		"mismatched cycle":     {CycleID: "Y-1", Cycle: NewJudgmentCycle("Y-2")},
	} {
		if _, err := f.try(in); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := f.s.ImproveJ8(context.Background(), ImproveJ8Input{Manual: true}); !errors.Is(err, ErrValidation) {
		t.Errorf("no declaration/invoker: err = %v", err)
	}
}

func TestImproveJ8_FailureLimit_AutoOnly_AC331(t *testing.T) {
	f := newJ8Fixture(t)
	f.occ(t, 10, j8Row{})
	f.inv.result = JudgmentLaunchOutput{Result: RunResultErrored}
	for i := 0; i < f.agent.FailureLimit; i++ {
		if res := f.auto(t); !res.Called || res.Result != RunResultErrored {
			t.Fatalf("run %d: %+v", i, res)
		}
	}
	calls := f.calls
	res := f.auto(t)
	if res.Called || f.calls != calls || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit || res.NotStarted[0].ChallengeID != "" {
		t.Fatalf("after the limit: res = %+v calls = %d", res, f.calls)
	}
	// 手動には当てない。成功すると連続が切れ、自動がまた呼べる。
	f.out(nil)
	if res := f.manual(t); !res.Called || res.Result != RunResultSucceeded {
		t.Fatalf("manual: %+v", res)
	}
	if res := f.auto(t); !res.Called || len(res.NotStarted) != 0 {
		t.Fatalf("auto after a success: %+v", res)
	}
}

func TestImproveJ8_InvalidOutputCountsTowardFailureLimit(t *testing.T) {
	f := newJ8Fixture(t)
	f.occ(t, 10, j8Row{})
	f.inv.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{"proposals":[{"layer":"none"}]}`)}
	for i := 0; i < f.agent.FailureLimit; i++ {
		if res := f.auto(t); res.Result != RunResultInvalidOutput {
			t.Fatalf("run %d: %+v", i, res)
		}
	}
	if res := f.auto(t); res.Called || len(res.NotStarted) != 1 {
		t.Fatalf("res = %+v", res)
	}
}

func TestImproveJ8_PerformanceWait_AC334_337_347(t *testing.T) {
	f := newJ8Fixture(t)
	perf := f.occ(t, 2, j8Row{cause: "performance"})
	ctxs := f.occ(t, 3, j8Row{})
	var inputs []string
	f.inv.onInvoke = func(in JudgmentLaunchInput) {
		for _, sec := range in.Sections {
			inputs = append(inputs, sec.Content)
		}
	}
	res := f.manual(t) // proposals は空
	if len(res.Filed) != 0 || len(res.Waited) != 2 {
		t.Fatalf("res = %+v", res)
	}
	for _, id := range perf {
		if f.handled(t, id) != "wait" {
			t.Errorf("O-%d handled = %q, want wait", id, f.handled(t, id))
		}
		if strings.Contains(strings.Join(inputs, "\n"), formatOccurrenceID(id)+" ") {
			t.Errorf("O-%d is in the J8 input", id)
		}
	}
	for _, id := range ctxs { // 根拠にならなかった未処理の発生は未処理のまま
		if f.handled(t, id) != "" {
			t.Errorf("O-%d handled = %q, want unhandled", id, f.handled(t, id))
		}
	}
	if f.count(t, `SELECT COUNT(*) FROM challenge WHERE origin = 'improvement'`) != 0 {
		t.Error("a challenge was filed for an empty proposals output")
	}
	// 次の J8 の入力に入る。
	inputs = nil
	f.manual(t)
	for _, id := range ctxs {
		if !strings.Contains(strings.Join(inputs, "\n"), formatOccurrenceID(id)) {
			t.Errorf("O-%d is not in the next J8 input", id)
		}
	}
}

func TestImproveJ8_EvidenceOutsideInput_InvalidOutputWritesNothing_AC342_346(t *testing.T) {
	for name, mk := range map[string]func(f *j8Fixture, t *testing.T) string{
		"nonexistent": func(_ *j8Fixture, _ *testing.T) string { return "O-99999" },
		"handled": func(f *j8Fixture, t *testing.T) string {
			return formatOccurrenceID(f.occ(t, 1, j8Row{handled: "C-9"})[0])
		},
		"unclassified": func(f *j8Fixture, t *testing.T) string {
			id := f.occ(t, 1, j8Row{})[0]
			mexec(t, f.s, `UPDATE occurrence SET cause = NULL, layer = NULL WHERE id = ?`, id)
			return formatOccurrenceID(id)
		},
		"good": func(f *j8Fixture, t *testing.T) string {
			return formatOccurrenceID(f.occ(t, 1, j8Row{outcome: "good"})[0])
		},
		"performance": func(f *j8Fixture, t *testing.T) string {
			return formatOccurrenceID(f.occ(t, 1, j8Row{cause: "performance"})[0])
		},
		"cut by a withdrawn improvement": func(f *j8Fixture, t *testing.T) string {
			id := f.occ(t, 1, j8Row{layer: "knowledge", target: "K"})[0]
			j8Withdrawn(t, f.s, "knowledge", "K", id)
			return formatOccurrenceID(id)
		},
		"malformed": func(_ *j8Fixture, _ *testing.T) string { return "x" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newJ8Fixture(t)
			ok := f.occ(t, 1, j8Row{})[0]
			f.occ(t, 1, j8Row{cause: "performance"})
			bad := mk(f, t)
			f.out([]any{j8Proposal("prompt", "T", formatOccurrenceID(ok)), j8Proposal("knowledge", "K", bad)})
			before, challengesBefore := f.handledAll(t), f.count(t, `SELECT COUNT(*) FROM challenge`)
			res := f.manual(t)
			if res.Result != RunResultInvalidOutput || len(res.Filed) != 0 {
				t.Fatalf("res = %+v", res)
			}
			if f.handledAll(t) != before || f.count(t, `SELECT COUNT(*) FROM challenge`) != challengesBefore ||
				f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'improvement'`) != 0 {
				t.Errorf("something was written: %s", f.handledAll(t))
			}
		})
	}
}

func TestImproveJ8_FilesImprovementChallenges_AC348_349_350_352_356_357(t *testing.T) {
	f := newJ8Fixture(t)
	p := f.occ(t, 2, j8Row{})
	k := f.occ(t, 1, j8Row{layer: "knowledge", target: "K"})
	rest := f.occ(t, 1, j8Row{layer: "entry_skill", target: "E"})
	f.occ(t, 1, j8Row{cause: "performance"})
	f.out([]any{
		j8Proposal("prompt", " T ", formatOccurrenceID(p[0]), formatOccurrenceID(p[1])),
		j8Proposal("knowledge", "K", formatOccurrenceID(k[0])),
	})
	res := f.manual(t)
	if len(res.Filed) != 2 || len(res.Linked) != 0 || len(res.Waited) != 1 {
		t.Fatalf("res = %+v", res)
	}
	pc, kc := res.Filed[0], res.Filed[1]
	for _, c := range []string{pc, kc} {
		if f.challengeField(t, c, "origin") != "improvement" || f.challengeField(t, c, "status") != "unclassified" || f.challengeField(t, c, "version") != "1" {
			t.Errorf("%s: origin/status/version wrong", c)
		}
	}
	if f.challengeField(t, pc, "improvement_layer") != "prompt" || f.challengeField(t, pc, "improvement_target") != "T" {
		t.Errorf("key = %q/%q", f.challengeField(t, pc, "improvement_layer"), f.challengeField(t, pc, "improvement_target"))
	}
	if f.challengeField(t, pc, "title") != "題名  T " {
		t.Errorf("title = %q", f.challengeField(t, pc, "title"))
	}
	desc := f.challengeField(t, pc, "description")
	for _, want := range []string{"層: prompt", "対象: T", "原因: context", "改善したい指標: rejection_rate", "期待する変化: 却下が減る", "種類: add",
		formatOccurrenceID(p[0]) + ", " + formatOccurrenceID(p[1]), "成果物の経路: Issue"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description lacks %q:\n%s", want, desc)
		}
	}
	// AC-356: prompt の完了条件にだけ分量と禁止語の検査の文を足す（AC-357）。
	if dc := f.challengeField(t, pc, "done_criteria"); !strings.HasPrefix(dc, "完了条件") || !strings.Contains(dc, "CheckPromptSizes") || !strings.Contains(dc, "禁止語") {
		t.Errorf("prompt done_criteria = %q", dc)
	}
	if dc := f.challengeField(t, kc, "done_criteria"); dc != "完了条件 K" {
		t.Errorf("knowledge done_criteria = %q", dc)
	}
	// AC-350
	for _, id := range p {
		if f.handled(t, id) != pc {
			t.Errorf("O-%d handled = %q, want %s", id, f.handled(t, id), pc)
		}
	}
	if f.handled(t, k[0]) != kc || f.handled(t, rest[0]) != "" {
		t.Errorf("handled = %s", f.handledAll(t))
	}
	// AC-352
	n, _ := parseChallengeID(pc)
	var after string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT after FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = 'improvement'`, n).Scan(&after)
	}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Layer  string   `json:"improvement_layer"`
		Target string   `json:"improvement_target"`
		IDs    []string `json:"occurrence_ids"`
	}
	if err := json.Unmarshal([]byte(after), &got); err != nil || got.Layer != "prompt" || got.Target != "T" || len(got.IDs) != 2 {
		t.Errorf("after = %s (%v)", after, err)
	}
}

func TestImproveJ8_RoutesFromDeclaration(t *testing.T) {
	f := newJ8Fixture(t)
	ids := f.occ(t, 2, j8Row{layer: "knowledge", target: "K"})
	f.out([]any{j8Proposal("knowledge", "K", formatOccurrenceID(ids[0]))})
	decl := &ConnectorsDeclaration{Repos: []ConnectorRepo{{Name: "r", Remote: "o/r"}}}
	git := newFakeSlotGit()
	git.states[f.s.Workspace()] = okState()
	res := f.run(t, ImproveJ8Input{Manual: true, ConnDecl: decl, SlotGit: git})
	if len(res.Filed) != 1 || !strings.Contains(f.challengeField(t, res.Filed[0], "description"), "成果物の経路: ワークスペースの変更（対象のリポジトリ: o/r）") {
		t.Fatalf("res = %+v\n%s", res, f.challengeField(t, res.Filed[0], "description"))
	}
	// SlotGit が渡されなければ origin は空で、経路は Issue。
	f.out([]any{j8Proposal("knowledge", "K2", formatOccurrenceID(ids[1]))})
	res = f.run(t, ImproveJ8Input{Manual: true, ConnDecl: decl})
	if len(res.Filed) != 1 || !strings.Contains(f.challengeField(t, res.Filed[0], "description"), "成果物の経路: Issue") {
		t.Fatalf("res = %+v", res)
	}
}

func TestImproveJ8_AtomicWhenWritesFail_AC351(t *testing.T) {
	boom := errors.New("injected failure")
	for name, inject := range map[string]func(s *Store){
		"challenge insert": func(s *Store) { s.insertChallenge = func(*sql.Tx, ImprovementKey) error { return boom } },
		"second challenge insert (after the first was filed and marked)": func(s *Store) {
			n := 0
			s.insertChallenge = func(*sql.Tx, ImprovementKey) error {
				if n++; n == 2 {
					return boom
				}
				return nil
			}
		},
		"improvement activity": func(s *Store) {
			s.insertActivity = func(tx *sql.Tx, row activityRow) error {
				if row.Action == ActivityActionImprovement {
					return boom
				}
				return defaultInsertActivity(tx, row)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newJ8Fixture(t)
			ids := f.occ(t, 2, j8Row{})
			f.occ(t, 1, j8Row{cause: "performance"})
			f.out([]any{j8Proposal("prompt", "T", formatOccurrenceID(ids[0])), j8Proposal("knowledge", "K", formatOccurrenceID(ids[1]))})
			mexec(t, f.s, `UPDATE occurrence SET layer = 'knowledge', target = 'K' WHERE id = ?`, ids[1])
			before, challenges := f.handledAll(t), f.count(t, `SELECT COUNT(*) FROM challenge`)
			inject(f.s)
			if _, err := f.try(ImproveJ8Input{Manual: true}); !errors.Is(err, boom) {
				t.Fatalf("err = %v", err)
			}
			if f.handledAll(t) != before || f.count(t, `SELECT COUNT(*) FROM challenge`) != challenges ||
				f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'improvement'`) != 0 {
				t.Errorf("partial write remained: %s", f.handledAll(t))
			}
		})
	}
}

func TestImproveJ8_AutoMappingErrorIsCountedAsInvalidOutput(t *testing.T) {
	f := newJ8Fixture(t)
	f.occ(t, 10, j8Row{})
	f.s.insertChallenge = func(*sql.Tx, ImprovementKey) error { return errors.New("injected") }
	ids := f.s.mustOccIDs(t)
	f.out([]any{j8Proposal("prompt", "T", formatOccurrenceID(ids[len(ids)-1]))})
	res := f.auto(t)
	if res.Result != RunResultInvalidOutput || !strings.Contains(res.Note, "mapping failed") {
		t.Fatalf("res = %+v", res)
	}
	if f.count(t, `SELECT COUNT(*) FROM run WHERE judgment = 'J8' AND result = 'invalid_output'`) != 1 {
		t.Error("the run was not rewritten to invalid_output in the store")
	}
}

func (s *Store) mustOccIDs(t *testing.T) []int64 {
	t.Helper()
	var ids []int64
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id FROM occurrence ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestImproveJ8_DuplicateSuppression_AC358_359_360_361(t *testing.T) {
	f := newJ8Fixture(t)
	ids := f.occ(t, 5, j8Row{})
	open := j8InsertImprovement(t, f.s, "prompt", "T", "in_progress")
	mexec(t, f.s, `UPDATE challenge SET version = 7 WHERE id = ?`, open)
	j8InsertImprovement(t, f.s, "prompt", "U", "done")
	j8InsertImprovement(t, f.s, "prompt", "W", "withdrawn")
	f.out([]any{
		j8Proposal("prompt", "  T\n", formatOccurrenceID(ids[0])), // AC-361: 前後の空白を除いて一致 → 紐づける
		j8Proposal("prompt", "U", formatOccurrenceID(ids[1])),     // AC-360: 完了しているだけ → 新しく起票
		j8Proposal("prompt", "W", formatOccurrenceID(ids[2])),     // 取り下げ済みも終端 → 新しく起票
	})
	res := f.manual(t)
	if len(res.Linked) != 1 || res.Linked[0] != formatChallengeID(open) || len(res.Filed) != 2 {
		t.Fatalf("res = %+v", res)
	}
	if f.handled(t, ids[0]) != formatChallengeID(open) { // AC-358
		t.Errorf("handled = %q", f.handled(t, ids[0]))
	}
	if f.challengeField(t, formatChallengeID(open), "status") != "in_progress" || f.challengeField(t, formatChallengeID(open), "version") != "7" { // AC-359
		t.Error("the linked challenge changed")
	}
	if f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'improvement'`) != 2 {
		t.Error("a linked proposal wrote an improvement activity")
	}
}

func TestImproveJ8_DifferentLayerIsDifferentKey_AC361(t *testing.T) {
	f := newJ8Fixture(t)
	ids := f.occ(t, 1, j8Row{layer: "entry_skill", target: "T"})
	j8InsertImprovement(t, f.s, "prompt", "T", "unclassified")
	f.out([]any{j8Proposal("entry_skill", "T", formatOccurrenceID(ids[0]))})
	if res := f.manual(t); len(res.Filed) != 1 || len(res.Linked) != 0 {
		t.Fatalf("res = %+v", res)
	}
}

func TestImproveJ8_ConcurrentJ8_RecheckInvalidatesWholeOutput(t *testing.T) {
	for name, target := range map[string]string{"evidence": "evidence", "wait": "wait"} {
		t.Run(name, func(t *testing.T) {
			f := newJ8Fixture(t)
			ev := f.occ(t, 1, j8Row{})[0]
			w := f.occ(t, 1, j8Row{cause: "performance"})[0]
			f.out([]any{j8Proposal("prompt", "T", formatOccurrenceID(ev))})
			var before string
			f.inv.onInvoke = func(JudgmentLaunchInput) { // 並行に走った別の J8 が先に処理した
				id := ev
				if target == "wait" {
					id = w
				}
				mexec(t, f.s, `UPDATE occurrence SET handled = 'C-9' WHERE id = ?`, id)
				before = f.handledAll(t)
			}
			res := f.manual(t)
			if res.Result != RunResultInvalidOutput || len(res.Filed) != 0 || f.handledAll(t) != before ||
				f.count(t, `SELECT COUNT(*) FROM challenge WHERE origin = 'improvement'`) != 0 {
				t.Errorf("res = %+v handled = %s", res, f.handledAll(t))
			}
		})
	}
}

func TestImproveJ8_RunFailureWritesNothing(t *testing.T) {
	f := newJ8Fixture(t)
	f.occ(t, 1, j8Row{cause: "performance"})
	f.occ(t, 1, j8Row{})
	f.inv.result = JudgmentLaunchOutput{Result: RunResultErrored}
	before := f.handledAll(t)
	if res := f.manual(t); res.Result != RunResultErrored || f.handledAll(t) != before {
		t.Errorf("res = %+v handled = %s", res, f.handledAll(t))
	}
}

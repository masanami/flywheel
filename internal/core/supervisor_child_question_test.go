package core

// 区切り S4b-3 のうち、子の問いの土台（止まり方 child_question・手 answer・answers の検査・根拠の検査・答えによる再開の上限・
// 対処の記録の問いと答え）を、実ストアで確かめる。子の問いの呼び出し元（委譲の報告・契約の判断待ち）の付け替えはこのファイルの範囲外。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

func svRun(out string) *RunJudgmentResult {
	return &RunJudgmentResult{Result: RunResultSucceeded, StructuredOutput: []byte(out)}
}

var childOffered = SupervisorHandsFor(StopKindChildQuestion)

func answerJSON(answers ...string) string {
	b, _ := json.Marshal(map[string]any{
		"hand": "answer", "findings": "調べた", "note": "", "basis": []any{}, "recommendation": "", "options": []any{}, "conditions": []any{},
		"answers": answers2any(answers),
	})
	return string(b)
}

// answers2any は "答え|path" の並びを answers の値にする（path は空なら basis が空）。
func answers2any(in []string) []any {
	out := []any{}
	for _, a := range in {
		parts := strings.SplitN(a, "|", 2)
		basis := []any{}
		if len(parts) == 2 && parts[1] != "" {
			basis = append(basis, map[string]any{"path": parts[1], "locator": "節"})
		}
		out = append(out, map[string]any{"answer": parts[0], "basis": basis})
	}
	return out
}

// childWorkspace は根拠の検査用のワークスペース（根）を作る。
func childWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("docs/a.md", "a")
	write(".flywheel/secret.txt", "s")
	write("../outside-"+filepath.Base(root)+".txt", "o")
	if err := os.Symlink(filepath.Join("..", "outside-"+filepath.Base(root)+".txt"), filepath.Join(root, "docs", "link-out.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.md", filepath.Join(root, "docs", "link-in.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(".flywheel", "secret.txt"), filepath.Join(root, "docs", "link-store.md")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(filepath.Dir(root), "outside-"+filepath.Base(root)+".txt")) })
	return root
}

// AC-1: 宣言に judgment_budget_usd.J4 があっても、読み込みはエラーにならない。
func TestChildQuestion_AC1_DeclarationWithJ4IsLoadable(t *testing.T) {
	t.Parallel()
	dir := writeAgentJSON(t, allKeysAgentJSON)
	if !strings.Contains(allKeysAgentJSON, `"J4": 2`) {
		t.Fatal("fixture lacks J4")
	}
	if _, err := LoadAgentDeclaration(dir); err != nil {
		t.Fatalf("LoadAgentDeclaration() with J4 = %v, want nil", err)
	}
}

// AC-2: 止まり方 child_question の選べる手は、answer と escalate だけ。
func TestChildQuestion_AC2_OnlyAnswerAndEscalate(t *testing.T) {
	t.Parallel()
	if got, want := SupervisorHandsFor(StopKindChildQuestion), []SupervisorHand{HandAnswer, HandEscalate}; !reflect.DeepEqual(got, want) {
		t.Errorf("hands for child_question = %v, want %v", got, want)
	}
	for _, h := range SupervisorHandValues() {
		allowed := supervisorHandAllowed(StopKindChildQuestion, h)
		if allowed != (h == HandAnswer || h == HandEscalate) {
			t.Errorf("hand %s allowed for child_question = %v", h, allowed)
		}
	}
	// answer を選べるのは、委譲先の問いと下書きの問いの 2 つの止まり方だけ。
	var answering []StopKind
	for _, k := range StopKindValues() {
		if supervisorHandAllowed(k, HandAnswer) {
			answering = append(answering, k)
		}
	}
	if want := []StopKind{StopKindChildQuestion, StopKindSpecQuestion}; !reflect.DeepEqual(answering, want) {
		t.Errorf("stop kinds offering answer = %v, want %v", answering, want)
	}
}

// AC-3: child_question で retry を返すと、手を実行できる出力として受け取らず（委譲先へ答えは返らず）、不備の理由が付く。
func TestChildQuestion_AC3_RetryIsRejected(t *testing.T) {
	t.Parallel()
	retry := `{"hand":"retry","findings":"f","note":"","basis":[],"recommendation":"","options":[],"conditions":[],"answers":[]}`
	out, flaw := inspectChildQuestionOutput(svRun(retry), childOffered, []ChildQuestion{{Question: "q"}}, t.TempDir())
	if out != nil || flaw == nil || !flaw.BadOutput || !strings.Contains(flaw.Reason, "offered") {
		t.Errorf("out=%v flaw=%+v, want a bad-output flaw for a hand outside the offered hands", out, flaw)
	}
}

// answers の検査と根拠の検査（AC-18・AC-29・AC-37〜AC-44）。
func TestChildQuestion_AnswersAndBasisChecks(t *testing.T) {
	t.Parallel()
	root := childWorkspace(t)
	one := []ChildQuestion{{Question: "q1"}}
	two := []ChildQuestion{{Question: "q1"}, {Question: "q2"}}
	choice := []ChildQuestion{{Question: "どれ", Options: []string{"A 案", "B 案"}}}
	outsideAbs := filepath.Join(filepath.Dir(root), "outside-"+filepath.Base(root)+".txt")
	cases := []struct {
		name string
		qs   []ChildQuestion
		out  string
		ok   bool
	}{
		{"AC-37 通常のファイルは根拠になる", one, answerJSON("答え|docs/a.md"), true},
		{"AC-37 根の下へ解決するリンクは根拠になる", one, answerJSON("答え|docs/link-in.md"), true},
		{"AC-37 選択肢の見出しを選べる", choice, answerJSON("B 案|docs/a.md"), true},
		{"AC-37 2 問に 2 件", two, answerJSON("x|docs/a.md", "y|docs/a.md"), true},
		{"AC-18 件数が問いより少ない", two, answerJSON("x|docs/a.md"), false},
		{"AC-18 件数が問いより多い", one, answerJSON("x|docs/a.md", "y|docs/a.md"), false},
		{"AC-29 選択肢に無い値", choice, answerJSON("C 案|docs/a.md"), false},
		{"AC-38 存在しない場所", one, answerJSON("x|docs/none.md"), false},
		{"AC-40 根の外（相対）", one, answerJSON("x|../outside-" + filepath.Base(root) + ".txt"), false},
		{"AC-40 根の外（絶対）", one, answerJSON("x|" + outsideAbs), false},
		{"AC-41 リンクの先が外", one, answerJSON("x|docs/link-out.md"), false},
		{"AC-42 ディレクトリ", one, answerJSON("x|docs"), false},
		{"AC-43 ストアの置き場の下", one, answerJSON("x|.flywheel/secret.txt"), false},
		{"AC-43 ストアの置き場へのリンク", one, answerJSON("x|docs/link-store.md"), false},
		{"AC-44 basis が空", one, answerJSON("x"), false},
		{"2 問のうち 1 問だけ根拠が無い", two, answerJSON("x|docs/a.md", "y|docs/none.md"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, flaw := inspectChildQuestionOutput(svRun(c.out), childOffered, c.qs, root)
			if c.ok {
				if flaw != nil || out == nil || out.Hand != HandAnswer {
					t.Fatalf("flaw = %+v, want accepted", flaw)
				}
				return
			}
			if out != nil || flaw == nil || !flaw.BadOutput {
				t.Fatalf("out = %v flaw = %+v, want a bad-output flaw (no answer returned to the delegate)", out, flaw)
			}
		})
	}
	// 根拠が 1 件でも数えられれば、数えられない根拠が混じっていてもその問いは根拠がある。
	mixed := `{"hand":"answer","findings":"f","note":"","basis":[],"recommendation":"","options":[],"conditions":[],"answers":[{"answer":"x","basis":[{"path":"docs/none.md","locator":""},{"path":"docs/a.md","locator":""}]}]}`
	if _, flaw := inspectChildQuestionOutput(svRun(mixed), childOffered, one, root); flaw != nil {
		t.Errorf("flaw = %+v, want accepted when at least one basis counts", flaw)
	}
}

// answers を持たない answer・answer 以外の手の answers は、出力の形の不備。escalate は answers なしで通る。
func TestChildQuestion_ParseAnswersShape(t *testing.T) {
	t.Parallel()
	noAnswers := `{"hand":"answer","findings":"f","note":"","basis":[],"recommendation":"","options":[],"conditions":[],"answers":[]}`
	if _, err := ParseSVOutput([]byte(noAnswers), childOffered); err == nil {
		t.Error("answer without answers must be invalid")
	}
	blank := `{"hand":"answer","findings":"f","note":"","basis":[],"recommendation":"","options":[],"conditions":[],"answers":[{"answer":" ","basis":[]}]}`
	if _, err := ParseSVOutput([]byte(blank), childOffered); err == nil {
		t.Error("a blank answer must be invalid")
	}
	stray := `{"hand":"escalate","findings":"f","note":"","basis":[],"recommendation":"r","options":[],"conditions":[],"answers":[{"answer":"x","basis":[]}]}`
	if _, err := ParseSVOutput([]byte(stray), childOffered); err == nil {
		t.Error("answers with escalate must be invalid")
	}
	esc := `{"hand":"escalate","findings":"f","note":"","basis":[],"recommendation":"r","options":[],"conditions":[],"answers":[]}`
	if _, err := ParseSVOutput([]byte(esc), childOffered); err != nil {
		t.Errorf("escalate = %v, want valid", err)
	}
	// スキーマに answers があり、必須である。
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(SVOutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["answers"]; !ok || !strings.Contains(strings.Join(schema.Required, ","), "answers") {
		t.Error("the SV output schema must declare answers (required)")
	}
}

// 子の問いの保留: 止まり方 child_question の保留は、スーパーバイザー待ちにならない。
func TestChildQuestion_HoldIsNeverPendingAndCarriesEscalation(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	cid, _ := parseChallengeID(c.cid)
	mk := func(opts HoldOptionsInput) (q string, stop, state, findings, rec sql.NullString, action sql.NullInt64) {
		t.Helper()
		if _, err := c.s.transition(context.Background(), ChannelCLI, c.cid, OpHold, func(ctx context.Context, tc *transitionCtx) error {
			_, err := insertHoldWithOptions(ctx, tc, "もとの問い", HoldKindHuman, tc.current.Status, nil, opts)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := c.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT question, stop_kind, supervisor_state, supervisor_findings, supervisor_recommendation, supervisor_action_id FROM hold WHERE challenge_id = ? ORDER BY id DESC LIMIT 1`, cid).
				Scan(&q, &stop, &state, &findings, &rec, &action)
		}); err != nil {
			t.Fatal(err)
		}
		c.release(t, "supervisor")
		return
	}
	// 判断点を呼ばずに上げた保留（「人に聞く種類」・supervisor が偽）。
	q, stop, state, _, _, _ := mk(HoldOptionsInput{StopKind: StopKindChildQuestion, SupervisorEnabled: true})
	if stop.String != "child_question" || state.Valid || q != "もとの問い" {
		t.Errorf("hold = %q %v %v, want child_question with NULL supervisor_state (never pending)", q, stop, state)
	}
	// スーパーバイザーが escalate で上げた保留。
	id := recordChildAction(t, c, HandEscalate, ActionExecuted)
	q, stop, state, findings, rec, action := mk(HoldOptionsInput{StopKind: StopKindChildQuestion, Escalation: &HoldEscalation{Findings: "調べた", Recommendation: "推奨", ActionID: id}})
	if stop.String != "child_question" || state.String != "escalated" || findings.String != "調べた" || rec.String != "推奨" || action.Int64 != id || q != "もとの問い" {
		t.Errorf("escalated hold = %q %v %v %v %v %v", q, stop, state, findings, rec, action)
	}
	// 子の問い以外に Escalation は付けられない。
	if _, err := c.s.transition(context.Background(), ChannelCLI, c.cid, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		_, err := insertHoldWithOptions(ctx, tc, "q", HoldKindHuman, tc.current.Status, nil, HoldOptionsInput{StopKind: StopKindFactsUnmet, Escalation: &HoldEscalation{}})
		return err
	}); err == nil {
		t.Error("an escalation for a stop kind other than child_question must be refused")
	}
}

func recordChildAction(t *testing.T, c *limitCase, hand SupervisorHand, result SupervisorActionResult) int64 {
	t.Helper()
	a, err := c.s.recordSupervisorAction(context.Background(), SupervisorActionInput{
		CycleID: c.cycID, ChallengeID: c.cid, StopKind: StopKindChildQuestion, Hand: hand, Findings: "f", Result: result,
		Questions: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

// AC-18・AC-19・AC-29・AC-30・AC-38・AC-39: 不備のある答えは、結果 failed の対処の記録と、問い全部の保留 1 件になる。
func TestChildQuestion_FlawedAnswerRecordsFailedAndRaisesOneHold(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	root := childWorkspace(t)
	_, flaw := inspectChildQuestionOutput(svRun(answerJSON("x|docs/none.md")), childOffered, []ChildQuestion{{Question: "q1"}}, root)
	if flaw == nil {
		t.Fatal("want a flaw")
	}
	ctx := context.Background()
	req := supervisorHandRequest{CycleID: c.cycID, Target: supervisorTarget{ChallengeID: 1, StopKind: StopKindChildQuestion}}
	hand := HandAnswer
	id, err := c.s.recordSupervisorChildFailure(ctx, req, &hand, flaw.Reason)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.s.transition(ctx, ChannelCLI, c.cid, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		_, err := insertHoldWithOptions(ctx, tc, "q1\nq2", HoldKindHuman, tc.current.Status, nil, HoldOptionsInput{
			StopKind: StopKindChildQuestion, Escalation: &HoldEscalation{Findings: flaw.Reason, Recommendation: "オーナーが判断", ActionID: id, FailedQuestion: true}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	acts, err := c.s.ListSupervisorActionsByChallenge(ctx, c.cid)
	if err != nil {
		t.Fatal(err)
	}
	// 対処の記録は、できた保留を指す（保留が無いのは、答えて保留を作らなかったときだけ）。
	if len(acts) != 1 || acts[0].Result != ActionFailed || acts[0].HoldID == nil || acts[0].StopKind != StopKindChildQuestion {
		t.Fatalf("actions = %+v, want one failed child_question action tied to the hold it produced", acts)
	}
	var n int
	var q, state string
	if err := c.s.db.Read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM hold`).Scan(&n); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT question, supervisor_state FROM hold`).Scan(&q, &state)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 || state != "escalated" || !strings.HasPrefix(q, supervisorFailedQuestionHead+"\n") || !strings.Contains(q, "q1\nq2") {
		t.Errorf("holds = %d, state %q, question %q; want 1 escalated hold with the failed line and all questions", n, state, q)
	}
}

// AC-45〜AC-50: 答えによる再開の上限（課題あたり 3 回。最後のオーナーの回答より後。J4 の記録・ほかの手・ほかの結果は数えない）。
func TestChildQuestion_AnswerLimit(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	ctx := context.Background()
	cid, _ := parseChallengeID(c.cid)
	reached := func() bool {
		var r bool
		if err := c.s.db.Read(ctx, func(tx *sql.Tx) error {
			var err error
			r, err = supervisorAnswerLimitReached(ctx, tx, cid)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return r
	}
	answered := func(task string, result SupervisorActionResult) {
		in := SupervisorActionInput{CycleID: c.cycID, ChallengeID: c.cid, TaskID: task, StopKind: StopKindChildQuestion, Hand: HandAnswer, Findings: "f", Result: result}
		if _, err := c.s.recordSupervisorAction(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	// 2 件（AC-50: 同じタスクに executed の answer が 2 件あっても、2 回の上限の対象外。3 件目の子の問いでもスーパーバイザーを呼べる）。
	answered(c.task, ActionExecuted)
	answered(c.task, ActionExecuted)
	if reached() {
		t.Fatal("2 answers must not reach the limit")
	}
	if hit, err := supervisorLimitHitForTest(c, StopKindChildQuestion, c.task); err != nil || hit != nil {
		t.Errorf("the 2-time limit must not apply to child_question: %v %v", hit, err)
	}
	// 数えないもの: 結果が failed・手が escalate・J4 の実行の記録（run の judgment が J4）。
	answered(c.task, ActionFailed)
	c.act(t, StopKindChildQuestion, HandEscalate, ActionExecuted, "", c.task)
	_ = insertJ4Run(t, c)
	if reached() {
		t.Fatal("failed answers, escalations and past J4 runs must not count (AC-49)")
	}
	// 3 件目で上限（タスクが違っても、課題で数える）。
	answered("", ActionExecuted)
	if !reached() {
		t.Fatal("3 executed answers must reach the limit (AC-45)")
	}
	// 上限のときの保留: 固定の 1 行・止まり方 child_question・supervisor_state は NULL・対処の記録は増えない（AC-46〜AC-48）。
	before, _ := c.s.ListSupervisorActionsByChallenge(ctx, c.cid)
	if _, err := c.s.transition(ctx, ChannelCLI, c.cid, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		_, err := insertHoldWithOptions(ctx, tc, "もとの問い", HoldKindHuman, tc.current.Status, nil, HoldOptionsInput{StopKind: StopKindChildQuestion, SupervisorEnabled: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var q string
	var stop, state sql.NullString
	if err := c.s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT question, stop_kind, supervisor_state FROM hold`).Scan(&q, &stop, &state)
	}); err != nil {
		t.Fatal(err)
	}
	if want := "スーパーバイザーが答えて再開できる回数の上限（3 回）に達した。\nもとの問い"; q != want {
		t.Errorf("question = %q, want %q", q, want)
	}
	if stop.String != "child_question" || state.Valid {
		t.Errorf("stop_kind = %v, supervisor_state = %v, want child_question, NULL", stop, state)
	}
	if after, _ := c.s.ListSupervisorActionsByChallenge(ctx, c.cid); len(after) != len(before) {
		t.Errorf("actions %d -> %d, want no action record for the limit", len(before), len(after))
	}
	// オーナーが回答すると、その回答より後の数えは 0 から始まる（回答より後の時刻の記録だけを数える）。
	later := c.s.currentTime().Add(time.Hour)
	c.s.now = func() time.Time { return later }
	c.release(t, "owner")
	c.s.now = func() time.Time { return later.Add(time.Minute) }
	if reached() {
		t.Error("answers before the last owner answer must not count")
	}
	answered(c.task, ActionExecuted)
	if reached() {
		t.Error("1 answer after the owner answer must not reach the limit")
	}
}

// supervisorLimitHitForTest は 2 回の上限の判定を読み取りで呼ぶ。
func supervisorLimitHitForTest(c *limitCase, kind StopKind, task string) ([]SupervisorHand, error) {
	var hit []SupervisorHand
	tid, _ := parseTaskID(task)
	err := c.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		hit, err = supervisorLimitHit(context.Background(), tx, 1, &tid, kind)
		return err
	})
	return hit, err
}

// insertJ4Run は過去の J4 の実行の記録を 1 件置く（再開の数えに入らないことの確認用）。
func insertJ4Run(t *testing.T, c *limitCase) int64 {
	t.Helper()
	var id int64
	if err := c.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO run (challenge_id, kind, judgment, challenge_version, plan_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd)
			VALUES (1, 'judgment', 'J4', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:01.000Z', 'succeeded', 1000000, 'judgment', 0)`)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("insert J4 run: %v", err)
	}
	return id
}

// AC-51・AC-52: 子の問いに答えた対処の記録に、問いと答えと根拠がある。保留は指さない。
func TestChildQuestion_AnswerRecordKeepsQuestionsAnswersAndBasis(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	ctx := context.Background()
	root := childWorkspace(t)
	qs := []ChildQuestion{{Question: "どちらにするか"}, {Question: "名前は"}}
	out, flaw := inspectChildQuestionOutput(svRun(answerJSON("A|docs/a.md", "foo|docs/link-in.md")), childOffered, qs, root)
	if flaw != nil {
		t.Fatalf("flaw = %+v", flaw)
	}
	req := supervisorHandRequest{CycleID: c.cycID, Target: supervisorTarget{ChallengeID: 1, StopKind: StopKindChildQuestion}, Output: *out}
	if _, _, err := c.s.recordSupervisorAnswer(ctx, req, qs[:1]); err == nil {
		t.Error("a mismatched number of questions must be refused")
	}
	id, executed, err := c.s.recordSupervisorAnswer(ctx, req, qs)
	if err != nil || !executed {
		t.Fatalf("executed = %v, err = %v", executed, err)
	}
	acts, err := c.s.ListSupervisorActionsByChallenge(ctx, c.cid)
	if err != nil || len(acts) != 1 || acts[0].ID != id {
		t.Fatalf("actions = %+v, %v", acts, err)
	}
	a := acts[0]
	if a.Hand == nil || *a.Hand != HandAnswer || a.Result != ActionExecuted || a.HoldID != nil {
		t.Errorf("action = %+v, want an executed answer without a hold", a)
	}
	want := []SupervisorQuestion{
		{Question: "どちらにするか", Answer: "A", Basis: []SupervisorBasis{{Path: "docs/a.md", Locator: "節"}}},
		{Question: "名前は", Answer: "foo", Basis: []SupervisorBasis{{Path: "docs/link-in.md", Locator: "節"}}},
	}
	if !reflect.DeepEqual(a.Questions, want) { // AC-51
		t.Errorf("questions = %+v, want %+v", a.Questions, want)
	}
	if len(a.Basis) != 2 { // AC-52
		t.Errorf("basis = %+v, want the bases of both answers", a.Basis)
	}
	// 作業ログに supervise が 1 行ある。
	var n int
	if err := c.s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM activity WHERE action = ?`, ActivityActionSupervise).Scan(&n)
	}); err != nil || n != 1 {
		t.Errorf("supervise activity rows = %d, %v; want 1", n, err)
	}
	// ほかの手の記録は questions が NULL（nil）。
	c.act(t, StopKindFactsUnmet, HandReverify, ActionExecuted, "", c.task)
	all, _ := c.s.ListSupervisorActionsByChallenge(ctx, c.cid)
	if all[1].Questions != nil {
		t.Errorf("a non-answer action has questions %+v, want none", all[1].Questions)
	}
}

// 根拠の検査の単体（空の path・根が空）。
func TestChildQuestion_BasisCountsEdgeCases(t *testing.T) {
	t.Parallel()
	root := childWorkspace(t)
	if basisCounts(root, ".FLYWHEEL/secret.txt") || basisCounts(root, ".Flywheel/secret.txt") {
		t.Error("the store directory must not count whatever the case of its name")
	}
	for _, p := range []string{"", "  ", ".", "docs/", "."} {
		if basisCounts(root, p) {
			t.Errorf("basisCounts(%q) = true, want false", p)
		}
	}
	if basisCounts("", "docs/a.md") {
		t.Error("an empty root must count nothing")
	}
	if !basisCounts(root, filepath.Join(root, "docs", "a.md")) {
		t.Error("an absolute path inside the root must count")
	}
}

// 判断点の結果の確かめ（verifyChildQuestionResult）: 検査を通った答えはそのまま返り、不備のある答えは理由が返り、その実行は失敗に数える。
func TestChildQuestion_VerifyResult(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	ctx := context.Background()
	root := childWorkspace(t)
	c.s.workspace = root
	qs := []ChildQuestion{{Question: "q1"}}
	good := svCall{Run: svRun(answerJSON("x|docs/a.md")), Offered: childOffered}
	if out, bad := c.s.verifyChildQuestionResult(ctx, good, qs); bad != "" || out == nil || out.Hand != HandAnswer {
		t.Fatalf("out = %v, bad = %q, want an accepted answer", out, bad)
	}
	runID := insertJ4Run(t, c)
	flawed := svCall{Run: svRun(answerJSON("x|docs/none.md")), Offered: childOffered}
	flawed.Run.RunID = formatRunID(runID)
	if out, bad := c.s.verifyChildQuestionResult(ctx, flawed, qs); out != nil || !strings.Contains(bad, "根拠") {
		t.Fatalf("out = %v, bad = %q, want the basis flaw", out, bad)
	}
	var result string
	if err := c.s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT result FROM run WHERE id = ?`, runID).Scan(&result)
	}); err != nil || result != string(RunResultInvalidOutput) {
		t.Errorf("run result = %q, %v; want %q (a flawed answer counts as a failed run)", result, err, RunResultInvalidOutput)
	}
}

// 手 answer は、承認・不可逆操作・計画・タスクの予算の表を変えない（承認の真正性）。
func TestChildQuestion_AnswerChangesNoApprovalOrBudget(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	root := childWorkspace(t)
	qs := []ChildQuestion{{Question: "q"}}
	out, flaw := inspectChildQuestionOutput(svRun(answerJSON("x|docs/a.md")), childOffered, qs, root)
	if flaw != nil {
		t.Fatal(flaw)
	}
	before := guardSnapshot(t, c.s)
	req := supervisorHandRequest{CycleID: c.cycID, Target: supervisorTarget{ChallengeID: 1, StopKind: StopKindChildQuestion}, Output: *out}
	if _, _, err := c.s.recordSupervisorAnswer(context.Background(), req, qs); err != nil {
		t.Fatal(err)
	}
	if after := guardSnapshot(t, c.s); after != before {
		t.Errorf("approvals, operations or budgets changed:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

// 判断の間に課題が着手中でなくなっていたら、答えは実行せず、断りの記録（rejected・state_changed）を残す（委譲先へ答えを返さない）。
func TestChildQuestion_AnswerIsRejectedWhenTheChallengeLeftProgress(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	ctx := context.Background()
	root := childWorkspace(t)
	qs := []ChildQuestion{{Question: "q"}}
	out, flaw := inspectChildQuestionOutput(svRun(answerJSON("x|docs/a.md")), childOffered, qs, root)
	if flaw != nil {
		t.Fatal(flaw)
	}
	coretest.Exec(t, c.s.workspace, `UPDATE challenge SET status = 'withdrawn' WHERE id = 1`)
	req := supervisorHandRequest{CycleID: c.cycID, Target: supervisorTarget{ChallengeID: 1, StopKind: StopKindChildQuestion}, Output: *out}
	_, executed, err := c.s.recordSupervisorAnswer(ctx, req, qs)
	if err != nil || executed {
		t.Fatalf("executed = %v, err = %v; want not executed", executed, err)
	}
	acts, err := c.s.ListSupervisorActionsByChallenge(ctx, c.cid)
	if err != nil || len(acts) != 1 {
		t.Fatalf("actions = %d, %v", len(acts), err)
	}
	if a := acts[0]; a.Result != ActionRejected || a.RejectReason == nil || *a.RejectReason != RejectStateChanged || len(a.Questions) != 0 {
		t.Errorf("action = %+v; want rejected, state_changed and no recorded answers", a)
	}
	if reached, err := c.s.answerLimitReached(ctx, 1); err != nil || reached {
		t.Errorf("a rejected answer must not count toward the limit: %v, %v", reached, err)
	}
}

// 判断の間に課題が着手中でなくなったら、上げる手も実行済みにしない（保留を作れないので、断りの記録 rejected・state_changed だけを残す）。
func TestChildQuestion_EscalateIsRejectedWhenTheChallengeLeftProgress(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	ctx := context.Background()
	coretest.Exec(t, c.s.workspace, `UPDATE challenge SET status = 'withdrawn' WHERE id = 1`)
	req := supervisorHandRequest{CycleID: c.cycID, Target: supervisorTarget{ChallengeID: 1, StopKind: StopKindChildQuestion},
		Output: SVOutput{Hand: HandEscalate, Findings: "f", Recommendation: "r"}}
	_, executed, err := c.s.recordSupervisorChildEscalate(ctx, req)
	if err != nil || executed {
		t.Fatalf("executed = %v, err = %v; want not executed", executed, err)
	}
	acts, err := c.s.ListSupervisorActionsByChallenge(ctx, c.cid)
	if err != nil || len(acts) != 1 {
		t.Fatalf("actions = %d, %v", len(acts), err)
	}
	if a := acts[0]; a.Result != ActionRejected || a.RejectReason == nil || *a.RejectReason != RejectStateChanged {
		t.Errorf("action = %+v; want rejected and state_changed", a)
	}
}

// AC-59: 過去の J4 の実行の記録がある課題を、課題の詳細（flywheel show が読む値）で読める。J4 の記録は実行の一覧に残る。
func TestChildQuestion_AC59_ChallengeWithPastJ4RunIsReadable(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	ctx := context.Background()
	runID := insertJ4Run(t, c)
	d, err := c.s.GetChallenge(ctx, c.cid)
	if err != nil {
		t.Fatalf("GetChallenge with a past J4 run: %v", err)
	}
	found := false
	for _, r := range d.Runs {
		if r.ID == formatRunID(runID) {
			found = r.Judgment == "J4"
		}
	}
	if !found {
		t.Errorf("the past J4 run is not in the challenge detail: %+v", d.Runs)
	}
}

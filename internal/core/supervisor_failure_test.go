package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// このファイルは区切り S4b-2 の「判断できなかったとき」と「選択肢つきの escalate」を、実ストアと偽の判断の IF で検証する
// （AC-167〜183・AC-219〜225）。

// 判断できなかったとき（手を実行しない・保留は上がる・対処の記録は failed・問いの先頭の固定の 1 行）の共通の確かめ。
func (f *svFixture) wantFailedAndEscalated(t *testing.T, id string, wantHand bool) {
	t.Helper()
	as := f.actions(t, id)
	if len(as) != 1 || as[0].Result != ActionFailed {
		t.Fatalf("actions = %+v, want exactly one failed action", as)
	}
	if (as[0].Hand != nil) != wantHand {
		t.Errorf("action hand = %v, want present = %v", as[0].Hand, wantHand)
	}
	if as[0].RunID == nil {
		t.Error("the failed action must keep the run when the judgment was launched")
	}
	r := f.holdRow(t, id)
	if r.state.String != "escalated" || !r.actionID.Valid || r.actionID.Int64 != as[0].ID {
		t.Errorf("hold = %+v, want escalated by the failed action", r)
	}
	d, err := f.s.GetChallenge(context.Background(), id)
	if err != nil || d.Status != StatusAwaitingHuman {
		t.Errorf("status = %v (%v), want awaiting_human (unchanged)", d.Status, err)
	}
	cid, _ := parseChallengeID(id)
	var q string
	var answered sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT question, answered_at FROM hold WHERE challenge_id = ?`, cid).Scan(&q, &answered)
	}); err != nil {
		t.Fatal(err)
	}
	if first := strings.SplitN(q, "\n", 2)[0]; first != "スーパーバイザーが判断できなかった。" {
		t.Errorf("first line of the question = %q, want the fixed line", first)
	}
	if answered.Valid {
		t.Error("the hold must not be answered")
	}
}

// AC-167〜169: 手ごとの必須の出力が欠けたとき、手を実行せず、保留は上がり、対処の記録は failed。
func TestSuperviseStage_MissingRequiredField_FailsWithoutRunningTheHand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		hand string
		kind StopKind
		set  map[string]any
	}{
		{"reverify", StopKindVerifyUncertain, map[string]any{"note": ""}},
		{"send_back", StopKindReworkLimit, map[string]any{"note": " "}},
		{"replan", StopKindReworkLimit, map[string]any{"note": ""}},
		{"defer", StopKindVerifyUncertain, map[string]any{"note": ""}},
		{"escalate", StopKindFactsUnmet, map[string]any{"recommendation": ""}},
	}
	for _, c := range cases {
		t.Run(c.hand, func(t *testing.T) {
			t.Parallel()
			f := newSVFixture(t)
			id := f.pendingHold(t, c.kind)
			set := map[string]any{"hand": c.hand}
			for k, v := range c.set {
				set[k] = v
			}
			f.inv.result = svResult(set)
			res := f.stage(t)
			f.wantFailedAndEscalated(t, id, false)
			if len(res.SuperviseHands) != 0 {
				t.Errorf("hands = %v, want none executed", res.SuperviseHands)
			}
		})
	}
}

// AC-170〜172: findings が空の出力。
func TestSuperviseStage_EmptyFindings_FailsWithoutRunningTheHand(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindFactsUnmet)
	f.inv.result = svResult(map[string]any{"findings": "  "})
	res := f.stage(t)
	f.wantFailedAndEscalated(t, id, false)
	if len(res.SuperviseHands) != 0 || f.holdRow(t, id).recommendation.String == "推奨する" {
		t.Errorf("hands = %v, recommendation = %q: the hand must not run", res.SuperviseHands, f.holdRow(t, id).recommendation.String)
	}
}

// AC-173: 閉じた一覧に無い hand。
func TestSuperviseStage_UnknownHand_Fails(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindFactsUnmet)
	f.inv.result = svResult(map[string]any{"hand": "approve"})
	f.stage(t)
	f.wantFailedAndEscalated(t, id, false)
}

// AC-174〜178: 判断点の失敗・型に合わない出力・費用の上限・時間の上限。
func TestSuperviseStage_JudgmentFailures_FailAndEscalate(t *testing.T) {
	t.Parallel()
	cases := map[string]JudgmentLaunchOutput{
		"errored":          {Result: RunResultErrored},
		"launch_failed":    {Result: RunResultLaunchFailed},
		"malformed":        {Result: RunResultMalformed},
		"budget_exhausted": {Result: RunResultBudgetExhausted, ReportedTotalCostUSD: float64Ptr(1)},
		"timed_out":        {Result: RunResultTimedOut},
		"wrong_type":       j6Result(map[string]any{"hand": 3, "findings": []any{}}),
		"not_json":         {Result: RunResultSucceeded, StructuredOutput: []byte(`"escalate"`), ReportedTotalCostUSD: float64Ptr(0.5)},
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSVFixture(t)
			id := f.pendingHold(t, StopKindFactsUnmet)
			f.inv.result = out
			res := f.stage(t)
			f.wantFailedAndEscalated(t, id, false)
			if len(res.SuperviseHands) != 0 || res.SuperviseProcessed != 1 {
				t.Errorf("hands = %v, processed = %d, want none and 1", res.SuperviseHands, res.SuperviseProcessed)
			}
		})
	}
}

// AC-180: 失敗で終わった保留について、同じサイクルで判断点を呼び直さない（同じ段をもう一度回しても呼ばない）。
func TestSuperviseStage_AfterAFailure_DoesNotCallAgainInTheSameCycle(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindFactsUnmet)
	f.inv.result = JudgmentLaunchOutput{Result: RunResultErrored}
	f.stage(t)
	f.stage(t)
	if len(f.inputs) != 1 || len(f.actions(t, id)) != 1 {
		t.Errorf("calls = %d, actions = %d, want 1 and 1", len(f.inputs), len(f.actions(t, id)))
	}
}

// AC-181・182: 利用上限で起動できなかったとき、待ちのまま残り、対処の記録は作られない。
func TestSuperviseStage_RateLimitedMidStage_KeepsTheRestPendingWithoutActions(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	a := f.pendingHold(t, StopKindVerifyUncertain)
	b := f.pendingHold(t, StopKindVerifyUncertain)
	f.inv.result = JudgmentLaunchOutput{Result: RunResultErrored, RateLimited: true}
	jc := NewJudgmentCycle(f.cycle)
	res, err := f.s.superviseStage(context.Background(), CycleRunInput{AgentDecl: f.agent, Invoker: f.inv}, f.cycle, jc)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b} {
		if len(f.actions(t, id)) != 0 || f.holdRow(t, id).state.String != "pending" {
			t.Errorf("%s: actions = %d, state = %q, want none and pending", id, len(f.actions(t, id)), f.holdRow(t, id).state.String)
		}
	}
	if len(f.inputs) != 1 || len(res.NotStarted) != 2 {
		t.Errorf("calls = %d, not_started = %v, want 1 call and both not started", len(f.inputs), res.NotStarted)
	}
}

// AC-183: サイクルの費用の上限で起動できなかったとき、待ちのまま残り、対処の記録は作られない。
func TestSuperviseStage_CycleBudgetExhausted_KeepsTheHoldPending(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	f.cycle = f.j6Fixture.cycle(t, 0.001)
	id := f.pendingHold(t, StopKindVerifyUncertain)
	res, err := f.s.superviseStage(context.Background(), CycleRunInput{AgentDecl: f.agent, Invoker: f.inv}, f.cycle, NewJudgmentCycle(f.cycle))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.inputs) != 0 || len(f.actions(t, id)) != 0 || f.holdRow(t, id).state.String != "pending" ||
		len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedCycleBudget {
		t.Errorf("calls = %d, actions = %d, state = %q, not_started = %v, want a cycle_budget stop with the hold pending",
			len(f.inputs), len(f.actions(t, id)), f.holdRow(t, id).state.String, res.NotStarted)
	}
}

func (f *svFixture) holdOptions(t *testing.T, id string) []HoldOption {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var raw sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT options FROM hold WHERE challenge_id = ?`, cid).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	var p *string
	if raw.Valid {
		p = &raw.String
	}
	opts, err := parseHoldOptions(p)
	if err != nil {
		t.Fatal(err)
	}
	return opts
}

// AC-219〜222: options を 2 件付けた escalate は、上げた保留の選択肢になる（識別 o1・o2、見出しと説明）。
func TestSuperviseStage_EscalateWithOptions_BecomesTheHoldOptions(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindFactsUnmet)
	f.inv.result = svResult(map[string]any{"options": []any{
		map[string]any{"label": "差し戻す", "description": "条件 2 が未達"},
		map[string]any{"label": "計画を見直す", "description": ""},
	}})
	f.stage(t)
	opts := f.holdOptions(t, id)
	if len(opts) != 2 || opts[0] != (HoldOption{ID: "o1", Label: "差し戻す", Description: "条件 2 が未達"}) ||
		opts[1] != (HoldOption{ID: "o2", Label: "計画を見直す"}) {
		t.Fatalf("options = %+v, want o1/o2 with the labels and descriptions", opts)
	}
	if as := f.actions(t, id); len(as) != 1 || as[0].Result != ActionExecuted {
		t.Errorf("actions = %+v, want one executed", as)
	}
	// 上げた保留は、選択肢の識別でも、文の回答でも答えられる（選択肢は必須にしない）。
	cid, _ := parseChallengeID(id)
	var req int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT option_required FROM hold WHERE challenge_id = ?`, cid).Scan(&req)
	}); err != nil || req != 0 {
		t.Errorf("option_required = %d (%v), want 0", req, err)
	}
}

// AC-223: options を付けなかった escalate は、選択肢 0 件。
func TestSuperviseStage_EscalateWithoutOptions_HasNoOptions(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindFactsUnmet)
	f.inv.result = svResult(nil)
	f.stage(t)
	if opts := f.holdOptions(t, id); len(opts) != 0 {
		t.Errorf("options = %+v, want none", opts)
	}
}

// AC-224・225: 1 件・7 件・label が空の要素を持つ options は出力の不備: 選択肢 0 件で上げ、対処の記録は failed。
func TestSuperviseStage_EscalateWithBadOptions_EscalatesWithoutOptionsAndFails(t *testing.T) {
	t.Parallel()
	opt := func(l string) map[string]any { return map[string]any{"label": l, "description": "d"} }
	cases := map[string][]any{
		"one":         {opt("a")},
		"seven":       {opt("a"), opt("b"), opt("c"), opt("d"), opt("e"), opt("f"), opt("g")},
		"empty_label": {opt("a"), opt("")},
	}
	for name, options := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSVFixture(t)
			id := f.pendingHold(t, StopKindFactsUnmet)
			f.inv.result = svResult(map[string]any{"options": options})
			f.stage(t)
			f.wantFailedAndEscalated(t, id, false)
			if opts := f.holdOptions(t, id); len(opts) != 0 {
				t.Errorf("options = %+v, want none", opts)
			}
		})
	}
}

// 失敗で上げた保留は、いまの保留と同じ操作で回答できる（回答の後の動きは変わらない）: 問いは先頭の 1 行のあとに元の問いを残す。
func TestSuperviseStage_FailedQuestionKeepsTheOriginalQuestion(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindFactsUnmet)
	cid, _ := parseChallengeID(id)
	var before string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT question FROM hold WHERE challenge_id = ?`, cid).Scan(&before)
	}); err != nil {
		t.Fatal(err)
	}
	f.inv.result = JudgmentLaunchOutput{Result: RunResultTimedOut}
	f.stage(t)
	var after string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT question FROM hold WHERE challenge_id = ?`, cid).Scan(&after)
	}); err != nil {
		t.Fatal(err)
	}
	if after != supervisorFailedQuestionHead+"\n"+before {
		t.Errorf("question = %q, want the fixed line then %q", after, before)
	}
}

// options を付けなかった escalate は、保留がもともと持つ選択肢（委譲先が返したもの）を消さない。
func TestSuperviseStage_EscalateWithoutOptions_KeepsTheHoldsOwnOptions(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindFactsUnmet)
	cid, _ := parseChallengeID(id)
	f.exec(t, `UPDATE hold SET options = ?, option_required = 1 WHERE challenge_id = ?`, `[{"id":"o1","label":"既存","description":""}]`, cid)
	f.inv.result = svResult(nil)
	f.stage(t)
	if opts := f.holdOptions(t, id); len(opts) != 1 || opts[0].Label != "既存" {
		t.Errorf("options = %+v, want the existing option kept", opts)
	}
}

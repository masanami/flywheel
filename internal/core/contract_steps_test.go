package core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// フローの移植 S1 の 8（AC-103〜AC-113）。契約の出力の flow・steps を contract_run・contract_run_step へ写す。

func stepOf(seq int, state string, mut func(s *ContractStep)) ContractStep {
	s := ContractStep{Seq: seq, Step: "analyze", Kind: "agent", Attempt: 1, State: state, StartedAt: "2026-01-01T00:00:00Z", ModelsUsed: []string{}}
	if mut != nil {
		mut(&s)
	}
	return s
}

func withSteps(flow *string, steps []ContractStep) func(st *ContractStatus) {
	return func(st *ContractStatus) { st.Flow = flow; st.Steps = steps }
}

func (f *contractFixture) stepsDump(t *testing.T) string {
	t.Helper()
	var out string
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT seq, step, kind, attempt, state, COALESCE(outcome, '~'), started_at, COALESCE(ended_at, '~'),
			COALESCE(cost_usd, -1), cost_unknown, COALESCE(model, '~'), models_used FROM contract_run_step ORDER BY seq`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var seq, attempt, cost, unknown int64
			var step, kind, state, outcome, started, ended, model, models string
			if err := rows.Scan(&seq, &step, &kind, &attempt, &state, &outcome, &started, &ended, &cost, &unknown, &model, &models); err != nil {
				return err
			}
			out += sqlJoin(seq, step, kind, attempt, state, outcome, started, ended, cost, unknown, model, models) + "\n"
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sqlJoin(vs ...any) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, "|")
}

func (f *contractFixture) pollWith(t *testing.T, mut func(st *ContractStatus)) {
	t.Helper()
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, mut)}
	f.poll(t)
}

func TestPollContractRuns_StepsAreMirroredIntoContractRunStep(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	flow := "ticket"
	cost, model, outcome, ended := 0.1234565, "m1", "ok", "2026-01-01T00:01:00Z"
	f.pollWith(t, withSteps(&flow, []ContractStep{
		stepOf(1, "finished", func(s *ContractStep) {
			s.Outcome, s.EndedAt, s.CostUSD, s.CostUnknown, s.Model, s.ModelsUsed = &outcome, &ended, &cost, true, &model, []string{"m1", "m2"}
		}),
		stepOf(2, "running", nil),
	}))

	t.Run("AC-103_row_count_equals_steps", func(t *testing.T) {
		if n := f.count(t, `SELECT COUNT(*) FROM contract_run_step`); n != 2 {
			t.Errorf("rows = %d, want 2", n)
		}
	})
	t.Run("AC-105_columns_match_the_output_and_cost_is_micros", func(t *testing.T) {
		want := "1|analyze|agent|1|finished|ok|2026-01-01T00:00:00Z|2026-01-01T00:01:00Z|123457|1|m1|[\"m1\",\"m2\"]\n" +
			"2|analyze|agent|1|running|~|2026-01-01T00:00:00Z|~|-1|0|~|[]\n"
		if got := f.stepsDump(t); got != want {
			t.Errorf("rows =\n%s\nwant\n%s", got, want)
		}
	})
	t.Run("AC-112_flow_is_recorded", func(t *testing.T) {
		if v := f.queryString(t, `SELECT flow FROM contract_run`); v.String != "ticket" {
			t.Errorf("flow = %v", v)
		}
	})
	t.Run("AC-106_reading_the_same_steps_twice_keeps_the_count", func(t *testing.T) {
		before := f.stepsDump(t)
		f.pollWith(t, withSteps(&flow, []ContractStep{
			stepOf(1, "finished", func(s *ContractStep) {
				s.Outcome, s.EndedAt, s.CostUSD, s.CostUnknown, s.Model, s.ModelsUsed = &outcome, &ended, &cost, true, &model, []string{"m1", "m2"}
			}),
			stepOf(2, "running", nil),
		}))
		if got := f.stepsDump(t); got != before {
			t.Errorf("rows changed:\n%s\nwant\n%s", got, before)
		}
	})
}

func TestPollContractRuns_StepRowsFollowTheLatestRead(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.pollWith(t, withSteps(nil, []ContractStep{stepOf(1, "running", nil), stepOf(2, "running", nil)}))
	t.Run("AC-107_running_becomes_finished_on_the_same_seq", func(t *testing.T) {
		f.pollWith(t, withSteps(nil, []ContractStep{stepOf(1, "finished", nil), stepOf(2, "running", nil)}))
		if v := f.queryString(t, `SELECT state FROM contract_run_step WHERE seq = 1`); v.String != "finished" {
			t.Errorf("state = %v", v)
		}
		if n := f.count(t, `SELECT COUNT(*) FROM contract_run_step`); n != 2 {
			t.Errorf("rows = %d, want 2", n)
		}
	})
	t.Run("AC-104_a_seq_missing_from_the_next_read_is_removed", func(t *testing.T) {
		f.pollWith(t, withSteps(nil, []ContractStep{stepOf(1, "finished", nil)}))
		if n := f.count(t, `SELECT COUNT(*) FROM contract_run_step`); n != 1 {
			t.Errorf("rows = %d, want 1", n)
		}
		if n := f.count(t, `SELECT COUNT(*) FROM contract_run_step WHERE seq = 2`); n != 0 {
			t.Errorf("seq 2 row remains")
		}
	})
	t.Run("AC-113_no_flow_in_the_output_is_NULL", func(t *testing.T) {
		if v := f.queryString(t, `SELECT flow FROM contract_run`); v.Valid {
			t.Errorf("flow = %v, want NULL", v)
		}
	})
	t.Run("an_empty_steps_array_removes_every_row", func(t *testing.T) {
		f.pollWith(t, withSteps(nil, []ContractStep{}))
		if n := f.count(t, `SELECT COUNT(*) FROM contract_run_step`); n != 0 {
			t.Errorf("rows = %d, want 0", n)
		}
	})
}

func TestPollContractRuns_AbsentStepsLeaveStepRowsAndOtherColumnsAlone(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	cost := 1.5
	f.pollWith(t, func(st *ContractStatus) {
		st.CostUSD = &cost
		st.Facts = map[string]string{"k": "v"}
		st.Steps = []ContractStep{stepOf(1, "running", nil)}
	})
	before := f.stepsDump(t)
	const cols = `SELECT state || '|' || summary || '|' || COALESCE(requested_kind, '') || '|' || cost_usd || '|' || COALESCE(facts, '') FROM contract_run`
	rowBefore := f.queryString(t, cols)

	// steps が無い（nil）出力。ほかの欄は同じ内容を出す。
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, &cost, func(st *ContractStatus) { st.Facts = map[string]string{"k": "v"} })}
	f.poll(t)

	t.Run("AC-108_existing_step_rows_are_unchanged", func(t *testing.T) {
		if got := f.stepsDump(t); got != before {
			t.Errorf("rows = %q, want %q", got, before)
		}
	})
	t.Run("AC-109_other_columns_are_the_same_as_without_steps", func(t *testing.T) {
		if got := f.queryString(t, cols); got != rowBefore {
			t.Errorf("row = %v, want %v", got, rowBefore)
		}
	})
}

// AC-120・AC-121: 契約の出力の steps の effort を contract_run_step.effort へ写す（無ければ NULL）。
func TestPollContractRuns_StepEffortIsRecorded(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	flow, effort := "ticket", "medium"
	f.pollWith(t, withSteps(&flow, []ContractStep{
		stepOf(1, "finished", func(s *ContractStep) { s.Effort = &effort }),
		stepOf(2, "running", nil),
	}))
	for seq, want := range map[int]string{1: "medium", 2: "~"} {
		got := f.queryString(t, fmt.Sprintf(`SELECT COALESCE(effort, '~') FROM contract_run_step WHERE seq = %d`, seq))
		if got.String != want {
			t.Errorf("AC-120/121 seq %d effort = %v, want %s", seq, got, want)
		}
	}
	// effort が付いても、ほかの列は S1 と同じに記録する。
	if got := f.queryString(t, `SELECT step || '|' || kind || '|' || state FROM contract_run_step WHERE seq = 1`); got.String != "analyze|agent|finished" {
		t.Errorf("other columns = %v", got)
	}
	// 再読込で effort が消えたら NULL に戻る。
	f.pollWith(t, withSteps(&flow, []ContractStep{stepOf(1, "finished", nil)}))
	if got := f.queryString(t, `SELECT COALESCE(effort, '~') FROM contract_run_step WHERE seq = 1`); got.String != "~" {
		t.Errorf("effort after removal = %v, want NULL", got)
	}
}

// 契約の出力の steps の findings_count（レビューのステップの指摘の数）を contract_run_step.findings_count へ写し、タスクの詳細に出す（無ければ NULL）。
func TestPollContractRuns_StepFindingsCountIsRecorded(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	flow, two, zero := "ticket", 2, 0
	f.pollWith(t, withSteps(&flow, []ContractStep{
		stepOf(1, "finished", func(s *ContractStep) { s.FindingsCount = &two }),
		stepOf(2, "finished", func(s *ContractStep) { s.FindingsCount = &zero }),
		stepOf(3, "running", nil),
	}))
	for seq, want := range map[int]string{1: "2", 2: "0", 3: "~"} {
		got := f.queryString(t, fmt.Sprintf(`SELECT COALESCE(CAST(findings_count AS TEXT), '~') FROM contract_run_step WHERE seq = %d`, seq))
		if got.String != want {
			t.Errorf("seq %d findings_count = %v, want %s", seq, got, want)
		}
	}
}

// ステップの行の findings_count が、課題の詳細の delegate.steps の要素の FindingsCount になる（NULL は nil、0 は 0）。
func TestTaskDetail_StepFindingsCount(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newMulti(t, "d", "P2", false, multiTask{key: "a"})
	cid, _ := parseChallengeID(id)
	_, tid := f.tdTask(t, id, "a")
	f.tdContractRun(t, cid, tid, "e1", tdCR{state: "running", summary: "s"})
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		ins := `INSERT INTO contract_run_step (contract_run_id, seq, step, kind, attempt, state, outcome, started_at, ended_at, cost_usd, cost_unknown, model, models_used, findings_count)
			VALUES ((SELECT id FROM contract_run WHERE external_run_id = 'e1'), ?, ?, 'llm', 1, 'finished', 'findings', '2026-11-01T00:00:00Z', '2026-11-01T00:00:05Z', 0, 0, NULL, '[]', ?)`
		for i, v := range []any{2, 0, nil} {
			if _, err := tx.Exec(ins, i+1, "self-review", v); err != nil {
				return err
			}
		}
		return nil
	})
	tk, _ := f.tdTask(t, id, "a")
	steps := tk.Detail.Delegate.Steps
	if len(steps) != 3 || steps[0].FindingsCount == nil || *steps[0].FindingsCount != 2 || steps[1].FindingsCount == nil || *steps[1].FindingsCount != 0 || steps[2].FindingsCount != nil {
		t.Fatalf("steps = %+v, want findings 2, 0, nil", steps)
	}
}

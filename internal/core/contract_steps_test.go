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

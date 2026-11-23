package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルのテスト名・サブテストの名前の AC-<n> は、core の見直しの区切り S2c の課題（masanami/flywheel#802）の
// 受入基準の番号。課題のツリー（ChallengeTree・ChallengeTreeNode）を、実ストアで確かめる。
// HTTP を要る基準（AC-1・54・55・56〜62・63〜67・70・71・73）は internal/server、型の基準（AC-74・75）は internal/view が確かめる。

var treeNow = time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)

// treeTK はテストの計画のタスク 1 件。status が空は承認の前（タスクの行を作らない）。
type treeTK struct {
	key    string
	deps   []string
	status string
}

func treeSpec(uses *bool, size string, tasks []treeTK) string {
	var ts []string
	for _, t := range tasks {
		deps := "[]"
		if len(t.deps) > 0 {
			deps = `["` + strings.Join(t.deps, `","`) + `"]`
		}
		ts = append(ts, fmt.Sprintf(`{"key":%q,"title":"題 %s","body":"b","done_criteria":"d","non_goals":[],"depends_on":%s,"repo":"r","operation":"brief"}`, t.key, t.key, deps))
	}
	u := ""
	if uses != nil {
		u = fmt.Sprintf(`"uses_integration":%t,`, *uses)
	}
	return `{"verdict":"plan","size":"` + size + `",` + u + `"summary":"要約","topics":[],"tasks":[` + strings.Join(ts, ",") + `]}`
}

func treeBool(b bool) *bool { return &b }

type treeFx struct {
	t  *testing.T
	s  *Store
	ws string
}

func newTreeFx(t *testing.T) *treeFx {
	s := newStoreForTest(t)
	return &treeFx{t: t, s: s, ws: s.workspace}
}

// challenge は課題を作って内部 ID を返す。
func (f *treeFx) challenge(title, status string) int {
	f.t.Helper()
	coretest.InsertChallenge(f.t, f.ws, title)
	id := coretest.CountChallenges(f.t, f.ws)
	coretest.SetChallengeStatus(f.t, f.ws, id, status)
	return id
}

func (f *treeFx) touch(id int, at string) {
	f.t.Helper()
	coretest.Exec(f.t, f.ws, `UPDATE challenge SET updated_at = ? WHERE id = ?`, at, id)
}

// plan は課題 cid に計画の版 version を足し、status が空でないタスクの行（と前提）を作る。返すのは key → タスクの内部 ID。
func (f *treeFx) plan(cid, version int, spec string, tasks []treeTK) map[string]int64 {
	f.t.Helper()
	coretest.Exec(f.t, f.ws, `INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, ?, 'b', '2026-11-01T00:00:00.000Z', ?)`, cid, version, spec)
	ids := map[string]int64{}
	for _, tk := range tasks {
		if tk.status == "" {
			continue
		}
		coretest.Exec(f.t, f.ws, `INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, non_goals, repo, operation, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'b', 'd', '[]', 'r', 'brief', ?, '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')`, cid, version, tk.key, "題 "+tk.key, tk.status)
		ids[tk.key] = coretest.QueryRows(f.t, f.ws, `SELECT MAX(id) FROM task`)[0][0].(int64)
	}
	for _, tk := range tasks {
		for _, d := range tk.deps {
			if tk.status != "" {
				coretest.Exec(f.t, f.ws, `INSERT INTO task_dependency (task_id, depends_on_id) VALUES (?, ?)`, ids[tk.key], ids[d])
			}
		}
	}
	return ids
}

func (f *treeFx) decl() *AgentDeclaration {
	f.t.Helper()
	d, err := LoadAgentDeclaration(f.ws)
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func (f *treeFx) node(cid int) *TreeChallenge {
	f.t.Helper()
	n, err := f.s.ChallengeTreeNode(context.Background(), formatChallengeID(int64(cid)), f.decl(), treeNow)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *treeFx) tree() *ChallengeTree {
	f.t.Helper()
	tr, err := f.s.ChallengeTree(context.Background(), f.decl(), treeNow)
	if err != nil {
		f.t.Fatal(err)
	}
	return tr
}

func treeIDs(cs []TreeChallenge) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func treePhases(n *TreeChallenge) map[string]TaskPhase {
	out := map[string]TaskPhase{}
	for _, t := range n.Tasks {
		out[t.Key] = t.Phase
	}
	return out
}

func TestChallengeTree_SelectionAndOrder_AC2to10_802(t *testing.T) {
	f := newTreeFx(t)
	inProg := f.challenge("着手中", "in_progress")
	done := f.challenge("済み", "done")
	withdrawn := f.challenge("取り下げ", "withdrawn")
	_, _, _ = inProg, done, withdrawn
	tr := f.tree()

	t.Run("AC-2 in_progress is in challenges", func(t *testing.T) {
		if !reflect.DeepEqual(treeIDs(tr.Challenges), []string{"C-1"}) {
			t.Errorf("challenges = %v", treeIDs(tr.Challenges))
		}
	})
	t.Run("AC-3 done is not in challenges", func(t *testing.T) {
		for _, id := range treeIDs(tr.Challenges) {
			if id == "C-2" {
				t.Errorf("done challenge in challenges")
			}
		}
	})
	t.Run("AC-4 withdrawn is not in challenges", func(t *testing.T) {
		for _, id := range treeIDs(tr.Challenges) {
			if id == "C-3" {
				t.Errorf("withdrawn challenge in challenges")
			}
		}
	})
	t.Run("AC-5 done is in recent_done", func(t *testing.T) {
		if !reflect.DeepEqual(treeIDs(tr.RecentDone), []string{"C-2"}) {
			t.Errorf("recent_done = %v", treeIDs(tr.RecentDone))
		}
	})
	t.Run("AC-6 withdrawn is not in recent_done", func(t *testing.T) {
		for _, id := range treeIDs(tr.RecentDone) {
			if id == "C-3" {
				t.Errorf("withdrawn challenge in recent_done")
			}
		}
	})
}

func TestChallengeTree_RecentDoneLimitAndOrder_AC7to9_802(t *testing.T) {
	f := newTreeFx(t)
	for i := 1; i <= 11; i++ {
		id := f.challenge(fmt.Sprintf("d%d", i), "done")
		f.touch(id, fmt.Sprintf("2026-11-%02dT00:00:00.000Z", i)) // C-1 が最も古く、C-11 が最も新しい
	}
	t.Run("AC-7 eleven done challenges give ten", func(t *testing.T) {
		if got := len(f.tree().RecentDone); got != 10 {
			t.Errorf("recent_done = %d, want 10", got)
		}
	})
	t.Run("AC-8 newest updated_at first", func(t *testing.T) {
		got := treeIDs(f.tree().RecentDone)
		want := []string{"C-11", "C-10", "C-9", "C-8", "C-7", "C-6", "C-5", "C-4", "C-3", "C-2"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("recent_done = %v, want %v (C-1, the oldest, is dropped)", got, want)
		}
	})
	t.Run("AC-9 same updated_at puts the larger ID first", func(t *testing.T) {
		g := newTreeFx(t)
		a := g.challenge("a", "done")
		b := g.challenge("b", "done")
		c := g.challenge("c", "done")
		for _, id := range []int{a, b, c} {
			g.touch(id, "2026-11-05T00:00:00.000Z")
		}
		if got := treeIDs(g.tree().RecentDone); !reflect.DeepEqual(got, []string{"C-3", "C-2", "C-1"}) {
			t.Errorf("recent_done = %v", got)
		}
	})
}

func TestChallengeTree_ChallengesAreInIDOrder_AC10_802(t *testing.T) {
	f := newTreeFx(t)
	for i := 0; i < 11; i++ { // C-9 < C-10 の数の順を確かめるため 10 件を超える
		f.challenge(fmt.Sprintf("c%d", i), "in_progress")
	}
	got := treeIDs(f.tree().Challenges)
	want := []string{}
	for i := 1; i <= 11; i++ {
		want = append(want, fmt.Sprintf("C-%d", i))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("challenges = %v, want ID ascending %v", got, want)
	}
}

var treeThree = []treeTK{{"a", nil, "pending"}, {"b", []string{"a"}, "pending"}, {"c", []string{"b"}, "pending"}}

func TestChallengeTree_TasksAfterApproval_AC11_AC16_802(t *testing.T) {
	f := newTreeFx(t)
	cid := f.challenge("承認後", "in_progress")
	ids := f.plan(cid, 1, treeSpec(nil, "S", treeThree), treeThree)
	n := f.node(cid)

	t.Run("AC-11 three tasks, each id is the task ID", func(t *testing.T) {
		if len(n.Tasks) != 3 {
			t.Fatalf("tasks = %d, want 3", len(n.Tasks))
		}
		for i, k := range []string{"a", "b", "c"} {
			if n.Tasks[i].ID == nil || *n.Tasks[i].ID != fmt.Sprintf("T-%d", ids[k]) {
				t.Errorf("tasks[%d].id = %v, want T-%d", i, n.Tasks[i].ID, ids[k])
			}
		}
	})
	t.Run("AC-16 depends_on after approval is the key, not the task ID", func(t *testing.T) {
		if !reflect.DeepEqual(n.Tasks[1].DependsOn, []string{"a"}) || !reflect.DeepEqual(n.Tasks[2].DependsOn, []string{"b"}) || len(n.Tasks[0].DependsOn) != 0 {
			t.Errorf("depends_on = %v / %v / %v", n.Tasks[0].DependsOn, n.Tasks[1].DependsOn, n.Tasks[2].DependsOn)
		}
	})
}

func TestChallengeTree_TasksBeforeApproval_AC12to15_802(t *testing.T) {
	f := newTreeFx(t)
	cid := f.challenge("承認待ち", "awaiting_plan_approval")
	two := []treeTK{{key: "a"}, {key: "b", deps: []string{"a"}}}
	f.plan(cid, 1, treeSpec(nil, "S", two), two)
	n := f.node(cid)
	t.Run("AC-12 two tasks", func(t *testing.T) {
		if len(n.Tasks) != 2 {
			t.Errorf("tasks = %d, want 2", len(n.Tasks))
		}
	})
	t.Run("AC-13 id is nil", func(t *testing.T) {
		for _, tk := range n.Tasks {
			if tk.ID != nil {
				t.Errorf("id = %v, want nil", *tk.ID)
			}
		}
	})
	t.Run("AC-14 status is nil", func(t *testing.T) {
		for _, tk := range n.Tasks {
			if tk.Status != nil {
				t.Errorf("status = %v, want nil", *tk.Status)
			}
		}
	})
	t.Run("AC-15 depends_on is the key array", func(t *testing.T) {
		if !reflect.DeepEqual(n.Tasks[1].DependsOn, []string{"a"}) {
			t.Errorf("depends_on = %v, want [a]", n.Tasks[1].DependsOn)
		}
	})
}

func TestChallengeTree_NoTasksCases_AC17_AC18_802(t *testing.T) {
	f := newTreeFx(t)
	none := f.challenge("計画なし", "classified")
	old := f.challenge("過去の形", "in_progress")
	coretest.Exec(t, f.ws, `INSERT INTO task_plan (challenge_id, version, body, created_at) VALUES (?, 1, '本文だけ', '2026-11-01T00:00:00.000Z')`, old)
	t.Run("AC-17 no plan gives an empty (non-nil) tasks", func(t *testing.T) {
		if n := f.node(none); n.Tasks == nil || len(n.Tasks) != 0 {
			t.Errorf("tasks = %#v, want empty non-nil", n.Tasks)
		}
	})
	t.Run("AC-18 an old-shape latest plan gives an empty tasks", func(t *testing.T) {
		if n := f.node(old); n.Tasks == nil || len(n.Tasks) != 0 {
			t.Errorf("tasks = %#v, want empty non-nil", n.Tasks)
		}
	})
}

func TestChallengeTree_ReplanDropsOldTasks_AC19_802(t *testing.T) {
	f := newTreeFx(t)
	cid := f.challenge("作り直し", "in_progress")
	v1 := []treeTK{{"old1", nil, "cancelled"}, {"old2", nil, "cancelled"}}
	f.plan(cid, 1, treeSpec(nil, "S", v1), v1)
	v2 := []treeTK{{"new1", nil, "pending"}}
	f.plan(cid, 2, treeSpec(nil, "S", v2), v2)
	n := f.node(cid)
	if len(n.Tasks) != 1 || n.Tasks[0].Key != "new1" {
		t.Errorf("tasks = %+v, want only the version-2 task", n.Tasks)
	}
}

func TestChallengeTree_Phases_AC20to32_802(t *testing.T) {
	cases := []struct {
		name  string
		tasks []treeTK
		want  map[string]TaskPhase
	}{
		{"AC-20 cancelled task is cancelled", []treeTK{{"x", nil, "cancelled"}}, map[string]TaskPhase{"x": TaskPhaseCancelled}},
		{"AC-21 pending with a cancelled prerequisite is blocked", []treeTK{{"a", nil, "cancelled"}, {"b", []string{"a"}, "pending"}}, map[string]TaskPhase{"a": TaskPhaseCancelled, "b": TaskPhaseBlocked}},
		{"AC-22 done task is done", []treeTK{{"x", nil, "done"}}, map[string]TaskPhase{"x": TaskPhaseDone}},
		{"AC-23 in_progress is running", []treeTK{{"x", nil, "in_progress"}}, map[string]TaskPhase{"x": TaskPhaseRunning}},
		{"AC-24 verifying is running", []treeTK{{"x", nil, "verifying"}}, map[string]TaskPhase{"x": TaskPhaseRunning}},
		{"AC-28 pending without prerequisites is next", []treeTK{{"x", nil, "pending"}}, map[string]TaskPhase{"x": TaskPhaseNext}},
		{"AC-29 pending with all prerequisites done is next", []treeTK{{"a", nil, "done"}, {"b", nil, "done"}, {"c", []string{"a", "b"}, "pending"}}, map[string]TaskPhase{"c": TaskPhaseNext}},
		{"AC-30 pending with an unfinished prerequisite is blocked", []treeTK{{"a", nil, "done"}, {"b", nil, "in_progress"}, {"c", []string{"a", "b"}, "pending"}}, map[string]TaskPhase{"c": TaskPhaseBlocked}},
		{"AC-31 plan awaiting approval: task without prerequisites is next", []treeTK{{key: "x"}}, map[string]TaskPhase{"x": TaskPhaseNext}},
		{"AC-32 plan awaiting approval: task with prerequisites is blocked", []treeTK{{key: "a"}, {key: "b", deps: []string{"a"}}}, map[string]TaskPhase{"a": TaskPhaseNext, "b": TaskPhaseBlocked}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTreeFx(t)
			status := "in_progress"
			if c.tasks[0].status == "" {
				status = "awaiting_plan_approval"
			}
			cid := f.challenge("c", status)
			f.plan(cid, 1, treeSpec(nil, "S", c.tasks), c.tasks)
			got := treePhases(f.node(cid))
			for k, want := range c.want {
				if got[k] != want {
					t.Errorf("phase[%s] = %q, want %q", k, got[k], want)
				}
			}
		})
	}

	// AC-25〜27: 保留とタスクの区分。
	hold := func(t *testing.T, kind string, answered bool, closed bool) TaskPhase {
		f := newTreeFx(t)
		cid := f.challenge("c", "in_progress")
		one := []treeTK{{"x", nil, "in_progress"}}
		ids := f.plan(cid, 1, treeSpec(nil, "S", one), one)
		ans, cl := "NULL", "NULL"
		if answered {
			ans = "'答え'"
		}
		if closed {
			cl = "'2026-11-02T00:00:00.000Z'"
		}
		coretest.Exec(t, f.ws, `INSERT INTO hold (challenge_id, question, from_status, raised_at, kind, task_id, answer, closed_at)
			VALUES (?, 'q', 'in_progress', '2026-11-02T00:00:00.000Z', ?, ?, `+ans+`, `+cl+`)`, cid, kind, ids["x"])
		return f.node(cid).Tasks[0].Phase
	}
	t.Run("AC-25 an unanswered owner hold makes the task waiting", func(t *testing.T) {
		if got := hold(t, "human", false, false); got != TaskPhaseWaiting {
			t.Errorf("phase = %q, want waiting", got)
		}
	})
	t.Run("AC-26 an answered hold does not", func(t *testing.T) {
		if got := hold(t, "human", true, false); got == TaskPhaseWaiting {
			t.Errorf("phase = waiting for an answered hold")
		}
	})
	t.Run("AC-27 a parent_direct hold does not", func(t *testing.T) {
		if got := hold(t, "parent_direct", false, false); got == TaskPhaseWaiting {
			t.Errorf("phase = waiting for a parent_direct hold")
		}
	})
}

func TestChallengeTree_TaskCounts_AC33_AC34_802(t *testing.T) {
	f := newTreeFx(t)
	cid := f.challenge("c", "in_progress")
	ts := []treeTK{{"a", nil, "done"}, {"b", nil, "done"}, {"c", nil, "cancelled"}, {"d", nil, "pending"}}
	f.plan(cid, 1, treeSpec(nil, "S", ts), ts)
	n := f.node(cid)
	t.Run("AC-33 tasks_total equals len(tasks)", func(t *testing.T) {
		if n.TasksTotal != len(n.Tasks) || n.TasksTotal != 4 {
			t.Errorf("tasks_total = %d, len = %d", n.TasksTotal, len(n.Tasks))
		}
	})
	t.Run("AC-34 tasks_done equals the done-phase tasks", func(t *testing.T) {
		if n.TasksDone != 2 {
			t.Errorf("tasks_done = %d, want 2", n.TasksDone)
		}
	})
}

func TestChallengeTree_Cost_AC36to39_802(t *testing.T) {
	f := newTreeFx(t)
	cid := f.challenge("c", "in_progress")
	v1 := []treeTK{{"old", nil, "cancelled"}}
	idsOld := f.plan(cid, 1, treeSpec(nil, "S", v1), v1)
	v2 := []treeTK{{"a", nil, "in_progress"}, {"b", nil, "pending"}}
	ids := f.plan(cid, 2, treeSpec(nil, "S", v2), v2)
	run := func(cost any, task any) {
		coretest.Exec(t, f.ws, `INSERT INTO run (kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd, task_id)
			VALUES ('delegate', ?, 1, 's', 1, 'h', '2026-11-02T00:00:00.000Z', '2026-11-02T00:00:00.000Z', '2026-11-02T00:10:00.000Z', 'succeeded', 1000000, 'impl', ?, ?)`, cid, cost, task)
	}
	run(int64(1_500_000), ids["a"])      // タスク a の委譲の実行
	run(int64(2_000_000), ids["a"])      // タスク a の実行がもう 1 件
	run(int64(250_000), nil)             // タスクに付かない判断点の実行
	run(int64(4_000_000), idsOld["old"]) // 前の版の計画のタスクの実行
	run(nil, ids["b"])                   // 費用の記録が無い実行
	n := f.node(cid)
	t.Run("AC-36 challenge cost is the sum of all runs (judgments and delegations)", func(t *testing.T) {
		if n.CostUSD != 7.75 {
			t.Errorf("cost_usd = %v, want 7.75", n.CostUSD)
		}
	})
	t.Run("AC-37 only runs without a recorded cost give 0", func(t *testing.T) {
		g := newTreeFx(t)
		c := g.challenge("c", "in_progress")
		coretest.Exec(t, g.ws, `INSERT INTO run (kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket)
			VALUES ('judgment', ?, 1, 's', 1, 'h', '2026-11-02T00:00:00.000Z', '2026-11-02T00:00:00.000Z', '2026-11-02T00:10:00.000Z', 'succeeded', 1000000, 'impl')`, c)
		if got := g.node(c).CostUSD; got != 0 {
			t.Errorf("cost_usd = %v, want 0", got)
		}
	})
	t.Run("AC-38 runs of old-version tasks are included in the challenge cost", func(t *testing.T) {
		g := newTreeFx(t)
		c := g.challenge("c", "in_progress")
		o := []treeTK{{"old", nil, "cancelled"}}
		oi := g.plan(c, 1, treeSpec(nil, "S", o), o)
		nw := []treeTK{{"new", nil, "pending"}}
		g.plan(c, 2, treeSpec(nil, "S", nw), nw)
		coretest.Exec(t, g.ws, `INSERT INTO run (kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd, task_id)
			VALUES ('delegate', ?, 1, 's', 1, 'h', '2026-11-02T00:00:00.000Z', '2026-11-02T00:00:00.000Z', '2026-11-02T00:10:00.000Z', 'succeeded', 1000000, 'impl', 3000000, ?)`, c, oi["old"])
		node := g.node(c)
		if node.CostUSD != 3 || len(node.Tasks) != 1 || node.Tasks[0].CostUSD != 0 {
			t.Errorf("cost_usd = %v, tasks = %+v; want 3 with the new task at 0", node.CostUSD, node.Tasks)
		}
	})
	t.Run("AC-39 task cost is the sum of that task's runs", func(t *testing.T) {
		if n.Tasks[0].CostUSD != 3.5 || n.Tasks[1].CostUSD != 0 {
			t.Errorf("task costs = %v, %v; want 3.5, 0", n.Tasks[0].CostUSD, n.Tasks[1].CostUSD)
		}
	})
}

func TestChallengeTree_Budget_AC40to42_802(t *testing.T) {
	f := newTreeFx(t)
	explicit := f.challenge("書いた額", "in_progress")
	spec := `{"verdict":"plan","size":"S","budget_impl_usd":10,"budget_review_usd":5,"topics":[],"tasks":[]}`
	f.plan(explicit, 1, spec, nil)
	bySize := f.challenge("サイズの既定", "in_progress")
	f.plan(bySize, 1, treeSpec(nil, "S", nil), nil)
	noPlan := f.challenge("計画なし", "classified")
	ctx := context.Background()
	t.Run("AC-40 budget_usd equals GetPlanView's BudgetUSD", func(t *testing.T) {
		pv, err := f.s.GetPlanView(ctx, "C-1", f.decl())
		if err != nil || pv.BudgetUSD == nil {
			t.Fatalf("plan view = %+v, %v", pv, err)
		}
		got := f.node(explicit).BudgetUSD
		if got == nil || *got != *pv.BudgetUSD || *got != 15 {
			t.Errorf("budget_usd = %v, want %v (15)", got, *pv.BudgetUSD)
		}
	})
	t.Run("AC-41 no plan gives nil", func(t *testing.T) {
		if got := f.node(noPlan).BudgetUSD; got != nil {
			t.Errorf("budget_usd = %v, want nil", *got)
		}
	})
	t.Run("AC-42 a plan without amounts uses the declaration's size default", func(t *testing.T) {
		pv, err := f.s.GetPlanView(ctx, "C-2", f.decl())
		if err != nil || pv.BudgetUSD == nil {
			t.Fatalf("plan view = %+v, %v", pv, err)
		}
		got := f.node(bySize).BudgetUSD
		if got == nil || *got != *pv.BudgetUSD || *got != 55 {
			t.Errorf("budget_usd = %v, want %v (55)", got, *pv.BudgetUSD)
		}
	})
}

func TestChallengeTree_Followups_AC43_AC44_802(t *testing.T) {
	f := newTreeFx(t)
	cid := f.challenge("c", "in_progress")
	other := f.challenge("up", "in_progress")
	coretest.InsertFollowup(t, f.ws, coretest.FollowupRow{ChallengeID: cid, Title: "u1"})
	coretest.InsertFollowup(t, f.ws, coretest.FollowupRow{ChallengeID: cid, Title: "u2"})
	coretest.InsertFollowup(t, f.ws, coretest.FollowupRow{ChallengeID: cid, Title: "済み", Status: "done", Note: "確認した"})
	coretest.InsertFollowup(t, f.ws, coretest.FollowupRow{ChallengeID: cid, Title: "上げた", PromotedChallengeID: other})
	t.Run("AC-43 followups_open counts the open followups", func(t *testing.T) {
		// 未は u1・u2 と、上げてあるもの 1 件。上げた先が取り下げでないものは AC-44 で除く。
		if got := f.node(cid).FollowupsOpen; got != 2 {
			t.Errorf("followups_open = %d, want 2", got)
		}
	})
	t.Run("AC-43 same value as status followups.open for the whole workspace", func(t *testing.T) {
		total, err := f.s.CountOpenFollowupsToTriage(context.Background())
		if err != nil || total != f.node(cid).FollowupsOpen {
			t.Errorf("status count = %d (%v), node = %d", total, err, f.node(cid).FollowupsOpen)
		}
	})
	t.Run("AC-44 a followup promoted to a non-withdrawn challenge is not counted", func(t *testing.T) {
		if got := f.node(cid).FollowupsOpen; got != 2 {
			t.Errorf("followups_open = %d, want 2 (promoted one excluded)", got)
		}
		coretest.SetChallengeStatus(t, f.ws, other, "withdrawn") // 上げた先が取り下げなら数える
		if got := f.node(cid).FollowupsOpen; got != 3 {
			t.Errorf("followups_open = %d, want 3 once the promoted challenge is withdrawn", got)
		}
	})
}

func TestChallengeTree_Relations_AC45to49_802(t *testing.T) {
	f := newTreeFx(t)
	pre := f.challenge("前提の課題", "in_progress")
	main := f.challenge("本体", "in_progress")
	child := f.challenge("子", "in_progress")
	coretest.InsertDependency(t, f.ws, main, pre)
	coretest.Exec(t, f.ws, `INSERT INTO challenge_parent (challenge_id, parent_id, created_at) VALUES (?, ?, '2026-11-01T00:00:00.000Z')`, child, main)
	n, c := f.node(main), f.node(child)
	t.Run("AC-45 dependencies has the prerequisite's challenge_id and title", func(t *testing.T) {
		if len(n.Dependencies) != 1 || n.Dependencies[0].ChallengeID != "C-1" || n.Dependencies[0].Title != "前提の課題" {
			t.Errorf("dependencies = %+v", n.Dependencies)
		}
	})
	t.Run("AC-46 an unfinished prerequisite is not satisfied", func(t *testing.T) {
		if n.Dependencies[0].Satisfied {
			t.Errorf("satisfied = true")
		}
	})
	t.Run("AC-47 parent has the parent's challenge_id", func(t *testing.T) {
		if c.Parent == nil || c.Parent.ChallengeID != "C-2" {
			t.Errorf("parent = %+v", c.Parent)
		}
	})
	t.Run("AC-48 no parent gives nil", func(t *testing.T) {
		if n.Parent != nil {
			t.Errorf("parent = %+v, want nil", n.Parent)
		}
	})
	t.Run("AC-49 children has the child's challenge_id", func(t *testing.T) {
		if len(n.Children) != 1 || n.Children[0].ChallengeID != "C-3" {
			t.Errorf("children = %+v", n.Children)
		}
	})
}

func TestChallengeTree_NextHumanActions_AC50_802(t *testing.T) {
	f := newTreeFx(t)
	cid := f.challenge("承認待ち", "awaiting_plan_approval")
	n := f.node(cid)
	for _, a := range n.NextHumanActions {
		if a == CardActionApprovePlan {
			return
		}
	}
	t.Errorf("next_human_actions = %v, want approve_plan included", n.NextHumanActions)
}

// AC-50（#798 の全体のレビューで足した）: 課題のツリーの next_human_actions は、課題の一覧のカードの導出（DeriveCard）と
// 課題ごとに同じ値・同じ並びである。人間の操作が出る課題と出ない課題を混ぜて確かめる。
func TestChallengeTree_NextHumanActionsEqualCardDerivation_AC50_798(t *testing.T) {
	f := newTreeFx(t)
	statuses := []string{"awaiting_plan_approval", "awaiting_human", "awaiting_completion_approval", "in_progress", "unclassified", "classified"}
	for i, st := range statuses {
		f.challenge(fmt.Sprintf("課題 %d %s", i+1, st), st)
	}
	ctx := context.Background()
	ov, err := f.s.GetOverviewFor(ctx, f.decl())
	if err != nil {
		t.Fatal(err)
	}
	all, err := f.s.ListChallenges(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	nodes := f.tree().Challenges
	if len(nodes) != len(statuses) || len(all) != len(statuses) {
		t.Fatalf("nodes = %v, all = %d, want %d", treeIDs(nodes), len(all), len(statuses))
	}
	sawEmpty, sawNonEmpty := false, false
	for i, c := range all {
		card, err := f.s.DeriveCard(ctx, c, ov, treeNow)
		if err != nil {
			t.Fatal(err)
		}
		if nodes[i].ID != c.ID {
			t.Fatalf("nodes[%d].ID = %s, want %s", i, nodes[i].ID, c.ID)
		}
		if !reflect.DeepEqual(nodes[i].NextHumanActions, card.NextHumanActions) {
			t.Errorf("%s (%s): tree next_human_actions = %v, card = %v", c.ID, c.Status, nodes[i].NextHumanActions, card.NextHumanActions)
		}
		if len(card.NextHumanActions) == 0 {
			sawEmpty = true
		} else {
			sawNonEmpty = true
		}
	}
	if !sawEmpty || !sawNonEmpty {
		t.Errorf("fixture must mix challenges with and without actions (empty=%v, non-empty=%v)", sawEmpty, sawNonEmpty)
	}
}

// 課題の一覧と、親子・前提は別の読み取りで読む。その間に増えた課題を親子・前提だけが指しているとき（一覧に無い課題）は、
// ID と題名が空の要素を返さず、その要素を飛ばす（#798 の全体のレビュー）。
func TestBuildTreeNodes_SkipsRelationsMissingFromTheList_798(t *testing.T) {
	f := newTreeFx(t)
	pre := f.challenge("前提の課題", "in_progress")
	main := f.challenge("本体", "in_progress")
	child := f.challenge("子", "in_progress")
	coretest.InsertDependency(t, f.ws, main, pre)
	coretest.Exec(t, f.ws, `INSERT INTO challenge_parent (challenge_id, parent_id, created_at) VALUES (?, ?, '2026-11-01T00:00:00.000Z')`, child, main)
	ctx := context.Background()
	all, err := f.s.ListChallenges(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pick := func(id int) Challenge {
		for _, c := range all {
			if c.ID == formatChallengeID(int64(id)) {
				return c
			}
		}
		t.Fatalf("challenge %d not in the list", id)
		return Challenge{}
	}
	without := func(ids ...int) []Challenge {
		var out []Challenge
	next:
		for _, c := range all {
			for _, id := range ids {
				if c.ID == formatChallengeID(int64(id)) {
					continue next
				}
			}
			out = append(out, c)
		}
		return out
	}
	t.Run("a prerequisite and a child that are not in the list are skipped", func(t *testing.T) {
		nodes, err := buildNodesForTest(ctx, f.s, without(pre, child), []Challenge{pick(main)}, f.decl())
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes[0].Dependencies) != 0 || len(nodes[0].Children) != 0 {
			t.Errorf("dependencies = %+v, children = %+v, want both empty", nodes[0].Dependencies, nodes[0].Children)
		}
	})
	t.Run("a parent that is not in the list stays nil", func(t *testing.T) {
		nodes, err := buildNodesForTest(ctx, f.s, without(main), []Challenge{pick(child)}, f.decl())
		if err != nil {
			t.Fatal(err)
		}
		if nodes[0].Parent != nil {
			t.Errorf("parent = %+v, want nil", nodes[0].Parent)
		}
	})
	t.Run("relations that are in the list are still returned", func(t *testing.T) {
		nodes, err := buildNodesForTest(ctx, f.s, all, []Challenge{pick(main)}, f.decl())
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes[0].Dependencies) != 1 || nodes[0].Dependencies[0].ChallengeID == "" || len(nodes[0].Children) != 1 || nodes[0].Children[0].ChallengeID == "" {
			t.Errorf("dependencies = %+v, children = %+v", nodes[0].Dependencies, nodes[0].Children)
		}
	})
}

func TestChallengeTree_UsesIntegration_AC51to53_802(t *testing.T) {
	f := newTreeFx(t)
	t.Run("AC-51 an approved plan that does not use integration is false", func(t *testing.T) {
		cid := f.challenge("承認済み", "in_progress")
		ts := []treeTK{{"a", nil, "pending"}}
		f.plan(cid, 1, treeSpec(treeBool(true), "S", ts), ts)                                 // 計画の値（真）と課題に記録した値（偽）を違えて、承認の後は記録した値を返すことを確かめる
		coretest.Exec(t, f.ws, `UPDATE challenge SET uses_integration = 0 WHERE id = ?`, cid) // 承認が記録する値
		if got := f.node(cid).UsesIntegration; got == nil || *got {
			t.Errorf("uses_integration = %v, want false", got)
		}
	})
	t.Run("AC-52 no plan gives nil", func(t *testing.T) {
		cid := f.challenge("計画なし", "classified")
		if got := f.node(cid).UsesIntegration; got != nil {
			t.Errorf("uses_integration = %v, want nil", *got)
		}
	})
	t.Run("AC-53 a plan awaiting approval that uses integration is true", func(t *testing.T) {
		cid := f.challenge("承認待ち", "awaiting_plan_approval")
		ts := []treeTK{{key: "a"}}
		f.plan(cid, 1, treeSpec(treeBool(true), "S", ts), ts)
		if got := f.node(cid).UsesIntegration; got == nil || !*got {
			t.Errorf("uses_integration = %v, want true", got)
		}
	})
}

func TestChallengeTreeNode_UnknownIDIsNotFound_802(t *testing.T) {
	f := newTreeFx(t)
	for _, id := range []string{"C-9", "bad"} {
		if _, err := f.s.ChallengeTreeNode(context.Background(), id, nil, treeNow); !errors.Is(err, ErrNotFound) {
			t.Errorf("ChallengeTreeNode(%q) error = %v, want ErrNotFound", id, err)
		}
	}
}

func TestPlanTaskPhases_AC68_AC69_802(t *testing.T) {
	ts := []PlanTaskView{{Key: "a"}, {Key: "b", DependsOn: []string{"a"}}}
	got := planTaskPhases(ts)
	if got[0] != TaskPhaseNext || got[1] != TaskPhaseBlocked {
		t.Errorf("phases = %v, want [next blocked] (AC-68・AC-69)", got)
	}
}

func TestUnansweredHolds_TaskID_AC70_AC71_802(t *testing.T) {
	f := newTreeFx(t)
	cid := f.challenge("c", "in_progress")
	one := []treeTK{{"x", nil, "in_progress"}}
	ids := f.plan(cid, 1, treeSpec(nil, "S", one), one)
	coretest.Exec(t, f.ws, `INSERT INTO hold (challenge_id, question, from_status, raised_at, kind, task_id, scope) VALUES (?, 'タスクの問い', 'in_progress', '2026-11-02T00:00:00.000Z', 'human', ?, 'task')`, cid, ids["x"])
	coretest.Exec(t, f.ws, `INSERT INTO hold (challenge_id, question, from_status, raised_at, kind) VALUES (?, '課題の問い', 'in_progress', '2026-11-03T00:00:00.000Z', 'human')`, cid)
	hs, err := f.s.ListUnansweredHolds(context.Background())
	if err != nil || len(hs) != 2 {
		t.Fatalf("holds = %+v, %v", hs, err)
	}
	t.Run("AC-70 a task hold has the task's ID", func(t *testing.T) {
		if hs[0].TaskID == nil || *hs[0].TaskID != fmt.Sprintf("T-%d", ids["x"]) {
			t.Errorf("task_id = %v", hs[0].TaskID)
		}
	})
	t.Run("AC-71 a hold without a task has nil", func(t *testing.T) {
		if hs[1].TaskID != nil {
			t.Errorf("task_id = %v, want nil", *hs[1].TaskID)
		}
	})
}

func TestTaskPhaseValues_AC74_802(t *testing.T) {
	var got []string
	for _, p := range TaskPhaseValues() {
		got = append(got, string(p))
	}
	want := []string{"running", "waiting", "next", "blocked", "done", "cancelled"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TaskPhaseValues = %v, want %v", got, want)
	}
}

// 完了の承認の要約（PrepareApprovalFor）は、完了承認画面に並べるタスクを同じ読み取りで返す。
// 最新の版（木）のタスクは木の区分で、木に無い前の版は済みのものだけ並べる。端末の要約（PrepareApproval）は読まない。
func TestPrepareApprovalFor_CompletionTasksListsTreeAndPriorDone(t *testing.T) {
	f := newTreeFx(t)
	cid := f.challenge("c", "awaiting_completion_approval")
	f.plan(cid, 1, treeSpec(nil, "S", []treeTK{{"old-done", nil, "done"}, {"old-open", nil, "in_progress"}}), []treeTK{{"old-done", nil, "done"}, {"old-open", nil, "in_progress"}})
	nw := []treeTK{{"new", nil, "done"}}
	ids := f.plan(cid, 2, treeSpec(nil, "S", nw), nw)
	coretest.Exec(t, f.ws, `UPDATE challenge SET integration_ref = 'integ/x', uses_integration = 1 WHERE id = ?`, cid)
	id := formatChallengeID(int64(cid))
	p, err := f.s.PrepareApprovalFor(context.Background(), id, f.decl())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.CompletionTasks) != 2 {
		t.Fatalf("tasks = %+v, want old-done and new only", p.CompletionTasks)
	}
	for _, ct := range p.CompletionTasks {
		wantInTree := ct.ID == formatTaskID(ids["new"])
		if ct.Phase != TaskPhaseDone || ct.InTree != wantInTree {
			t.Errorf("task %+v: want phase done, in_tree=%v", ct, wantInTree)
		}
	}
	if p.IntegrationRef == nil || *p.IntegrationRef != "integ/x" || p.UsesIntegration == nil || !*p.UsesIntegration {
		t.Errorf("integration = %v / %v, want integ/x / true", p.IntegrationRef, p.UsesIntegration)
	}
	plain, err := f.s.PrepareApproval(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if plain.CompletionTasks != nil || plain.OpenFollowups != nil {
		t.Errorf("PrepareApproval must not load screen values: %+v", plain)
	}
}

// 課題のツリーの読み取りは、課題の一覧・親子・前提・フォローアップ・タスク・実行の記録を 1 つの読み取りのトランザクションの中で読む
// （masanami/flywheel#837）。一覧を読んだ直後に別の接続がフォローアップと費用を足しても、結果は足す前の時点でそろう。
func TestChallengeTree_ReadsOneSnapshot_837(t *testing.T) {
	setup := func(t *testing.T) (*treeFx, int) {
		f := newTreeFx(t)
		cid := f.challenge("親", "in_progress")
		return f, cid
	}
	inject := func(f *treeFx, cid int) {
		f.s.treeListRead = func() {
			coretest.Exec(f.t, f.ws, `INSERT INTO followup (challenge_id, title, source, status, created_at, updated_at) VALUES (?, 'f', 'learn', 'open', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')`, cid)
			coretest.InsertChallenge(f.t, f.ws, "途中で足した")
		}
	}
	countActivities := func(f *treeFx) int64 {
		return coretest.QueryRows(f.t, f.ws, `SELECT COUNT(*) FROM activity`)[0][0].(int64)
	}

	t.Run("ChallengeTree", func(t *testing.T) {
		f, cid := setup(t)
		inject(f, cid)
		before := countActivities(f)
		tr := f.tree()
		if got := tr.Challenges[0].FollowupsOpen; got != 0 {
			t.Errorf("followups_open = %d, want 0（一覧と同じ時点）", got)
		}
		if n := len(tr.Challenges); n != 1 {
			t.Errorf("challenges = %d, want 1（途中で足した課題は入らない）", n)
		}
		if countActivities(f) != before {
			t.Error("読み取りが作業ログを書いた")
		}
		f.s.treeListRead = nil
		if got := f.tree().Challenges[0].FollowupsOpen; got != 1 {
			t.Errorf("次の読み取りの followups_open = %d, want 1", got)
		}
	})
	t.Run("ChallengeTreeNode", func(t *testing.T) {
		f, cid := setup(t)
		inject(f, cid)
		if got := f.node(cid).FollowupsOpen; got != 0 {
			t.Errorf("followups_open = %d, want 0（一覧と同じ時点）", got)
		}
	})
}

// buildNodesForTest は buildTreeNodes を、読み取りのトランザクションの中で呼ぶ（一覧 all を差し替えて守りを確かめる）。
func buildNodesForTest(ctx context.Context, s *Store, all, picked []Challenge, agent *AgentDeclaration) (nodes []TreeChallenge, err error) {
	err = s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		nodes, err = s.buildTreeNodes(ctx, tx, all, picked, agent, nil, treeNow)
		return err
	})
	return nodes, err
}

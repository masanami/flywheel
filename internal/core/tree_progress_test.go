package core

import (
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルのテスト名の AC-<n> は、core の見直しの区切り S3d の課題（masanami/flywheel#929）の受入基準の番号。
// ツリーの progress（タスクと課題）を、実ストアで確かめる。

func (f *treeFx) setUses(cid int, v bool) {
	f.t.Helper()
	coretest.Exec(f.t, f.ws, `UPDATE challenge SET uses_integration = ? WHERE id = ?`, v, cid)
}

// taskRun はタスクの契約の実行を 1 件足す。verifiedRef が空でなければ verified_ref に入れる。
func (f *treeFx) taskRun(cid int, key, ext, verifiedRef string) {
	f.t.Helper()
	coretest.InsertTaskContractRun(f.t, f.ws, cid, key, "c", ext, "running")
	if verifiedRef != "" {
		coretest.Exec(f.t, f.ws, `UPDATE contract_run SET verified_ref = ? WHERE external_run_id = ?`, verifiedRef, ext)
	}
}

func taskProgressOf(n *TreeChallenge, key string) *TaskProgress {
	for _, t := range n.Tasks {
		if t.Key == key {
			return t.Progress
		}
	}
	return nil
}

func wantTaskProgress(t *testing.T, got *TaskProgress, want string) {
	t.Helper()
	switch {
	case want == "" && got != nil:
		t.Errorf("progress = %q, want nil", *got)
	case want != "" && (got == nil || string(*got) != want):
		t.Errorf("progress = %v, want %q", got, want)
	}
}

func TestChallengeTree_TaskProgress_AC85to91_929(t *testing.T) {
	one := func(st string) []treeTK { return []treeTK{{"x", nil, st}} }
	cases := []struct {
		name string
		uses bool
		st   string
		run  string // 最後の契約の実行の verified_ref（空は足さない）
		hold bool
		want string
		ph   TaskPhase
	}{
		{"AC-85 in_progress is working", true, "in_progress", "", false, "working", TaskPhaseRunning},
		{"AC-86 verifying without verified_ref is verifying", true, "verifying", "", false, "verifying", TaskPhaseRunning},
		{"AC-86 verifying with a run lacking verified_ref", true, "verifying", "-", false, "verifying", TaskPhaseRunning},
		{"AC-87 verifying in a challenge without integration is verifying", false, "verifying", "sha", false, "verifying", TaskPhaseRunning},
		{"#964 verifying with only verified_ref is verified（詳細の verified と同じ）", true, "verifying", "sha", false, "verified", TaskPhaseRunning},
		{"AC-88 #964 verifying with verified_ref and land_ref is landing（詳細の sent と同じ）", true, "verifying", "sha+land", false, "landing", TaskPhaseRunning},
		{"AC-89 waiting task is null", true, "in_progress", "", true, "", TaskPhaseWaiting},
		{"AC-90 done task is null", true, "done", "sha", false, "", TaskPhaseDone},
		{"cancelled task is null", true, "cancelled", "", false, "", TaskPhaseCancelled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTreeFx(t)
			cid := f.challenge("c", "in_progress")
			ids := f.plan(cid, 1, treeSpec(treeBool(c.uses), "S", one(c.st)), one(c.st))
			f.setUses(cid, c.uses)
			if c.run != "" {
				v, land := c.run, false
				if v == "-" {
					v = ""
				}
				if v == "sha+land" {
					v, land = "sha", true
				}
				f.taskRun(cid, "x", "e1", v)
				if land {
					coretest.Exec(t, f.ws, `UPDATE contract_run SET land_ref = 'ref' WHERE external_run_id = 'e1'`)
				}
			}
			if c.hold {
				coretest.Exec(t, f.ws, `INSERT INTO hold (challenge_id, question, from_status, raised_at, kind, task_id) VALUES (?, 'q', 'in_progress', '2026-11-02T00:00:00.000Z', 'human', ?)`, cid, ids["x"])
			}
			n := f.node(cid)
			wantTaskProgress(t, taskProgressOf(n, "x"), c.want)
			if got := treePhases(n)["x"]; got != c.ph {
				t.Errorf("phase = %q, want %q（AC-91: phase の値は変えない）", got, c.ph)
			}
		})
	}
}

func TestChallengeTree_TaskProgress_UsesLatestContractRun_AC88_929(t *testing.T) {
	f := newTreeFx(t)
	cid := f.challenge("c", "in_progress")
	tk := []treeTK{{"x", nil, "verifying"}}
	f.plan(cid, 1, treeSpec(treeBool(true), "S", tk), tk)
	f.setUses(cid, true)
	f.taskRun(cid, "x", "e1", "old")
	coretest.Exec(t, f.ws, `UPDATE contract_run SET ended_at = '2026-11-03T00:00:00.000Z' WHERE external_run_id = 'e1'`)
	f.taskRun(cid, "x", "e2", "")
	wantTaskProgress(t, taskProgressOf(f.node(cid), "x"), "verifying")
}

func TestChallengeTree_TaskProgress_NilBeforeApproval_929(t *testing.T) {
	f := newTreeFx(t)
	cid := f.challenge("c", "awaiting_plan_approval")
	tk := []treeTK{{key: "x"}}
	f.plan(cid, 1, treeSpec(treeBool(true), "S", tk), tk)
	wantTaskProgress(t, taskProgressOf(f.node(cid), "x"), "")
	if f.node(cid).Progress != nil {
		t.Errorf("challenge progress = %v, want nil", f.node(cid).Progress)
	}
}

func TestChallengeTree_ChallengeProgress_AC92to95_929(t *testing.T) {
	cases := []struct {
		name    string
		uses    bool
		status  string
		tasks   []treeTK
		reflect string // "open" 終わっていない上げる依頼 / "ended" 終わった上げる依頼 / ""
		want    string
	}{
		{"AC-92 all tasks done and an open reflect run", true, "in_progress", []treeTK{{"a", nil, "done"}, {"b", nil, "done"}}, "open", "reflecting"},
		{"AC-92 cancelled tasks are excluded", true, "in_progress", []treeTK{{"a", nil, "done"}, {"b", nil, "cancelled"}}, "open", "reflecting"},
		{"AC-92 no open reflect run is null", true, "in_progress", []treeTK{{"a", nil, "done"}}, "", ""},
		{"AC-92 an ended reflect run is null", true, "in_progress", []treeTK{{"a", nil, "done"}}, "ended", ""},
		{"AC-93 verifying is checking", true, "verifying", []treeTK{{"a", nil, "done"}}, "", "checking"},
		{"AC-94 an unfinished task is null", true, "in_progress", []treeTK{{"a", nil, "done"}, {"b", nil, "in_progress"}}, "open", ""},
		{"AC-95 without integration and verifying is null", false, "verifying", []treeTK{{"a", nil, "done"}}, "", ""},
		{"without integration and a reflect run is null", false, "in_progress", []treeTK{{"a", nil, "done"}}, "open", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTreeFx(t)
			cid := f.challenge("c", c.status)
			f.plan(cid, 1, treeSpec(treeBool(c.uses), "S", c.tasks), c.tasks)
			f.setUses(cid, c.uses)
			if c.reflect != "" {
				coretest.InsertContractRun(t, f.ws, cid, "c", "er1", "running")
				coretest.Exec(t, f.ws, `UPDATE contract_run SET kind = 'reflect', task_id = NULL WHERE external_run_id = 'er1'`)
				if c.reflect == "ended" {
					coretest.Exec(t, f.ws, `UPDATE contract_run SET ended_at = '2026-11-03T00:00:00.000Z' WHERE external_run_id = 'er1'`)
				}
			}
			got := f.node(cid).Progress
			switch {
			case c.want == "" && got != nil:
				t.Errorf("progress = %q, want nil", *got)
			case c.want != "" && (got == nil || string(*got) != c.want):
				t.Errorf("progress = %v, want %q", got, c.want)
			}
		})
	}
}

// 終わった契約の実行の verified_ref・land_ref は進みに使わない（タスクの詳細が統合先へ入れる進みを出さないのと同じ）。
func TestChallengeTree_TaskProgress_EndedRunIsVerifying_964(t *testing.T) {
	f := newTreeFx(t)
	cid := f.challenge("c", "in_progress")
	tk := []treeTK{{"x", nil, "verifying"}}
	f.plan(cid, 1, treeSpec(treeBool(true), "S", tk), tk)
	f.setUses(cid, true)
	f.taskRun(cid, "x", "e1", "sha")
	coretest.Exec(t, f.ws, `UPDATE contract_run SET land_ref = 'ref', ended_at = '2026-11-03T00:00:00.000Z' WHERE external_run_id = 'e1'`)
	wantTaskProgress(t, taskProgressOf(f.node(cid), "x"), "verifying")
}

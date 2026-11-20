package core

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// このファイルは、起動するタスクの文面の組み立て（LLM を使わない）と、form: cli の起動での {task_file}・{task_id}・
// {uses_integration} の差し込み、取り込み元の無いタスクの起動、form: plugin の委譲が J3 を呼ばないことを検証する。

// taskDocFixture は、論点・問い・タスク 3 個の承認済みの計画を持つ着手中の課題。
type taskDocFixture struct {
	*delegateFixture
	id  string
	cid int64
}

func newTaskDocFixture(t *testing.T) *taskDocFixture {
	t.Helper()
	f := newDelegateFixture(t)
	spec := planSpec(func(m map[string]any) {
		m["summary"] = "方針 POLICY-MARKER"
		m["topics"] = []any{map[string]any{
			"title": "論点 TOPIC-A", "decision": "決定 DECISION-A",
			"questions": []any{
				map[string]any{"question": "問い QUESTION-ONE", "options": []any{map[string]any{"label": "選択肢 OPT-ONE-A"}, map[string]any{"label": "選択肢 OPT-ONE-B"}}, "recommended": 1},
				map[string]any{"question": "問い QUESTION-TWO", "options": []any{map[string]any{"label": "選択肢 OPT-TWO-A"}, map[string]any{"label": "選択肢 OPT-TWO-B"}}, "recommended": 1},
			},
		}}
		m["uses_integration"] = true
		m["tasks"] = []any{
			map[string]any{"key": "k1", "title": "題名 THIS-TITLE", "body": "本文 THIS-BODY", "done_criteria": "条件 THIS-DONE", "non_goals": []string{"範囲外 THIS-NONGOAL"}, "depends_on": []string{}, "repo": "main-repo", "operation": "impl"},
			map[string]any{"key": "k2", "title": "題名 OTHER-TWO", "body": "本文 OTHER-TWO-BODY", "done_criteria": "条件 OTHER-TWO-DONE", "non_goals": []string{}, "depends_on": []string{}, "repo": "main-repo", "operation": "impl"},
			map[string]any{"key": "k3", "title": "題名 OTHER-THREE", "body": "本文 OTHER-THREE-BODY", "done_criteria": "条件 OTHER-THREE-DONE", "non_goals": []string{}, "depends_on": []string{}, "repo": "main-repo", "operation": "impl"},
		}
	})
	id := f.newInProgress(t, "t", "P1", spec)
	cid, _ := parseChallengeID(id)
	return &taskDocFixture{delegateFixture: f, id: id, cid: cid}
}

func (f *taskDocFixture) answer(t *testing.T, q int64, choice *int64, text *string) {
	t.Helper()
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := recordPlanAnswer(context.Background(), tx, PlanAnswerInput{
			ChallengeID: f.cid, PlanVersion: 1, QuestionNumber: q, Choice: choice, Text: text,
			Actor: "alice", Channel: ChannelCLI, Verification: VerificationTTYConfirm,
		}, f.s.currentTime())
		return err
	})
	if err != nil {
		t.Fatalf("recordPlanAnswer: %v", err)
	}
}

func (f *taskDocFixture) taskByTitle(t *testing.T, title string) Task {
	t.Helper()
	for _, tk := range taskRows(t, f.s, f.cid) {
		if tk.Title == title {
			return tk
		}
	}
	t.Fatalf("no task titled %q", title)
	return Task{}
}

// doc は課題の承認済みの計画と title のタスクで、文面を組み立てる（判断の呼び出しの口は渡さない）。
func (f *taskDocFixture) doc(t *testing.T, title string) string {
	t.Helper()
	task := f.taskByTitle(t, title)
	var out string
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		plan, ok, err := loadApprovedPlan(context.Background(), tx, f.cid)
		if err != nil || !ok {
			t.Fatalf("approved plan: ok=%v err=%v", ok, err)
		}
		out, err = loadTaskDocument(context.Background(), tx, f.cid, plan, task)
		return err
	})
	if err != nil {
		t.Fatalf("loadTaskDocument: %v", err)
	}
	return out
}

func i64(v int64) *int64    { return &v }
func strp(s string) *string { return &s }

func requireContains(t *testing.T, doc, want string) {
	t.Helper()
	if !strings.Contains(doc, want) {
		t.Errorf("the task document lacks %q:\n%s", want, doc)
	}
}

func requireAbsent(t *testing.T, doc, unwanted string) {
	t.Helper()
	if strings.Contains(doc, unwanted) {
		t.Errorf("the task document must not contain %q:\n%s", unwanted, doc)
	}
}

func TestTaskDocument_Background(t *testing.T) {
	t.Parallel()
	f := newTaskDocFixture(t)
	doc := f.doc(t, "題名 THIS-TITLE")
	t.Run("AC-27 承認済みの計画の方針", func(t *testing.T) { requireContains(t, doc, "方針 POLICY-MARKER") })
	t.Run("AC-28 完了したら見えるもの", func(t *testing.T) { requireContains(t, doc, "SCENE-MARKER") })
	t.Run("AC-29 計画のやらないこと", func(t *testing.T) { requireContains(t, doc, "NONGOAL-MARKER") })
	t.Run("AC-30 論点ごとの問いの文", func(t *testing.T) {
		requireContains(t, doc, "QUESTION-ONE")
		requireContains(t, doc, "QUESTION-TWO")
	})
}

func TestTaskDocument_Answers(t *testing.T) {
	t.Parallel()
	t.Run("AC-31 選択肢で答えた問いは選んだ選択肢の文", func(t *testing.T) {
		f := newTaskDocFixture(t)
		f.answer(t, 1, i64(2), nil)
		doc := f.doc(t, "題名 THIS-TITLE")
		requireContains(t, doc, "答え: 選択肢 OPT-ONE-B")
		requireAbsent(t, doc, "OPT-ONE-A")
	})
	t.Run("AC-32 文で答えた問いはその文", func(t *testing.T) {
		f := newTaskDocFixture(t)
		f.answer(t, 2, nil, strp("自由な答え FREE-ANSWER"))
		requireContains(t, f.doc(t, "題名 THIS-TITLE"), "答え: 自由な答え FREE-ANSWER")
	})
	t.Run("AC-33 同じ問いに答えが 2 行あれば後の行の答え", func(t *testing.T) {
		f := newTaskDocFixture(t)
		f.answer(t, 1, i64(1), nil)
		f.answer(t, 1, nil, strp("後の答え LATER-ANSWER"))
		doc := f.doc(t, "題名 THIS-TITLE")
		requireContains(t, doc, "答え: 後の答え LATER-ANSWER")
		requireAbsent(t, doc, "OPT-ONE-A")
	})
}

func TestTaskDocument_ThisTask(t *testing.T) {
	t.Parallel()
	f := newTaskDocFixture(t)
	doc := f.doc(t, "題名 THIS-TITLE")
	t.Run("AC-34 題名", func(t *testing.T) { requireContains(t, doc, "題名: 題名 THIS-TITLE") })
	t.Run("AC-35 何をするか", func(t *testing.T) { requireContains(t, doc, "本文 THIS-BODY") })
	t.Run("AC-36 完了条件", func(t *testing.T) { requireContains(t, doc, "条件 THIS-DONE") })
	t.Run("AC-37 やらないこと", func(t *testing.T) { requireContains(t, doc, "範囲外 THIS-NONGOAL") })
}

func TestTaskDocument_OtherTasks(t *testing.T) {
	t.Parallel()
	f := newTaskDocFixture(t)
	doc := f.doc(t, "題名 THIS-TITLE")
	t.Run("AC-38 ほかのタスクの題名", func(t *testing.T) {
		requireContains(t, doc, "題名 OTHER-TWO")
		requireContains(t, doc, "題名 OTHER-THREE")
	})
	t.Run("AC-39 ほかのタスクの何をするかは入らない", func(t *testing.T) {
		requireAbsent(t, doc, "OTHER-TWO-BODY")
		requireAbsent(t, doc, "OTHER-THREE-BODY")
	})
	t.Run("AC-40 ほかのタスクは範囲外の文", func(t *testing.T) { requireContains(t, doc, "ほかのタスクは範囲外である") })
	t.Run("AC-41 取りやめのタスクは一覧に入らない", func(t *testing.T) {
		if err := f.exec(`UPDATE task SET status = 'cancelled' WHERE title = '題名 OTHER-THREE'`); err != nil {
			t.Fatal(err)
		}
		doc := f.doc(t, "題名 THIS-TITLE")
		requireContains(t, doc, "題名 OTHER-TWO")
		requireAbsent(t, doc, "OTHER-THREE")
	})
	t.Run("AC-43 ほかのタスクが無ければその旨", func(t *testing.T) {
		if err := f.exec(`UPDATE task SET status = 'cancelled' WHERE title = '題名 OTHER-TWO'`); err != nil {
			t.Fatal(err)
		}
		doc := f.doc(t, "題名 THIS-TITLE")
		requireContains(t, doc, "同じ課題のほかのタスクは無い。")
		requireAbsent(t, doc, "ほかのタスクは範囲外である")
	})
}

func TestTaskDocument_OtherTasksAreOfTheLatestPlanOnly(t *testing.T) {
	t.Parallel()
	f := newTaskDocFixture(t)
	if err := f.exec(`INSERT INTO task_plan (challenge_id, version, body, created_at) VALUES (?, 2, 'v2', '2026-09-26T00:00:00.000Z')`, f.cid); err != nil {
		t.Fatal(err)
	}
	// 版 1 のタスクは、最新（版 2）の計画のタスクではないので、ほかのタスクの一覧に入らない。
	requireAbsent(t, f.doc(t, "題名 THIS-TITLE"), "OTHER-TWO")
}

func TestTaskDocument_AC42_ThreeGroupsInOrder(t *testing.T) {
	t.Parallel()
	f := newTaskDocFixture(t)
	doc := f.doc(t, "題名 THIS-TITLE")
	a, b, c := strings.Index(doc, "# 背景"), strings.Index(doc, "# このタスク"), strings.Index(doc, "# ほかのタスク")
	if a < 0 || b < 0 || c < 0 || (a >= b || b >= c) {
		t.Fatalf("group offsets = 背景 %d, このタスク %d, ほかのタスク %d; want ascending:\n%s", a, b, c, doc)
	}
}

// AC-44: 文面の組み立ては、判断点の呼び出しの口（JudgmentInvoker）を受け取らない関数で行う。
func TestTaskDocument_AC44_BuiltWithoutAJudgmentInvoker(t *testing.T) {
	t.Parallel()
	_ = loadTaskDocument // 引数は context・tx・課題・計画・タスクだけ（JudgmentInvoker を受け取らない）
	_ = formatTaskDocument
	f := newTaskDocFixture(t)
	f.j3 = nil // 判断の呼び出しの口が無くても組み立てられる。
	requireContains(t, f.doc(t, "題名 THIS-TITLE"), "題名: 題名 THIS-TITLE")
}

// --- form: cli の起動 ---

const taskFileConnectorsJSON = `{
  "version": 1,
  "human_question_kinds": [],
  "connectors": [
    {"id": "tool", "form": "cli", "permission_mode": "default",
     "commands": {
       "start": ["tool", "start", "--file", "{task_file}", "--task", "{task_id}", "--integration", "{uses_integration}", "--number", "{issue_number}", "--url", "{issue_url}", "--key", "{external_key}"],
       "status": ["tool", "status", "{run_id}", "{issue_number}"],
       "resume": ["tool", "resume", "{run_id}", "{issue_number}", "{approved_ref}", "--answer", "{answer}"],
       "cancel": ["tool", "cancel", "{run_id}", "{issue_number}"]
     },
     "contract_version": 1,
     "operations": [{"id": "impl", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]}
  ],
  "repos": [
    {"name": "main-repo", "remote": "o/r", "default_branch": "main", "connector": "tool", "slots": {"provider": "clone", "paths": ["slot1"]}}
  ]
}`

func argAfter(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("flag %s not in %v", flag, args)
	return ""
}

func TestContractStart_TaskFileAndIds(t *testing.T) {
	t.Parallel()
	f := newContractFixtureWith(t, taskFileConnectorsJSON)
	rep, err := f.start(t)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %v", f.client.starts)
	}
	args := f.client.starts[0]
	file := argAfter(t, args, "--file")
	taskID := firstTaskID(t, f.s, f.cid)

	t.Run("AC-45 {task_file} は絶対パス", func(t *testing.T) {
		if !filepath.IsAbs(file) {
			t.Errorf("task file %q is not absolute", file)
		}
	})
	t.Run("AC-46 起動の実行の記録の置き場の下にある", func(t *testing.T) {
		want := filepath.Join(f.s.Workspace(), ".flywheel", "runs", rep.RunID) + string(os.PathSeparator)
		if !strings.HasPrefix(file, want) {
			t.Errorf("task file %q is not under %q", file, want)
		}
	})
	t.Run("AC-47 中身は組み立てた文面と同じ", func(t *testing.T) {
		got, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var want string
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			cid, _ := parseChallengeID(f.cid)
			plan, _, err := loadApprovedPlan(context.Background(), tx, cid)
			if err != nil {
				return err
			}
			task, err := loadTask(context.Background(), tx, mustTaskInt(taskID))
			if err != nil {
				return err
			}
			want, err = loadTaskDocument(context.Background(), tx, cid, plan, *task)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("task file content:\n got: %q\nwant: %q", got, want)
		}
	})
	t.Run("AC-48 契約の実行が終わった後も残る", func(t *testing.T) {
		f.client.last = contractOut(ContractStateSucceeded, nil, nil)
		f.client.queue = []ContractLaunchOutput{f.client.last}
		f.poll(t)
		if _, err := os.Stat(file); err != nil {
			t.Errorf("task file is gone after the contract run ended: %v", err)
		}
	})
	t.Run("AC-49 {task_id} は T-<番号>", func(t *testing.T) {
		if got := argAfter(t, args, "--task"); got != taskID || !strings.HasPrefix(got, "T-") {
			t.Errorf("{task_id} = %q, want %q", got, taskID)
		}
	})
	t.Run("AC-50 統合先を使わないと記録した課題は false", func(t *testing.T) {
		if got := argAfter(t, args, "--integration"); got != "false" {
			t.Errorf("{uses_integration} = %q, want false", got)
		}
	})
}

func mustTaskInt(id string) int64 { n, _ := parseTaskID(id); return n }

func TestContractStart_AC51_UsesIntegrationUnrecordedIsFalse(t *testing.T) {
	t.Parallel()
	f := newContractFixtureWith(t, taskFileConnectorsJSON)
	if err := f.delegateFixture.exec(`UPDATE challenge SET uses_integration = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	if got := argAfter(t, f.client.starts[0], "--integration"); got != "false" {
		t.Errorf("{uses_integration} = %q, want false", got)
	}
}

func TestContractStart_UsesIntegrationRecordedTrueIsTrue(t *testing.T) {
	t.Parallel()
	f := newContractFixtureWith(t, taskFileConnectorsJSON)
	if err := f.delegateFixture.exec(`UPDATE challenge SET uses_integration = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	if got := argAfter(t, f.client.starts[0], "--integration"); got != "true" {
		t.Errorf("{uses_integration} = %q, want true", got)
	}
}

func TestContractStart_SourcelessTaskWithTaskFileDeclaration(t *testing.T) {
	t.Parallel()
	f := newContractFixtureWith(t, taskFileConnectorsJSON) // 取り込み元の無い課題
	if _, err := f.start(t); err != nil {
		t.Fatalf("AC-52 start without a source: %v", err)
	}
	args := f.client.starts[0]
	t.Run("AC-53 {issue_number} は空", func(t *testing.T) {
		if got := argAfter(t, args, "--number"); got != "" {
			t.Errorf("= %q", got)
		}
	})
	t.Run("AC-54 {issue_url} は空", func(t *testing.T) {
		if got := argAfter(t, args, "--url"); got != "" {
			t.Errorf("= %q", got)
		}
	})
	t.Run("AC-55 {external_key} は空", func(t *testing.T) {
		if got := argAfter(t, args, "--key"); got != "" {
			t.Errorf("= %q", got)
		}
	})
	t.Run("AC-59 起動の引数の数は宣言の配列の要素の数と同じ", func(t *testing.T) {
		if want := len(f.conn.Connectors[0].Commands.Start); len(args) != want {
			t.Errorf("len(args) = %d, want %d", len(args), want)
		}
	})
	t.Run("AC-56 status の {issue_number} は空", func(t *testing.T) {
		f.client.last = contractOut(ContractStateRunning, nil, nil)
		f.poll(t)
		if len(f.client.statuses) == 0 {
			t.Fatal("status was not called")
		}
		st := f.client.statuses[0]
		if len(st) != 4 || st[3] != "" {
			t.Errorf("status args = %q, want 4 elements with an empty {issue_number}", st)
		}
	})
	c := &f.conn.Connectors[0]
	t.Run("AC-57 resume の {issue_number} は空", func(t *testing.T) {
		got, err := fillCommandArgs(c, c.Commands.Resume, "C-1", nil, "ext-1", contractAnswer{Choice: "a"})
		if err != nil || len(got) != len(c.Commands.Resume) || got[3] != "" {
			t.Errorf("resume args = %q err = %v, want an empty {issue_number}", got, err)
		}
	})
	t.Run("AC-58 cancel の {issue_number} は空", func(t *testing.T) {
		got, err := fillCommandArgs(c, c.Commands.Cancel, "C-1", nil, "ext-1", contractAnswer{})
		if err != nil || len(got) != len(c.Commands.Cancel) || got[3] != "" {
			t.Errorf("cancel args = %q err = %v, want an empty {issue_number}", got, err)
		}
	})
	t.Run("AC-181 resume の {approved_ref} は空", func(t *testing.T) {
		got, err := fillCommandArgs(c, c.Commands.Resume, "C-1", nil, "ext-1", contractAnswer{Choice: "a"})
		if err != nil || got[4] != "" {
			t.Errorf("resume args = %q err = %v, want an empty {approved_ref}", got, err)
		}
	})
}

func TestCheckOperationLaunchable_AC60_TaskFileDeclarationPassesWithoutASource(t *testing.T) {
	t.Parallel()
	f := newContractFixtureWith(t, taskFileConnectorsJSON)
	c := &f.conn.Connectors[0]
	if err := checkOperationLaunchable(c, &c.Operations[0], false); err != nil {
		t.Fatalf("a declaration with {task_file} must pass without a source: %v", err)
	}
}

func TestRunDelegation_AC61_WithoutTaskFileASourcelessTaskIsNotStarted(t *testing.T) {
	t.Parallel()
	needsSource := strings.Replace(taskFileConnectorsJSON, `"--file", "{task_file}", `, ``, 1)
	needsSource = strings.Replace(needsSource, `"--task", "{task_id}", `, ``, 1) // {task_file} も他の新しい差し込みも書かない宣言
	f := newContractFixtureWith(t, needsSource)
	in := f.input(f.cycle(t, 300), nil)
	in.Contract = f.client
	res, err := f.s.RunDelegation(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedPlanUnavailable || len(f.client.starts) != 0 {
		t.Fatalf("not_started = %+v starts = %v, want plan_unavailable and no start", res.NotStarted, f.client.starts)
	}
}

// --- form: plugin は J3 を呼ばない ---

func TestPluginDelegation_DoesNotCallJ3AndCarriesTheTaskDocument(t *testing.T) {
	t.Parallel()
	g := newDelegateFixture(t)
	id := g.newInProgress(t, "one", "P1", planSpec(func(m map[string]any) { m["summary"] = "方針 ONE-POLICY" }))
	g.mustRun(t, &id)
	t.Run("AC-62・AC-332 J3 の実行の記録を作らない", func(t *testing.T) {
		if n := countRows(t, g.s, `SELECT COUNT(*) FROM run WHERE judgment = 'J3'`); n != 0 || len(g.j3Inputs) != 0 {
			t.Errorf("J3 runs = %d, J3 calls = %d, want none", n, len(g.j3Inputs))
		}
	})
	t.Run("AC-63 標準入力に組み立てたタスクの文面がある", func(t *testing.T) {
		l := g.lastLaunch(t)
		cid, _ := parseChallengeID(id)
		var want string
		task := taskRows(t, g.s, cid)[0]
		if err := g.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			plan, _, err := loadApprovedPlan(context.Background(), tx, cid)
			if err != nil {
				return err
			}
			want, err = loadTaskDocument(context.Background(), tx, cid, plan, task)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if l.Brief != want || !strings.Contains(l.Brief, "方針 ONE-POLICY") {
			t.Errorf("Brief = %q, want the task document %q", l.Brief, want)
		}
	})
}

func TestBriefFormDelegation_AC65_CallsJ3(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	f.newInProgress(t, "t", "P1", briefSpec(nil))
	f.mustRun(t, nil)
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM run WHERE judgment = 'J3'`); n != 1 || len(f.j3Inputs) != 1 {
		t.Errorf("J3 runs = %d, calls = %d, want 1", n, len(f.j3Inputs))
	}
	if got := f.lastLaunch(t).Brief; got != "BRIEF-MARKER" {
		t.Errorf("Brief = %q, want the J3 output", got)
	}
}

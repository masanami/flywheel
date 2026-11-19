package core_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// E2ESeedEnvVar は、本物の serve の E2E（web/e2e-real/。`make e2e-real`）の種のストアを作るワークスペースの
// ディレクトリ。渡されなければ t.TempDir() に作り、ふだんの go test では種の中身の検査だけをする
// （AC-439・#813）。渡されたときも同じ検査を通してから、そのディレクトリに種のストアを残す。
const E2ESeedEnvVar = "FLYWHEEL_E2E_SEED_WORKSPACE"

// 種の課題・フォローアップの題名。web/e2e-real/ の試験が同じ題名を探す（変えるときは両方を直す）。
const (
	seedTitlePlanApproval = "計画承認待ちの課題"
	seedTitleInProgress   = "着手中の課題"
	seedTitleNeedsHuman   = "人間対応待ちの課題"
	seedTitleDone         = "完了した課題"
	seedFollowupOpen      = "未のフォローアップ"
	seedFollowupDone      = "済みのフォローアップ"
	seedFollowupDropped   = "捨てたフォローアップ"
)

// seedPlanSpec は計画の構造化した出力。tasks は (key, 前提) の並び。withQuestion は問いを 1 件持たせる。
func seedPlanSpec(t *testing.T, keys []string, deps map[string][]string, withQuestion bool) string {
	t.Helper()
	type task struct {
		Key          string   `json:"key"`
		Title        string   `json:"title"`
		Body         string   `json:"body"`
		DoneCriteria string   `json:"done_criteria"`
		NonGoals     []string `json:"non_goals"`
		DependsOn    []string `json:"depends_on"`
		Repo         string   `json:"repo"`
		Operation    string   `json:"operation"`
	}
	tasks := []task{}
	for _, k := range keys {
		d := deps[k]
		if d == nil {
			d = []string{}
		}
		tasks = append(tasks, task{Key: k, Title: "タスク " + k, Body: "タスク " + k + " の本文", DoneCriteria: "完了条件 " + k, NonGoals: []string{}, DependsOn: d, Repo: "example/repo", Operation: "brief"})
	}
	topics := []any{}
	if withQuestion {
		topics = append(topics, map[string]any{
			"title": "進め方", "decision": "段階を分けて進める",
			"questions": []any{map[string]any{
				"question":    "どちらの案で進めますか",
				"options":     []any{map[string]any{"label": "案 A"}, map[string]any{"label": "案 B"}},
				"recommended": 1,
			}},
		})
	}
	b, err := json.Marshal(map[string]any{"verdict": "plan", "size": "S", "uses_integration": false, "summary": "要約", "topics": topics, "tasks": tasks})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestE2ESeed は本物の serve の E2E の種のストアを作り、中身を確かめる（AC-439・#813）。
// ID は作る順（C-1 から・F-1 から）で、web/e2e-real/ の試験が題名で探す。
//
//	C-1 計画承認待ち（タスク 2 件・問い 1 件）
//	C-2 着手中（タスク 3 件。済み・作業中・未着手）
//	C-3 人間対応待ち（作業中のタスクに保留が付いている）
//	C-4 完了
//	F-1 未 / F-2 済み / F-3 捨てた（フォローアップ。取り込めるものは上流を読むので種にしない）
func TestE2ESeed(t *testing.T) {
	t.Parallel()
	dir := os.Getenv(E2ESeedEnvVar)
	if dir == "" {
		dir = t.TempDir()
	} else if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		// 既存のストアに足すと ID がずれるので、空のディレクトリ（か存在しない場所）だけを受ける。
		t.Fatalf("%s=%s is not empty", E2ESeedEnvVar, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	st, err := core.OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, title := range []string{seedTitlePlanApproval, seedTitleInProgress, seedTitleNeedsHuman, seedTitleDone} {
		_, err := st.CreateChallenge(ctx, core.ChannelCLI, core.CreateInput{Title: title, DoneCriteria: "完了条件"})
		must(err)
	}
	for _, title := range []string{seedFollowupOpen, seedFollowupDone, seedFollowupDropped} {
		_, err := st.CreateFollowup(ctx, core.ChannelCLI, core.FollowupInput{ChallengeID: "C-4", Title: title, Detail: title + "の説明", Source: core.FollowupSourceLearn})
		must(err)
	}
	_, err = st.CompleteFollowups(ctx, core.ChannelCLI, []string{"F-2"}, "確かめた")
	must(err)
	must(st.Close())

	// C-1: 分類済みにして計画を登録する（計画承認待ちになる）。分類は公開の口（ClassifyChallenge）で記録する。構造化した出力（タスク 2 件・問い 1 件）を付ける。
	st, err = core.OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ClassifyChallenge(ctx, core.ChannelCLI, "C-1", core.ClassifyInput{Priority: "P2"})
	must(err)
	_, _, err = st.PlanChallenge(ctx, core.ChannelCLI, "C-1", core.PlanInput{Body: "計画の本文。\n\n- 手順 1\n- 手順 2"})
	must(err)
	must(st.Close())
	coretest.SetPlanSpec(t, dir, 1, 1, seedPlanSpec(t, []string{"a", "b"}, map[string][]string{"b": {"a"}}, true))

	// C-2・C-3: 承認済みの計画（着手中）。公開 API では作れない状態（タスクの進み・保留・完了・捨てた）だけを直接書く。
	coretest.InsertApprovedPlan(t, dir, 2, "着手中の計画", seedPlanSpec(t, []string{"a", "b", "c"}, map[string][]string{"b": {"a"}, "c": {"b"}}, false))
	coretest.Exec(t, dir, `UPDATE task SET status = 'done' WHERE challenge_id = 2 AND key = 'a'`)
	coretest.Exec(t, dir, `UPDATE task SET status = 'in_progress' WHERE challenge_id = 2 AND key = 'b'`)
	coretest.InsertApprovedPlan(t, dir, 3, "人間対応待ちの計画", seedPlanSpec(t, []string{"a", "b"}, map[string][]string{"b": {"a"}}, false))
	coretest.Exec(t, dir, `UPDATE task SET status = 'in_progress' WHERE challenge_id = 3 AND key = 'a'`)
	coretest.InsertHold(t, dir, 3, "どちらの案で進めますか", "in_progress", "2026-11-19T00:00:00.000Z", nil)
	coretest.Exec(t, dir, `UPDATE hold SET task_id = (SELECT id FROM task WHERE challenge_id = 3 AND key = 'a') WHERE challenge_id = 3`)
	coretest.Exec(t, dir, `UPDATE challenge SET status = 'awaiting_human', version = version + 1 WHERE id = 3`)
	coretest.SetChallengeStatus(t, dir, 4, "done")
	coretest.Exec(t, dir, `UPDATE followup SET status = 'dropped', reason = '要らなくなった' WHERE id = 3`)

	assertE2ESeed(t, dir)
}

// assertE2ESeed は種の中身（課題の状態・タスクの進み・保留・問い・フォローアップの状態）を、core の公開の読み口で確かめる。
func assertE2ESeed(t *testing.T, dir string) {
	t.Helper()
	st, err := core.OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	tree, err := st.ChallengeTree(ctx, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]core.TreeChallenge{}
	for _, c := range append(append([]core.TreeChallenge{}, tree.Challenges...), tree.RecentDone...) {
		all[c.ID] = c
	}
	wantStatus := map[string]core.Status{
		"C-1": core.StatusAwaitingPlanApproval, "C-2": core.StatusInProgress, "C-3": core.StatusAwaitingHuman, "C-4": core.StatusDone,
	}
	wantTitle := map[string]string{"C-1": seedTitlePlanApproval, "C-2": seedTitleInProgress, "C-3": seedTitleNeedsHuman, "C-4": seedTitleDone}
	for id, want := range wantStatus {
		c, ok := all[id]
		if !ok {
			t.Errorf("%s is missing from the challenge tree", id)
			continue
		}
		if c.Status != want || c.Title != wantTitle[id] {
			t.Errorf("%s = %s %q, want %s %q", id, c.Status, c.Title, want, wantTitle[id])
		}
	}
	phases := func(id string) map[string]core.TaskPhase {
		out := map[string]core.TaskPhase{}
		for _, tk := range all[id].Tasks {
			out[tk.Key] = tk.Phase
		}
		return out
	}
	// 計画承認待ち: タスク 2 件（承認の前なので、前提のないものが「次」、前提のあるものが「待ち」）
	if want := map[string]core.TaskPhase{"a": core.TaskPhaseNext, "b": core.TaskPhaseBlocked}; !reflect.DeepEqual(phases("C-1"), want) {
		t.Errorf("C-1 task phases = %v, want %v", phases("C-1"), want)
	}
	// 着手中: タスク 3 件（済み・作業中・未着手）
	if want := map[string]core.TaskPhase{"a": core.TaskPhaseDone, "b": core.TaskPhaseRunning, "c": core.TaskPhaseBlocked}; !reflect.DeepEqual(phases("C-2"), want) {
		t.Errorf("C-2 task phases = %v, want %v", phases("C-2"), want)
	}
	if all["C-2"].TasksTotal != 3 || all["C-2"].TasksDone != 1 {
		t.Errorf("C-2 tasks = %d/%d, want 1/3 done", all["C-2"].TasksDone, all["C-2"].TasksTotal)
	}
	// 人間対応待ち: タスクに保留が付いている（そのタスクはオーナーの返事を待つ区分になる）
	if want := map[string]core.TaskPhase{"a": core.TaskPhaseWaiting, "b": core.TaskPhaseBlocked}; !reflect.DeepEqual(phases("C-3"), want) {
		t.Errorf("C-3 task phases = %v, want %v", phases("C-3"), want)
	}
	// 計画承認待ちの計画の問いは 1 件
	d, err := st.GetChallengeWithoutReap(ctx, "C-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Plans) != 1 || d.Plans[0].Spec == nil {
		t.Fatalf("C-1 plans = %+v, want one plan with a spec", d.Plans)
	}
	var spec struct {
		Topics []struct {
			Questions []json.RawMessage `json:"questions"`
		} `json:"topics"`
	}
	if err := json.Unmarshal([]byte(*d.Plans[0].Spec), &spec); err != nil {
		t.Fatal(err)
	}
	questions := 0
	for _, tp := range spec.Topics {
		questions += len(tp.Questions)
	}
	if questions != 1 {
		t.Errorf("C-1 plan questions = %d, want 1", questions)
	}

	// フォローアップ: 未・済み・捨てたが各 1 件
	fs, err := st.ListFollowups(ctx, core.FollowupFilter{Status: "all"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[core.FollowupStatus]string{}
	for _, f := range fs {
		got[f.Status] = f.Title
	}
	want := map[core.FollowupStatus]string{
		core.FollowupStatusOpen: seedFollowupOpen, core.FollowupStatusDone: seedFollowupDone, core.FollowupStatusDropped: seedFollowupDropped,
	}
	if len(fs) != 3 || !reflect.DeepEqual(got, want) {
		t.Errorf("followups = %v (%d rows), want %v", got, len(fs), want)
	}
}

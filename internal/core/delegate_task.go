package core

// このファイルは委譲の段が使うタスクの口を持つ: 起動できるタスクの判定、委譲の起動・結果によるタスクの
// 状態の変化、J3 の入力の区画「タスク」。タスクの行の読み書きは task.go の口を使う。
// 課題の状態と遷移は変えない。委譲の履歴・再開・やり直しの数え・予算の枠は、課題と計画の版の単位のまま。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// labelTaskSection は J3 の入力の区画「タスク」の見出し。
const labelTaskSection = "タスク"

// isLaunchableTaskStatus は、状態が起動できるタスクのもの（pending・in_progress）か。
func isLaunchableTaskStatus(s TaskStatus) bool {
	return s == TaskStatusPending || s == TaskStatusInProgress
}

// loadLaunchableTasks は課題 cid の起動できるタスク（task の行のうち状態が pending か in_progress のもの。
// 計画の版は問わない）を ID の昇順で返す。構造化した出力ではなく行で数える。
func loadLaunchableTasks(ctx context.Context, tx *sql.Tx, cid int64) ([]Task, error) {
	all, err := listTasks(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(all))
	for _, t := range all {
		if isLaunchableTaskStatus(t.Status) {
			out = append(out, t)
		}
	}
	return out, nil
}

// launchGate は、起動できるタスクの数と課題の統合先の使用から、委譲を起動できるかを決める。
//
//   - 起動できるタスクが 0 個 → no_tasks
//   - 課題が統合先を使う（uses_integration が 1）、または起動できるタスクが 2 個以上 → integration_unavailable
//   - ちょうど 1 個で、そのタスクが対応する子が起動を妨げる（taskUpstreamExclusion）→ upstream_closed・upstream_missing
//   - ちょうど 1 個 → 起動できる（task を返す。NotStarted は nil）
//
// 計画の検査（validateJ2Output）はここで掛けない（呼び出し側が、起動できるときだけ後で行う）。
func launchGate(ctx context.Context, tx *sql.Tx, challengeID string, cid int64) (*Task, *NotStarted, error) {
	tasks, err := loadLaunchableTasks(ctx, tx, cid)
	if err != nil {
		return nil, nil, err
	}
	if len(tasks) == 0 {
		return nil, &NotStarted{ChallengeID: challengeID, Reason: NotStartedNoTasks, Detail: "起動できるタスクが無い"}, nil
	}
	integ, err := challengeUsesIntegration(ctx, tx, cid)
	if err != nil {
		return nil, nil, err
	}
	if len(tasks) >= 2 || (integ != nil && *integ) {
		return nil, &NotStarted{ChallengeID: challengeID, Reason: NotStartedIntegrationUnavailable,
			Detail: "統合先を使う課題と、起動できるタスクが 2 個以上の課題は、後の区切りまで起動しない"}, nil
	}
	// タスクが対応する子の上流の状態（記録が無い・閉じた・無い）で起動しない。
	if ns, err := taskUpstreamExclusion(ctx, tx, challengeID, cid, &tasks[0]); err != nil || ns != nil {
		return nil, ns, err
	}
	return &tasks[0], nil, nil
}

// taskIntID はタスクの内部整数 ID。
func taskIntID(t *Task) int64 {
	n, _ := parseTaskID(t.ID)
	return n
}

// taskIDPtr はタスクの内部整数 ID のポインタ（ID を持たないタスクのゼロ値は nil＝NULL）。
func taskIDPtr(t *Task) *int64 {
	if n := taskIntID(t); n > 0 {
		return &n
	}
	return nil
}

// newInvokerRecorder は、委譲の run を原因とする作業ログ（経路 invoker）の記録口を tx に作る。
func (s *Store) newInvokerRecorder(tx *sql.Tx, runID *int64) (*activityRecorder, error) {
	actor, err := s.resolveActor()
	if err != nil {
		return nil, err
	}
	return &activityRecorder{
		tx: tx, at: s.currentTime(), actor: actor, channel: ChannelInvoker, verification: VerificationNone,
		insert: s.insertActivity, runID: runID,
	}, nil
}

// startTaskTx は委譲の起動で、pending のタスクを in_progress にする（作業ログは同じトランザクション）。
// すでに in_progress など pending でないタスクの再開・やり直しでは何も変えない。
func (s *Store) startTaskTx(ctx context.Context, tx *sql.Tx, taskID int64, runID *int64) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ?`, taskID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if TaskStatus(status) != TaskStatusPending {
		return nil
	}
	rec, err := s.newInvokerRecorder(tx, runID)
	if err != nil {
		return err
	}
	_, err = setTaskStatus(ctx, tx, rec, taskID, TaskStatusInProgress)
	return err
}

// verifyTaskInTransition は、課題が検証中へ進む遷移（tc）と同じトランザクションで、タスクを verifying にする
// （作業ログも同じトランザクション）。taskID が nil（過去の行など）なら何もしない。
func verifyTaskInTransition(ctx context.Context, tc *transitionCtx, taskID *int64) error {
	if taskID == nil {
		return nil
	}
	// 作業中のタスクだけを検証中にする（計画の作り直しで取りやめになったタスクなどを動かさない）。
	var status string
	if err := tc.tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ?`, *taskID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if TaskStatus(status) != TaskStatusInProgress {
		return nil
	}
	_, err := setTaskStatus(ctx, tc.tx, tc.rec, *taskID, TaskStatusVerifying)
	return err
}

// verifyContractTask は、契約の run（externalRunID）が結び付くタスクを検証中にする（課題が検証中へ進む遷移 tc と同じ
// トランザクション）。タスクに結び付かない契約の run（過去の行）では何もしない。
func verifyContractTask(ctx context.Context, tc *transitionCtx, externalRunID string) error {
	taskID, err := contractRunTaskID(ctx, tc.tx, tc.id, externalRunID)
	if err != nil {
		return err
	}
	return verifyTaskInTransition(ctx, tc, taskID)
}

// contractRunTaskID は課題の、契約の実行 ID が externalRunID の契約の run の task_id を読む（無ければ nil）。
func contractRunTaskID(ctx context.Context, tx *sql.Tx, challengeID int64, externalRunID string) (*int64, error) {
	var v sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT task_id FROM contract_run WHERE challenge_id = ? AND external_run_id = ?`,
		challengeID, externalRunID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !v.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v.Int64, nil
}

// formatTaskSection は J3 の入力の区画「タスク」の本文（題名・何をするか・完了条件・やらないこと）。
func formatTaskSection(t Task) string {
	nonGoals := "（なし）"
	if len(t.NonGoals) > 0 {
		lines := make([]string, len(t.NonGoals))
		for i, g := range t.NonGoals {
			lines[i] = "- " + g
		}
		nonGoals = "\n" + strings.Join(lines, "\n")
	}
	return fmt.Sprintf("題名: %s\n\n何をするか:\n%s\n\n完了条件:\n%s\n\nやらないこと: %s", t.Title, t.Body, t.DoneCriteria, nonGoals)
}

// taskDocPlan は、タスクの文面の「背景」に入れる、承認済みの計画の中身。
type taskDocPlan struct {
	Summary  string
	Topics   []taskDocTopic
	Expected []StageExpected
	NonGoals []string
}

// taskDocTopic は論点 1 つ（題名・どう決めたか・問いと記録した答え）。
type taskDocTopic struct {
	Title     string
	Decision  string
	Questions []taskDocQuestion
}

// taskDocQuestion は問い 1 つと、記録した答え（答えの記録が無いときは Answer が空）。
type taskDocQuestion struct {
	Number   int64
	Text     string
	Answer   string
	Answered bool
}

// formatTaskDocument は起動するタスクの文面（UTF-8 の Markdown）を組み立てる。LLM も判断の呼び出しの口も使わない。
// 上から、背景（承認済みの計画の、方針・論点ごとの問いと答え・完了したら見えるもの・やらないこと）、このタスク
// （題名・何をするか・完了条件・やらないこと）、ほかのタスク（題名の一覧と、範囲外の明記）の順。ほかのタスクの
// 「何をするか」は入れない。others は呼び出し側が取りやめを除いて渡す。
func formatTaskDocument(plan taskDocPlan, task Task, others []Task) string {
	var b strings.Builder
	b.WriteString("# 背景\n\n## 方針\n\n")
	b.WriteString(blankOr(plan.Summary, "（なし）"))
	b.WriteString("\n\n## 論点と決定\n\n")
	if len(plan.Topics) == 0 {
		b.WriteString("（なし）\n\n")
	}
	for _, t := range plan.Topics {
		fmt.Fprintf(&b, "### %s\n\n決定: %s\n\n", t.Title, blankOr(t.Decision, "（なし）"))
		for _, q := range t.Questions {
			ans := "（答えの記録なし）"
			if q.Answered {
				ans = q.Answer
			}
			fmt.Fprintf(&b, "- 問い: %s\n  答え: %s\n", q.Text, ans)
		}
		if len(t.Questions) > 0 {
			b.WriteString("\n")
		}
	}
	b.WriteString("## 完了したら見えるもの\n\n")
	if len(plan.Expected) == 0 {
		b.WriteString("（なし）\n")
	}
	for _, e := range plan.Expected {
		fmt.Fprintf(&b, "- [%s] %s\n  前: %s\n  後: %s\n", e.Kind, e.Scene, e.Before, e.After)
	}
	b.WriteString("\n## やらないこと\n\n")
	b.WriteString(bulletList(plan.NonGoals))
	b.WriteString("\n# このタスク\n\n")
	fmt.Fprintf(&b, "題名: %s\n\n## 何をするか\n\n%s\n\n## 完了条件\n\n%s\n\n## やらないこと\n\n%s\n", task.Title, task.Body, task.DoneCriteria, bulletList(task.NonGoals))
	b.WriteString("\n# ほかのタスク\n\n")
	if len(others) == 0 {
		b.WriteString("同じ課題のほかのタスクは無い。\n")
		return b.String()
	}
	b.WriteString("ほかのタスクは範囲外である。このタスクの範囲だけを行う。\n\n")
	for _, o := range others {
		fmt.Fprintf(&b, "- %s\n", o.Title)
	}
	return b.String()
}

func blankOr(s, alt string) string {
	if strings.TrimSpace(s) == "" {
		return alt
	}
	return s
}

func bulletList(items []string) string {
	if len(items) == 0 {
		return "（なし）\n"
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString("- " + it + "\n")
	}
	return b.String()
}

// loadTaskDocument は課題 cid の、承認済みの計画 plan とタスク task から、タスクの文面を組み立てる（読むだけ）。
// 問いの答えは plan_answer のその問いの最後の行（選択肢の答えは選択肢の文、文の答えはその文）。ほかのタスクは、
// 最新の計画の版のタスクのうち、このタスク以外で取りやめでないもの。
func loadTaskDocument(ctx context.Context, tx *sql.Tx, cid int64, plan approvedPlan, task Task) (string, error) {
	var raw j2RawOutput
	if plan.HasSpec {
		if err := json.Unmarshal([]byte(plan.Spec), &raw); err != nil {
			return "", fmt.Errorf("%w: the approved plan spec is malformed", ErrValidation)
		}
	}
	answers, err := listPlanAnswers(ctx, tx, cid, int64(plan.Version))
	if err != nil {
		return "", err
	}
	byQuestion := make(map[int64]PlanAnswer, len(answers))
	for _, a := range answers {
		byQuestion[a.QuestionNumber] = a
	}
	doc := taskDocPlan{Expected: raw.Expected, NonGoals: raw.NonGoals}
	if raw.Summary != nil {
		doc.Summary = *raw.Summary
	}
	var n int64
	for _, t := range raw.Topics {
		topic := taskDocTopic{Title: t.Title, Decision: t.Decision}
		for _, q := range t.Questions {
			n++
			tq := taskDocQuestion{Number: n, Text: q.Question}
			if a, ok := byQuestion[n]; ok {
				switch {
				case a.Choice != nil && *a.Choice >= 1 && int(*a.Choice) <= len(q.Options):
					tq.Answer, tq.Answered = q.Options[*a.Choice-1].Label, true
				case a.Text != nil:
					tq.Answer, tq.Answered = *a.Text, true
				}
			}
			topic.Questions = append(topic.Questions, tq)
		}
		doc.Topics = append(doc.Topics, topic)
	}
	var others []Task
	if latest, ok, err := latestPlanVersion(ctx, tx, cid); err != nil {
		return "", err
	} else if ok {
		all, err := listTasks(ctx, tx, cid)
		if err != nil {
			return "", err
		}
		for _, t := range all {
			if t.PlanVersion == latest && t.ID != task.ID && t.Status != TaskStatusCancelled {
				others = append(others, t)
			}
		}
	}
	return formatTaskDocument(doc, task, others), nil
}

// taskFileName は、起動の実行の記録の置き場へ書くタスクの文面のファイル名。
const taskFileName = "task.md"

// writeTaskFile は文面 text を、起動の実行の記録の置き場 runDir の下の task.md へ UTF-8 で書き、その絶対パスを返す。
// 契約の実行が終わった後も消さない。
func writeTaskFile(runDir, text string) (string, error) {
	abs, err := filepath.Abs(runDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(abs, taskFileName)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

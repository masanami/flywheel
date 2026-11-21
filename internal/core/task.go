package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

// このファイルはタスク（task）・タスクの前提（task_dependency）の行レベルの読み書きを持つ（スキーマの版 26）。
// どの口も、呼び出し側のトランザクションの中で使う（承認・遷移と同じトランザクションで、タスクの行と作業ログを書くため）。
// タスクの作成と状態の変化は、口の中で作業ログへ残す（entity = task・entity_id = T-<番号>・action = create か task_status）。
// タスクは版を持たないので、作業ログの after に version は載せない。

var (
	// errDuplicateTaskKey は、同じ課題・同じ計画の版の中で key が重なったことを表す
	// （一意索引 task_challenge_plan_key の違反を翻訳する）。ErrValidation として扱える。
	errDuplicateTaskKey = fmt.Errorf("%w: duplicate task key within the same challenge and plan version", ErrValidation)
)

var taskIDPattern = regexp.MustCompile(`^T-[1-9][0-9]*$`)

func formatTaskID(id int64) string { return fmt.Sprintf("T-%d", id) }

// parseTaskID は s をタスクの内部整数 ID として解釈する。形式不正は ok=false。
func parseTaskID(s string) (int64, bool) {
	if !taskIDPattern.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "T-"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// TaskInput は createTasks に渡すタスク 1 件。Key は計画の中の識別子（"" は NULL＝計画から作らないタスク）。
// DependsOn は前提のタスクの Key（同じ呼び出しで作るタスクの Key）。
type TaskInput struct {
	Key          string
	Title        string
	Body         string
	DoneCriteria string
	NonGoals     []string
	Repo         string
	Operation    string
	DependsOn    []string
	// SourceKey は対応する取り込み元の子の external_key（"" は NULL＝子に対応しないタスク）。
	SourceKey string
	// BudgetImplMicros・BudgetReviewMicros はタスクの実装の枠・レビュー対応の枠（USD の 100 万分の 1。nil は NULL）。
	BudgetImplMicros   *int64
	BudgetReviewMicros *int64
}

// Task はタスク 1 件（前提のタスクの ID つき）。ID・DependsOn の要素は表示形（"T-<n>"）、ChallengeID は "C-<n>"。
// Key は "" が NULL、NonGoals は nil が NULL を表す。
type Task struct {
	ID           string
	ChallengeID  string
	PlanVersion  int64
	Key          string
	Title        string
	Body         string
	DoneCriteria string
	NonGoals     []string
	Repo         string
	Operation    string
	Status       TaskStatus
	DependsOn    []string
	// SourceKey は対応する取り込み元の子の external_key（"" は NULL）。
	SourceKey string
	CreatedAt time.Time
	UpdatedAt time.Time
	// BudgetImplUSD・BudgetReviewUSD・SpentUSD はタスクの実装の枠・レビュー対応の枠（増額を含む実効値）と、そのタスクの実行の費用の合計（USD）。
	// 課題の詳細（GetChallenge）だけが埋める。枠を決められないタスク（記録が空で、見直し前の決め方の値も引けない）は 0。
	BudgetImplUSD   float64
	BudgetReviewUSD float64
	SpentUSD        float64
	// Detail は委譲先の実行・成果物・値・検証・統合先へ入れる進み。課題の詳細（GetChallenge）だけが埋める。
	Detail TaskDetail
}

func (in TaskInput) valid() bool {
	for _, v := range []string{in.Title, in.Body, in.DoneCriteria, in.Repo, in.Operation} {
		if strings.TrimSpace(v) == "" {
			return false
		}
	}
	return true
}

// createTasks は challengeID の課題の計画の版 planVersion のタスクをまとめて作る（状態は pending）。
// 前提（DependsOn）は同じ呼び出しの Key で指す。存在しない Key・自分自身への前提・必須値の欠落は ErrValidation、
// 同じ Key が重なれば errDuplicateTaskKey（ErrValidation）、課題が無ければ ErrNotFound。
// 作ったタスクを入力の順で返す。
func createTasks(ctx context.Context, tx *sql.Tx, rec *activityRecorder, challengeID, planVersion int64, in []TaskInput) ([]Task, error) {
	if planVersion < 1 {
		return nil, ErrValidation
	}
	byKey := make(map[string]int, len(in))
	for i, t := range in {
		if !t.valid() {
			return nil, ErrValidation
		}
		if t.Key == "" {
			continue
		}
		if _, dup := byKey[t.Key]; dup {
			return nil, errDuplicateTaskKey
		}
		byKey[t.Key] = i
	}
	for _, t := range in {
		for _, k := range t.DependsOn {
			if _, ok := byKey[k]; !ok || k == t.Key {
				return nil, ErrValidation
			}
		}
	}
	if hasDependencyCycle(in, byKey) {
		return nil, ErrValidation
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM challenge WHERE id = ?`, challengeID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}

	nowStr := formatTimestamp(rec.at)
	ids := make([]int64, len(in))
	for i, t := range in {
		var nonGoals any
		if t.NonGoals != nil {
			b, err := json.Marshal(t.NonGoals)
			if err != nil {
				return nil, err
			}
			nonGoals = string(b)
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, non_goals, repo, operation, status, source_key, created_at, updated_at, budget_impl_micros, budget_review_micros)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			challengeID, planVersion, nullableTextColumn(t.Key), t.Title, t.Body, t.DoneCriteria, nonGoals, t.Repo, t.Operation,
			string(TaskStatusPending), nullableTextColumn(t.SourceKey), nowStr, nowStr,
			int64PtrColumn(t.BudgetImplMicros), int64PtrColumn(t.BudgetReviewMicros))
		if store.IsUniqueViolation(err) {
			return nil, errDuplicateTaskKey
		}
		if err != nil {
			return nil, err
		}
		if ids[i], err = res.LastInsertId(); err != nil {
			return nil, err
		}
		if err := rec.record("task", ids[i], "create", nil, map[string]any{"status": string(TaskStatusPending)}); err != nil {
			return nil, err
		}
	}
	for i, t := range in {
		for _, k := range t.DependsOn {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO task_dependency (task_id, depends_on_id) VALUES (?, ?)`, ids[i], ids[byKey[k]]); err != nil {
				return nil, err
			}
		}
	}
	out := make([]Task, 0, len(ids))
	for _, id := range ids {
		t, err := loadTask(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, nil
}

const taskSelect = `SELECT id, challenge_id, plan_version, key, title, body, done_criteria, non_goals, repo, operation, status, source_key, created_at, updated_at FROM task`

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var (
		id, challengeID, planVersion int64
		key, nonGoals, sourceKey     sql.NullString
		status, createdAt, updatedAt string
		t                            Task
	)
	if err := row.Scan(&id, &challengeID, &planVersion, &key, &t.Title, &t.Body, &t.DoneCriteria, &nonGoals, &t.Repo, &t.Operation,
		&status, &sourceKey, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	t.ID, t.ChallengeID, t.PlanVersion, t.Key, t.Status = formatTaskID(id), formatChallengeID(challengeID), planVersion, key.String, TaskStatus(status)
	t.SourceKey = sourceKey.String
	if nonGoals.Valid {
		if err := json.Unmarshal([]byte(nonGoals.String), &t.NonGoals); err != nil {
			return nil, fmt.Errorf("task %s non_goals: %w", t.ID, err)
		}
	}
	var err error
	if t.CreatedAt, err = parseTimestamp(createdAt); err != nil {
		return nil, err
	}
	if t.UpdatedAt, err = parseTimestamp(updatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// loadTask は id のタスクを前提つきで読む。無ければ ErrNotFound。
func loadTask(ctx context.Context, tx *sql.Tx, id int64) (*Task, error) {
	t, err := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	deps, err := loadTaskDependencies(ctx, tx, `SELECT task_id, depends_on_id FROM task_dependency WHERE task_id = ? ORDER BY depends_on_id`, id)
	if err != nil {
		return nil, err
	}
	t.DependsOn = deps[id]
	return t, nil
}

func loadTaskDependencies(ctx context.Context, tx *sql.Tx, q string, args ...any) (map[int64][]string, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]string{}
	for rows.Next() {
		var task, dep int64
		if err := rows.Scan(&task, &dep); err != nil {
			return nil, err
		}
		out[task] = append(out[task], formatTaskID(dep))
	}
	return out, rows.Err()
}

// listTasks は challengeID の課題のタスクを ID の昇順で、前提のタスクの ID つきで返す。無ければ空（nil ではない）。
func listTasks(ctx context.Context, tx *sql.Tx, challengeID int64) ([]Task, error) {
	rows, err := tx.QueryContext(ctx, taskSelect+` WHERE challenge_id = ? ORDER BY id`, challengeID)
	if err != nil {
		return nil, err
	}
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	deps, err := loadTaskDependencies(ctx, tx,
		`SELECT d.task_id, d.depends_on_id FROM task_dependency d JOIN task t ON t.id = d.task_id WHERE t.challenge_id = ? ORDER BY d.task_id, d.depends_on_id`, challengeID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		id, _ := parseTaskID(out[i].ID)
		out[i].DependsOn = deps[id]
	}
	return out, nil
}

// taskFilter は setTasksStatus の対象の条件。ゼロ値の欄は条件にしない。
type taskFilter struct {
	// PlanVersion が非 nil なら、その計画の版のタスクだけ。
	PlanVersion *int64
	// Statuses が空でなければ、そのどれかの状態のタスクだけ。
	Statuses []TaskStatus
}

// setTaskStatus は id のタスクの状態を to にする。タスクが無ければ ErrNotFound、to が閉集合の外なら ErrValidation。
// 状態が変わったときだけ updated_at を更新し、作業ログ（action = task_status）を残す。変わったかを返す。
func setTaskStatus(ctx context.Context, tx *sql.Tx, rec *activityRecorder, id int64, to TaskStatus) (bool, error) {
	parsed, ok := ParseTaskStatus(string(to))
	if !ok {
		return false, ErrValidation
	}
	to = parsed
	var from string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ?`, id).Scan(&from); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	if from == string(to) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task SET status = ?, updated_at = ? WHERE id = ?`, string(to), formatTimestamp(rec.at), id); err != nil {
		return false, err
	}
	if err := rec.record("task", id, "task_status", map[string]any{"status": from}, map[string]any{"status": string(to)}); err != nil {
		return false, err
	}
	return true, nil
}

// setTasksStatus は challengeID の課題のタスクのうち filter に合うものの状態を to にする（ID の昇順に 1 個ずつ
// setTaskStatus と同じ規則で）。状態が変わったタスクの数を返す。
func setTasksStatus(ctx context.Context, tx *sql.Tx, rec *activityRecorder, challengeID int64, filter taskFilter, to TaskStatus) (int, error) {
	q := `SELECT id FROM task WHERE challenge_id = ?`
	args := []any{challengeID}
	if filter.PlanVersion != nil {
		q += ` AND plan_version = ?`
		args = append(args, *filter.PlanVersion)
	}
	if len(filter.Statuses) > 0 {
		marks := make([]string, len(filter.Statuses))
		for i, s := range filter.Statuses {
			marks[i] = "?"
			args = append(args, string(s))
		}
		q += ` AND status IN (` + strings.Join(marks, ",") + `)`
	}
	rows, err := tx.QueryContext(ctx, q+` ORDER BY id`, args...)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	changed := 0
	for _, id := range ids {
		ok, err := setTaskStatus(ctx, tx, rec, id, to)
		if err != nil {
			return changed, err
		}
		if ok {
			changed++
		}
	}
	return changed, nil
}

// PlanAnswerInput は recordPlanAnswer の入力。Choice（選んだ選択肢の番号）と Text（文の答え）はどちらか一方だけを指定する。
type PlanAnswerInput struct {
	ChallengeID    int64
	PlanVersion    int64
	QuestionNumber int64
	Choice         *int64
	Text           *string
	Recommended    bool
	Actor          string
	Channel        Channel
	Verification   Verification
	// RelaySession・RelayQuote は対話で受けたときだけ（"" は NULL）。
	RelaySession string
	RelayQuote   string
}

// PlanAnswer は計画の問いへの答え 1 件。ID は "" ではなく内部整数（表示形を持たない）。Choice・Text は nil が NULL。
type PlanAnswer struct {
	ID             int64
	ChallengeID    string
	PlanVersion    int64
	QuestionNumber int64
	Choice         *int64
	Text           *string
	Recommended    bool
	Actor          string
	Channel        Channel
	Verification   Verification
	RelaySession   string
	RelayQuote     string
	AnsweredAt     time.Time
}

// recordPlanAnswer は計画の問いへの答えを 1 行追加する。選択肢の答えと文の答えの両方（または両方無し）・
// 1 未満の問いの番号や選択肢の番号・本人確認の欠落は ErrValidation、課題が無ければ ErrNotFound。
func recordPlanAnswer(ctx context.Context, tx *sql.Tx, in PlanAnswerInput, at time.Time) (*PlanAnswer, error) {
	if (in.Choice == nil) == (in.Text == nil) || in.QuestionNumber < 1 || in.PlanVersion < 1 ||
		(in.Choice != nil && *in.Choice < 1) || in.Actor == "" || in.Channel == "" || in.Verification == "" {
		return nil, ErrValidation
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM challenge WHERE id = ?`, in.ChallengeID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO plan_answer (challenge_id, plan_version, question_number, choice, text, recommended, actor, channel, verification, relay_session, relay_quote, answered_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ChallengeID, in.PlanVersion, in.QuestionNumber, int64PtrColumn(in.Choice), stringPtrColumn(in.Text), in.Recommended,
		in.Actor, string(in.Channel), string(in.Verification), nullableTextColumn(in.RelaySession), nullableTextColumn(in.RelayQuote), formatTimestamp(at))
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return loadPlanAnswer(ctx, tx, id)
}

const planAnswerSelect = `SELECT id, challenge_id, plan_version, question_number, choice, text, recommended, actor, channel, verification, relay_session, relay_quote, answered_at FROM plan_answer`

func scanPlanAnswer(row interface{ Scan(...any) error }) (*PlanAnswer, error) {
	var (
		a                         PlanAnswer
		cid                       int64
		choice                    sql.NullInt64
		text, session, quote      sql.NullString
		channel, verification, at string
	)
	if err := row.Scan(&a.ID, &cid, &a.PlanVersion, &a.QuestionNumber, &choice, &text, &a.Recommended, &a.Actor, &channel, &verification, &session, &quote, &at); err != nil {
		return nil, err
	}
	a.ChallengeID, a.Channel, a.Verification = formatChallengeID(cid), Channel(channel), Verification(verification)
	if choice.Valid {
		v := choice.Int64
		a.Choice = &v
	}
	if text.Valid {
		v := text.String
		a.Text = &v
	}
	a.RelaySession, a.RelayQuote = session.String, quote.String
	var err error
	if a.AnsweredAt, err = parseTimestamp(at); err != nil {
		return nil, err
	}
	return &a, nil
}

func loadPlanAnswer(ctx context.Context, tx *sql.Tx, id int64) (*PlanAnswer, error) {
	a, err := scanPlanAnswer(tx.QueryRowContext(ctx, planAnswerSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// listPlanAnswers は challengeID の課題の計画の版 planVersion の答えを、問いの番号の昇順に 1 問 1 件で返す。
// 同じ問いに答えが複数あれば、後の行（id が大きい方）を使う。無ければ空（nil ではない）。
func listPlanAnswers(ctx context.Context, tx *sql.Tx, challengeID, planVersion int64) ([]PlanAnswer, error) {
	rows, err := tx.QueryContext(ctx, planAnswerSelect+` WHERE challenge_id = ? AND plan_version = ? ORDER BY id`, challengeID, planVersion)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	latest := map[int64]PlanAnswer{}
	for rows.Next() {
		a, err := scanPlanAnswer(rows)
		if err != nil {
			return nil, err
		}
		latest[a.QuestionNumber] = *a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]PlanAnswer, 0, len(latest))
	for _, a := range latest {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QuestionNumber < out[j].QuestionNumber })
	return out, nil
}

// challengeUsesIntegration は課題の uses_integration を読む（NULL＝未決か過去の課題は nil）。課題が無ければ ErrNotFound。
func challengeUsesIntegration(ctx context.Context, tx *sql.Tx, challengeID int64) (*bool, error) {
	var v sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT uses_integration FROM challenge WHERE id = ?`, challengeID).Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !v.Valid {
		return nil, nil
	}
	b := v.Int64 != 0
	return &b, nil
}

// setChallengeUsesIntegration は課題の uses_integration を書く（nil は NULL に戻す）。課題が無ければ ErrNotFound。
func setChallengeUsesIntegration(ctx context.Context, tx *sql.Tx, challengeID int64, v *bool) error {
	var col any
	if v != nil {
		col = 0
		if *v {
			col = 1
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE challenge SET uses_integration = ? WHERE id = ?`, col, challengeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// holdTaskID は保留の task_id を読む（NULL は nil）。保留が無ければ ErrNotFound。
func holdTaskID(ctx context.Context, tx *sql.Tx, holdID int64) (*int64, error) {
	var v sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT task_id FROM hold WHERE id = ?`, holdID).Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !v.Valid {
		return nil, nil
	}
	return &v.Int64, nil
}

func stringPtrColumn(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// hasDependencyCycle は、同じ呼び出しのタスクの前提（DependsOn。byKey は Key → in の添字）が循環しているかを返す。
// 循環があると、循環の中のどのタスクも前提が満たされず、始められなくなる。
func hasDependencyCycle(in []TaskInput, byKey map[string]int) bool {
	const (
		unvisited = iota
		visiting
		done
	)
	state := make([]int, len(in))
	var visit func(i int) bool
	visit = func(i int) bool {
		state[i] = visiting
		for _, k := range in[i].DependsOn {
			j := byKey[k]
			if state[j] == visiting || (state[j] == unvisited && visit(j)) {
				return true
			}
		}
		state[i] = done
		return false
	}
	for i := range in {
		if state[i] == unvisited && visit(i) {
			return true
		}
	}
	return false
}

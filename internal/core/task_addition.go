package core

// このファイルは、計画の後にタスクを足す記録（足す依頼。表 task_addition。区切り S4c-1）の core の API と型を持つ。
//
//   - 足す依頼は、「足したことの記録」と、足されたタスクの印（task.addition_id）を兼ねる。タスクの状態に「提案」は無い
//     （足す中身は足す依頼の行の spec が持つ）。
//   - 出どころは S4c-1 では expand（続きを決めるタスクが済んだ）だけ。同じ出どころのタスクについて expand の行は 1 つ
//     （部分一意索引 task_addition_expand_origin）。2 つ目は ErrValidation で断る。
//   - 足す依頼から立つ保留は、いまある保留の遷移を使う。ここは保留を作らず、立てた保留の ID（hold_id）と、保留の側へどの足す依頼の問いか（hold.task_addition_id）を記録するだけ。
//   - 状態: pending（仕様待ち）・awaiting_owner（オーナー待ち）は開いている。applied（足した）・none（足さない）・
//     declined（見送り）・superseded（取り替え）は閉じていて、閉じた依頼の状態は変えない（ErrInvalidTransition）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

// TaskAdditionOrigin は足す依頼の出どころの閉集合。
type TaskAdditionOrigin string

// 足す依頼の出どころ。followup（フォローアップを上げた）は S4c-2 から。
const (
	TaskAdditionOriginExpand TaskAdditionOrigin = "expand"
)

// TaskAdditionOriginValues は TaskAdditionOrigin の閉集合の写しを返す。
func TaskAdditionOriginValues() []TaskAdditionOrigin {
	return []TaskAdditionOrigin{TaskAdditionOriginExpand}
}

// TaskAdditionState は足す依頼の状態の閉集合。
type TaskAdditionState string

// 足す依頼の状態。
const (
	TaskAdditionPending       TaskAdditionState = "pending"        // 仕様待ち
	TaskAdditionAwaitingOwner TaskAdditionState = "awaiting_owner" // オーナー待ち
	TaskAdditionApplied       TaskAdditionState = "applied"        // 足した
	TaskAdditionNone          TaskAdditionState = "none"           // 足さない
	TaskAdditionDeclined      TaskAdditionState = "declined"       // 見送り
	TaskAdditionSuperseded    TaskAdditionState = "superseded"     // 取り替え
)

// TaskAdditionStateValues は TaskAdditionState の閉集合の写しを返す。
func TaskAdditionStateValues() []TaskAdditionState {
	return []TaskAdditionState{TaskAdditionPending, TaskAdditionAwaitingOwner, TaskAdditionApplied, TaskAdditionNone, TaskAdditionDeclined, TaskAdditionSuperseded}
}

// ParseTaskAdditionState は s を足す依頼の状態として解釈する（完全一致だけ）。
func ParseTaskAdditionState(s string) (TaskAdditionState, bool) {
	for _, v := range TaskAdditionStateValues() {
		if string(v) == s {
			return v, true
		}
	}
	return "", false
}

// IsOpen は状態が開いている（仕様待ち・オーナー待ち）かを返す。
func (s TaskAdditionState) IsOpen() bool {
	return s == TaskAdditionPending || s == TaskAdditionAwaitingOwner
}

var taskAdditionIDPattern = regexp.MustCompile(`^TA-[1-9][0-9]*$`)

func formatTaskAdditionID(id int64) string { return fmt.Sprintf("TA-%d", id) }

// parseTaskAdditionID は s を足す依頼の内部整数 ID として解釈する。形式不正は ok=false。
func parseTaskAdditionID(s string) (int64, bool) {
	if !taskAdditionIDPattern.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "TA-"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// TaskAddition は足す依頼 1 件。ID は "TA-<n>"、ChallengeID は "C-<n>"、OriginTaskID は "T-<n>"。
// TotalAfterUSD・LimitUSD は最後に判定したときの、足した後の見込みと上限（記録のため。判定には使わない。判定していない足す依頼は nil）。
type TaskAddition struct {
	ID            string
	ChallengeID   string
	Origin        TaskAdditionOrigin
	OriginTaskID  *string
	State         TaskAdditionState
	Spec          *string
	TotalAfterUSD *float64
	LimitUSD      *float64
	// FollowupID は出どころが followup の足す依頼の、元のフォローアップの ID（"F-<n>"）。出どころが expand なら nil。
	FollowupID *string
	// HoldID は足す依頼から立てた保留（task_add_over_limit・task_add_question）の内部整数 ID。無ければ nil。
	HoldID    *int64
	CreatedAt time.Time
	DecidedAt *time.Time
	// TaskIDs はこの足す依頼で足したタスクの ID（task.addition_id が指すもの。ID の昇順。無ければ空のスライス）。
	TaskIDs []string
}

// TaskAdditionInput は createTaskAdditionTx の入力。状態は仕様待ち（pending）で作る。
type TaskAdditionInput struct {
	Origin TaskAdditionOrigin
	// OriginTaskID は出どころのタスク（"T-<n>"。同じ課題のタスク）。
	OriginTaskID string
	// Spec は足す中身（JSON）。省けば nil。
	Spec *string
}

// TaskAdditionUpdate は setTaskAdditionStateTx が、状態と同じ書き込みで更新する値。nil の欄は変えない。
type TaskAdditionUpdate struct {
	Spec          *string
	TotalAfterUSD *float64
	LimitUSD      *float64
	HoldID        *int64
}

const taskAdditionSelect = `SELECT id, challenge_id, origin, origin_task_id, state, spec, total_after_micros, limit_micros, hold_id, created_at, decided_at, followup_id FROM task_addition`

func scanTaskAddition(row interface{ Scan(...any) error }) (*TaskAddition, error) {
	var (
		id, cid           int64
		total             sql.NullInt64
		origin, state     string
		originTask, limit sql.NullInt64
		spec, decided     sql.NullString
		holdID            sql.NullInt64
		followupID        sql.NullInt64
		created           string
		ta                TaskAddition
	)
	if err := row.Scan(&id, &cid, &origin, &originTask, &state, &spec, &total, &limit, &holdID, &created, &decided, &followupID); err != nil {
		return nil, err
	}
	ta.ID, ta.ChallengeID, ta.Origin, ta.State = formatTaskAdditionID(id), formatChallengeID(cid), TaskAdditionOrigin(origin), TaskAdditionState(state)
	if originTask.Valid {
		v := formatTaskID(originTask.Int64)
		ta.OriginTaskID = &v
	}
	if spec.Valid {
		v := spec.String
		ta.Spec = &v
	}
	if total.Valid {
		v := microsToUSD(total.Int64)
		ta.TotalAfterUSD = &v
	}
	if limit.Valid {
		v := microsToUSD(limit.Int64)
		ta.LimitUSD = &v
	}
	if holdID.Valid {
		v := holdID.Int64
		ta.HoldID = &v
	}
	if followupID.Valid {
		v := formatFollowupID(followupID.Int64)
		ta.FollowupID = &v
	}
	var err error
	if ta.CreatedAt, err = parseTimestamp(created); err != nil {
		return nil, err
	}
	if decided.Valid {
		t, err := parseTimestamp(decided.String)
		if err != nil {
			return nil, err
		}
		ta.DecidedAt = &t
	}
	ta.TaskIDs = []string{}
	return &ta, nil
}

// fillTaskAdditionTaskIDs は足す依頼 tas の TaskIDs を、task.addition_id から埋める。
func fillTaskAdditionTaskIDs(ctx context.Context, tx *sql.Tx, tas []*TaskAddition) error {
	for _, ta := range tas {
		id, _ := parseTaskAdditionID(ta.ID)
		rows, err := tx.QueryContext(ctx, `SELECT id FROM task WHERE addition_id = ? ORDER BY id`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var tid int64
			if err := rows.Scan(&tid); err != nil {
				_ = rows.Close()
				return err
			}
			ta.TaskIDs = append(ta.TaskIDs, formatTaskID(tid))
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func loadTaskAddition(ctx context.Context, tx *sql.Tx, id int64) (*TaskAddition, error) {
	ta, err := scanTaskAddition(tx.QueryRowContext(ctx, taskAdditionSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return ta, fillTaskAdditionTaskIDs(ctx, tx, []*TaskAddition{ta})
}

// listTaskAdditions は課題 cid の足す依頼を ID の昇順で返す（閉じたものも含む。無ければ空のスライス）。
func listTaskAdditions(ctx context.Context, tx *sql.Tx, cid int64) ([]TaskAddition, error) {
	rows, err := tx.QueryContext(ctx, taskAdditionSelect+` WHERE challenge_id = ? ORDER BY id`, cid)
	if err != nil {
		return nil, err
	}
	var ptrs []*TaskAddition
	for rows.Next() {
		ta, err := scanTaskAddition(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		ptrs = append(ptrs, ta)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := fillTaskAdditionTaskIDs(ctx, tx, ptrs); err != nil {
		return nil, err
	}
	out := make([]TaskAddition, 0, len(ptrs))
	for _, p := range ptrs {
		out = append(out, *p)
	}
	return out, nil
}

// createTaskAdditionTx は足す依頼を 1 件作る書き込み本体（呼び出し側の書き込みトランザクションの中で使う）。
func createTaskAdditionTx(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid int64, in TaskAdditionInput) (*TaskAddition, error) {
	challengeID := formatChallengeID(cid)
	if _, err := loadChallenge(ctx, tx, cid); err != nil {
		return nil, err
	}
	if in.OriginTaskID == "" {
		return nil, fmt.Errorf("%w: an expand task addition needs its origin task", ErrValidation)
	}
	tid, ok := parseTaskID(in.OriginTaskID)
	if !ok {
		return nil, fmt.Errorf("%w: malformed task id %q", ErrValidation, in.OriginTaskID)
	}
	var owner int64
	if err := tx.QueryRowContext(ctx, `SELECT challenge_id FROM task WHERE id = ?`, tid).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: task %s does not exist", ErrValidation, in.OriginTaskID)
		}
		return nil, err
	}
	if owner != cid {
		return nil, fmt.Errorf("%w: task %s is not a task of %s", ErrValidation, in.OriginTaskID, challengeID)
	}
	var spec any
	if in.Spec != nil {
		spec = *in.Spec
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO task_addition (challenge_id, origin, origin_task_id, state, spec, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		cid, string(in.Origin), tid, string(TaskAdditionPending), spec, formatTimestamp(rec.at))
	if store.IsUniqueViolation(err) {
		return nil, fmt.Errorf("%w: a task addition of origin %q already exists for task %s", ErrValidation, in.Origin, in.OriginTaskID)
	}
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	after := map[string]any{"challenge_id": challengeID, "origin": string(in.Origin), "state": string(TaskAdditionPending), "origin_task_id": in.OriginTaskID}
	if err := rec.record("task_addition", id, "create", nil, after); err != nil {
		return nil, err
	}
	return loadTaskAddition(ctx, tx, id)
}

// setTaskAdditionStateTx は足す依頼の状態を動かす書き込み本体（呼び出し側の書き込みトランザクションの中で使う）。
func setTaskAdditionStateTx(ctx context.Context, tx *sql.Tx, rec *activityRecorder, aid int64, to TaskAdditionState, upd TaskAdditionUpdate) (*TaskAddition, error) {
	cur, err := loadTaskAddition(ctx, tx, aid)
	if err != nil {
		return nil, err
	}
	// 開いている足す依頼だけが動ける（同じ状態への書き込みは、仕様・額・保留の値だけを更新する）。
	if !cur.State.IsOpen() {
		return nil, ErrInvalidTransition
	}
	// オーナー待ちの足す依頼は、同じ課題の保留を指す（hold_id）。指す保留が無い・別の課題の保留のオーナー待ちは書かない。
	if to == TaskAdditionAwaitingOwner {
		holdID := cur.HoldID
		if upd.HoldID != nil {
			holdID = upd.HoldID
		}
		if holdID == nil {
			return nil, fmt.Errorf("%w: an awaiting_owner task addition needs its hold", ErrValidation)
		}
		cid, _ := parseChallengeID(cur.ChallengeID)
		var owner int64
		if err := tx.QueryRowContext(ctx, `SELECT challenge_id FROM hold WHERE id = ?`, *holdID).Scan(&owner); err != nil || owner != cid {
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: the hold of an awaiting_owner task addition must be a hold of its challenge", ErrValidation)
		}
	}
	set := []string{"state = ?"}
	args := []any{string(to)}
	if upd.Spec != nil {
		set, args = append(set, "spec = ?"), append(args, *upd.Spec)
	}
	if upd.TotalAfterUSD != nil {
		set, args = append(set, "total_after_micros = ?"), append(args, usdToMicros(*upd.TotalAfterUSD))
	}
	if upd.LimitUSD != nil {
		set, args = append(set, "limit_micros = ?"), append(args, usdToMicros(*upd.LimitUSD))
	}
	if upd.HoldID != nil {
		set, args = append(set, "hold_id = ?"), append(args, *upd.HoldID)
	}
	if !to.IsOpen() {
		set, args = append(set, "decided_at = ?"), append(args, formatTimestamp(rec.at))
	}
	args = append(args, aid)
	if _, err := tx.ExecContext(ctx, `UPDATE task_addition SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
		return nil, err
	}
	// 保留が、どの足す依頼の問いかをストアに記録する（問いを重ねて hold_id が上書きされても、保留の側に残る）。
	if upd.HoldID != nil {
		res, err := tx.ExecContext(ctx, `UPDATE hold SET task_addition_id = ? WHERE id = ? AND challenge_id = (SELECT challenge_id FROM task_addition WHERE id = ?)`,
			aid, *upd.HoldID, aid)
		if err != nil {
			return nil, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return nil, err
		} else if n != 1 {
			return nil, fmt.Errorf("%w: the hold of a task addition must be a hold of its challenge", ErrValidation)
		}
	}
	if err := rec.record("task_addition", aid, "state", map[string]any{"state": string(cur.State)}, map[string]any{"state": string(to)}); err != nil {
		return nil, err
	}
	return loadTaskAddition(ctx, tx, aid)
}

// GetTaskAddition は足す依頼 id を返す。無ければ ErrNotFound。
func (s *Store) GetTaskAddition(ctx context.Context, id string) (*TaskAddition, error) {
	aid, ok := parseTaskAdditionID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var out *TaskAddition
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = loadTaskAddition(ctx, tx, aid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

// ListTaskAdditions は課題 challengeID の足す依頼を ID の昇順で返す（閉じたものも含む。無ければ空のスライス）。課題が無ければ ErrNotFound。
func (s *Store) ListTaskAdditions(ctx context.Context, challengeID string) ([]TaskAddition, error) {
	cid, ok := parseChallengeID(challengeID)
	if !ok {
		return nil, ErrNotFound
	}
	var out []TaskAddition
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		if _, err := loadChallenge(ctx, tx, cid); err != nil {
			return err
		}
		var err error
		out, err = listTaskAdditions(ctx, tx, cid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

// supersedeOpenTaskAdditions は課題 cid の閉じていない足す依頼（pending・awaiting_owner）を、取り替え（superseded）で閉じる
// （decided_at を記録し、作業ログに残す）。計画の承認の書き込みの中で呼ぶ。立てた保留は、ここでは触れない。
func supersedeOpenTaskAdditions(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, state FROM task_addition WHERE challenge_id = ? AND state IN (?, ?) ORDER BY id`,
		cid, string(TaskAdditionPending), string(TaskAdditionAwaitingOwner))
	if err != nil {
		return err
	}
	type open struct {
		id    int64
		state string
	}
	var opens []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.state); err != nil {
			_ = rows.Close()
			return err
		}
		opens = append(opens, o)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, o := range opens {
		if _, err := tx.ExecContext(ctx, `UPDATE task_addition SET state = ?, decided_at = ? WHERE id = ?`,
			string(TaskAdditionSuperseded), formatTimestamp(rec.at), o.id); err != nil {
			return err
		}
		if err := rec.record("task_addition", o.id, "state", map[string]any{"state": o.state}, map[string]any{"state": string(TaskAdditionSuperseded)}); err != nil {
			return err
		}
	}
	return nil
}

// fillTaskAdded は課題 cid のタスク tasks の Added を、task.addition_id が指す足す依頼から埋める（計画の承認で作ったタスクは nil のまま）。
func fillTaskAdded(ctx context.Context, tx *sql.Tx, cid int64, tasks []Task) error {
	rows, err := tx.QueryContext(ctx, `SELECT t.id, a.origin, a.origin_task_id, a.followup_id FROM task t JOIN task_addition a ON a.id = t.addition_id WHERE t.challenge_id = ?`, cid)
	if err != nil {
		return err
	}
	added := map[string]*TaskAdded{}
	for rows.Next() {
		var id int64
		var origin string
		var originTask, followupID sql.NullInt64
		if err := rows.Scan(&id, &origin, &originTask, &followupID); err != nil {
			_ = rows.Close()
			return err
		}
		a := &TaskAdded{Origin: TaskAdditionOrigin(origin)}
		if originTask.Valid {
			v := formatTaskID(originTask.Int64)
			a.OriginTaskID = &v
		}
		if followupID.Valid {
			v := formatFollowupID(followupID.Int64)
			a.FollowupID = &v
		}
		added[formatTaskID(id)] = a
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for i := range tasks {
		if a, ok := added[tasks[i].ID]; ok {
			a.AddedAt = tasks[i].CreatedAt
			tasks[i].Added = a
		}
	}
	return nil
}

// AddedTask は計画の後に足されたタスク 1 件（課題・題名・出どころ）。完了の承認の要約と cycle の出力が使う。
type AddedTask struct {
	ChallengeID string
	TaskID      string
	Title       string
	Origin      TaskAdditionOrigin
}

// listAddedTasks は課題 cid（0 なら全課題）の、足されたタスクを ID の昇順で返す。since が空でなければ、その時刻以後に足したものだけ。
func listAddedTasks(ctx context.Context, tx *sql.Tx, cid int64, since string) ([]AddedTask, error) {
	q := `SELECT t.id, t.challenge_id, t.title, a.origin FROM task t JOIN task_addition a ON a.id = t.addition_id WHERE 1 = 1`
	var args []any
	if cid != 0 {
		q += ` AND t.challenge_id = ?`
		args = append(args, cid)
	}
	if since != "" {
		q += ` AND t.created_at >= ?`
		args = append(args, since)
	}
	rows, err := tx.QueryContext(ctx, q+` ORDER BY t.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AddedTask{}
	for rows.Next() {
		var id, ch int64
		var title, origin string
		if err := rows.Scan(&id, &ch, &title, &origin); err != nil {
			return nil, err
		}
		out = append(out, AddedTask{ChallengeID: formatChallengeID(ch), TaskID: formatTaskID(id), Title: title, Origin: TaskAdditionOrigin(origin)})
	}
	return out, rows.Err()
}

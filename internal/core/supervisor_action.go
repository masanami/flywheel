package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// このファイルは対処の記録（supervisor_action。スーパーバイザーの判断 1 回と、core がその手を実行した結果）の
// 書く・引く・数える口を持つ（区切り S4b-2）。書く口は、手の実行の書き込み（呼び出し側のトランザクション）の中で
// 使える形（insertSupervisorAction・countSupervisorActions）を正本にし、Store のメソッドはそれを 1 つのトランザクションで包む。
// 子の問いと答え（questions の列）は S4b-3。

// SupervisorBasis は根拠の場所 1 件（path はワークスペースの根からの相対パス、locator は節や行の手がかり）。
type SupervisorBasis struct {
	Path    string `json:"path"`
	Locator string `json:"locator"`
}

// SupervisorCondition は split_followup で回した条件 1 件（number は入力の「回せる条件」の番号、text は回した条件の文）。
type SupervisorCondition struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

// SupervisorAction は対処の記録 1 件。
type SupervisorAction struct {
	ID int64
	// CycleID は対処したサイクルの ID（"Y-<n>"）。
	CycleID     string
	ChallengeID string  // "C-<n>"
	TaskID      *string // "T-<n>"。保留がタスクを指さないときは nil
	// HoldID は対処した保留（hold.id。保留には表示用の ID が無い）。無ければ nil。
	HoldID *int64
	// RunID はスーパーバイザーの判断の実行（"R-<n>"）。起動できたときだけ。
	RunID    *string
	StopKind StopKind
	// Hand は選んだ手。判断できなかったときは nil。
	Hand       *SupervisorHand
	Findings   string
	Note       string
	Basis      []SupervisorBasis
	Conditions []SupervisorCondition // 無ければ nil
	Result     SupervisorActionResult
	// RejectReason は断りの理由。結果が rejected でなければ nil。
	RejectReason *SupervisorRejectReason
	CreatedAt    time.Time
}

// SupervisorActionInput は recordSupervisorAction の入力。ID の文字列は、課題 "C-<n>"・タスク "T-<n>"・run "R-<n>"。
type SupervisorActionInput struct {
	CycleID     string
	ChallengeID string
	TaskID      string // 空は NULL
	HoldID      *int64
	RunID       string // 空は NULL
	StopKind    StopKind
	Hand        SupervisorHand // 空は NULL（判断できなかった）
	Findings    string
	Note        string
	Basis       []SupervisorBasis
	Conditions  []SupervisorCondition // 空は NULL
	Result      SupervisorActionResult
	// RejectReason は結果が rejected のときだけ必須（それ以外は空でなければならない）。
	RejectReason SupervisorRejectReason
}

// SupervisorActionCountQuery は CountSupervisorActions の条件。
type SupervisorActionCountQuery struct {
	ChallengeID string
	// TaskID が空なら、タスクを指さない記録（task_id が NULL）だけを数える。
	TaskID   string
	StopKind StopKind
	// Hands が空でなければ、その手の記録だけを数える。ExcludeHands の手の記録は数えない。
	Hands        []SupervisorHand
	ExcludeHands []SupervisorHand
	// ExecutedOnly が真なら、結果が executed の記録だけを数える。
	ExecutedOnly bool
	// AfterLastOwnerAnswer が真なら、その課題の最後のオーナーの回答（回答した者が supervisor でない回答済みの保留の、最大の回答時刻）
	// 以後の記録だけを数える（時刻が等しい記録は数える）。オーナーの回答が無ければ制限しない。
	AfterLastOwnerAnswer bool
}

// supervisorCountedHandsExcluded は、回数の上限（2 回）の数えから外す手。保留を閉じない手（defer・escalate）。子の問いに答える手（answer）は S4b-3 で足す。
var supervisorCountedHandsExcluded = []SupervisorHand{HandDefer, HandEscalate}

// SupervisorHandledCountQuery は「回数の上限」の数え（同じタスク〔無ければ同じ課題〕の同じ止まり方への、最後のオーナーの回答以後（時刻が等しいものを含む）の、
// 手が見送り・上げる・答えるでなく、結果が executed の対処の記録）の条件を返す。
func SupervisorHandledCountQuery(challengeID, taskID string, kind StopKind) SupervisorActionCountQuery {
	return SupervisorActionCountQuery{
		ChallengeID: challengeID, TaskID: taskID, StopKind: kind,
		ExcludeHands: append([]SupervisorHand(nil), supervisorCountedHandsExcluded...),
		ExecutedOnly: true, AfterLastOwnerAnswer: true,
	}
}

func marshalNullable(v any, empty bool) (any, error) {
	if empty {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func emptyAsNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// insertSupervisorAction は対処の記録を 1 行書き、その ID を返す。入力の誤りは ErrValidation（何も書かない）。
func insertSupervisorAction(ctx context.Context, tx *sql.Tx, at time.Time, in SupervisorActionInput) (int64, error) {
	cid, ok := parseChallengeID(in.ChallengeID)
	if !ok {
		return 0, ErrValidation
	}
	if _, ok := ParseStopKind(string(in.StopKind)); !ok {
		return 0, ErrValidation
	}
	cycleID, ok := parseCycleID(in.CycleID)
	if !ok {
		return 0, ErrValidation
	}
	if in.Hand != "" {
		if _, ok := ParseSupervisorHand(string(in.Hand)); !ok {
			return 0, ErrValidation
		}
	}
	switch in.Result {
	case ActionExecuted, ActionFailed, ActionRejected:
	default:
		return 0, ErrValidation
	}
	if (in.Result == ActionRejected) != (in.RejectReason != "") {
		return 0, fmt.Errorf("%w: reject_reason is required exactly when the result is rejected", ErrValidation)
	}
	if in.RejectReason != "" && in.RejectReason != RejectStateChanged && in.RejectReason != RejectOwnerAnswered {
		return 0, ErrValidation
	}
	var taskID, runID any
	if in.TaskID != "" {
		id, ok := parseTaskID(in.TaskID)
		if !ok {
			return 0, ErrValidation
		}
		taskID = id
	}
	if in.RunID != "" {
		id, ok := parseRunID(in.RunID)
		if !ok {
			return 0, ErrValidation
		}
		runID = id
	}
	for _, c := range in.Conditions {
		if c.Number < 1 {
			return 0, fmt.Errorf("%w: condition numbers start at 1", ErrValidation)
		}
	}
	basis := in.Basis
	if basis == nil {
		basis = []SupervisorBasis{}
	}
	basisJSON, err := json.Marshal(basis)
	if err != nil {
		return 0, err
	}
	conditions, err := marshalNullable(in.Conditions, len(in.Conditions) == 0)
	if err != nil {
		return 0, err
	}
	var holdID any
	if in.HoldID != nil {
		holdID = *in.HoldID
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO supervisor_action (cycle_id, challenge_id, task_id, hold_id, run_id, stop_kind, hand, findings, note, basis, conditions, result, reject_reason, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cycleID, cid, taskID, holdID, runID, string(in.StopKind), emptyAsNull(string(in.Hand)), in.Findings, in.Note, string(basisJSON), conditions,
		string(in.Result), emptyAsNull(string(in.RejectReason)), formatTimestamp(at))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// recordSupervisorAction は対処の記録を 1 件書く（1 つの書き込みのトランザクション）。入力の誤りは ErrValidation。
// 非公開: 結果が executed の記録は回数の上限の数えに入るので、スーパーバイザーの段の外（CLI・サーバー）から書かせない。試験だけが呼ぶ。
func (s *Store) recordSupervisorAction(ctx context.Context, in SupervisorActionInput) (*SupervisorAction, error) {
	var id int64
	err := classifyReadWriteErr(s.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = insertSupervisorAction(ctx, tx, s.currentTime(), in)
		return err
	}))
	if err != nil {
		return nil, err
	}
	var out *SupervisorAction
	err = classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		list, err := querySupervisorActions(ctx, tx, `WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if len(list) != 1 {
			return fmt.Errorf("core: supervisor_action %d vanished", id)
		}
		out = &list[0]
		return nil
	}))
	return out, err
}

const supervisorActionColumns = `id, cycle_id, challenge_id, task_id, hold_id, run_id, stop_kind, hand, findings, note, basis, conditions, result, reject_reason, created_at`

func querySupervisorActions(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]SupervisorAction, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+supervisorActionColumns+` FROM supervisor_action `+where+` ORDER BY id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []SupervisorAction{}
	for rows.Next() {
		var (
			a                           SupervisorAction
			cid, cycleNum               int64
			taskID, holdID, runID       sql.NullInt64
			hand, conditions, rejection sql.NullString
			kind, basis, result, at     string
		)
		if err := rows.Scan(&a.ID, &cycleNum, &cid, &taskID, &holdID, &runID, &kind, &hand, &a.Findings, &a.Note, &basis, &conditions, &result, &rejection, &at); err != nil {
			return nil, err
		}
		a.CycleID = formatCycleID(cycleNum)
		a.ChallengeID = formatChallengeID(cid)
		if taskID.Valid {
			v := formatTaskID(taskID.Int64)
			a.TaskID = &v
		}
		if holdID.Valid {
			v := holdID.Int64
			a.HoldID = &v
		}
		if runID.Valid {
			v := formatRunID(runID.Int64)
			a.RunID = &v
		}
		a.StopKind = StopKind(kind)
		if hand.Valid {
			v := SupervisorHand(hand.String)
			a.Hand = &v
		}
		if err := json.Unmarshal([]byte(basis), &a.Basis); err != nil {
			return nil, fmt.Errorf("supervisor_action.basis is not valid JSON: %w", err)
		}
		if conditions.Valid {
			if err := json.Unmarshal([]byte(conditions.String), &a.Conditions); err != nil {
				return nil, fmt.Errorf("supervisor_action.conditions is not valid JSON: %w", err)
			}
		}
		a.Result = SupervisorActionResult(result)
		if rejection.Valid {
			v := SupervisorRejectReason(rejection.String)
			a.RejectReason = &v
		}
		if a.CreatedAt, err = parseTimestamp(at); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListSupervisorActionsByChallenge は課題の対処の記録を古い順（ID の昇順）に返す。課題が無ければ ErrNotFound。
func (s *Store) ListSupervisorActionsByChallenge(ctx context.Context, challengeID string) ([]SupervisorAction, error) {
	cid, ok := parseChallengeID(challengeID)
	if !ok {
		return nil, ErrNotFound
	}
	var out []SupervisorAction
	err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		if _, err := loadChallenge(ctx, tx, cid); err != nil {
			return err
		}
		var err error
		out, err = querySupervisorActions(ctx, tx, `WHERE challenge_id = ?`, cid)
		return err
	}))
	return out, err
}

// ListSupervisorActionsByTask はタスクの対処の記録を古い順（ID の昇順）に返す。形式の不正なタスクの ID は ErrNotFound。
func (s *Store) ListSupervisorActionsByTask(ctx context.Context, taskID string) ([]SupervisorAction, error) {
	id, ok := parseTaskID(taskID)
	if !ok {
		return nil, ErrNotFound
	}
	var out []SupervisorAction
	err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = querySupervisorActions(ctx, tx, `WHERE task_id = ?`, id)
		return err
	}))
	return out, err
}

// lastOwnerAnswerAt は課題の最後のオーナーの回答の時刻（回答した者が supervisor でない回答済みの保留の、最大の回答時刻）。無ければ ""。
func lastOwnerAnswerAt(ctx context.Context, tx *sql.Tx, challengeID int64) (string, error) {
	var at sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(answered_at) FROM hold WHERE challenge_id = ? AND answered_at IS NOT NULL AND COALESCE(answered_by, '') <> ?`,
		challengeID, supervisorActorName).Scan(&at); err != nil {
		return "", err
	}
	return at.String, nil
}

// supervisorActionWhere は q に当たる対処の記録の WHERE 句（WHERE は含まない）と引数を返す。
func supervisorActionWhere(ctx context.Context, tx *sql.Tx, q SupervisorActionCountQuery) (string, []any, error) {
	cid, ok := parseChallengeID(q.ChallengeID)
	if !ok {
		return "", nil, ErrNotFound
	}
	conds := []string{"challenge_id = ?", "stop_kind = ?"}
	args := []any{cid, string(q.StopKind)}
	if q.TaskID == "" {
		conds = append(conds, "task_id IS NULL")
	} else {
		id, ok := parseTaskID(q.TaskID)
		if !ok {
			return "", nil, ErrNotFound
		}
		conds = append(conds, "task_id = ?")
		args = append(args, id)
	}
	if len(q.Hands) > 0 {
		conds = append(conds, "hand IN ("+placeholders(len(q.Hands))+")")
		for _, h := range q.Hands {
			args = append(args, string(h))
		}
	}
	if len(q.ExcludeHands) > 0 {
		// 手が NULL の記録（判断できなかった）は、除く手に当たらない。
		conds = append(conds, "(hand IS NULL OR hand NOT IN ("+placeholders(len(q.ExcludeHands))+"))")
		for _, h := range q.ExcludeHands {
			args = append(args, string(h))
		}
	}
	if q.ExecutedOnly {
		conds = append(conds, "result = ?")
		args = append(args, string(ActionExecuted))
	}
	if q.AfterLastOwnerAnswer {
		at, err := lastOwnerAnswerAt(ctx, tx, cid)
		if err != nil {
			return "", nil, err
		}
		if at != "" {
			// 時刻が等しいときは順序が決まらないので、数える側に倒す（少なく数えて自動の対処が 1 回余分に通るより、多く数えてオーナーへ上げるほうが安全）。
			conds = append(conds, "created_at >= ?")
			args = append(args, at)
		}
	}
	return strings.Join(conds, " AND "), args, nil
}

// countSupervisorActions は q に当たる対処の記録の件数を返す。
func countSupervisorActions(ctx context.Context, tx *sql.Tx, q SupervisorActionCountQuery) (int, error) {
	where, args, err := supervisorActionWhere(ctx, tx, q)
	if err != nil {
		return 0, err
	}
	var n int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM supervisor_action WHERE `+where, args...).Scan(&n)
	return n, err
}

// supervisorActionHands は q に当たる対処の記録の手を、実行の順（ID の昇順）に返す（手が無い記録は空の名前。数え countSupervisorActions と同じ条件）。
func supervisorActionHands(ctx context.Context, tx *sql.Tx, q SupervisorActionCountQuery) ([]SupervisorHand, error) {
	where, args, err := supervisorActionWhere(ctx, tx, q)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT hand FROM supervisor_action WHERE `+where+` ORDER BY id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SupervisorHand
	for rows.Next() {
		var h sql.NullString
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, SupervisorHand(h.String)) // 手が無い記録（判断できなかった）は空の名前
	}
	return out, rows.Err()
}

// CountSupervisorActions は q に当たる対処の記録の件数を返す（課題・タスクの形式の不正は ErrNotFound）。
func (s *Store) CountSupervisorActions(ctx context.Context, q SupervisorActionCountQuery) (int, error) {
	var n int
	err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = countSupervisorActions(ctx, tx, q)
		return err
	}))
	return n, err
}

// countExecutedSupervisorHands は課題の、結果が executed の対処の記録を、手ごとに数えた値を返す（打った手の件数）。
// failed・rejected の記録と、手が NULL の記録は入れない。無ければ空の写像（nil でない）。
func countExecutedSupervisorHands(ctx context.Context, tx *sql.Tx, challengeID int64) (map[SupervisorHand]int, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT hand, COUNT(*) FROM supervisor_action WHERE challenge_id = ? AND result = ? AND hand IS NOT NULL GROUP BY hand`,
		challengeID, string(ActionExecuted))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[SupervisorHand]int{}
	for rows.Next() {
		var hand string
		var n int
		if err := rows.Scan(&hand, &n); err != nil {
			return nil, err
		}
		out[SupervisorHand(hand)] = n
	}
	return out, rows.Err()
}

package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"time"
)

// NoticeKind は知らせの種類。
type NoticeKind string

// NoticeKind の 4 値。
const (
	NoticeTaskAdded           NoticeKind = "task_added"
	NoticeReleased            NoticeKind = "released"
	NoticeCycleEnded          NoticeKind = "cycle_ended"
	NoticeSupervisorEscalated NoticeKind = "supervisor_escalated"
)

// noticeLimit は知らせの口が返す件数の上限。
const noticeLimit = 20

// NoticeKindValues は NoticeKind の閉集合の全値を返す。
func NoticeKindValues() []NoticeKind {
	return []NoticeKind{NoticeTaskAdded, NoticeReleased, NoticeCycleEnded, NoticeSupervisorEscalated}
}

// Notice は起きたことの知らせ 1 件。文は持たない（画面が種類と値から組み立てる）。種類ごとに使う値は次のとおり。
//   - task_added: ChallengeID・AddedCount・Origin
//   - released: ChallengeID
//   - cycle_ended: CycleID・EndReason
//   - supervisor_escalated: ChallengeID・TaskID（タスクを指さない保留では空）
type Notice struct {
	Kind        NoticeKind
	At          time.Time
	ChallengeID string
	TaskID      string
	AddedCount  int
	Origin      TaskAdditionOrigin
	CycleID     string
	EndReason   CycleEndReason
}

// ListNotices は作業ログとサイクルの記録から知らせを導き、新しい順に直近 20 件までを返す。読むたびに導き、
// ストアは変わらない（会話・発言の表にも書かない。既読は持たない）。
func (s *Store) ListNotices(ctx context.Context) ([]Notice, error) {
	out := []Notice{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		for _, read := range []func(context.Context, *sql.Tx) ([]Notice, error){
			readTaskAddedNotices, readReleasedNotices, readCycleEndedNotices, readEscalatedNotices,
		} {
			part, err := read(ctx, tx)
			if err != nil {
				return err
			}
			out = append(out, part...)
		}
		return nil
	})
	if err != nil {
		return nil, classifyReadWriteErr(err)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > noticeLimit {
		out = out[:noticeLimit]
	}
	return out, nil
}

// readTaskAddedNotices は足す依頼の作業ログの、タスクを足した出来事（action=add_tasks）を新しい順に最大 20 件読む。
func readTaskAddedNotices(ctx context.Context, tx *sql.Tx) ([]Notice, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.at, ta.challenge_id, ta.origin, a.after
		FROM activity a JOIN task_addition ta ON ta.id = a.entity_id
		WHERE a.entity = 'task_addition' AND a.action = 'add_tasks' ORDER BY a.at DESC, a.id DESC LIMIT ?`, noticeLimit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Notice
	for rows.Next() {
		var (
			at, origin string
			cid        int64
			after      sql.NullString
		)
		if err := rows.Scan(&at, &cid, &origin, &after); err != nil {
			return nil, err
		}
		n := Notice{Kind: NoticeTaskAdded, ChallengeID: formatChallengeID(cid), Origin: TaskAdditionOrigin(origin)}
		if n.At, err = parseTimestamp(at); err != nil {
			return nil, err
		}
		var body struct {
			TaskIDs []string `json:"task_ids"`
		}
		if after.Valid {
			if err := json.Unmarshal([]byte(after.String), &body); err != nil {
				return nil, err
			}
		}
		n.AddedCount = len(body.TaskIDs)
		out = append(out, n)
	}
	return out, rows.Err()
}

// readReleasedNotices は反映の操作の実行の作業ログ（entity=operation・op_execute）のうち、後の状態が実行済みのものを
// 新しい順に最大 20 件読む。取り消し・やり直しで同じ出来事の名前が付く記録（後の状態が pending・closed）は含めない。
func readReleasedNotices(ctx context.Context, tx *sql.Tx) ([]Notice, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.at, o.challenge_id
		FROM activity a JOIN operation o ON o.id = a.entity_id
		WHERE a.entity = 'operation' AND a.action = ? AND json_extract(a.after, '$.state') = ?
		ORDER BY a.at DESC, a.id DESC LIMIT ?`, ActivityActionOpExecute, string(OperationStateExecuted), noticeLimit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Notice
	for rows.Next() {
		var (
			at  string
			cid int64
		)
		if err := rows.Scan(&at, &cid); err != nil {
			return nil, err
		}
		n := Notice{Kind: NoticeReleased, ChallengeID: formatChallengeID(cid)}
		if n.At, err = parseTimestamp(at); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// readCycleEndedNotices は終わった理由が入っている終わったサイクルを、新しい順に最大 20 件読む。
// 個別の操作の周と、終わった理由が空の周は含めない。
func readCycleEndedNotices(ctx context.Context, tx *sql.Tx) ([]Notice, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, ended_at, end_reason FROM cycle
		WHERE ended_at IS NOT NULL AND end_reason IS NOT NULL AND end_reason <> '' AND `+notIndividualOpFilter+`
		ORDER BY ended_at DESC, id DESC LIMIT ?`, noticeLimit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Notice
	for rows.Next() {
		var (
			id         int64
			at, reason string
		)
		if err := rows.Scan(&id, &at, &reason); err != nil {
			return nil, err
		}
		n := Notice{Kind: NoticeCycleEnded, CycleID: formatCycleID(id), EndReason: CycleEndReason(reason)}
		if n.At, err = parseTimestamp(at); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// readEscalatedNotices はスーパーバイザーの印が escalated の保留を読む。時刻は、その保留に結び付く対処の記録の最新の時刻
// （無ければ保留が立った時刻）。
func readEscalatedNotices(ctx context.Context, tx *sql.Tx) ([]Notice, error) {
	rows, err := tx.QueryContext(ctx, `SELECT h.challenge_id, h.task_id, COALESCE((SELECT MAX(sa.created_at) FROM supervisor_action sa WHERE sa.hold_id = h.id), h.raised_at) AS at
		FROM hold h WHERE h.supervisor_state = ? ORDER BY at DESC, h.id DESC LIMIT ?`, string(SupervisorStateEscalated), noticeLimit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Notice
	for rows.Next() {
		var (
			cid    int64
			taskID sql.NullInt64
			at     string
		)
		if err := rows.Scan(&cid, &taskID, &at); err != nil {
			return nil, err
		}
		n := Notice{Kind: NoticeSupervisorEscalated, ChallengeID: formatChallengeID(cid)}
		if taskID.Valid {
			n.TaskID = formatTaskID(taskID.Int64)
		}
		if n.At, err = parseTimestamp(at); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

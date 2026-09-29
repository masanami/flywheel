package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// challengeIDPattern は課題 ID の表示形の閉集合（"C-<正の整数>"）。先頭 0 や
// 負・0 は許さない（"C-0"・"C-01" は不一致）。
var challengeIDPattern = regexp.MustCompile(`^C-[1-9][0-9]*$`)

func formatChallengeID(id int64) string { return fmt.Sprintf("C-%d", id) }
func formatOperationID(id int64) string { return fmt.Sprintf("OP-%d", id) }

// parseChallengeID は s を課題の内部整数 ID として解釈する。形式不正
// （閉集合 ^C-[1-9][0-9]*$ に不一致）は ok=false を返す。呼び出し側はこれを
// 「存在しない ID」と同じ扱いにする（ErrNotFound。fail-closed）。
func parseChallengeID(s string) (int64, bool) {
	if !challengeIDPattern.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "C-"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Challenge は課題（§データモデル challenge）。
type Challenge struct {
	ID           string // "C-<n>"
	Title        string
	Description  string
	DoneCriteria string
	Urgency      *Urgency
	Priority     *Priority
	Status       Status
	Version      int
	Reporter     string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Plan は計画の 1 版（§データモデル task_plan）。
type Plan struct {
	Version   int
	Body      string
	CreatedAt time.Time
	// Spec は J2 の構造化した出力の JSON（task_plan.spec。#85）。人が
	// `plan --file`／`--stdin` で登録した計画は nil。
	Spec *string
}

// Approval は承認・差し戻しの 1 件（§データモデル approval）。#9 は書き込まない
// （読み取りだけ実装する）。
type Approval struct {
	Kind          ApprovalKind
	Decision      ApprovalDecision
	OperationID   *string // "OP-<n>"（nil 可）
	TargetVersion int
	Actor         string
	Channel       string
	Verification  string
	Reason        *string
	DecidedAt     time.Time
}

// Hold は保留の 1 件（§データモデル hold）。#9 は書き込まない（読み取りだけ実装する）。
type Hold struct {
	Question   string
	FromStatus Status
	RaisedAt   time.Time
	Answer     *string
	AnsweredAt *time.Time
	AnsweredBy *string
}

// IrreversibleOperation は不可逆操作の 1 件（§データモデル operation。core 内部の
// Operation 型（状態機械の操作トークン。transition.go）と名前が衝突するため
// IrreversibleOperation とする）。#9 は書き込まない（読み取りだけ実装する）。
type IrreversibleOperation struct {
	ID          string // "OP-<n>"
	ChallengeID string // "C-<n>"
	Kind        OperationKind
	Summary     string
	Ref         *string
	State       OperationState
	Version     int
	CreatedAt   time.Time
}

// ChallengeDetail は show が返す課題の全体像（人間記入欄・状態に加え、計画の
// 全版・承認と差し戻しの記録・保留の記録・不可逆操作・GitHub Issue との対応）。
type ChallengeDetail struct {
	Challenge
	Plans      []Plan
	Approvals  []Approval
	Holds      []Hold
	Operations []IrreversibleOperation
	// SourceBinding は課題と上流の GitHub Issue の対応（#56。
	// docs/features/m2-github-issue-ingest.md §IF / API「`show` の拡張」）。
	// 対応が無ければ nil（`create` で作った課題・スキーマ版 1 から上げたストアの
	// 既存の課題。AC-48・AC-103）。
	SourceBinding *SourceBinding
}

// CreateInput は CreateChallenge の入力。
type CreateInput struct {
	Title        string
	Description  string
	DoneCriteria string
	Urgency      *string // nil なら未指定
}

// EditInput は EditChallenge の入力。nil のフィールドは「指定なし（変更しない）」
// を表す。
type EditInput struct {
	Title        *string
	Description  *string
	DoneCriteria *string
	Urgency      *string
}

// ListOptions は ListChallenges の絞り込み条件。
type ListOptions struct {
	Status *string // nil なら絞り込まない
}

const challengeSelectColumns = `SELECT id, title, description, done_criteria, urgency, priority, status, version, reporter, created_at, updated_at FROM challenge`

// rowScanner は *sql.Row と *sql.Rows の両方が満たすインタフェース。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanChallengeRow は 1 行を Challenge へ変換する。行が無ければ（sql.ErrNoRows）
// ErrNotFound を返す。
func scanChallengeRow(scanner rowScanner) (*Challenge, error) {
	var (
		id                                                 int64
		title, description, doneCriteria, status, reporter string
		createdAtStr, updatedAtStr                         string
		urgency, priority                                  sql.NullString
		version                                            int
	)
	if err := scanner.Scan(&id, &title, &description, &doneCriteria, &urgency, &priority, &status, &version, &reporter, &createdAtStr, &updatedAtStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	c := &Challenge{
		ID:           formatChallengeID(id),
		Title:        title,
		Description:  description,
		DoneCriteria: doneCriteria,
		Status:       Status(status),
		Version:      version,
		Reporter:     reporter,
	}
	if urgency.Valid {
		u := Urgency(urgency.String)
		c.Urgency = &u
	}
	if priority.Valid {
		p := Priority(priority.String)
		c.Priority = &p
	}
	createdAt, err := parseTimestamp(createdAtStr)
	if err != nil {
		return nil, err
	}
	updatedAt, err := parseTimestamp(updatedAtStr)
	if err != nil {
		return nil, err
	}
	c.CreatedAt = createdAt
	c.UpdatedAt = updatedAt
	return c, nil
}

// loadChallenge は id の課題を tx から読む。無ければ ErrNotFound。
func loadChallenge(ctx context.Context, tx *sql.Tx, id int64) (*Challenge, error) {
	row := tx.QueryRowContext(ctx, challengeSelectColumns+" WHERE id = ?", id)
	return scanChallengeRow(row)
}

func loadPlans(ctx context.Context, tx *sql.Tx, challengeID int64) ([]Plan, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version, body, created_at, spec FROM task_plan WHERE challenge_id = ? ORDER BY version ASC`, challengeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	plans := make([]Plan, 0)
	for rows.Next() {
		var p Plan
		var createdAtStr string
		var spec sql.NullString
		if err := rows.Scan(&p.Version, &p.Body, &createdAtStr, &spec); err != nil {
			return nil, err
		}
		createdAt, err := parseTimestamp(createdAtStr)
		if err != nil {
			return nil, err
		}
		p.CreatedAt = createdAt
		if spec.Valid {
			v := spec.String
			p.Spec = &v
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return plans, nil
}

func loadApprovals(ctx context.Context, tx *sql.Tx, challengeID int64) ([]Approval, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT operation_id, kind, decision, target_version, actor, channel, verification, reason, decided_at
		 FROM approval WHERE challenge_id = ? ORDER BY id ASC`, challengeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	approvals := make([]Approval, 0)
	for rows.Next() {
		var (
			opID           sql.NullInt64
			kind, decision string
			reason         sql.NullString
			decidedAtStr   string
			a              Approval
		)
		if err := rows.Scan(&opID, &kind, &decision, &a.TargetVersion, &a.Actor, &a.Channel, &a.Verification, &reason, &decidedAtStr); err != nil {
			return nil, err
		}
		a.Kind = ApprovalKind(kind)
		a.Decision = ApprovalDecision(decision)
		if opID.Valid {
			s := formatOperationID(opID.Int64)
			a.OperationID = &s
		}
		if reason.Valid {
			r := reason.String
			a.Reason = &r
		}
		decidedAt, err := parseTimestamp(decidedAtStr)
		if err != nil {
			return nil, err
		}
		a.DecidedAt = decidedAt
		approvals = append(approvals, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return approvals, nil
}

func loadHolds(ctx context.Context, tx *sql.Tx, challengeID int64) ([]Hold, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT question, from_status, raised_at, answer, answered_at, answered_by
		 FROM hold WHERE challenge_id = ? ORDER BY id ASC`, challengeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	holds := make([]Hold, 0)
	for rows.Next() {
		var (
			fromStatus                        string
			raisedAtStr                       string
			answer, answeredAtStr, answeredBy sql.NullString
			h                                 Hold
		)
		if err := rows.Scan(&h.Question, &fromStatus, &raisedAtStr, &answer, &answeredAtStr, &answeredBy); err != nil {
			return nil, err
		}
		h.FromStatus = Status(fromStatus)
		raisedAt, err := parseTimestamp(raisedAtStr)
		if err != nil {
			return nil, err
		}
		h.RaisedAt = raisedAt
		if answer.Valid {
			v := answer.String
			h.Answer = &v
		}
		if answeredAtStr.Valid {
			t, err := parseTimestamp(answeredAtStr.String)
			if err != nil {
				return nil, err
			}
			h.AnsweredAt = &t
		}
		if answeredBy.Valid {
			v := answeredBy.String
			h.AnsweredBy = &v
		}
		holds = append(holds, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return holds, nil
}

// operationSelectColumns は operation テーブルの全カラムを select する共通の頭部。
// loadOperations（1課題に従属する一覧）・listOperationsInState（overview.go。
// ストア全体を状態で絞る一覧）が共有する（self-review 指摘: 以前は行の変換
// （Scan・created_at のパース・Ref の NULL 処理・IrreversibleOperation の組み立て）が
// 2箇所にほぼ同じ内容で複製されており、operation にカラムが増えたとき片方だけ
// 直し忘れるおそれがあった）。
const operationSelectColumns = `SELECT id, challenge_id, kind, summary, ref, state, version, created_at FROM operation`

// scanOperationRow は operationSelectColumns の1行を IrreversibleOperation へ変換する。
func scanOperationRow(scanner rowScanner) (*IrreversibleOperation, error) {
	var (
		id, challengeID int64
		kind, summary   string
		ref             sql.NullString
		state           string
		version         int
		createdAtStr    string
	)
	if err := scanner.Scan(&id, &challengeID, &kind, &summary, &ref, &state, &version, &createdAtStr); err != nil {
		return nil, err
	}
	createdAt, err := parseTimestamp(createdAtStr)
	if err != nil {
		return nil, err
	}
	op := &IrreversibleOperation{
		ID:          formatOperationID(id),
		ChallengeID: formatChallengeID(challengeID),
		Kind:        OperationKind(kind),
		Summary:     summary,
		State:       OperationState(state),
		Version:     version,
		CreatedAt:   createdAt,
	}
	if ref.Valid {
		v := ref.String
		op.Ref = &v
	}
	return op, nil
}

func loadOperations(ctx context.Context, tx *sql.Tx, challengeID int64) ([]IrreversibleOperation, error) {
	rows, err := tx.QueryContext(ctx, operationSelectColumns+` WHERE challenge_id = ? ORDER BY id ASC`, challengeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	ops := make([]IrreversibleOperation, 0)
	for rows.Next() {
		op, err := scanOperationRow(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, *op)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ops, nil
}

// nullableUrgency は u を any 値（string または NULL 相当の nil）にする。
// SQL のバインド値にも、作業ログ before/after の JSON に載せる値にも使う
// （レビュー指摘: 元は同一本体の関数が nullableUrgency／urgencyDiffValue の
// 2つに分かれており、呼び分けの根拠が無い状態で重複していた）。
func nullableUrgency(u *Urgency) any {
	if u == nil {
		return nil
	}
	return string(*u)
}

func urgencyEqual(a, b *Urgency) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// nullablePriority は nullableUrgency と同じ規則で p を any 値にする
// （#10 の classify が優先度を設定するようになったため、SQL のバインド値にも
// 作業ログ before/after の JSON にも使う）。
func nullablePriority(p *Priority) any {
	if p == nil {
		return nil
	}
	return string(*p)
}

// insertChallengeRow は課題を状態 未分類・版 1 で 1 行 INSERT し、割り当てられた
// id と、実際に永続化されたタイムスタンプ（formatTimestamp → parseTimestamp を
// 往復させ常にミリ秒精度に丸めたもの。以後 GetChallenge 等でストアから再読込した
// 値と time.Time.Equal で一致させるため）を返す。CreateChallenge（起票者=actor）と
// Ingest の課題作成（起票者=Issue の作成者の login。#56）が共有する。
func insertChallengeRow(ctx context.Context, tx *sql.Tx, at time.Time, title, description, doneCriteria string, urgency *Urgency, reporter string) (id int64, now time.Time, err error) {
	nowStr := formatTimestamp(at)
	now, err = parseTimestamp(nowStr)
	if err != nil {
		return 0, time.Time{}, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO challenge (title, description, done_criteria, urgency, status, version, reporter, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		title, description, doneCriteria, nullableUrgency(urgency), string(StatusUnclassified), reporter, nowStr, nowStr,
	)
	if err != nil {
		return 0, time.Time{}, err
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, time.Time{}, err
	}
	return id, now, nil
}

// createdChallengeAfter は課題を作ったときの作業ログの after（人間記入欄＋
// 状態 未分類＋起票者の 6 キー。M1 の create の after と同じ形で version は
// 載せない）を組み立てる。CreateChallenge の create と Ingest の ingest_create
// が共有する作成専用の形で、更新（edit・ingest_update）の after には使わない。
func createdChallengeAfter(title, description, doneCriteria string, urgency *Urgency, reporter string) map[string]any {
	return map[string]any{
		"title":         title,
		"description":   description,
		"done_criteria": doneCriteria,
		"urgency":       nullableUrgency(urgency),
		"status":        string(StatusUnclassified),
		"reporter":      reporter,
	}
}

// CreateChallenge は課題を状態 unclassified で作る（T1）。タイトルは
// strings.TrimSpace が空なら ErrValidation（値はそのまま保存し正規化しない）。
// 緊急度は指定時のみ閉集合を検査する。起票者（reporter）は actor と同じ値。
func (s *Store) CreateChallenge(ctx context.Context, ch Channel, in CreateInput) (*Challenge, error) {
	if strings.TrimSpace(in.Title) == "" {
		return nil, ErrValidation
	}

	var urgency *Urgency
	if in.Urgency != nil {
		u, ok := ParseUrgency(*in.Urgency)
		if !ok {
			return nil, ErrValidation
		}
		urgency = &u
	}

	var result *Challenge
	err := s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		id, now, err := insertChallengeRow(ctx, tx, rec.at, in.Title, in.Description, in.DoneCriteria, urgency, rec.actor)
		if err != nil {
			return err
		}

		after := createdChallengeAfter(in.Title, in.Description, in.DoneCriteria, urgency, rec.actor)
		if err := rec.record("challenge", id, "create", nil, after); err != nil {
			return err
		}

		result = &Challenge{
			ID:           formatChallengeID(id),
			Title:        in.Title,
			Description:  in.Description,
			DoneCriteria: in.DoneCriteria,
			Urgency:      urgency,
			Status:       StatusUnclassified,
			Version:      1,
			Reporter:     rec.actor,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// GetChallenge は課題の全体像（人間記入欄・状態・計画の全版・承認と差し戻しの
// 記録・保留の記録・不可逆操作・GitHub Issue との対応）を返す。id の形式が不正、または存在しなければ
// ErrNotFound。
func (s *Store) GetChallenge(ctx context.Context, id string) (*ChallengeDetail, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}

	var detail ChallengeDetail
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		c, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		detail.Challenge = *c

		plans, err := loadPlans(ctx, tx, cid)
		if err != nil {
			return err
		}
		detail.Plans = plans

		approvals, err := loadApprovals(ctx, tx, cid)
		if err != nil {
			return err
		}
		detail.Approvals = approvals

		holds, err := loadHolds(ctx, tx, cid)
		if err != nil {
			return err
		}
		detail.Holds = holds

		operations, err := loadOperations(ctx, tx, cid)
		if err != nil {
			return err
		}
		detail.Operations = operations

		sb, err := loadSourceBindingByChallengeID(ctx, tx, cid)
		if err != nil {
			return err
		}
		detail.SourceBinding = sb.toPublic()
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &detail, nil
}

// ListChallenges は課題の一覧を id 昇順で返す。opt.Status が指定されていれば、
// その状態（コードまたは表示名。ParseStatus と同じ規則）の課題だけに絞る。
// 閉集合外の値は ErrValidation。
func (s *Store) ListChallenges(ctx context.Context, opt ListOptions) ([]Challenge, error) {
	var status *Status
	if opt.Status != nil {
		st, ok := ParseStatus(*opt.Status)
		if !ok {
			return nil, ErrValidation
		}
		status = &st
	}

	var result []Challenge
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		query := challengeSelectColumns
		var args []any
		if status != nil {
			query += " WHERE status = ?"
			args = append(args, string(*status))
		}
		query += " ORDER BY id ASC"

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			c, err := scanChallengeRow(rows)
			if err != nil {
				return err
			}
			result = append(result, *c)
		}
		return rows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if result == nil {
		result = []Challenge{}
	}
	return result, nil
}

// EditChallenge は人間記入欄（タイトル・説明・完了条件・緊急度）を変更する。
// 判定順は 入力の検証（ErrValidation）→ 存在（ErrNotFound）→ done なら
// ErrTerminalState → 差分計算。差分が無ければ何も書かず（version・updated_at・
// 作業ログを変えず）現在値を返す。差分があれば version を 1 増やし、
// UPDATE … WHERE id=? AND version=? の楽観ロックで書き込む。
func (s *Store) EditChallenge(ctx context.Context, ch Channel, id string, in EditInput) (*Challenge, error) {
	// タイトルは必須（§課題の起票・参照・編集）。create と同じ規則（trim して空なら
	// ErrValidation。値そのものは正規化しない）を edit にも適用する。AC の文言は
	// create を対象に書かれているが、「タイトルは必須」は課題の人間記入欄全体に
	// かかる不変条件であり、edit だけこれを回避できると create の検証と矛盾する
	// （レビュー指摘。空タイトルの課題が生まれてしまう）。
	if in.Title != nil && strings.TrimSpace(*in.Title) == "" {
		return nil, ErrValidation
	}

	var newUrgency *Urgency
	if in.Urgency != nil {
		u, ok := ParseUrgency(*in.Urgency)
		if !ok {
			return nil, ErrValidation
		}
		newUrgency = &u
	}

	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}

	var result *Challenge
	err := s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		// 終端状態の判定は遷移表（transition.go の IsTerminal）に委ねる。
		// StatusDone を直書きすると、終端状態が増えたとき（H6「取り下げ」＝S2 予定）に
		// edit の拒否だけが遷移表の更新から取り残される（レビュー指摘）。
		if IsTerminal(Table, StatusVocabulary, current.Status) {
			return ErrTerminalState
		}

		before := map[string]any{}
		after := map[string]any{}

		newTitle := current.Title
		if in.Title != nil && *in.Title != current.Title {
			before["title"] = current.Title
			after["title"] = *in.Title
			newTitle = *in.Title
		}
		newDescription := current.Description
		if in.Description != nil && *in.Description != current.Description {
			before["description"] = current.Description
			after["description"] = *in.Description
			newDescription = *in.Description
		}
		newDoneCriteria := current.DoneCriteria
		if in.DoneCriteria != nil && *in.DoneCriteria != current.DoneCriteria {
			before["done_criteria"] = current.DoneCriteria
			after["done_criteria"] = *in.DoneCriteria
			newDoneCriteria = *in.DoneCriteria
		}
		newUrgencyValue := current.Urgency
		if in.Urgency != nil && !urgencyEqual(current.Urgency, newUrgency) {
			before["urgency"] = nullableUrgency(current.Urgency)
			after["urgency"] = nullableUrgency(newUrgency)
			newUrgencyValue = newUrgency
		}

		if len(after) == 0 {
			result = current
			return nil
		}

		newVersion := current.Version + 1
		nowStr := formatTimestamp(rec.at)
		now, err := parseTimestamp(nowStr)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE challenge SET title = ?, description = ?, done_criteria = ?, urgency = ?, version = ?, updated_at = ?
			 WHERE id = ? AND version = ?`,
			newTitle, newDescription, newDoneCriteria, nullableUrgency(newUrgencyValue), newVersion, nowStr, cid, current.Version,
		)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("core: edit challenge %s: expected to update 1 row, updated %d", id, affected)
		}

		after["version"] = newVersion
		if err := rec.record("challenge", cid, "edit", before, after); err != nil {
			return err
		}

		result = &Challenge{
			ID:           formatChallengeID(cid),
			Title:        newTitle,
			Description:  newDescription,
			DoneCriteria: newDoneCriteria,
			Urgency:      newUrgencyValue,
			Priority:     current.Priority,
			Status:       current.Status,
			Version:      newVersion,
			Reporter:     current.Reporter,
			CreatedAt:    current.CreatedAt,
			UpdatedAt:    now,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

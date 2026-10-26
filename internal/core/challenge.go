package core

import (
	"context"
	"database/sql"
	"encoding/json"
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
	// StageFields は「一言で」「位置づけ」と段階の欄（S4。NULL は nil）。
	StageFields
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
	// PlanVersion は計画の承認が指す計画の版（task_plan.version）。計画の承認以外・計画の版を
	// 記録しない既存の承認の行は nil。TargetVersion（課題の版）とは別の値。
	PlanVersion  *int
	Actor        string
	Channel      string
	Verification string
	Reason       *string
	DecidedAt    time.Time
	// RelaySession・RelayQuote は方式 relayed の記録の会話の手がかり（M4P24）。
	// relayed 以外は nil。
	RelaySession *string
	RelayQuote   *string
	// RejectedFields は課題の差し戻しでずれた欄（閉集合の順）。指定の無い差し戻しと承認は nil。
	RejectedFields []string
}

// Hold は保留の 1 件（§データモデル hold）。#9 は書き込まない（読み取りだけ実装する）。
type Hold struct {
	Question   string
	FromStatus Status
	RaisedAt   time.Time
	Answer     *string
	AnsweredAt *time.Time
	AnsweredBy *string
	RunID      *string // 保留の原因の run の ID（"R-<n>"）。無ければ nil
	// AnsweredChannel・AnsweredVerification は回答の経路と本人確認の方式。未回答・
	// 記録の無い既存の行は nil。RelaySession・RelayQuote は方式 relayed の回答の
	// 会話の手がかり（M4P24）。relayed 以外は nil。
	AnsweredChannel      *string
	AnsweredVerification *string
	RelaySession         *string
	RelayQuote           *string
	// ClosedAt は保留を回答なしで閉じた日時（課題の取り下げ。M45H29）。閉じていなければ nil。
	// Answer が nil でも ClosedAt が非 nil の保留は、開いている保留ではない。
	ClosedAt *time.Time
}

// Open は保留がまだ開いている（回答も閉じる操作も無い）かを返す。
func (h Hold) Open() bool { return h.Answer == nil && h.ClosedAt == nil }

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

	// 以下は実行の表示用（docs/features/m4.5-learning-and-self-improvement.md S1
	// 「show・status の不可逆操作の要素に足すキー」）。読み取りの経路
	// （loadOperations・listOperationsInState）が埋める。それ以外の経路の戻り値では
	// 既定値（nil・0）のままである。

	// ApprovedHead は最新の承認の記録の head_sha。承認が無い・head が無ければ nil。
	ApprovedHead *string
	// ExecutedAt は実行済みにした日時。実行済みでなければ nil。
	ExecutedAt *time.Time
	// MergeCommit はマージのコミット SHA。無ければ nil。
	MergeCommit *string
	// LastExecutionResult は直前の試行（id が最大の行）の結果。試行が無い・終了していなければ nil。
	LastExecutionResult *ExecutionResult
	// ConsecutiveFailures は連続の回数（consecutiveFailures の導出）。
	ConsecutiveFailures int
	// WaitingSince は最新の承認の後の最初の試行の開始日時。承認済み（approved）の間だけ値を持つ。
	WaitingSince *time.Time
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
	// Runs は課題の run の一覧（新しい順・最大 20 件。#86。docs/features/
	// m3-invoker-delegation.md §観測「show は、課題の run の一覧…を示す」）。
	// 無ければ空スライス（nil でない）。
	Runs []Run
	// IssuesAnswer は IssuesHoldID の保留の回答。論点が無い・未回答なら nil。
	IssuesAnswer *string
	// Dependencies は課題の依存先（依存先の ID の昇順。S2）、Dependents はこの課題に依存する課題の ID（昇順）。
	// 無ければ空スライス（nil でない）。
	Dependencies []DependencyView
	Dependents   []string
	// FollowupOf はJ6 のフォローアップで起票した課題の元の課題の ID（challenge.followup_of。S3。applyJ6 が設定）。無ければ nil。
	FollowupOf *string
	// Improvement は改修課題（origin=improvement）の改修の鍵。改修課題でなければ nil。
	Improvement *ImprovementKey
}

// ImprovementKey は改修課題の鍵（challenge.improvement_layer・improvement_target。マイグレーション 0012）。
type ImprovementKey struct {
	Layer  string
	Target string
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
	// Headline・Context・Figure は文字列、Issues・Expected・Actual・NonGoals は JSON の配列の
	// 文字列（欄の形は stages.go）。空文字は NULL にする。
	Headline *string
	Context  *string
	Issues   *string
	Expected *string
	Actual   *string
	NonGoals *string
	Figure   *string
}

// ListOptions は ListChallenges の絞り込み条件。
type ListOptions struct {
	Status *string // nil なら絞り込まない
}

const challengeSelectColumns = `SELECT id, title, description, done_criteria, urgency, priority, status, version, reporter, created_at, updated_at, headline, context, issues, issues_hold_id, expected, actual, non_goals, figure FROM challenge`

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
		stages                                             stageScan
	)
	dests := append([]any{&id, &title, &description, &doneCriteria, &urgency, &priority, &status, &version, &reporter, &createdAtStr, &updatedAtStr}, stages.dests()...)
	if err := scanner.Scan(dests...); err != nil {
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
	sf, err := stages.fields()
	if err != nil {
		return nil, err
	}
	c.StageFields = sf
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
		`SELECT operation_id, kind, decision, target_version, plan_version, actor, channel, verification, reason, decided_at, relay_session, relay_quote, rejected_fields
		 FROM approval WHERE challenge_id = ? ORDER BY id ASC`, challengeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	approvals := make([]Approval, 0)
	for rows.Next() {
		var (
			opID           sql.NullInt64
			planVersion    sql.NullInt64
			kind, decision string
			reason         sql.NullString
			relaySession   sql.NullString
			relayQuote     sql.NullString
			rejectedFields sql.NullString
			decidedAtStr   string
			a              Approval
		)
		if err := rows.Scan(&opID, &kind, &decision, &a.TargetVersion, &planVersion, &a.Actor, &a.Channel, &a.Verification, &reason, &decidedAtStr, &relaySession, &relayQuote, &rejectedFields); err != nil {
			return nil, err
		}
		a.Kind = ApprovalKind(kind)
		a.Decision = ApprovalDecision(decision)
		if opID.Valid {
			s := formatOperationID(opID.Int64)
			a.OperationID = &s
		}
		if planVersion.Valid {
			v := int(planVersion.Int64)
			a.PlanVersion = &v
		}
		if reason.Valid {
			r := reason.String
			a.Reason = &r
		}
		a.RelaySession = nullStr(relaySession)
		a.RelayQuote = nullStr(relayQuote)
		if rejectedFields.Valid {
			if err := json.Unmarshal([]byte(rejectedFields.String), &a.RejectedFields); err != nil {
				return nil, fmt.Errorf("core: decode approval.rejected_fields: %w", err)
			}
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
		`SELECT question, from_status, raised_at, answer, answered_at, answered_by, run_id,
		        answered_channel, answered_verification, relay_session, relay_quote, closed_at
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
			runID                             sql.NullInt64
			ansChannel, ansVerif, relSess     sql.NullString
			relQuote, closedAtStr             sql.NullString
			h                                 Hold
		)
		if err := rows.Scan(&h.Question, &fromStatus, &raisedAtStr, &answer, &answeredAtStr, &answeredBy, &runID, &ansChannel, &ansVerif, &relSess, &relQuote, &closedAtStr); err != nil {
			return nil, err
		}
		if runID.Valid {
			v := formatRunID(runID.Int64)
			h.RunID = &v
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
		h.AnsweredChannel = nullStr(ansChannel)
		h.AnsweredVerification = nullStr(ansVerif)
		h.RelaySession = nullStr(relSess)
		h.RelayQuote = nullStr(relQuote)
		if closedAtStr.Valid {
			t, err := parseTimestamp(closedAtStr.String)
			if err != nil {
				return nil, err
			}
			h.ClosedAt = &t
		}
		holds = append(holds, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return holds, nil
}

// loadIssuesAnswer は論点を出した保留（holdID）の回答の文を返す。参照が無い・未回答・
// 保留が見つからなければ nil（回答の正本は hold.answer）。
func loadIssuesAnswer(ctx context.Context, tx *sql.Tx, challengeID int64, holdID *int64) (*string, error) {
	if holdID == nil {
		return nil, nil
	}
	var answer sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT answer FROM hold WHERE id = ? AND challenge_id = ?`, *holdID, challengeID).Scan(&answer)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return nullStr(answer), nil
}

// operationSelectColumns は operation テーブルの全カラムを select する共通の頭部。
// loadOperations（1課題に従属する一覧）・listOperationsInState（overview.go。
// ストア全体を状態で絞る一覧）が共有する（self-review 指摘: 以前は行の変換
// （Scan・created_at のパース・Ref の NULL 処理・IrreversibleOperation の組み立て）が
// 2箇所にほぼ同じ内容で複製されており、operation にカラムが増えたとき片方だけ
// 直し忘れるおそれがあった）。
const operationSelectColumns = `SELECT id, challenge_id, kind, summary, ref, state, version, created_at, executed_at, merge_commit FROM operation`

// scanOperationRow は operationSelectColumns の1行を IrreversibleOperation へ変換する。
func scanOperationRow(scanner rowScanner) (*IrreversibleOperation, error) {
	var (
		id, challengeID int64
		kind, summary   string
		ref             sql.NullString
		state           string
		version         int
		createdAtStr    string
		executedAtStr   sql.NullString
		mergeCommit     sql.NullString
	)
	if err := scanner.Scan(&id, &challengeID, &kind, &summary, &ref, &state, &version, &createdAtStr, &executedAtStr, &mergeCommit); err != nil {
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
	if executedAtStr.Valid {
		t, err := parseTimestamp(executedAtStr.String)
		if err != nil {
			return nil, err
		}
		op.ExecutedAt = &t
	}
	if mergeCommit.Valid {
		v := mergeCommit.String
		op.MergeCommit = &v
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
	if err := fillOperationExecutionInfo(ctx, tx, ops); err != nil {
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
//
// origin は課題の出どころ（ChallengeOrigin*）で必須。followupOf は元の課題の ID
// （"C-n"）で、フォローアップの課題だけが値を持つ（nil なら NULL）。
func insertChallengeRow(ctx context.Context, tx *sql.Tx, at time.Time, title, description, doneCriteria string, urgency *Urgency, reporter string, origin ChallengeOrigin, followupOf *string, improvement *ImprovementKey) (id int64, now time.Time, err error) {
	origin, ok := ParseChallengeOrigin(string(origin))
	if !ok {
		return 0, time.Time{}, fmt.Errorf("insert challenge: invalid origin %q", origin)
	}
	if (origin == ChallengeOriginFollowup) != (followupOf != nil) {
		return 0, time.Time{}, fmt.Errorf("insert challenge: followup_of must be set exactly for origin followup")
	}
	if (origin == ChallengeOriginImprovement) != (improvement != nil) {
		return 0, time.Time{}, fmt.Errorf("insert challenge: improvement key must be set exactly for origin improvement: %w", ErrValidation)
	}
	if improvement != nil && (strings.TrimSpace(improvement.Layer) == "" || strings.TrimSpace(improvement.Target) == "") {
		return 0, time.Time{}, fmt.Errorf("insert challenge: improvement layer and target must not be empty: %w", ErrValidation)
	}
	var impLayer, impTarget any
	if improvement != nil {
		impLayer, impTarget = improvement.Layer, improvement.Target
	}
	nowStr := formatTimestamp(at)
	now, err = parseTimestamp(nowStr)
	if err != nil {
		return 0, time.Time{}, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO challenge (title, description, done_criteria, urgency, status, version, reporter, created_at, updated_at, origin, followup_of, improvement_layer, improvement_target)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)`,
		title, description, doneCriteria, nullableUrgency(urgency), string(StatusUnclassified), reporter, nowStr, nowStr, string(origin), nullableString(followupOf), impLayer, impTarget,
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
		id, now, err := insertChallengeRow(ctx, tx, rec.at, in.Title, in.Description, in.DoneCriteria, urgency, rec.actor, ChallengeOriginManual, nil, nil)
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
// 記録・保留の記録・不可逆操作・GitHub Issue との対応・run の一覧〈新しい順・最大 20 件〉）を返す。
// id の形式が不正、または存在しなければ ErrNotFound。
//
// run の一覧は ListRuns と同じく、先に中断した run を interrupted で閉じる（書き込みを
// 伴いうる。§invoker の共通の規則「次にストアを開いた flywheel のコマンドがその run を
// interrupted で終了させる」）。課題の詳細と run の一覧は別々のトランザクションで読む
// （同じ時点の値である保証は無い）。
func (s *Store) GetChallenge(ctx context.Context, id string) (*ChallengeDetail, error) {
	return s.getChallenge(ctx, id, true)
}

// GetChallengeWithoutReap は GetChallenge と同じ形を返すが、run の一覧を読む前に
// 中断した run を回収しない（閲覧だけの経路。M4P12）。
func (s *Store) GetChallengeWithoutReap(ctx context.Context, id string) (*ChallengeDetail, error) {
	return s.getChallenge(ctx, id, false)
}

func (s *Store) getChallenge(ctx context.Context, id string, reap bool) (*ChallengeDetail, error) {
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

		answer, err := loadIssuesAnswer(ctx, tx, cid, c.IssuesHoldID)
		if err != nil {
			return err
		}
		detail.IssuesAnswer = answer

		eval, err := evaluateDependencies(ctx, tx)
		if err != nil {
			return err
		}
		detail.Dependencies, err = dependencyViews(ctx, tx, eval, cid)
		if err != nil {
			return err
		}
		detail.Dependents, err = loadDependents(ctx, tx, cid)
		if err != nil {
			return err
		}
		var followupOf, impLayer, impTarget sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT followup_of, improvement_layer, improvement_target FROM challenge WHERE id = ?`, cid).Scan(&followupOf, &impLayer, &impTarget); err != nil {
			return err
		}
		if impLayer.Valid && impTarget.Valid {
			detail.Improvement = &ImprovementKey{Layer: impLayer.String, Target: impTarget.String}
		}
		if followupOf.Valid {
			v := followupOf.String
			detail.FollowupOf = &v
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	// run の一覧は、ListRuns と同じく先に中断した run を回収してから読む
	// （`runs` と `show` で同じ run が同じ結果に見える）。
	list := s.ListRunsWithoutReap
	if reap {
		list = s.ListRuns
	}
	runs, err := list(ctx, RunListOptions{ChallengeID: &id, Limit: showRunsLimit})
	if err != nil {
		return nil, err
	}
	detail.Runs = runs
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

	patch, err := in.stagePatch()
	if err != nil {
		return nil, ErrValidation
	}

	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}

	var result *Challenge
	err = s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
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

		newStage := current.applyPatch(patch, before, after)

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
		stageArgs, err := newStage.args()
		if err != nil {
			return err
		}
		args := append([]any{newTitle, newDescription, newDoneCriteria, nullableUrgency(newUrgencyValue), newVersion, nowStr}, stageArgs...)
		args = append(args, cid, current.Version)
		res, err := tx.ExecContext(ctx,
			`UPDATE challenge SET title = ?, description = ?, done_criteria = ?, urgency = ?, version = ?, updated_at = ?, `+stageUpdateSQL+`
			 WHERE id = ? AND version = ?`,
			args...,
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
			StageFields:  newStage,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// stagePatch は EditInput の段階の欄を検査して StagePatch にする。形に合わない値は
// ErrValidation を包んだエラー。nil の欄は変えない、空文字は NULL。
func (in EditInput) stagePatch() (StagePatch, error) {
	var p StagePatch
	if in.Headline != nil {
		v, err := NormalizeEditText(*in.Headline, CheckHeadline)
		if err != nil {
			return p, err
		}
		p.Headline = StageSet(v)
	}
	if in.Context != nil {
		v, err := NormalizeEditText(*in.Context, CheckContext)
		if err != nil {
			return p, err
		}
		p.Context = StageSet(v)
	}
	if in.Figure != nil {
		v, err := NormalizeEditText(*in.Figure, CheckFigure)
		if err != nil {
			return p, err
		}
		p.Figure = StageSet(v)
	}
	if in.Issues != nil {
		v, err := parseEditJSON(*in.Issues, ParseIssuesJSON)
		if err != nil {
			return p, err
		}
		p.Issues = StageSet(v)
	}
	if in.Expected != nil {
		v, err := parseEditJSON(*in.Expected, ParseExpectedJSON)
		if err != nil {
			return p, err
		}
		p.Expected = StageSet(v)
	}
	if in.Actual != nil {
		v, err := parseEditJSON(*in.Actual, ParseActualJSON)
		if err != nil {
			return p, err
		}
		p.Actual = StageSet(v)
	}
	if in.NonGoals != nil {
		v, err := parseEditJSON(*in.NonGoals, ParseNonGoalsJSON)
		if err != nil {
			return p, err
		}
		p.NonGoals = StageSet(v)
	}
	return p, nil
}

// parseEditJSON は edit の JSON の欄を解釈する。空文字は NULL（nil）。
func parseEditJSON[T any](text string, parse func(string) ([]T, error)) ([]T, error) {
	if text == "" {
		return nil, nil
	}
	return parse(text)
}

// nullStr は NULL 可の文字列列を *string へ写す（NULL は nil）。
func nullStr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

// nullableString は s が nil なら SQL の NULL、そうでなければ値を返す。
func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

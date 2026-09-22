// このファイルは不可逆操作（operation）の登録 `op add` と、単独の承認・
// 差し戻し（`approve <OP-ID>` / `reject <OP-ID> --reason`）の公開 API を持つ
// （Issue #13）。完了の承認（T13）が未承認の release を同じトランザクションで
// 一括承認する D12 のロジックは internal/core/approval.go に置く（承認の
// 種類ごとの分岐は approve <C-ID> の①②の枠組みに属するため）。
//
// 単独の承認・差し戻しも、本人確認つきの操作の2段階 API（① Prepare*＝読み取り、
// ② Execute*＝書き込み）に従う。① と ② の間に対象（不可逆操作の版）が変われば
// ② が ErrConflict を返す。

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// operationIDPattern は不可逆操作 ID の表示形の閉集合（"OP-<正の整数>"）。
// challengeIDPattern（challenge.go）と同じ規則（先頭 0・負・0 は不一致）。
var operationIDPattern = regexp.MustCompile(`^OP-[1-9][0-9]*$`)

// parseOperationID は s を不可逆操作の内部整数 ID として解釈する。形式不正は
// ok=false を返す（呼び出し側はこれを「存在しない ID」と同じ扱いにする。
// fail-closed）。
func parseOperationID(s string) (int64, bool) {
	if !operationIDPattern.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "OP-"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// OperationInput は CreateOperation の入力。
type OperationInput struct {
	ChallengeID string
	Kind        string
	Summary     string
	Ref         *string
}

func nullableRef(r *string) any {
	if r == nil {
		return nil
	}
	return *r
}

// CreateOperation は不可逆操作を未承認（state="pending"）の状態で課題に登録する
// （`op add`）。Kind が閉集合外、または Summary が strings.TrimSpace で空なら
// ErrValidation（トランザクションを開く前に判定）。課題が存在しなければ
// ErrNotFound、完了していれば ErrTerminalState。登録は課題の版を1増やす
// （親要件チケット #4 §アーキテクチャ決定: 「未承認の不可逆操作の集合の変化を
// 課題の版の比較で検出するため」）。
func (s *Store) CreateOperation(ctx context.Context, ch Channel, in OperationInput) (*IrreversibleOperation, error) {
	kind, ok := ParseOperationKind(in.Kind)
	if !ok {
		return nil, ErrValidation
	}
	if strings.TrimSpace(in.Summary) == "" {
		return nil, ErrValidation
	}
	cid, ok := parseChallengeID(in.ChallengeID)
	if !ok {
		return nil, ErrNotFound
	}

	var result *IrreversibleOperation
	err := s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if IsTerminal(Table, StatusVocabulary, current.Status) {
			return ErrTerminalState
		}

		nowStr := formatTimestamp(rec.at)
		now, err := parseTimestamp(nowStr)
		if err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx,
			`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at) VALUES (?, ?, ?, ?, ?, 1, ?)`,
			cid, string(kind), in.Summary, nullableRef(in.Ref), string(OperationStatePending), nowStr,
		)
		if err != nil {
			return err
		}
		opID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		newVersion := current.Version + 1
		cres, err := tx.ExecContext(ctx,
			`UPDATE challenge SET version = ?, updated_at = ? WHERE id = ? AND version = ?`,
			newVersion, nowStr, cid, current.Version,
		)
		if err != nil {
			return err
		}
		affected, err := cres.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("core: op add for challenge %s: expected to bump 1 row, updated %d", in.ChallengeID, affected)
		}

		after := map[string]any{
			"operation_id": formatOperationID(opID),
			"kind":         string(kind),
			"summary":      in.Summary,
			"ref":          nullableRef(in.Ref),
			"state":        string(OperationStatePending),
		}
		if err := rec.record("operation", opID, "op_add", nil, after); err != nil {
			return err
		}

		result = &IrreversibleOperation{
			ID:          formatOperationID(opID),
			ChallengeID: current.ID,
			Kind:        kind,
			Summary:     in.Summary,
			Ref:         in.Ref,
			State:       OperationStatePending,
			Version:     1,
			CreatedAt:   now,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// OperationApprovalPreview は PrepareOperationApproval・PrepareOperationRejection
// が返す、不可逆操作の単独の承認・差し戻しの①（読み取り）の結果。要約テキストの
// 整形は呼び出し側（internal/cli/approval.go）が行う。
type OperationApprovalPreview struct {
	OperationID     string
	ChallengeID     string
	ChallengeTitle  string
	ChallengeStatus Status
	Kind            OperationKind
	Summary         string
	Ref             *string
	Version         int // 表示時点の不可逆操作の版（Execute* の ExpectedVersion にそのまま渡す）
	// ChallengeVersion は表示時点の課題の版（Execute* の
	// ExpectedChallengeVersion にそのまま渡す）。要約は課題の ID・タイトル・
	// 状態も見せるため、①②の間に課題が変われば ② を ErrConflict にする
	// （self-review 指摘。不可逆操作の版だけでは、要約に出した課題側の変化
	// ── T15 の差し戻しや edit ── を検出できず、人間が見た前提が崩れたまま
	// 本番反映の承認が成立しうる）。`op add`・不可逆操作の承認・差し戻しが
	// 課題の版を 1 増やす設計（親 #4 §アーキテクチャ決定）とも整合する。
	ChallengeVersion int
}

// operationRow は operation テーブルの1行分の内部表現（読み取り専用の走査に使う）。
type operationRow struct {
	ChallengeID int64
	Kind        string
	Summary     string
	Ref         sql.NullString
	State       OperationState
	Version     int
	CreatedAt   string // formatTimestamp 済みの文字列のまま保持し、呼び出し側で parseTimestamp する
}

func loadOperationRow(ctx context.Context, tx *sql.Tx, id int64) (*operationRow, error) {
	var row operationRow
	err := tx.QueryRowContext(ctx,
		`SELECT challenge_id, kind, summary, ref, state, version, created_at FROM operation WHERE id = ?`, id,
	).Scan(&row.ChallengeID, &row.Kind, &row.Summary, &row.Ref, &row.State, &row.Version, &row.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

// prepareOperationApproval は PrepareOperationApproval・PrepareOperationRejection が
// 共有する読み取り。不可逆操作が存在しなければ ErrNotFound。既に承認・差し戻し済み
// （state != "pending"）なら ErrInvalidTransition（fail-closed。二重の承認・差し戻しを
// 防ぐ）。
func (s *Store) prepareOperationApproval(ctx context.Context, id string) (*OperationApprovalPreview, error) {
	oid, ok := parseOperationID(id)
	if !ok {
		return nil, ErrNotFound
	}

	var preview OperationApprovalPreview
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		row, err := loadOperationRow(ctx, tx, oid)
		if err != nil {
			return err
		}
		if row.State != OperationStatePending {
			return ErrInvalidTransition
		}
		challenge, err := loadChallenge(ctx, tx, row.ChallengeID)
		if err != nil {
			return err
		}

		preview = OperationApprovalPreview{
			OperationID:      formatOperationID(oid),
			ChallengeID:      challenge.ID,
			ChallengeTitle:   challenge.Title,
			ChallengeStatus:  challenge.Status,
			Kind:             OperationKind(row.Kind),
			Summary:          row.Summary,
			Version:          row.Version,
			ChallengeVersion: challenge.Version,
		}
		if row.Ref.Valid {
			v := row.Ref.String
			preview.Ref = &v
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &preview, nil
}

// PrepareOperationApproval は `approve <OP-ID>` の①（読み取り）。
func (s *Store) PrepareOperationApproval(ctx context.Context, id string) (*OperationApprovalPreview, error) {
	return s.prepareOperationApproval(ctx, id)
}

// PrepareOperationRejection は `reject <OP-ID> --reason <r>` の①（読み取り）。
// reason が strings.TrimSpace で空なら、端末を開く前に ErrValidation で拒否する
// （PrepareRejection と同じ規律）。
func (s *Store) PrepareOperationRejection(ctx context.Context, id string, reason string) (*OperationApprovalPreview, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, ErrValidation
	}
	return s.prepareOperationApproval(ctx, id)
}

// OperationApprovalRequest は ExecuteOperationApproval の入力（②＝書き込み）。
// Decision が ApprovalDecisionRejected のときだけ Reason が必須。
type OperationApprovalRequest struct {
	OperationID string
	// ExpectedVersion・ExpectedChallengeVersion は①が見せた時点の
	// 不可逆操作の版・課題の版。どちらかが変わっていれば ErrConflict。
	ExpectedVersion          int
	ExpectedChallengeVersion int
	Decision                 ApprovalDecision
	Reason                   *string
}

// ExecuteOperationApproval は `approve <OP-ID>` / `reject <OP-ID> --reason` の
// ②（書き込み）。確認が成立した（att が有効な）ことと、表示時点の不可逆操作の版
// （ExpectedVersion）がまだ最新であることを条件に、承認・差し戻しの記録と
// 不可逆操作の状態変更、課題の版の1増加、作業ログへの記録を1つのトランザクションで
// 行う。版が変わっていれば ErrConflict。課題が完了（done）した後でも実行できる
// （保留した本番反映のため。terminal_state の判定はしない）。
func (s *Store) ExecuteOperationApproval(ctx context.Context, req OperationApprovalRequest, att Attestation) (*IrreversibleOperation, *Approval, error) {
	switch req.Decision {
	case ApprovalDecisionApproved:
		if req.Reason != nil {
			return nil, nil, ErrValidation
		}
	case ApprovalDecisionRejected:
		if req.Reason == nil || strings.TrimSpace(*req.Reason) == "" {
			return nil, nil, ErrValidation
		}
	default:
		return nil, nil, ErrValidation
	}

	if !attestationValid(att, req.OperationID) {
		return nil, nil, ErrVerificationRejected
	}
	oid, ok := parseOperationID(req.OperationID)
	if !ok {
		return nil, nil, ErrNotFound
	}

	var opOut IrreversibleOperation
	var approvalOut Approval
	err := s.mutateAs(ctx, att.actor, att.channel, att.verification, func(tx *sql.Tx, rec *activityRecorder) error {
		row, err := loadOperationRow(ctx, tx, oid)
		if err != nil {
			return err
		}
		if row.Version != req.ExpectedVersion {
			return ErrConflict
		}
		if row.State != OperationStatePending {
			return ErrInvalidTransition
		}

		challenge, err := loadChallenge(ctx, tx, row.ChallengeID)
		if err != nil {
			return err
		}
		// 要約は課題の ID・タイトル・状態も見せるため、①②の間に課題が
		// 変わっていれば承認を成立させない（self-review 指摘。仕様
		// §承認「要約を表示してから確認が入力されるまでの間に、対象
		// （課題のフィールド・状態・計画の版・その課題の未承認の不可逆操作の
		// 集合）が変わっていれば、承認を成立させない」）。
		if challenge.Version != req.ExpectedChallengeVersion {
			return ErrConflict
		}

		nowStr := formatTimestamp(rec.at)
		now, err := parseTimestamp(nowStr)
		if err != nil {
			return err
		}

		newState, err := approveOperationInTx(ctx, tx, rec, operationApprovalWrite{
			opID:        oid,
			challengeID: row.ChallengeID,
			version:     row.Version,
			decision:    req.Decision,
			reason:      req.Reason,
			att:         att,
			nowStr:      nowStr,
		})
		if err != nil {
			return err
		}

		// 不可逆操作の承認・差し戻しでも課題の版を1増やす（親要件チケット #4
		// §アーキテクチャ決定）。これにより、完了の承認の要約表示後・確認前に
		// 別プロセスが対象の release を先に承認した場合、その完了の承認の
		// ExpectedVersion 比較が ErrConflict を検出できる。
		cres, err := tx.ExecContext(ctx,
			`UPDATE challenge SET version = ?, updated_at = ? WHERE id = ? AND version = ?`,
			challenge.Version+1, nowStr, row.ChallengeID, challenge.Version,
		)
		if err != nil {
			return err
		}
		caffected, err := cres.RowsAffected()
		if err != nil {
			return err
		}
		if caffected != 1 {
			return fmt.Errorf("core: %s operation %s: expected to bump challenge %d version, updated %d rows", req.Decision, req.OperationID, row.ChallengeID, caffected)
		}

		createdAt, err := parseTimestamp(row.CreatedAt)
		if err != nil {
			return err
		}
		opOut = IrreversibleOperation{
			ID:          formatOperationID(oid),
			ChallengeID: formatChallengeID(row.ChallengeID),
			Kind:        OperationKind(row.Kind),
			Summary:     row.Summary,
			State:       newState,
			Version:     row.Version + 1,
			CreatedAt:   createdAt,
		}
		if row.Ref.Valid {
			v := row.Ref.String
			opOut.Ref = &v
		}

		opIDStr := formatOperationID(oid)
		approvalOut = Approval{
			Kind:          ApprovalKindRelease,
			Decision:      req.Decision,
			OperationID:   &opIDStr,
			TargetVersion: row.Version,
			Actor:         att.Actor(),
			Channel:       string(att.Channel()),
			Verification:  string(att.Verification()),
			Reason:        req.Reason,
			DecidedAt:     now,
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return &opOut, &approvalOut, nil
}

// operationApprovalWrite は approveOperationInTx の入力（不可逆操作 1 件分の
// 承認・差し戻し）。
type operationApprovalWrite struct {
	opID        int64
	challengeID int64
	version     int // 更新前の不可逆操作の版（approval.target_version・楽観ロックの条件）
	decision    ApprovalDecision
	reason      *string // 差し戻しのときだけ非 nil
	att         Attestation
	nowStr      string // 呼び出し元のトランザクションと共有する日時
}

// approveOperationInTx は不可逆操作 1 件について、呼び出し元のトランザクション内で
// (1) operation の state と version の更新、(2) approval 行（種類は操作の種類に
// かかわらず ApprovalKindRelease・対象の operation_id つき＝H2・H5）の記録、
// (3) 作業ログへの entity="operation" のエントリの記録、を行い、新しい状態を返す。
//
// 単独の承認・差し戻し（ExecuteOperationApproval）と D12 の一括承認
// （approval.go の autoApproveReleases）が共有する唯一の実装である
// （self-review 指摘: 以前は同じ 3 つの書き込みが 2 箇所に別実装で存在し、
// 片方だけを直すと経路によって記録の形がずれる状態だった）。課題の版の増分は
// 呼び出し元が行う（単独の経路は 1 件ごとに +1、D12 は遷移が 1 回だけ +1 する）。
func approveOperationInTx(ctx context.Context, tx *sql.Tx, rec *activityRecorder, w operationApprovalWrite) (OperationState, error) {
	newState, ok := operationStateForDecision(w.decision)
	if !ok {
		return "", ErrValidation
	}
	newVersion := w.version + 1

	ores, err := tx.ExecContext(ctx,
		`UPDATE operation SET state = ?, version = ? WHERE id = ? AND version = ?`,
		string(newState), newVersion, w.opID, w.version,
	)
	if err != nil {
		return "", err
	}
	oaffected, err := ores.RowsAffected()
	if err != nil {
		return "", err
	}
	if oaffected != 1 {
		return "", fmt.Errorf("core: %s operation %s: expected to update 1 row, updated %d", w.decision, formatOperationID(w.opID), oaffected)
	}

	var reasonVal any
	if w.decision == ApprovalDecisionRejected {
		if w.reason == nil {
			return "", ErrValidation
		}
		reasonVal = *w.reason
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, reason, decided_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.challengeID, w.opID, string(ApprovalKindRelease), string(w.decision), w.version,
		w.att.Actor(), string(w.att.Channel()), string(w.att.Verification()), reasonVal, w.nowStr,
	); err != nil {
		return "", err
	}

	before := map[string]any{"state": string(OperationStatePending)}
	after := map[string]any{"state": string(newState), "version": newVersion}
	if w.decision == ApprovalDecisionRejected {
		after["reason"] = *w.reason
	}
	// action は decision の値（approved/rejected）ではなく、challenge
	// エンティティの作業ログと同じ動詞形（approve/reject。transition.go の
	// OpApprove/OpReject と同じ文字列）を使う（log の action 語彙を
	// エンティティ間で揃えるため）。
	action := string(OpReject)
	if w.decision == ApprovalDecisionApproved {
		action = string(OpApprove)
	}
	if err := rec.record("operation", w.opID, action, before, after); err != nil {
		return "", err
	}
	return newState, nil
}

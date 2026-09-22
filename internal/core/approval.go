// このファイルは本人確認つきの操作（承認 approve・差し戻し reject・保留への
// 回答 answer＝T5・T6・T12・T13・T15）の公開 API を持つ（Issue #12）。
//
// 機能全体の設計（親要件チケット #4）が定める2段階 API をそのまま実装する:
//
//	① 対象の要約と版を得る（読み取り）  = Prepare*
//	② 版と本人確認の結果を添えて実行する（書き込み） = Execute*
//
// ① と ② の間に対象（課題のフィールド・状態・計画の版）が変われば、② が
// ErrConflict を返す（§クリティカル設計決定1）。CLI は ① の結果を
// /dev/tty に表示し、core.Verify で確認を得てから ② を呼ぶ（internal/cli/
// approval.go）。M4 の UI も同じ2段階を使う想定であり、① の戻り値は
// 構造化データ（要約テキストの整形は呼び出し側の責務）にしている。

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// loadUnansweredHold は challengeID（内部整数 ID）の「現在の未回答の保留」
// （最新の1件）を返す。PrepareAnswer（①）と ExecuteAnswer の
// resolvePreceding（②。同じトランザクション内での再読み込み）が同じ
// クエリを共有するために切り出した（self-review 指摘: 別々に持つと、
// 承認対象の同一性を決める不変条件〈①が見せた保留と②が回答を書き込む
// 保留が同じであること〉が、将来どちらか一方だけクエリが変わっても
// 検出できない形で壊れうる）。行が無ければ sql.ErrNoRows をそのまま返す
// （呼び出し側が current.ID を使った文脈つきのエラーメッセージを組み立てる）。
func loadUnansweredHold(ctx context.Context, tx *sql.Tx, cid int64) (holdID int64, question string, fromStatus Status, raisedAt time.Time, err error) {
	var fs, raisedAtStr string
	if err = tx.QueryRowContext(ctx,
		`SELECT id, question, from_status, raised_at FROM hold WHERE challenge_id = ? AND answer IS NULL ORDER BY id DESC LIMIT 1`, cid,
	).Scan(&holdID, &question, &fs, &raisedAtStr); err != nil {
		return 0, "", "", time.Time{}, err
	}
	raisedAt, err = parseTimestamp(raisedAtStr)
	if err != nil {
		return 0, "", "", time.Time{}, err
	}
	return holdID, question, Status(fs), raisedAt, nil
}

// errNoUnansweredHold は loadUnansweredHold が sql.ErrNoRows を返したときに
// PrepareAnswer・ExecuteAnswer の両方が使う、文脈つきのエラーを組み立てる。
func errNoUnansweredHold(challengeID string) error {
	return fmt.Errorf("core: challenge %s is awaiting_human but has no unanswered hold", challengeID)
}

// approvalKindForStatus は、課題の現在の状態から承認の種類を決める
// （「approve <課題 ID> は、課題の状態から承認の種類を決める」）。
// 遷移表 T5・T6（計画承認待ち）／T13・T15（完了確認待ち）の遷移元と一致する
// 状態だけを解決できる。それ以外は ok=false。
func approvalKindForStatus(s Status) (ApprovalKind, bool) {
	switch s {
	case StatusAwaitingPlanApproval:
		return ApprovalKindPlan, true
	case StatusAwaitingCompletionApproval:
		return ApprovalKindCompletion, true
	default:
		return "", false
	}
}

// ApprovalPreview は PrepareApproval・PrepareRejection が返す、approve・reject
// の① （読み取り）の結果。人間向けの要約テキストの整形は呼び出し側
// （internal/cli/approval.go）が行う。
type ApprovalPreview struct {
	ChallengeID string
	Title       string
	Kind        ApprovalKind // ApprovalKindPlan または ApprovalKindCompletion
	Version     int          // 表示時点の課題の版（Execute* の ExpectedVersion にそのまま渡す）

	// Kind == ApprovalKindPlan のときだけ有効（最新の計画の版と本文）。
	PlanVersion int
	PlanBody    string

	// Kind == ApprovalKindCompletion のときだけ有効。
	DoneCriteria string
}

// prepareApproval は PrepareApproval・PrepareRejection が共有する読み取り。
// 課題が存在しない／ID の形式が不正なら ErrNotFound、完了なら
// ErrTerminalState、計画承認待ち・完了確認待ちのいずれでもなければ
// ErrInvalidTransition（Lookup(current.Status, OpApprove/OpReject) の定義域と
// 一致する。T5・T6・T13・T15 以外の（状態, 操作）の組は遷移表に無い）。
func (s *Store) prepareApproval(ctx context.Context, id string) (*ApprovalPreview, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}

	var preview ApprovalPreview
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if IsTerminal(Table, StatusVocabulary, current.Status) {
			return ErrTerminalState
		}
		kind, ok := approvalKindForStatus(current.Status)
		if !ok {
			return ErrInvalidTransition
		}

		preview = ApprovalPreview{
			ChallengeID:  current.ID,
			Title:        current.Title,
			Kind:         kind,
			Version:      current.Version,
			DoneCriteria: current.DoneCriteria,
		}

		if kind == ApprovalKindPlan {
			var version int
			var body string
			err := tx.QueryRowContext(ctx,
				`SELECT version, body FROM task_plan WHERE challenge_id = ? ORDER BY version DESC LIMIT 1`, cid,
			).Scan(&version, &body)
			switch {
			case err == nil:
				preview.PlanVersion = version
				preview.PlanBody = body
			case errors.Is(err, sql.ErrNoRows):
				// 計画承認待ちなのに計画行が無い状態は本来到達しない
				// （PlanChallenge が常に計画を登録してから遷移する）。以前は
				// ここで 0 件のまま preview を返しており、結果として
				// 「計画 v0:」＋空本文の要約のまま承認が成立してしまう
				// fail-open だった（self-review 指摘。PrepareAnswer が同種の
				// 不変条件破れをエラーにしているのと非対称だった）。
				// PrepareAnswer と同じ規律でエラーにする。
				return fmt.Errorf("core: challenge %s is awaiting_plan_approval but has no plan", current.ID)
			default:
				return err
			}
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &preview, nil
}

// PrepareApproval は approve の①（読み取り）。
func (s *Store) PrepareApproval(ctx context.Context, id string) (*ApprovalPreview, error) {
	return s.prepareApproval(ctx, id)
}

// PrepareRejection は reject の①（読み取り）。reason が
// strings.TrimSpace で空なら、端末を開く前に ErrValidation で拒否する
// （「差し戻しは理由を必須とする」。AC-48）。要約の内容自体は
// PrepareApproval と同じ形（「差し戻しの要約は、対応する承認の要約と同じ
// 内容に、入力された理由を加えたもの」なので、理由は呼び出し側が保持する）。
func (s *Store) PrepareRejection(ctx context.Context, id string, reason string) (*ApprovalPreview, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, ErrValidation
	}
	return s.prepareApproval(ctx, id)
}

// AnswerPreview は PrepareAnswer が返す、answer の①（読み取り）の結果。
type AnswerPreview struct {
	ChallengeID string
	Title       string
	Question    string
	Answer      string // 入力された回答をそのまま返す（要約の組み立てに使う）
	Version     int    // 表示時点の課題の版（ExecuteAnswer の ExpectedVersion にそのまま渡す）
	FromStatus  Status // 回答が成立したときに戻る状態（保留に入る直前の状態）
}

// PrepareAnswer は answer の①（読み取り）。answer が空なら、端末を開く前に
// ErrValidation で拒否する。課題が人間対応待ちでなければ ErrInvalidTransition
// （完了なら ErrTerminalState）。人間対応待ちなのに未回答の保留が無い場合は
// （状態機械の不変条件が破れているため）fail-closed にエラーを返す。
func (s *Store) PrepareAnswer(ctx context.Context, id string, answer string) (*AnswerPreview, error) {
	if strings.TrimSpace(answer) == "" {
		return nil, ErrValidation
	}

	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}

	var preview AnswerPreview
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if IsTerminal(Table, StatusVocabulary, current.Status) {
			return ErrTerminalState
		}
		if current.Status != StatusAwaitingHuman {
			return ErrInvalidTransition
		}

		_, question, fromStatus, _, err := loadUnansweredHold(ctx, tx, cid)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errNoUnansweredHold(current.ID)
			}
			return err
		}

		preview = AnswerPreview{
			ChallengeID: current.ID,
			Title:       current.Title,
			Question:    question,
			Answer:      answer,
			Version:     current.Version,
			FromStatus:  fromStatus,
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &preview, nil
}

// ApprovalRequest は ExecuteApproval の入力（approve・reject 共通。②
// ＝書き込み）。Decision が ApprovalDecisionRejected のときだけ Reason が
// 必須（非 nil かつ trim 非空）。ApprovalDecisionApproved のときは Reason は
// nil であること。
type ApprovalRequest struct {
	ChallengeID     string
	ExpectedVersion int
	Decision        ApprovalDecision
	Reason          *string
}

// ExecuteApproval は approve（T5・T13）・reject（T6・T15）の②（書き込み）。
// 確認が成立した（att が有効な）ことと、表示時点の版（ExpectedVersion）が
// まだ最新であることを条件に、承認の記録・遷移・作業ログを 1 つの
// トランザクションで行う。版が変わっていれば ErrConflict。
func (s *Store) ExecuteApproval(ctx context.Context, req ApprovalRequest, att Attestation) (*Challenge, *Approval, error) {
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

	op := OpReject
	if req.Decision == ApprovalDecisionApproved {
		op = OperationForApprove(false)
	}

	var approvalOut Approval
	apply := func(ctx context.Context, tc *transitionCtx) error {
		kind, ok := approvalKindForStatus(tc.current.Status)
		if !ok {
			// Lookup(current.Status, op) がここまでに成功しているため、
			// current.Status は必ず承認待ちの2状態のいずれか
			// （runTransition の判定順を参照）。到達しないはずの
			// fail-closed な防御。
			return fmt.Errorf("core: cannot determine approval kind for status %q", tc.current.Status)
		}

		var reasonVal any
		if req.Decision == ApprovalDecisionRejected {
			reasonVal = *req.Reason
		}

		if _, err := tc.tx.ExecContext(ctx,
			`INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, reason, decided_at)
			 VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?, ?)`,
			tc.id, string(kind), string(req.Decision), tc.current.Version, att.Actor(), string(att.Channel()), string(att.Verification()), reasonVal, tc.nowStr,
		); err != nil {
			return err
		}

		tc.after["approval_kind"] = string(kind)
		tc.after["decision"] = string(req.Decision)
		tc.after["target_version"] = tc.current.Version
		if req.Decision == ApprovalDecisionRejected {
			tc.after["reason"] = *req.Reason
		}

		approvalOut = Approval{
			Kind:          kind,
			Decision:      req.Decision,
			OperationID:   nil,
			TargetVersion: tc.current.Version,
			Actor:         att.Actor(),
			Channel:       string(att.Channel()),
			Verification:  string(att.Verification()),
			Reason:        req.Reason,
			DecidedAt:     tc.now,
		}
		return nil
	}

	c, err := s.verifiedTransition(ctx, att, req.ChallengeID, req.ExpectedVersion, op, NoStatus, nil, apply)
	if err != nil {
		return nil, nil, err
	}
	return c, &approvalOut, nil
}

// AnswerRequest は ExecuteAnswer の入力（②＝書き込み）。
type AnswerRequest struct {
	ChallengeID     string
	ExpectedVersion int
	Answer          string
}

// ExecuteAnswer は answer（T12）の②（書き込み）。「保留に入る直前の状態」は
// 版が一致していることを確認した直後、このトランザクションの中で改めて
// 「その時点で未回答の保留」を読んで決める（PrepareAnswer と同じクエリ）。
// ExpectedVersion が一致していれば、Prepare の時点から保留の集合は変わって
// いない（保留を増減させる操作は必ず課題の版も進めるため）ので、この
// 再読み込みは Prepare が見たのと同じ行を指す。
func (s *Store) ExecuteAnswer(ctx context.Context, req AnswerRequest, att Attestation) (*Challenge, *Hold, error) {
	if strings.TrimSpace(req.Answer) == "" {
		return nil, nil, ErrValidation
	}

	var holdOut Hold
	var holdID int64

	resolvePreceding := func(ctx context.Context, tx *sql.Tx, cid int64, current *Challenge) (Status, error) {
		id, question, fromStatus, raisedAt, err := loadUnansweredHold(ctx, tx, cid)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", errNoUnansweredHold(current.ID)
			}
			return "", err
		}
		holdID = id
		holdOut = Hold{Question: question, FromStatus: fromStatus, RaisedAt: raisedAt}
		return fromStatus, nil
	}

	apply := func(ctx context.Context, tc *transitionCtx) error {
		res, err := tc.tx.ExecContext(ctx,
			`UPDATE hold SET answer = ?, answered_at = ?, answered_by = ? WHERE id = ? AND answer IS NULL`,
			req.Answer, tc.nowStr, att.Actor(), holdID,
		)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("core: answer hold %d: expected to update 1 row, updated %d", holdID, affected)
		}

		tc.after["answer"] = req.Answer

		answer := req.Answer
		answeredAt := tc.now
		answeredBy := att.Actor()
		holdOut.Answer = &answer
		holdOut.AnsweredAt = &answeredAt
		holdOut.AnsweredBy = &answeredBy
		return nil
	}

	c, err := s.verifiedTransition(ctx, att, req.ChallengeID, req.ExpectedVersion, OpAnswer, NoStatus, resolvePreceding, apply)
	if err != nil {
		return nil, nil, err
	}
	return c, &holdOut, nil
}

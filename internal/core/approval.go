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
	// PendingReleases は、この完了の承認と同時に承認される未承認の release
	// （D12）。PendingOperations の kind==release の部分集合。
	PendingReleases []IrreversibleOperation
	// OtherPendingOperations は、この完了の承認では承認されない未承認の
	// 不可逆操作（delete・external_send・other。§決定事項の記録 A1）。
	OtherPendingOperations []IrreversibleOperation
}

// approvesPendingReleases は D12 の規則の正本: 「その操作が、対象の課題の
// 未承認の release を同じトランザクションで承認するか」。ExecuteApproval の
// 実挙動と、①の要約が見せる分類（ApprovalPreview.ReleaseEffect）が同じ
// 述語を使うことで、要約と実際の結果が食い違わないようにする
// （self-review 指摘: 以前は要約が --hold-release・reject でも「同時に承認
// される release」として一覧を出しており、本人確認の要約＝人間が承認判断の
// 根拠にする唯一のテキスト〔H9〕が事実と異なっていた）。
func approvesPendingReleases(kind ApprovalKind, decision ApprovalDecision, holdRelease bool) bool {
	return decision == ApprovalDecisionApproved && kind == ApprovalKindCompletion && !holdRelease
}

// ReleaseEffect は「この approve／reject を成立させたとき、その課題の未承認の
// 不可逆操作がどうなるか」を表す（要約テキストの組み立てに使う）。
type ReleaseEffect struct {
	// Approved は、この操作と同時に承認される不可逆操作（D12 の release）。
	Approved []IrreversibleOperation
	// NotApproved は、この操作では承認されず未承認のまま残る不可逆操作。
	NotApproved []IrreversibleOperation
}

// ReleaseEffect は、decision・holdRelease を指定したときの不可逆操作への影響を
// 返す（D12・A1）。完了の承認（holdRelease==false）なら未承認の release だけが
// Approved、それ以外（--hold-release＝T14・差し戻し・計画の承認）は未承認の
// 不可逆操作がすべて NotApproved になる。
func (p *ApprovalPreview) ReleaseEffect(decision ApprovalDecision, holdRelease bool) ReleaseEffect {
	if approvesPendingReleases(p.Kind, decision, holdRelease) {
		return ReleaseEffect{Approved: p.PendingReleases, NotApproved: p.OtherPendingOperations}
	}
	notApproved := make([]IrreversibleOperation, 0, len(p.PendingReleases)+len(p.OtherPendingOperations))
	notApproved = append(notApproved, p.PendingReleases...)
	notApproved = append(notApproved, p.OtherPendingOperations...)
	return ReleaseEffect{NotApproved: notApproved}
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

		if kind == ApprovalKindCompletion {
			// 完了の承認の要約は、同時に承認される release と、同時には承認
			// されない不可逆操作の一覧を含む（D12・A1）。
			ops, err := loadOperations(ctx, tx, cid)
			if err != nil {
				return err
			}
			for _, op := range ops {
				if op.State != OperationStatePending {
					continue
				}
				if op.Kind == OperationKindRelease {
					preview.PendingReleases = append(preview.PendingReleases, op)
				} else {
					preview.OtherPendingOperations = append(preview.OtherPendingOperations, op)
				}
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
// （「差し戻しは理由を必須とする」。AC-48）。戻り値の形は PrepareApproval と
// 同じで、理由は呼び出し側が保持する。
//
// 差し戻しは release を承認しない（D12 の一括承認は完了の承認にだけ伴う）
// ため、完了確認待ちの課題への差し戻しでは ReleaseEffect は未承認の不可逆
// 操作をすべて「同時には承認されない」側に分類する（要約と実挙動を一致させる
// ため）。これは「差し戻しの要約は、対応する承認の要約と同じ内容に、入力
// された理由を加えたもの」という規則の例外として仕様に明記済み
// （docs/features/m1-core.md の §承認・受入基準 41。Issue #40）。
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
// nil であること。HoldRelease は Decision==ApprovalDecisionApproved かつ対象が
// 完了確認待ち（T14）のときだけ true にできる（それ以外は CLI が usage_error で
// 先に拒否する＝core.OperationForApprove のコメント）。
type ApprovalRequest struct {
	ChallengeID     string
	ExpectedVersion int
	Decision        ApprovalDecision
	HoldRelease     bool
	Reason          *string
}

// ExecuteApproval は approve（T5・T13・T14）・reject（T6・T15）の②（書き込み）。
// 確認が成立した（att が有効な）ことと、表示時点の版（ExpectedVersion）が
// まだ最新であることを条件に、承認の記録・遷移・作業ログを 1 つの
// トランザクションで行う。版が変わっていれば ErrConflict。
//
// D12: 完了の承認（T13。Decision==Approved かつ HoldRelease==false）は、
// 同じトランザクションでその課題の未承認の release をすべて自動承認する
// （承認の記録・作業ログのエントリは、完了の承認そのものとは別に、release
// ごとに1件ずつ残る）。HoldRelease==true（T14）では release を一切承認せず
// 未承認のまま残す。
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
		if req.HoldRelease {
			return nil, nil, ErrValidation
		}
	default:
		return nil, nil, ErrValidation
	}

	op := OpReject
	if req.Decision == ApprovalDecisionApproved {
		op = OperationForApprove(req.HoldRelease)
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

		// 計画の承認は、要約に表示した計画の版（この時点の最新の計画。版が変われば
		// 課題の版も進み ErrConflict になるので、Prepare が見せた版と同じ）も記録する。
		// 委譲は target_version（課題の版）ではなくこの値で計画を引く。
		var planVersionVal any
		var planVersionOut *int
		if kind == ApprovalKindPlan && req.Decision == ApprovalDecisionApproved {
			var pv sql.NullInt64
			if err := tc.tx.QueryRowContext(ctx,
				`SELECT MAX(version) FROM task_plan WHERE challenge_id = ?`, tc.id).Scan(&pv); err != nil {
				return err
			}
			if !pv.Valid {
				return fmt.Errorf("core: challenge %s is awaiting_plan_approval but has no plan", tc.current.ID)
			}
			planVersionVal = pv.Int64
			v := int(pv.Int64)
			planVersionOut = &v
		}

		if _, err := tc.tx.ExecContext(ctx,
			`INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, plan_version, actor, channel, verification, reason, decided_at)
			 VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			tc.id, string(kind), string(req.Decision), tc.current.Version, planVersionVal, att.Actor(), string(att.Channel()), string(att.Verification()), reasonVal, tc.nowStr,
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
			PlanVersion:   planVersionOut,
			Actor:         att.Actor(),
			Channel:       string(att.Channel()),
			Verification:  string(att.Verification()),
			Reason:        req.Reason,
			DecidedAt:     tc.now,
		}

		// D12: 完了の承認（HoldRelease==false）は、同じトランザクションで
		// その課題の未承認の release をすべて自動承認する。完了の承認と
		// release ごとの承認は、それぞれ別の approval 行・別の作業ログの
		// エントリとして残る（この if の外で1件だけ積んだ上の approvalOut・
		// activity とは別に、release の数だけ積む）。T14（HoldRelease==true）・
		// 計画の承認（kind==ApprovalKindPlan）・reject では行わない。
		if approvesPendingReleases(kind, req.Decision, req.HoldRelease) {
			if err := autoApproveReleases(ctx, tc, att); err != nil {
				return err
			}
		}
		return nil
	}

	c, err := s.verifiedTransition(ctx, att, req.ChallengeID, req.ExpectedVersion, op, NoStatus, nil, apply)
	if err != nil {
		return nil, nil, err
	}
	return c, &approvalOut, nil
}

// autoApproveReleases は tc.id の課題が持つ未承認（state="pending"）の
// release をすべて承認する（D12）。呼び出し元（ExecuteApproval の apply）と
// 同じトランザクション・同じ nowStr（decided_at・activity.at）を使う。
// release ごとに: (1) operation.state を "approved" にし version を1増やす、
// (2) approval 行を1件（operation_id・kind=release つき）記録する、
// (3) 作業ログへ entity="operation" のエントリを1件記録する。
func autoApproveReleases(ctx context.Context, tc *transitionCtx, att Attestation) error {
	rows, err := tc.tx.QueryContext(ctx,
		`SELECT id, version FROM operation WHERE challenge_id = ? AND kind = ? AND state = ? ORDER BY id ASC`,
		tc.id, string(OperationKindRelease), string(OperationStatePending),
	)
	if err != nil {
		return err
	}
	type pendingRelease struct {
		id      int64
		version int
	}
	var pending []pendingRelease
	for rows.Next() {
		var p pendingRelease
		if err := rows.Scan(&p.id, &p.version); err != nil {
			_ = rows.Close()
			return err
		}
		pending = append(pending, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, p := range pending {
		// 書き込みの形（operation の更新・approval 行・作業ログのエントリ）は
		// 単独の承認と同じ実装を共有する（operation.go の
		// approveOperationInTx。self-review 指摘）。課題の版はこの経路では
		// 増やさない（完了の遷移そのものが 1 回だけ +1 する）。
		if _, err := approveOperationInTx(ctx, tc.tx, tc.rec, operationApprovalWrite{
			opID:        p.id,
			challengeID: tc.id,
			version:     p.version,
			decision:    ApprovalDecisionApproved,
			att:         att,
			nowStr:      tc.nowStr,
		}); err != nil {
			return err
		}
	}
	return nil
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

		// S3（Issue #39）: 未設定（保留の answer が無い）から値が入るので、
		// before にそのキーを null で載せる（plan の plan_version・classify の
		// priority・edit の urgency とそろえる）。
		tc.before["answer"] = nil
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

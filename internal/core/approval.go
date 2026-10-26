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
	"encoding/json"
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
		`SELECT id, question, from_status, raised_at FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL ORDER BY id DESC LIMIT 1`, cid,
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

	// StageFields・IssuesAnswer は、表示時点の課題の段階の欄と、論点の保留の回答（無ければ nil）。
	StageFields
	IssuesAnswer *string
	// Pairs は予想と実物を位置で対にしたもの（Pairs の唯一の呼び出し。どちらも NULL なら空）。
	Pairs []StagePair

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
	// VerifiedHeadSHAs は PendingReleases の release の ID ごとに、J5 が検証の入力にした
	// 最新の head（latestVerifiedHead）。記録のない release は載せない（承認を止めない）。
	VerifiedHeadSHAs map[string]string
	// ReapprovalForbidden は PendingReleases の release の ID ごとに、承認し直せないか
	// （直前の試行の結果が merged_unapproved か。rejectReapproval と同じ判定）。
	// CLI が release を同時の承認から外す判定（M45H16）に使い、CLI は結果の比較をしない。
	ReapprovalForbidden map[string]bool
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
			StageFields:  current.StageFields,
			Pairs:        Pairs(current.Expected, current.Actual),
		}
		answer, err := loadIssuesAnswer(ctx, tx, cid, current.IssuesHoldID)
		if err != nil {
			return err
		}
		preview.IssuesAnswer = answer

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
					head, err := latestVerifiedHead(ctx, tx, op.Ref)
					if err != nil {
						return err
					}
					if head != nil {
						if preview.VerifiedHeadSHAs == nil {
							preview.VerifiedHeadSHAs = map[string]string{}
						}
						preview.VerifiedHeadSHAs[op.ID] = *head
					}
					if oid, ok := parseOperationID(op.ID); ok {
						forbidden, err := reapprovalForbidden(ctx, tx, oid)
						if err != nil {
							return err
						}
						if preview.ReapprovalForbidden == nil {
							preview.ReapprovalForbidden = map[string]bool{}
						}
						preview.ReapprovalForbidden[op.ID] = forbidden
					}
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
	// RejectedFields は課題の差し戻しでずれた欄（RejectedField の閉集合の値。重複は 1 つにし、
	// 閉集合の順に記録する）。差し戻し以外・閉集合の外の値は ErrValidation。
	RejectedFields []string

	// ReleaseHeads と ExcludedReleaseIDs は、完了の承認（HoldRelease==false）で同時に承認する
	// release の ID と ① で取った head の組（Operation.ID と HeadSHA だけを使う）と、① が外した
	// release の ID。2 つの和集合が ② の時点の未承認の release の全体と一致しない入力・head が空の組・
	// 承認する組の head が最新の検証の head（記録があるとき）と異なる入力は、何も書かず ErrConflict。
	// 外した release は pending のまま残る。head は ② で取り直さず、承認の記録の head_sha にそのまま残す。
	// 完了の承認以外（計画・差し戻し・HoldRelease）では使わない。
	ReleaseHeads       []ReleaseHead
	ExcludedReleaseIDs []string
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

	rejectedFields, ok := NormalizeRejectedFields(req.RejectedFields)
	if !ok || (len(rejectedFields) > 0 && req.Decision != ApprovalDecisionRejected) {
		return nil, nil, ErrValidation
	}
	var rejectedFieldsVal any
	var rejectedFieldsOut []string
	if len(rejectedFields) > 0 {
		for _, f := range rejectedFields {
			rejectedFieldsOut = append(rejectedFieldsOut, string(f))
		}
		b, err := json.Marshal(rejectedFieldsOut)
		if err != nil {
			return nil, nil, err
		}
		rejectedFieldsVal = string(b)
	}

	if req.Decision == ApprovalDecisionRejected && !attestationPermits(att, req.ChallengeID, actionReject) {
		return nil, nil, ErrVerificationRejected
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

		relaySession, relayQuote := att.relayColumns()
		approvalRes, err := tc.tx.ExecContext(ctx,
			`INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, plan_version, actor, channel, verification, reason, decided_at, relay_session, relay_quote, rejected_fields)
			 VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			tc.id, string(kind), string(req.Decision), tc.current.Version, planVersionVal, att.Actor(), string(att.Channel()), string(att.Verification()), reasonVal, tc.nowStr, relaySession, relayQuote, rejectedFieldsVal,
		)
		if err != nil {
			return err
		}
		approvalRowID, err := approvalRes.LastInsertId()
		if err != nil {
			return err
		}
		if err := s.recordApprovalOccurrence(ctx, tc, kind, req.Decision, approvalRowID); err != nil {
			return err
		}

		tc.after["approval_kind"] = string(kind)
		tc.after["decision"] = string(req.Decision)
		tc.after["target_version"] = tc.current.Version
		att.addRelayTo(tc.after)
		if req.Decision == ApprovalDecisionRejected {
			tc.after["reason"] = *req.Reason
		}
		if rejectedFieldsOut != nil {
			tc.after["rejected_fields"] = rejectedFieldsOut
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

			RejectedFields: rejectedFieldsOut,
		}
		approvalOut.RelaySession, approvalOut.RelayQuote = att.relayPtrs()

		// D12: 完了の承認（HoldRelease==false）は、同じトランザクションで
		// その課題の未承認の release をすべて自動承認する。完了の承認と
		// release ごとの承認は、それぞれ別の approval 行・別の作業ログの
		// エントリとして残る（この if の外で1件だけ積んだ上の approvalOut・
		// activity とは別に、release の数だけ積む）。T14（HoldRelease==true）・
		// 計画の承認（kind==ApprovalKindPlan）・reject では行わない。
		if approvesPendingReleases(kind, req.Decision, req.HoldRelease) {
			if err := autoApproveReleases(ctx, tc, att, req); err != nil {
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

// recordApprovalOccurrence は計画・完了の承認と差し戻しの発生を、承認の行と同じ
// トランザクションの中で 1 行作る。種類は承認の種類（kind）と決定で決め、
// rejected_fields の有無では決めない。参照はその承認の行（完了の承認は kind=completion の行。
// release ごとの承認の行ではない）。周は作らない経路なので NULL。計画の承認は発生を作らない。
func (s *Store) recordApprovalOccurrence(ctx context.Context, tc *transitionCtx, kind ApprovalKind, decision ApprovalDecision, approvalRowID int64) error {
	var occKind OccurrenceKind
	outcome := OccurrenceOutcomeBad
	switch {
	case kind == ApprovalKindPlan && decision == ApprovalDecisionRejected:
		occKind = OccurrenceKindPlanRejected
	case kind == ApprovalKindCompletion && decision == ApprovalDecisionRejected:
		occKind = OccurrenceKindCompletionRejected
	case kind == ApprovalKindCompletion && decision == ApprovalDecisionApproved:
		occKind = OccurrenceKindCompletionApproved
		good, err := completionApprovedIsGood(ctx, tc.tx, tc.id)
		if err != nil {
			return err
		}
		if good {
			outcome = OccurrenceOutcomeGood
		}
	default:
		return nil
	}
	return s.insertOccurrenceRow(tc.tx, occurrenceInput{
		ChallengeID: tc.id,
		Kind:        occKind,
		Ref:         OccurrenceRef{ApprovalID: &approvalRowID},
		Outcome:     outcome,
		At:          tc.now,
	})
}

// completionApprovedIsGood は M45H21 の規則。その課題の最新の計画の承認の行より後に
// completion_rejected と verification_not_met の発生が無ければ true。差し戻しの行は
// plan_version を持たないので、区切りは承認の行の id（completion_rejected）と日時
// （verification_not_met。参照が承認の行とは限らない）で取る。計画の承認の行が無ければ全体を見る。
func completionApprovedIsGood(ctx context.Context, tx *sql.Tx, challengeID int64) (bool, error) {
	var planID sql.NullInt64
	var planAt sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT id, decided_at FROM approval WHERE challenge_id = ? AND kind = ? AND decision = ? ORDER BY id DESC LIMIT 1`,
		challengeID, string(ApprovalKindPlan), string(ApprovalDecisionApproved)).Scan(&planID, &planAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM occurrence
		  WHERE challenge_id = ?
		    AND ((kind = ? AND approval_id > ?) OR (kind = ? AND created_at >= ?))`,
		challengeID,
		string(OccurrenceKindCompletionRejected), planID.Int64,
		string(OccurrenceKindVerificationNotMet), planAt.String,
	).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// autoApproveReleases は tc.id の課題が持つ未承認（state="pending"）の
// release をすべて承認する（D12）。呼び出し元（ExecuteApproval の apply）と
// 同じトランザクション・同じ nowStr（decided_at・activity.at）を使う。
// release ごとに: (1) operation.state を "approved" にし version を1増やす、
// (2) approval 行を1件（operation_id・kind=release つき）記録する、
// (3) 作業ログへ entity="operation" のエントリを1件記録する。
//
// 承認する release は req.ReleaseHeads の組だけで、req の組と外した release（req.ExcludedReleaseIDs）の
// 和集合が未承認の release の全体と一致しなければ、何も書かず ErrConflict を返す（外した release は
// pending のまま残る）。承認する組に、直前の試行の結果が merged_unapproved の release が入っていれば、
// 何も書かず ReapprovalForbiddenError（verification_rejected）を返す。
func autoApproveReleases(ctx context.Context, tc *transitionCtx, att Attestation, req ApprovalRequest) error {
	rows, err := tc.tx.QueryContext(ctx,
		`SELECT id, version, ref FROM operation WHERE challenge_id = ? AND kind = ? AND state = ? ORDER BY id ASC`,
		tc.id, string(OperationKindRelease), string(OperationStatePending),
	)
	if err != nil {
		return err
	}
	type pendingRelease struct {
		id      int64
		version int
		ref     *string
	}
	var pending []pendingRelease
	for rows.Next() {
		var p pendingRelease
		var ref sql.NullString
		if err := rows.Scan(&p.id, &p.version, &ref); err != nil {
			_ = rows.Close()
			return err
		}
		if ref.Valid {
			v := ref.String
			p.ref = &v
		}
		pending = append(pending, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	// 入力の検査。承認する組（head つき）と外した release が互いに素で、和集合が未承認の
	// release の全体と一致すること。
	pendingIDs := make(map[int64]bool, len(pending))
	for _, p := range pending {
		pendingIDs[p.id] = true
	}
	seen := make(map[int64]bool, len(pending))
	heads := make(map[int64]string, len(req.ReleaseHeads))
	for _, rh := range req.ReleaseHeads {
		oid, ok := parseOperationID(rh.Operation.ID)
		if !ok || !pendingIDs[oid] || seen[oid] {
			return ErrConflict
		}
		seen[oid] = true
		if rh.HeadSHA == nil || strings.TrimSpace(*rh.HeadSHA) == "" {
			return ErrConflict
		}
		heads[oid] = strings.TrimSpace(*rh.HeadSHA)
	}
	for _, id := range req.ExcludedReleaseIDs {
		oid, ok := parseOperationID(id)
		if !ok || !pendingIDs[oid] || seen[oid] {
			return ErrConflict
		}
		seen[oid] = true
	}
	if len(seen) != len(pending) {
		return ErrConflict
	}
	for _, p := range pending {
		head, approve := heads[p.id]
		if !approve {
			continue
		}
		// 直前の試行の結果が merged_unapproved の release は、CLI の判定に関わらず承認しない
		// （M45H16。--relayed の経路でも同じ）。承認する組に入っていたら何も書かず終える。
		if err := rejectReapproval(ctx, tc.tx, p.id); err != nil {
			return err
		}
		verified, err := latestVerifiedHead(ctx, tc.tx, p.ref)
		if err != nil {
			return err
		}
		if verified != nil && *verified != head {
			return ErrConflict
		}
	}

	for _, p := range pending {
		head, approve := heads[p.id]
		if !approve {
			continue
		}
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
			headSHA:     head,
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

	if !attestationPermits(att, req.ChallengeID, actionAnswer) {
		return nil, nil, ErrVerificationRejected
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
		var runID sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT run_id FROM hold WHERE id = ?`, id).Scan(&runID); err != nil {
			return "", err
		}
		holdOut = Hold{Question: question, FromStatus: fromStatus, RaisedAt: raisedAt}
		if runID.Valid {
			v := formatRunID(runID.Int64)
			holdOut.RunID = &v
		}
		return fromStatus, nil
	}

	apply := func(ctx context.Context, tc *transitionCtx) error {
		relaySession, relayQuote := att.relayColumns()
		res, err := tc.tx.ExecContext(ctx,
			`UPDATE hold SET answer = ?, answered_at = ?, answered_by = ?, answered_channel = ?, answered_verification = ?, relay_session = ?, relay_quote = ? WHERE id = ? AND answer IS NULL AND closed_at IS NULL`,
			req.Answer, tc.nowStr, att.Actor(), string(att.Channel()), string(att.Verification()), relaySession, relayQuote, holdID,
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
		att.addRelayTo(tc.after)

		answer := req.Answer
		answeredAt := tc.now
		answeredBy := att.Actor()
		holdOut.Answer = &answer
		holdOut.AnsweredAt = &answeredAt
		holdOut.AnsweredBy = &answeredBy
		ch, ver := string(att.Channel()), string(att.Verification())
		holdOut.AnsweredChannel, holdOut.AnsweredVerification = &ch, &ver
		holdOut.RelaySession, holdOut.RelayQuote = att.relayPtrs()
		return nil
	}

	c, err := s.verifiedTransition(ctx, att, req.ChallengeID, req.ExpectedVersion, OpAnswer, NoStatus, resolvePreceding, apply)
	if err != nil {
		return nil, nil, err
	}
	return c, &holdOut, nil
}

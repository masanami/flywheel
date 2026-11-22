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
	// PlanTopics・PlanTasks・PlanUsesIntegration は最新の計画の論点と問い（答えつき）・タスク（前提は計画の中の key）・統合先を使うか。
	// 構造化した出力を持たない計画では、論点・タスクは空で UsesIntegration は nil。
	PlanTopics          []PlanTopicView
	PlanTasks           []PlanTaskView
	PlanUsesIntegration *bool
	// PlanTaskPhases は PlanTasks と同じ順のタスクの表示の区分（承認の前なので next か blocked）。
	PlanTaskPhases []TaskPhase
	// PlanSummary は計画の一言の要約（書かれていない・過去の形の計画では nil）。PlanBudgetUSD は計画の予算の上限
	// （PrepareApprovalFor に宣言を渡したときだけ、宣言のサイズごとの既定も使って決める。決められないとき nil）。
	// 端末の approve の要約と --json はどちらも使わない。
	PlanSummary   *string
	PlanBudgetUSD *float64
	// PlanBudgetLimitUSD・PlanBudgetLimitReason は、承認を待っている版の計画の上限（計画が上限を書かなければ見込み PlanBudgetUSD と同じ額。
	// 見込みが決められなければ nil）と、上限が見込みより大きいときだけの上限の理由（同じ額なら nil）。
	PlanBudgetLimitUSD    *float64
	PlanBudgetLimitReason *string

	// Kind == ApprovalKindCompletion のときだけ有効。
	DoneCriteria string
	// AddedTasks は、計画の後に足されたタスク（ID の昇順。完了の承認の要約に出す。PrepareApproval でも埋める。計画の承認では空）。
	AddedTasks []AddedTask
	// DirectCompletion は、この完了確認待ちが親が直接行った完了の記録（T17）によるときだけ非 nil。
	// 成果物の参照を完了条件と並べて見せる（完了の検証〈J5〉は通っていない）。
	DirectCompletion *DirectCompletion
	// ConditionsAfterRelease は、完了確認待ちへ進めた検証（T8）が「本番反映で満たされる見込み」と
	// 区分した達成条件（#451）。承認で本番反映されると満たされる条件として要約に出す。無ければ nil。
	ConditionsAfterRelease []string
	// MergeabilityUnconfirmed は、完了確認待ちへ進めた検証（T8）が PR のマージ可否を確かめられないまま
	// 進めたとき true。要約に「マージ可否は未確認」と出す。
	MergeabilityUnconfirmed bool
	// PendingReleases は、この完了の承認と同時に承認される未承認の release
	// （D12）。PendingOperations の kind==release の部分集合。
	PendingReleases []IrreversibleOperation
	// HasApprovedRelease は、課題に承認済みで未実行の release（state=approved）があるか。あれば、この
	// 操作で本番反映が承認されなくても、その release が走れば ConditionsAfterRelease は満たされうる
	// （判定は isApprovedUnexecutedRelease で、noWaitingRelease と共有する）。
	HasApprovedRelease bool
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
	// ReleasesToWithdraw・RunningReleases は、この完了確認待ちを差し戻したときに取り消す未実行の release
	// （承認待ち・承認済み）の ID と、試行が終わっていない（実行中の）ため止めずに残す release の ID
	// （マージされうる）。判定は差し戻しの書き込みと同じ releaseOnCompletionRejection。差し戻しの要約が使う。
	ReleasesToWithdraw []string
	RunningReleases    []string

	// 以下は完了の承認の要約を PrepareApprovalFor で読んだときだけ埋める、完了承認画面の値（要約と同じ
	// 読み取りで読む）。端末の要約（PrepareApproval）は埋めない。
	// CompletionTasks は課題のタスク（最新の版のタスクと、前の版の済みのタスク）。
	CompletionTasks []CompletionTask
	// OpenFollowups は課題にぶら下がる未（open）のフォローアップ。
	OpenFollowups []Followup
	// ConflictFixes は通したコンフリクトの解消（古い順）。
	ConflictFixes []ConflictFixPassed
	// UsesIntegration・IntegrationRef は課題が統合先を使うかと、記録した統合先の識別。
	UsesIntegration *bool
	IntegrationRef  *string
}

// CompletionTask は完了承認画面に並べるタスク 1 件。計画を作り直した課題では、前の版のタスクも課題のタスクに入るので、
// 最新の版（木）にあるものに加えて、前の版の済みのタスク（課題の上限の枠に数え、反映するものの一部）だけを並べる。
// 前の版の未完・取りやめのタスクは並べない。木に無いタスクは InTree=false（詳細の画面が開けない）。
type CompletionTask struct {
	ID              string
	Title           string
	Phase           TaskPhase
	InTree          bool
	SpentUSD        float64
	BudgetImplUSD   float64
	BudgetReviewUSD float64
}

func loadCompletionScreen(ctx context.Context, tx *sql.Tx, cid int64, agent *AgentDeclaration, p *ApprovalPreview) error {
	tasks, err := listTasks(ctx, tx, cid)
	if err != nil {
		return err
	}
	if err := fillTaskBudgets(ctx, tx, cid, tasks, agent); err != nil {
		return err
	}
	sh, err := loadTreeShared(ctx, tx)
	if err != nil {
		return err
	}
	var n TreeChallenge
	if err := fillTreeTasks(ctx, tx, cid, agent, sh, &n); err != nil {
		return err
	}
	phaseByID := map[string]TaskPhase{}
	for _, tt := range n.Tasks {
		if tt.ID != nil {
			phaseByID[*tt.ID] = tt.Phase
		}
	}
	p.CompletionTasks = []CompletionTask{}
	for _, t := range tasks {
		ct := CompletionTask{ID: t.ID, Title: t.Title, SpentUSD: t.SpentUSD, BudgetImplUSD: t.BudgetImplUSD, BudgetReviewUSD: t.BudgetReviewUSD}
		if ph, ok := phaseByID[t.ID]; ok {
			ct.Phase, ct.InTree = ph, true
		} else if t.Status == TaskStatusDone {
			ct.Phase = TaskPhaseDone
		} else {
			continue
		}
		p.CompletionTasks = append(p.CompletionTasks, ct)
	}
	fs, err := listFollowups(ctx, tx, FollowupFilter{Status: string(FollowupStatusOpen), ChallengeID: formatChallengeID(cid)})
	if err != nil {
		return err
	}
	p.OpenFollowups = fs
	if p.ConflictFixes, err = loadConflictFixes(ctx, tx, cid); err != nil {
		return err
	}
	if p.UsesIntegration, err = challengeUsesIntegration(ctx, tx, cid); err != nil {
		return err
	}
	var ref sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT integration_ref FROM challenge WHERE id = ?`, cid).Scan(&ref); err != nil {
		return err
	}
	if ref.Valid && ref.String != "" {
		v := ref.String
		p.IntegrationRef = &v
	}
	return nil
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
func (s *Store) prepareApproval(ctx context.Context, id string, agent *AgentDeclaration, withScreen bool) (*ApprovalPreview, error) {
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
				// 予算の上限は、宣言が渡されたとき（API）だけ決める。端末の要約は予算の上限を出さない。
				pv, err := loadPlanView(ctx, tx, cid, agent)
				if err != nil {
					return err
				}
				if pv != nil {
					preview.PlanTopics, preview.PlanTasks, preview.PlanUsesIntegration = pv.Topics, pv.Tasks, pv.UsesIntegration
					preview.PlanTaskPhases, preview.PlanSummary, preview.PlanBudgetUSD = planTaskPhases(pv.Tasks), pv.Summary, pv.BudgetUSD
					if pv.BudgetUSD != nil {
						_, lim, reason := planBudgetLimit(pv)
						preview.PlanBudgetLimitUSD, preview.PlanBudgetLimitReason = &lim, reason
					}
				}
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
			if preview.AddedTasks, err = listAddedTasks(ctx, tx, cid, ""); err != nil {
				return err
			}
			if withScreen {
				// 完了承認画面が使う値は、要約と同じ読み取り（同じトランザクション）で読む。
				// 別の読み取りにすると、間に課題が動いたとき要約と食い違う。
				if err := loadCompletionScreen(ctx, tx, cid, agent, &preview); err != nil {
					return err
				}
			}
			dc, err := loadCurrentDirectCompletion(ctx, tx, cid)
			if err != nil {
				return err
			}
			preview.DirectCompletion = dc
			conds, err := loadConditionsAfterRelease(ctx, tx, cid)
			if err != nil {
				return err
			}
			preview.ConditionsAfterRelease = conds
			if preview.MergeabilityUnconfirmed, err = loadMergeabilityUnconfirmed(ctx, tx, cid); err != nil {
				return err
			}
			// 完了の承認の要約は、同時に承認される release と、同時には承認
			// されない不可逆操作の一覧を含む（D12・A1）。
			ops, err := loadOperations(ctx, tx, cid)
			if err != nil {
				return err
			}
			now := s.currentTime()
			for _, op := range ops {
				if isApprovedUnexecutedRelease(op) {
					preview.HasApprovedRelease = true
				}
				if op.Kind == OperationKindRelease && (op.State == OperationStatePending || op.State == OperationStateApproved) {
					if oid, ok := parseOperationID(op.ID); ok {
						action, _, err := releaseOnCompletionRejection(ctx, tx, oid, now)
						if err != nil {
							return err
						}
						switch action {
						case rejectReleaseWithdraw:
							preview.ReleasesToWithdraw = append(preview.ReleasesToWithdraw, op.ID)
						case rejectReleaseRunning:
							preview.RunningReleases = append(preview.RunningReleases, op.ID)
						}
					}
				}
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

// activityKeyConditionsAfterRelease は、T8（verify_met）の作業ログの after に残す「本番反映で
// 満たされる見込み」の達成条件の配列のキー（#451）。
const activityKeyConditionsAfterRelease = "conditions_after_release"

// loadConditionsAfterRelease は課題の最新の T8（verify_met）の作業ログから、本番反映で満たされる
// 見込みの達成条件を返す。完了確認待ちへ入る遷移は T8 だけなので、最新の T8 がいまの完了確認待ちを
// 作った検証である。キーが無い（区分の無い出力・人が進めた検証）なら nil。表示のためだけの値なので、
// 読めない値（取り込んだ・手で直した行など）は無いものとして扱い、承認・差し戻しを止めない。
func loadConditionsAfterRelease(ctx context.Context, tx *sql.Tx, cid int64) ([]string, error) {
	var after sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT after FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = ? ORDER BY id DESC LIMIT 1`,
		cid, string(OpVerifyMet)).Scan(&after)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !after.Valid {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(after.String), &m) != nil {
		return nil, nil
	}
	raw, ok := m[activityKeyConditionsAfterRelease]
	if !ok {
		return nil, nil
	}
	var conds []string
	if json.Unmarshal(raw, &conds) != nil {
		return nil, nil
	}
	if len(conds) == 0 {
		return nil, nil
	}
	return conds, nil
}

// PrepareApproval は approve の①（読み取り）。
func (s *Store) PrepareApproval(ctx context.Context, id string) (*ApprovalPreview, error) {
	return s.prepareApproval(ctx, id, nil, false)
}

// PrepareApprovalFor は PrepareApproval に、計画の予算の上限をサイズごとの既定から決めるための宣言を渡す版
// （API の承認の要約が使う。完了確認待ちなら、完了承認画面の値も同じ読み取りで読む。agent が nil でも読む）。
func (s *Store) PrepareApprovalFor(ctx context.Context, id string, agent *AgentDeclaration) (*ApprovalPreview, error) {
	return s.prepareApproval(ctx, id, agent, true)
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
	return s.prepareApproval(ctx, id, nil, false)
}

// AnswerPreview は PrepareAnswer が返す、answer の①（読み取り）の結果。
type AnswerPreview struct {
	ChallengeID string
	Title       string
	Question    string
	Answer      string // 入力された回答をそのまま返す（要約の組み立てに使う）
	Version     int    // 表示時点の課題の版（ExecuteAnswer の ExpectedVersion にそのまま渡す）
	FromStatus  Status // 回答が成立したときに戻る状態（保留に入る直前の状態）
	// Option・OptionLabel は選択肢で答えるときの、選んだ識別と、解決した見出し（選んでいなければ空）。
	// Answer は入力された回答の文（選択肢で答えるときは添え書き。無ければ空）。
	Option      string
	OptionLabel string
	// AnswerText は解決した回答の文（選択肢で答えるときは見出し、添え書きがあれば「見出し・改行・添え書き」。選ばないときは Answer と同じ）。
	// Options・OptionRequired はその保留の選択肢の並びと「識別の無い回答を断る」印。
	AnswerText     string
	Options        []HoldOption
	OptionRequired bool
}

// PrepareAnswer は answer の①（読み取り）。answer が空なら、端末を開く前に
// ErrValidation で拒否する。課題が人間対応待ちでなければ ErrInvalidTransition
// （完了なら ErrTerminalState）。人間対応待ちなのに未回答の保留が無い場合は
// （状態機械の不変条件が破れているため）fail-closed にエラーを返す。
func (s *Store) PrepareAnswer(ctx context.Context, id string, answer string) (*AnswerPreview, error) {
	return s.PrepareAnswerOption(ctx, id, answer, "", false)
}

// PrepareAnswerOption は PrepareAnswer に、選ぶ選択肢の識別（option。空なら選ばない）を足した版。option が空でないときは answer が空でもよく
// （見出しが回答の文になる）、返す値の OptionLabel に解決した見出しが入る。保留の選択肢に無い識別・選択肢を持たない保留への識別・
// 識別の無い回答を断る印のある保留への識別の無い回答・識別と replan の併用は、何も書かず ErrValidation（本人確認の前に断る）。
func (s *Store) PrepareAnswerOption(ctx context.Context, id string, answer string, option string, replan bool) (*AnswerPreview, error) {
	if option != "" && replan {
		return nil, fmt.Errorf("%w: --option and --replan cannot be combined", ErrValidation)
	}
	if option == "" && strings.TrimSpace(answer) == "" {
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

		holdID, question, fromStatus, _, err := loadUnansweredHold(ctx, tx, cid)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errNoUnansweredHold(current.ID)
			}
			return err
		}
		opts, required, err := loadHoldOptionsOf(ctx, tx, holdID)
		if err != nil {
			return err
		}
		text, chosen, err := resolveAnswerOption(opts, required, option, answer)
		if err != nil {
			return err
		}
		label := ""
		if chosen != nil {
			for _, o := range opts {
				if o.ID == *chosen {
					label = o.Label
				}
			}
		}

		preview = AnswerPreview{
			Option:         option,
			OptionLabel:    label,
			AnswerText:     text,
			Options:        opts,
			OptionRequired: required,
			ChallengeID:    current.ID,
			Title:          current.Title,
			Question:       question,
			Answer:         answer,
			Version:        current.Version,
			FromStatus:     fromStatus,
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
	// HeadSources は、承認する組の head が検証の head と異なるとき、core が自分で比較して「既定ブランチの取り込みだけで
	// CI が通っている」ことを確かめるための取得の口。CLI の判定の申告は使わない。口が無い・確かめられない組は ErrConflict。
	HeadSources ReleaseHeadSources
	// Agent は、計画の承認で、枠のキーを持たない 1 個のタスクの枠を見直し前の決め方（サイズごとの既定）で決めるための宣言
	// （nil 可。nil のときそのタスクの枠は空のまま残り、読むときに見直し前の決め方の値になる）。計画の承認以外では使わない。
	Agent *AgentDeclaration
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

	// 検証の head と異なる head の承認は、core が自分で比較して確かめる（取得はトランザクションの外）。
	var assessed map[int64]*ReleaseHeadAssessment
	if req.Decision == ApprovalDecisionApproved && !req.HoldRelease && len(req.ReleaseHeads) > 0 {
		heads := make(map[int64]string, len(req.ReleaseHeads))
		for _, rh := range req.ReleaseHeads {
			oid, ok := parseOperationID(rh.Operation.ID)
			if !ok || rh.HeadSHA == nil {
				continue
			}
			heads[oid] = strings.TrimSpace(*rh.HeadSHA)
		}
		var err error
		if assessed, err = s.assessHeadsForApproval(ctx, heads, req.HeadSources); err != nil {
			return nil, nil, err
		}
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
		// 計画の承認は、答えの無い問いへの推奨の記録・タスクの行の作成・統合先を使うかの記録を、同じトランザクションで行う。
		if kind == ApprovalKindPlan && req.Decision == ApprovalDecisionApproved && planVersionOut != nil {
			if err := onPlanApproved(ctx, tc, att, int64(*planVersionOut), req.Agent); err != nil {
				return err
			}
		}
		// 本番反映のやり直し（#485）で置き換えられた前の完了の承認に、この承認を結ぶ。
		if kind == ApprovalKindCompletion && req.Decision == ApprovalDecisionApproved {
			if err := linkSupersededApprovals(ctx, tc.tx, tc.id, approvalRowID); err != nil {
				return err
			}
			// 課題が完了になるので、終わっていないタスク（オーケストレーターが直接行った完了を承認したときに残る）を取りやめる。
			if err := cancelUnfinishedTasks(ctx, tc.tx, tc.rec, tc.id); err != nil {
				return err
			}
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
			ID:            formatApprovalID(approvalRowID),
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

		// 親が直接した完了の差し戻し（T15 の戻り先が人間対応待ち）は、「親が直接やる」保留を立てて戻す。
		// 問いの文は差し戻しの理由（#481）。
		if req.Decision == ApprovalDecisionRejected && tc.target == StatusAwaitingHuman {
			if err := raiseParentDirectHoldOnReject(ctx, tc, *req.Reason); err != nil {
				return err
			}
		}

		// 完了の差し戻しは、その課題の未実行の release を同じトランザクションで取り消す。
		if kind == ApprovalKindCompletion && req.Decision == ApprovalDecisionRejected {
			// 課題が着手中へ戻るとき、承認済みの計画の版の done のタスクを in_progress にする。
			// オーケストレーターの直接の完了の差し戻し（保留へ戻る）ではタスクの状態を変えない。
			// 統合先を使う課題は、計画を作り直す道へ送られ（分類済へ戻る）、タスクを済みのまま残す。
			if tc.target == StatusInProgress {
				if err := reopenDoneTasks(ctx, tc.tx, tc.rec, tc.id); err != nil {
					return err
				}
			}
			withdrawn, running, err := withdrawUnexecutedReleases(ctx, tc)
			if err != nil {
				return err
			}
			approvalOut.WithdrawnReleases, approvalOut.RunningReleases = withdrawn, running
			if len(withdrawn) > 0 {
				tc.after["withdrawn_releases"] = withdrawn
			}
			if len(running) > 0 {
				tc.after["running_releases"] = running
			}
		}

		// D12: 完了の承認（HoldRelease==false）は、同じトランザクションで
		// その課題の未承認の release をすべて自動承認する。完了の承認と
		// release ごとの承認は、それぞれ別の approval 行・別の作業ログの
		// エントリとして残る（この if の外で1件だけ積んだ上の approvalOut・
		// activity とは別に、release の数だけ積む）。T14（HoldRelease==true）・
		// 計画の承認（kind==ApprovalKindPlan）・reject では行わない。
		if approvesPendingReleases(kind, req.Decision, req.HoldRelease) {
			if err := autoApproveReleases(ctx, tc, att, req, assessed); err != nil {
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
func autoApproveReleases(ctx context.Context, tc *transitionCtx, att Attestation, req ApprovalRequest, assessed map[int64]*ReleaseHeadAssessment) error {
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
	recordMergeIn := make(map[int64]string, len(pending))
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
		ok, record, _ := releaseHeadVerdict(verified, head, assessed[p.id], false)
		if !ok {
			return ErrConflict
		}
		if record {
			recordMergeIn[p.id] = *verified
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
		if _, _, err := approveOperationInTx(ctx, tc.tx, tc.rec, operationApprovalWrite{
			opID:        p.id,
			challengeID: tc.id,
			version:     p.version,
			decision:    ApprovalDecisionApproved,
			att:         att,
			nowStr:      tc.nowStr,
			headSHA:     head,
			mergeInHead: recordMergeIn[p.id],
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
	// Replan は回答に付ける「範囲が変わる」印。真なら、委譲を再開せず、課題を計画の作り直し（分類済）へ戻す。
	// 保留の直前の状態が着手中・検証中でなければ ErrValidation（何も書かない）。
	Replan bool
	// Option は回答で選ぶ選択肢の識別（空なら選ばない）。空でなければ Answer は添え書き（空でもよい）で、回答の文は選んだ選択肢の
	// 見出し（添え書きがあれば「見出し・改行・添え書き」）。保留の選択肢から解決し直して記録する（answer_option）。
	// 保留に無い識別・選択肢を持たない保留への識別・Replan との併用・識別の無い回答を断る印のある保留への識別の無い回答は、
	// ErrValidation（何も書かない）。
	Option string
	// Agent・Conn は、足す依頼の保留で「足す」を選んだとき、保存した出力を検査する宣言（nil なら core が作業ディレクトリの宣言を読む）。
	Agent *AgentDeclaration
	Conn  *ConnectorsDeclaration
}

// ExecuteAnswer は answer（T12）の②（書き込み）。「保留に入る直前の状態」は
// 版が一致していることを確認した直後、このトランザクションの中で改めて
// 「その時点で未回答の保留」を読んで決める（PrepareAnswer と同じクエリ）。
// ExpectedVersion が一致していれば、Prepare の時点から保留の集合は変わって
// いない（保留を増減させる操作は必ず課題の版も進めるため）ので、この
// 再読み込みは Prepare が見たのと同じ行を指す。
func (s *Store) ExecuteAnswer(ctx context.Context, req AnswerRequest, att Attestation) (*Challenge, *Hold, error) {
	if req.Option == "" && strings.TrimSpace(req.Answer) == "" {
		return nil, nil, ErrValidation
	}
	if req.Option != "" && req.Replan {
		return nil, nil, fmt.Errorf("%w: --option and --replan cannot be combined", ErrValidation)
	}

	if !attestationPermits(att, req.ChallengeID, actionAnswer) {
		return nil, nil, ErrVerificationRejected
	}

	answerText := req.Answer
	var chosenOption *string
	var holdOut Hold
	var holdID int64
	var holdReasonValue holdReason
	var holdTaskID sql.NullInt64
	// replan は計画を作り直す回答か（--replan、または足す依頼の保留で選んだ「計画を作り直す」）。
	replan := req.Replan

	resolvePreceding := func(ctx context.Context, tx *sql.Tx, cid int64, current *Challenge) (Status, error) {
		id, question, fromStatus, raisedAt, err := loadUnansweredHold(ctx, tx, cid)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", errNoUnansweredHold(current.ID)
			}
			return "", err
		}
		holdID = id
		opts, required, err := loadHoldOptionsOf(ctx, tx, id)
		if err != nil {
			return "", err
		}
		answerText, chosenOption, err = resolveAnswerOption(opts, required, req.Option, req.Answer)
		if err != nil {
			return "", err
		}
		replan = req.Replan || isTaskAdditionReplanChoice(ctx, tx, id, chosenOption)
		if replan {
			// 作業している契約の実行（開いていて、最後に読んだ状態が running のタスクの実行。「入れてよい」を返して委譲先が受け取った実行は、終わるか
			// レビュー待ちへ戻るまで running を返すので含まれる。保留の間の成功が未処理のものも含む）と、「入れてよい」を返そうとして届いたか分からない実行
			// （通す中身があり、届いた記録が無く、最後に読んだ状態が終わりでない。委譲先が入れている最中かもしれない。次の状態の確認で決まれば解ける）があるときは、
			// 動いている作業が作り直す前の計画のまま進む・検証中へ進むので、印は成立させない。
			// 届いた記録のあるレビュー待ち（land_refused）・問い待ちのタスクの実行、終わりを読んだ実行と、上げる依頼は、作り直した計画の承認の後に取り消すので、あっても受ける。
			var working int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND kind = ? AND (
				(ended_at IS NULL AND state = ?)
				OR (ended_at IS NULL AND COALESCE(land_ref, '') <> '' AND land_delivered = 0 AND state NOT IN (?, ?, ?))
				OR held_success = 1)`,
				cid, contractRunKindTask, string(ContractStateRunning),
				string(ContractStateSucceeded), string(ContractStateFailed), string(ContractStateCancelled)).Scan(&working); err != nil {
				return "", err
			}
			if working > 0 {
				return "", fmt.Errorf("%w: replan is not allowed while a contract run is working: %s", ErrValidation, workingTaskNames(ctx, tx, cid))
			}
		}
		var runID sql.NullInt64
		var kind string
		var reason sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT run_id, kind, reason, task_id FROM hold WHERE id = ?`, id).Scan(&runID, &kind, &reason, &holdTaskID); err != nil {
			return "", err
		}
		holdReasonValue = holdReason(reason.String)
		holdOut = Hold{Kind: HoldKind(kind), Question: question, FromStatus: fromStatus, RaisedAt: raisedAt, Options: opts, OptionRequired: required}
		if runID.Valid {
			v := formatRunID(runID.Int64)
			holdOut.RunID = &v
		}
		return fromStatus, nil
	}

	apply := func(ctx context.Context, tc *transitionCtx) error {
		relaySession, relayQuote := att.relayColumns()
		// 選ばれた識別を読む使う側は、回答を記録する前（保留がまだ開いている間）に呼ぶ。開いている保留の書き換え（editHoldText）が使える。
		if chosenOption != nil && s.answerOptionUse != nil {
			if err := s.answerOptionUse(ctx, tc, holdID, *chosenOption); err != nil {
				return err
			}
		}
		if chosenOption != nil && holdReasonValue == holdReasonTaskAddOverLimit {
			if err := applyTaskAdditionAnswer(ctx, tc, att, s.answerDecls(req), holdID, *chosenOption); err != nil {
				return err
			}
		}
		res, err := tc.tx.ExecContext(ctx,
			`UPDATE hold SET answer = ?, answered_at = ?, answered_by = ?, answered_channel = ?, answered_verification = ?, relay_session = ?, relay_quote = ?, replan = ?, answer_option = ? WHERE id = ? AND answer IS NULL AND closed_at IS NULL`,
			answerText, tc.nowStr, att.Actor(), string(att.Channel()), string(att.Verification()), relaySession, relayQuote, boolToInt(replan), stringPtrColumn(chosenOption), holdID,
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
		tc.after["answer"] = answerText
		if chosenOption != nil {
			tc.after["answer_option"] = *chosenOption
		}
		if replan {
			tc.after["replan"] = true
		}
		att.addRelayTo(tc.after)

		answer := answerText
		holdOut.AnswerOption = chosenOption
		answeredAt := tc.now
		answeredBy := att.Actor()
		holdOut.Answer = &answer
		holdOut.AnsweredAt = &answeredAt
		holdOut.AnsweredBy = &answeredBy
		ch, ver := string(att.Channel()), string(att.Verification())
		holdOut.AnsweredChannel, holdOut.AnsweredVerification = &ch, &ver
		holdOut.RelaySession, holdOut.RelayQuote = att.relayPtrs()
		holdOut.Replan = replan
		// 理由 facts_unmet の保留への回答は、満たさなかった回数を 0 に戻す（次に読んだレビュー待ちから数え直す）。
		// 戻す対象は、保留が結び付くタスクの契約の実行。保留がタスクに結び付かないときは、その課題の終わっていないタスクの契約の実行。
		if holdReasonValue == holdReasonFactsUnmet {
			var err error
			if holdTaskID.Valid {
				_, err = tc.tx.ExecContext(ctx, `UPDATE contract_run SET facts_unmet_count = 0 WHERE challenge_id = ? AND task_id = ?`, tc.id, holdTaskID.Int64)
			} else {
				_, err = tc.tx.ExecContext(ctx, `UPDATE contract_run SET facts_unmet_count = 0 WHERE challenge_id = ?
					AND task_id IN (SELECT id FROM task WHERE challenge_id = ? AND status NOT IN ('done', 'cancelled'))`, tc.id, tc.id)
			}
			if err != nil {
				return err
			}
		}
		if holdTaskID.Valid && !replan {
			return resumeIntegrationTaskAfterAnswer(ctx, tc, holdReasonValue, holdTaskID.Int64)
		}
		return nil
	}

	c, err := s.verifiedTransitionReplan(ctx, att, req.ChallengeID, req.ExpectedVersion, OpAnswer, NoStatus, resolvePreceding, req.Replan, func() bool { return replan }, apply)
	if err != nil {
		return nil, nil, err
	}
	return c, &holdOut, nil
}

// withdrawUnexecutedReleases は完了の差し戻しと同じトランザクションで、tc.id の課題の未実行の release
// （承認待ち・承認済み）を closed（理由 challenge_rejected）にし、作業ログへ理由つきで残す。差し戻した
// 課題を直して再び完了を承認すると新しい release が立つので、取り消した行は履歴として残す。
// 試行が終わっていない（マージを依頼している最中の）release は止めず、running として返す。
func withdrawUnexecutedReleases(ctx context.Context, tc *transitionCtx) (withdrawn, running []string, err error) {
	rows, err := tc.tx.QueryContext(ctx,
		`SELECT id, version, state FROM operation WHERE challenge_id = ? AND kind = ? AND state IN (?, ?) ORDER BY id ASC`,
		tc.id, string(OperationKindRelease), string(OperationStatePending), string(OperationStateApproved))
	if err != nil {
		return nil, nil, err
	}
	type target struct {
		id      int64
		version int
		state   string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.version, &t.state); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	for _, t := range targets {
		action, stale, err := releaseOnCompletionRejection(ctx, tc.tx, t.id, tc.now)
		if err != nil {
			return nil, nil, err
		}
		switch action {
		case rejectReleaseKeep:
			continue
		case rejectReleaseRunning:
			running = append(running, formatOperationID(t.id))
			continue
		}
		if stale != nil {
			// 止まった試行は取り下げ（withdraw）・`op exec` の再実行と同じく interrupted として閉じてから行を閉じる。
			if err := closeOperationExecution(ctx, tc.tx, stale.ID, closeOperationExecutionInput{EndedAt: tc.now, Result: ExecutionResultInterrupted}); err != nil {
				return nil, nil, err
			}
		}
		res, err := tc.tx.ExecContext(ctx,
			`UPDATE operation SET state = ?, version = ?, closed_reason = ?, closed_at = ? WHERE id = ? AND version = ? AND state = ?`,
			string(OperationStateClosed), t.version+1, string(ClosedReasonChallengeRejected), tc.nowStr, t.id, t.version, t.state)
		if err != nil {
			return nil, nil, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return nil, nil, err
		} else if n != 1 {
			return nil, nil, fmt.Errorf("core: reject completion: expected to close operation %s, updated %d rows", formatOperationID(t.id), n)
		}
		if err := tc.rec.record("operation", t.id, ActivityActionOpClose,
			map[string]any{"state": t.state},
			map[string]any{"state": string(OperationStateClosed), "version": t.version + 1, "closed_reason": string(ClosedReasonChallengeRejected),
				"reason": "課題の完了が差し戻されたため、未実行の本番反映を取り消した"}); err != nil {
			return nil, nil, err
		}
		withdrawn = append(withdrawn, formatOperationID(t.id))
	}
	return withdrawn, running, nil
}

// rejectReleaseAction は完了の差し戻しで未実行の release 1 件をどう扱うか。
type rejectReleaseAction int

const (
	// rejectReleaseWithdraw は取り消す（closed・challenge_rejected）。
	rejectReleaseWithdraw rejectReleaseAction = iota
	// rejectReleaseRunning は試行が終わっていない（止まっていない）ので止めずに残す（マージされうる）。
	rejectReleaseRunning
	// rejectReleaseKeep は直前の試行が merged_unapproved で、本番反映の片付け（release_tidy）に任せて触らない。
	rejectReleaseKeep
)

// releaseOnCompletionRejection は完了の差し戻しでの未実行の release（opID）の扱いを決める規則の正本。
// 差し戻しの要約（prepareApproval）と差し戻しの書き込み（withdrawUnexecutedReleases）が共有する。
// 取り消す release に止まった（stale の）試行があれば、それを stale で返す（書き込み側が interrupted として閉じる）。
func releaseOnCompletionRejection(ctx context.Context, tx *sql.Tx, opID int64, now time.Time) (rejectReleaseAction, *operationExecutionRow, error) {
	last, err := latestOperationExecution(ctx, tx, opID)
	if err != nil {
		return 0, nil, err
	}
	switch {
	case last == nil:
		return rejectReleaseWithdraw, nil, nil
	case last.Result == ExecutionResultMergedUnapproved:
		return rejectReleaseKeep, nil, nil
	case !last.open():
		return rejectReleaseWithdraw, nil, nil
	case !releaseAttemptStale(last, now):
		return rejectReleaseRunning, nil, nil
	default:
		return rejectReleaseWithdraw, last, nil
	}
}

package core

// このファイルは委譲の起動（親要件チケット #98 §J3 ブリーフと委譲の起動・
// §意思決定の主体の判定。決定 M3H4・M3P5・M3P21）の本体 Store.RunDelegation を持つ。
//
// 1 件の流れ:
//
//	① 課題・承認済みの計画・宣言を読み、意思決定者と行を決める（DecideDecider）
//	② J3（課題に固有のブリーフ）を RunJudgment で起動する。上流の本文・コメントは
//	   この直前に取得する（読んだ記録は付けない）
//	③ J3 の出力を検査する。不正なら run を invalid_output にし、委譲は起動しない
//	④ スロットを割り当て、同じトランザクションで委譲の run を記録する（AcquireSlot）
//	⑤ トランザクションの外で委譲の子を待つ（heartbeat を更新する）
//	⑥ 結果と費用を記録し、スロットを解放する
//
// 照合（delegate_reconcile.go）はここに含める。結末の課題の状態への写像（#104）、衝突の
// 予測と直列化グループ（#106）はここに含めない。固定の節の組み立て（埋め込みの雛形）
// と claude の起動は internal/invoker が持ち、core は DelegationInvoker の IF だけを
// 知る（core は invoker を import しない）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

// DelegateLaunchInput は Store.RunDelegation が DelegationInvoker へ渡す、委譲の
// 起動 1 回ぶんの情報。ストアのハンドルは含まない。
type DelegateLaunchInput struct {
	// SessionID は、この run が使うセッションの ID（小文字 UUID。core が採番する）。
	SessionID string
	// Workspace はワークスペース（出力の保存の記録に使う）。
	Workspace string
	// WorkDir は委譲の作業ディレクトリ（割り当てたスロットの作業ツリー）。
	WorkDir string
	// RunDir は標準出力・標準エラー・渡した入力の保存先（.flywheel/runs/<run の ID>/）。
	RunDir string
	// OutputSchema は --json-schema に渡す委譲の報告のスキーマ。
	OutputSchema []byte
	// MaxBudgetUSD は --max-budget-usd に渡す上限額（USD）。
	MaxBudgetUSD float64
	// TimeoutSec は 1 回の起動の時間の上限（秒。宣言の timeout_sec.delegate）。
	TimeoutSec int
	// PermissionMode は接続ツールの宣言の権限モード。
	PermissionMode PermissionMode
	// Decider・DeciderRow は意思決定者と該当した行（ブリーフの冒頭に書く）。
	Decider    Decider
	DeciderRow int
	// Brief は J3 の出力（課題に固有の部分）。
	Brief string
	// SourceIssueNumber・SourceIssueURL は取り込み元の Issue（対応が無ければ 0・空）。
	// 固定の節が求める `Closes #<Issue 番号>` の番号の出所になる。
	SourceIssueNumber int
	SourceIssueURL    string
	// Invocation は形態 plugin の操作の invocation の差し込みを埋めた文字列
	// （形態 brief、または invocation が無い操作は空）。
	Invocation string
	// IsResume が true なら、SessionID は再開元のセッションの ID で、`--resume` で起動する
	// （Brief・Invocation・Decider は使わない。固定の文面と ResumeAnswer・ResumeBranch を渡す）。
	IsResume bool
	// ResumeKind は IsResume のとき、渡す固定の文面の種類。
	ResumeKind ResumeKind
	// ResumeAnswer は ResumeKind が answer のときの人間の回答、ResumeQuestion はその保留の問い。
	// ResumeFeedback は ResumeKind が rework のときの J5 の差し戻しの指摘。
	ResumeAnswer   string
	ResumeQuestion string
	ResumeFeedback string
	// ResumeBranch は再開のブリーフに書く、報告のブランチ名（無ければ空）。
	ResumeBranch string
}

// DelegationInvoker は core が定義する、委譲の起動 1 回の実行の IF
// （internal/invoker.Launcher が実装する）。結果の判別・費用の抽出は
// JudgmentInvoker と同じ形（JudgmentLaunchOutput）で返す。
type DelegationInvoker interface {
	InvokeDelegation(ctx context.Context, in DelegateLaunchInput) (JudgmentLaunchOutput, error)
}

// DelegateInput は Store.RunDelegation の入力。
type DelegateInput struct {
	// ChallengeID が非 nil なら、その課題 1 件だけを対象にする（ID を指定した個別の
	// 操作。状態の条件は適用し、上流の状態による除外は適用しない）。nil なら、
	// 着手中で承認済みの計画を持ち、終了していない run を持たない課題すべてを、
	// 優先度→ID の昇順に処理する。
	ChallengeID *string
	AgentDecl   *AgentDeclaration
	ConnDecl    *ConnectorsDeclaration
	// Judgment は J3 の起動、Delegate は委譲の起動の実行者。
	Judgment JudgmentInvoker
	Delegate DelegationInvoker
	// Upstream は J3 の起動の直前の上流の取得（取り込み元の対応の無い課題では呼ばれない）。
	Upstream UpstreamThreadSource
	// Git はスロットの作業ツリーの検査と払い出しの口。
	Git SlotGit
	// Reconcile は委譲の後の照合が呼ぶ、リモートのブランチと head ブランチの PR の取得
	// （GET だけ）。
	Reconcile UpstreamBranchSource
	// Predictor は衝突の予測の口の起動（nil なら予測の口を呼べず、予測が要るリポジトリの
	// 委譲の候補は 1 つの直列化グループに入る）。
	Predictor ConflictPredictor
	// CycleID はこの操作が属する周（BeginCycle が返した ID）。空は ErrValidation。
	CycleID string
	// Cycle が非 nil なら、枠超過の状態をこの JudgmentCycle と共有する。
	Cycle *JudgmentCycle
}

// DelegateResult は Store.RunDelegation の出力。Items の Outcome は、委譲の run が
// succeeded のときの報告の結末（completed|questions|blocked）。Status は、照合が課題の
// 状態を変えたときのその状態（変えなければ nil）。
type DelegateResult = JudgmentAutoResult

// errDelegationNotEligible は、スロットを割り当てる直前に読み直した課題が、委譲の
// 対象でなくなっていた（着手中でなくなった）ことを表す。
var errDelegationNotEligible = fmt.Errorf("%w: the challenge is no longer in progress", ErrInvalidTransition)

// approvedPlan は課題の承認済みの計画（最後に承認された版）。HasSpec は構造化した出力
// （J2 の出力）を持つか（人が `plan --file` で登録した計画は持たない）。
type approvedPlan struct {
	Version int
	Body    string
	Spec    string
	HasSpec bool
}

// delegationContext は 1 件の委譲の起動の前に読む、課題と承認済みの計画と宣言の事実。
type delegationContext struct {
	Challenge Challenge
	Plan      approvedPlan
	Holds     []Hold
	Binding   *sourceBinding
	Validated *j2ValidatedOutput
	Repo      ConnectorRepo
	Connector Connector
	Operation ConnectorOperation
	// ImplBudgetMicros は実装の委譲に渡す上限額・予約額（その計画の版の実装枠の残り）。
	// ReviewBudgetMicros はレビュー対応枠の残りで、評価額（実装枠の残りと足した額）にだけ使う。
	ImplBudgetMicros   int64
	ReviewBudgetMicros int64
	Decider            Decider
	DeciderRow         int
	Invocation         string
	// Resume が非 nil なら `--resume` で起動する（J3 は呼ばない）。LimitHit が非 nil なら
	// 連続失敗の上限に達しており、委譲を起動しない。
	Resume   *resumePlan
	LimitHit *failureLimitHit
	// ReworkHit が非 nil なら差し戻しの上限に達しており、委譲を起動しない。
	ReworkHit *reworkLimitHit
	// ResumeBranchPushed は Resume のブランチがリモートにあるか（再開のスロットの選び方に使う）。
	ResumeBranchPushed bool
}

// estimateMicros は周の上限の評価額（実装枠の残り ＋ レビュー対応枠の残り）。`--max-budget-usd` と
// 予約額には実装枠の残りだけを使う（ImplBudgetMicros）。
func (dc *delegationContext) estimateMicros() int64 {
	return dc.ImplBudgetMicros + dc.ReviewBudgetMicros
}

// loadApprovedPlanAnySpec は challengeID の承認済みの計画（計画の承認の最後の承認が指す版）を、
// 構造化した出力の有無を問わずに返す。承認が無い・版が無いときは ok=false。
func loadApprovedPlanAnySpec(ctx context.Context, tx *sql.Tx, challengeID int64) (approvedPlan, bool, error) {
	approvals, err := loadApprovals(ctx, tx, challengeID)
	if err != nil {
		return approvedPlan{}, false, err
	}
	version := 0
	for _, a := range approvals {
		if a.Kind == ApprovalKindPlan && a.Decision == ApprovalDecisionApproved {
			version = a.TargetVersion
		}
	}
	if version == 0 {
		return approvedPlan{}, false, nil
	}
	plans, err := loadPlans(ctx, tx, challengeID)
	if err != nil {
		return approvedPlan{}, false, err
	}
	for _, p := range plans {
		if p.Version == version {
			ap := approvedPlan{Version: p.Version, Body: p.Body}
			if p.Spec != nil {
				ap.Spec, ap.HasSpec = *p.Spec, true
			}
			return ap, true, nil
		}
	}
	return approvedPlan{}, false, nil
}

// loadApprovedPlan は loadApprovedPlanAnySpec のうち、構造化した出力を持つ計画だけを返す
// （委譲は構造化した出力のない計画を扱えない）。
func loadApprovedPlan(ctx context.Context, tx *sql.Tx, challengeID int64) (approvedPlan, bool, error) {
	p, ok, err := loadApprovedPlanAnySpec(ctx, tx, challengeID)
	if err != nil || !ok || !p.HasSpec {
		return approvedPlan{}, false, err
	}
	return p, true, nil
}

// selectDelegationTargets は ID を省略した `run` の対象の課題の内部整数 ID を、優先度→ID の
// 昇順で返す。対象は着手中で、構造化した出力を持つ承認済みの計画があり、終了していない run を
// 持たず、上流の状態が closed・missing・ポリシーの状態が out_of_policy でない課題。
func selectDelegationTargets(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	challenges, err := loadChallengesByStatusSorted(ctx, tx, StatusInProgress)
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, ch := range challenges {
		cid, ok := parseChallengeID(ch.ID)
		if !ok {
			continue
		}
		active, err := challengeHasActiveRun(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if active {
			continue
		}
		excluded, err := challengeAutoExcludedByPolicy(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if excluded {
			continue
		}
		if _, ok, err := loadApprovedPlan(ctx, tx, cid); err != nil {
			return nil, err
		} else if !ok {
			continue
		}
		out = append(out, cid)
	}
	return out, nil
}

// fillInvocation は操作の invocation の差し込み（閉集合の 4 つ）を埋める。
func fillInvocation(invocation string, challengeID string, sb *sourceBinding) (string, error) {
	out := strings.ReplaceAll(invocation, "{challenge_id}", challengeID)
	if invocationNeedsSource(out) {
		if sb == nil {
			return "", fmt.Errorf("%w: the operation needs the source issue but the challenge has none", ErrValidation)
		}
		_, number, ok := parseExternalKey(sb.ExternalKey)
		if !ok {
			return "", fmt.Errorf("%w: malformed external_key %q", ErrValidation, sb.ExternalKey)
		}
		out = strings.ReplaceAll(out, "{issue_number}", fmt.Sprintf("%d", number))
		out = strings.ReplaceAll(out, "{issue_url}", sb.URL)
		out = strings.ReplaceAll(out, "{external_key}", sb.ExternalKey)
	}
	return out, nil
}

// loadDelegationContext は ch の委譲の前提を読む。承認済みの計画に構造化した出力が無い・
// 宣言と突き合わせて不正なら ErrValidation（人が登録した計画は委譲できない）。
func (s *Store) loadDelegationContext(ctx context.Context, in DelegateInput, ch Challenge) (*delegationContext, error) {
	cid, ok := parseChallengeID(ch.ID)
	if !ok {
		return nil, ErrNotFound
	}
	dc := &delegationContext{Challenge: ch}
	var override *planBudgetOverride
	var spend bucketSpend
	var history *launchHistory
	var planOK bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		p, ok, err := loadApprovedPlan(ctx, tx, cid)
		if err != nil {
			return err
		}
		dc.Plan, planOK = p, ok
		if !ok {
			return nil
		}
		if dc.Holds, err = loadHolds(ctx, tx, cid); err != nil {
			return err
		}
		if dc.Binding, err = loadSourceBindingByChallengeID(ctx, tx, cid); err != nil {
			return err
		}
		if override, err = loadPlanBudgetOverride(ctx, tx, cid, int64(p.Version)); err != nil {
			return err
		}
		if spend, err = loadBucketSpend(ctx, tx, cid, int64(p.Version)); err != nil {
			return err
		}
		history, err = loadLaunchHistory(ctx, tx, cid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if !planOK {
		return nil, fmt.Errorf("%w: the challenge has no approved plan with a structured spec", ErrValidation)
	}

	validated, ok := validateJ2Output([]byte(dc.Plan.Spec), j2ValidationContext{Agent: in.AgentDecl, Conn: in.ConnDecl, HasSource: dc.Binding != nil})
	if !ok || validated.Verdict != J2VerdictPlan {
		return nil, fmt.Errorf("%w: the approved plan does not match the current declarations", ErrValidation)
	}
	dc.Validated = validated
	repo, connector := in.ConnDecl.findConnectorRepo(validated.Repo)
	if repo == nil || connector == nil {
		return nil, fmt.Errorf("%w: unknown repository %q", ErrValidation, validated.Repo)
	}
	dc.Repo, dc.Connector = *repo, *connector
	for _, op := range connector.Operations {
		if op.ID == validated.Operation {
			dc.Operation = op
		}
	}
	rem := computeBucketRemaining(applyBudgetOverride(validated.ResolvedImplUSD, validated.ResolvedReviewUSD, override), spend)
	dc.ImplBudgetMicros, dc.ReviewBudgetMicros = rem.Impl, rem.Review
	dc.Decider, dc.DeciderRow = DecideDecider(dc.Operation, validated.CrossRepo, validated.RelatedRepos)
	dc.Resume, dc.LimitHit = history.decideLaunch(dc.Plan.Version, in.AgentDecl.FailureLimit)
	if rework, hit := history.decideRework(dc.Plan.Version, in.AgentDecl.ReworkLimit); hit != nil {
		dc.ReworkHit = hit
	} else if rework != nil && dc.LimitHit == nil {
		dc.Resume = rework
	}
	// 費用が取れない失敗の後の再開に限り、失敗前の実装枠の残りを上限に 1 回だけ起動を許す。
	// 人間が flywheel budget で枠を置き直して 1 USD 以上が残っているときは、その額に従う。
	if dc.ImplBudgetMicros < minImplLaunchMicros && dc.LimitHit == nil && dc.ReworkHit == nil {
		if grant := history.resumeBudgetGrant(dc.Plan.Version, dc.Resume); grant > dc.ImplBudgetMicros {
			dc.ImplBudgetMicros = grant
		}
	}
	if dc.Resume != nil {
		err := s.db.Read(ctx, func(tx *sql.Tx) error {
			b, err := loadRunBranch(ctx, tx, dc.Resume.Target, in.ConnDecl.HumanQuestionKinds, dc.Repo.DefaultBranch)
			dc.Resume.Branch = b
			return err
		})
		if err = classifyReadWriteErr(err); err != nil {
			return nil, err
		}
	}
	if connector.Form == ConnectorFormPlugin {
		inv, err := fillInvocation(dc.Operation.Invocation, ch.ID, dc.Binding)
		if err != nil {
			return nil, err
		}
		dc.Invocation = inv
	}
	return dc, nil
}

// prepareJ3 は J3 の入力（データの区画）を組み立てる。取り込み元の対応のある課題では、
// ここ（J3 の起動の直前）で上流の本文・全コメント・参照先を取得する。読んだ記録は付けない
// （J3 の後も課題の読んだ時点の値は変わらない）。取得に失敗したら NotStarted
// （upstream_fetch_failed）を返し、run を作らない。
func (s *Store) prepareJ3(ctx context.Context, in DelegateInput, dc *delegationContext) ([]JudgmentDataSection, *NotStarted, error) {
	ch := dc.Challenge
	urgency, priority := "", ""
	if ch.Urgency != nil {
		urgency = string(*ch.Urgency)
	}
	if ch.Priority != nil {
		priority = string(*ch.Priority)
	}
	sections := []JudgmentDataSection{
		{Label: "課題", Content: fmt.Sprintf(
			"タイトル: %s\n\n説明:\n%s\n\n達成条件:\n%s\n\n緊急度: %s\n優先度: %s\n起票者: %s",
			ch.Title, ch.Description, ch.DoneCriteria, urgency, priority, ch.Reporter)},
		{Label: "承認済みの計画の本文", Content: dc.Plan.Body},
		{Label: "承認済みの計画の構造化した出力", Content: dc.Plan.Spec},
	}
	if t := formatHoldsSection(dc.Holds); t != "" {
		sections = append(sections, JudgmentDataSection{Label: "保留の記録", Content: t})
	}
	if t := formatSourceBindingSection(dc.Binding); t != "" {
		sections = append(sections, JudgmentDataSection{Label: "取り込み元の対応", Content: t})
	}
	if dc.Binding != nil {
		repo, number, ok := parseExternalKey(dc.Binding.ExternalKey)
		if !ok {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedUpstreamFetchFailed,
				Detail: fmt.Sprintf("malformed external_key %q", dc.Binding.ExternalKey)}, nil
		}
		uc, err := FetchUpstreamContext(ctx, in.Upstream, repo, number)
		if err != nil {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedUpstreamFetchFailed, Detail: err.Error()}, nil
		}
		sections = append(sections, buildUpstreamInput(uc).Sections...)
	}
	sections = append(sections,
		JudgmentDataSection{Label: "操作", Content: fmt.Sprintf(
			"リポジトリ: %s\n接続ツール: %s（形態: %s）\n操作の識別子: %s",
			dc.Repo.Name, dc.Connector.ID, dc.Connector.Form, dc.Operation.ID)},
		JudgmentDataSection{Label: "意思決定者", Content: fmt.Sprintf("意思決定者: %s\n該当した行: %d", dc.Decider, dc.DeciderRow)},
	)
	return sections, nil, nil
}

// RunDelegation は `flywheel run [<C-ID>]` の本体（J3 と委譲の起動）。対象の選び方・J3 の
// 入力・出力の検査・意思決定者の判定・スロットの割り当て・委譲の run の記録は core が持ち、
// 呼び出し元（internal/cli）は宣言の読み込み・invoker と取得と git の組み立て・周の開始と
// 終了だけを行う（P2）。
//
//   - in.ChallengeID が非 nil: 着手中でなければ ErrInvalidTransition、終了していない run が
//     あれば ErrRunInProgress、使えるスロットが無ければ ErrSlotUnavailable、周の上限で起動
//     できなければ ErrBudgetExceeded を返す。
//   - in.ChallengeID が nil: 対象を優先度→ID の昇順に順に処理する。枠超過・周の上限・
//     スロットが無い・上流の取得の失敗で起動しなかった課題は NotStarted へ入れる。
func (s *Store) RunDelegation(ctx context.Context, in DelegateInput) (*DelegateResult, error) {
	if in.AgentDecl == nil || in.ConnDecl == nil || in.Judgment == nil || in.Delegate == nil ||
		in.Upstream == nil || in.Git == nil || in.Reconcile == nil || in.CycleID == "" {
		return nil, ErrValidation
	}
	if in.Cycle != nil && in.Cycle.cycleID != in.CycleID {
		return nil, ErrValidation
	}
	if in.ChallengeID != nil {
		ch, err := s.loadChallengeForAuto(ctx, *in.ChallengeID)
		if err != nil {
			return nil, err
		}
		if ch.Status != StatusInProgress {
			return nil, ErrInvalidTransition
		}
		cid, _ := parseChallengeID(ch.ID)
		var active bool
		err = s.db.Read(ctx, func(tx *sql.Tx) error {
			a, err := challengeHasActiveRun(ctx, tx, cid)
			active = a
			return err
		})
		if err = classifyReadWriteErr(err); err != nil {
			return nil, err
		}
		if active {
			return nil, ErrRunInProgress
		}
		return s.runSingleDelegation(ctx, in, *ch)
	}

	var targetIDs []int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ids, err := selectDelegationTargets(ctx, tx)
		targetIDs = ids
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	jc := in.Cycle
	if jc == nil {
		jc = NewJudgmentCycle(in.CycleID)
	}
	// 枠超過を記録した周は、残りの判断の呼び出しと委譲を起動しない。予測の口の呼び出しも同じで、
	// 起動しない候補のために費用を積まない（予測の口が無いときと同じ扱いにする）。
	if jc.RateLimited() {
		in.Predictor = nil
	}
	plan, err := s.planDelegation(ctx, in, targetIDs)
	if err != nil {
		return nil, err
	}
	return s.executePlan(ctx, in, jc, plan)
}

func appendDelegateOutcome(r *DelegateResult, item *JudgmentAutoItem, notStarted *NotStarted) {
	if notStarted != nil {
		r.NotStarted = append(r.NotStarted, *notStarted)
	}
	if item != nil {
		r.Items = append(r.Items, *item)
	}
}

// delegateOne は課題 1 件の J3 と委譲を実行する。jc が非 nil なら周の枠超過・上限・スロットの
// 不足を NotStarted で返し、nil（ID を指定した個別の操作）ならエラーで返す。item と
// notStarted はどちらか一方だけが非 nil（エラー時は両方 nil）。
func (s *Store) delegateOne(ctx context.Context, in DelegateInput, jc *JudgmentCycle, ch Challenge, run *delegateRun) (*JudgmentAutoItem, *NotStarted, error) {
	if jc != nil && jc.RateLimited() {
		return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedRateLimited}, nil
	}
	dc, err := s.loadDelegationContext(ctx, in, ch)
	if err != nil {
		return nil, nil, err
	}

	if dc.LimitHit != nil {
		return s.holdForFailureLimit(ctx, dc)
	}
	if dc.ReworkHit != nil {
		return s.holdForReworkLimit(ctx, dc)
	}

	// 実装枠の残りが起動の最小額に満たなければ、J3 の費用を払う前に起動しない。
	if dc.ImplBudgetMicros < minImplLaunchMicros {
		if jc != nil {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedRunBudget,
				Detail: budgetShortfallDetail(bucketRemaining{Impl: dc.ImplBudgetMicros, Review: dc.ReviewBudgetMicros})}, nil
		}
		return nil, nil, ErrBudgetExceeded
	}

	// 同時の起動の上限と使えるスロットの枠。空くまで同じ周の中で待つ（J3 の費用を払う前）。
	adm, err := run.admit(ctx, jc)
	if err != nil {
		if jc != nil && errors.Is(err, ErrSlotUnavailable) {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedSlotUnavailable}, nil
		}
		return nil, nil, err
	}
	defer adm.finish()
	// 待っている間に別のグループが枠超過を記録したかもしれない（--resume は J3 を呼ばないので、
	// ここで確かめ直す）。
	if jc != nil && jc.RateLimited() {
		return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedRateLimited}, nil
	}

	// J3 と実装枠の両方が周の上限に入らないなら、J3 の費用を払う前に起動しない
	// （最終の評価は、スロットの割り当てと同じトランザクションの中で行う）。
	// `--resume` の再開は J3 を呼ばない（子は前の文脈を持つ）。
	j3Budget := in.AgentDecl.JudgmentBudgetFor(JudgmentJ3)
	if dc.Resume != nil {
		j3Budget = 0
	}
	if err := s.checkCycleBudget(ctx, in.CycleID, j3Budget+microsToUSD(dc.estimateMicros())); err != nil {
		if jc != nil && errors.Is(err, ErrBudgetExceeded) {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedCycleBudget}, nil
		}
		return nil, nil, err
	}
	if dc.Resume != nil {
		return s.launchDelegation(ctx, in, jc, dc, "", adm)
	}
	sections, notStarted, err := s.prepareJ3(ctx, in, dc)
	if err != nil || notStarted != nil {
		return nil, notStarted, err
	}

	runIn := RunJudgmentInput{
		ChallengeID:  ch.ID,
		Judgment:     JudgmentJ3,
		MaxBudgetUSD: j3Budget,
		TimeoutSec:   in.AgentDecl.TimeoutSec.Judgment,
		Sections:     sections,
		OutputSchema: j3OutputSchema(),
		Invoker:      in.Judgment,
	}
	var j3 *RunJudgmentResult
	if jc != nil {
		j3, notStarted, err = jc.RunJudgment(ctx, s, runIn)
		if err != nil || notStarted != nil {
			return nil, notStarted, err
		}
	} else {
		cycleID := in.CycleID
		runIn.CycleID = &cycleID
		if j3, err = s.RunJudgment(ctx, runIn); err != nil {
			return nil, nil, err
		}
	}
	if j3.Result != RunResultSucceeded {
		return &JudgmentAutoItem{ChallengeID: j3.ChallengeID, RunID: j3.RunID, Result: j3.Result}, nil, nil
	}
	brief, ok := validateJ3Output(j3.StructuredOutput)
	if !ok {
		if err := s.invalidateRunOutput(ctx, j3.RunID); err != nil {
			return nil, nil, err
		}
		return &JudgmentAutoItem{ChallengeID: j3.ChallengeID, RunID: j3.RunID, Result: RunResultInvalidOutput}, nil, nil
	}

	return s.launchDelegation(ctx, in, jc, dc, brief, adm)
}

// launchDelegation はスロットの割り当てと委譲の run の記録（同じトランザクション）・子の起動・
// 結果の記録とスロットの解放を行う。
func (s *Store) launchDelegation(ctx context.Context, in DelegateInput, jc *JudgmentCycle, dc *delegationContext, brief string, adm *admission) (*JudgmentAutoItem, *NotStarted, error) {
	ch := dc.Challenge
	cid, _ := parseChallengeID(ch.ID)
	var sessionID string
	var resumedFrom *int64
	var prevReportedMicros *int64
	if dc.Resume != nil {
		sessionID = dc.Resume.Target.SessionID
		v := runIntID(dc.Resume.Target)
		resumedFrom = &v
		prevReportedMicros = dc.Resume.Target.ReportedTotalCostUSD
		if dc.Resume.Branch != "" && dc.Repo.Slots.Provider != string(slotProviderWorktree) {
			exists, err := in.Reconcile.BranchExists(ctx, dc.Repo.Remote, dc.Resume.Branch)
			dc.ResumeBranchPushed = err == nil && exists
		}
	} else {
		sid, err := newLowercaseUUIDv4()
		if err != nil {
			return nil, nil, fmt.Errorf("%w: generate session id: %w", ErrStoreError, err)
		}
		sessionID = sid
	}
	host, _ := os.Hostname()
	pid := int64(os.Getpid())
	now := s.currentTime()
	var cycleIDInt *int64
	if n, ok := parseCycleID(in.CycleID); ok {
		cycleIDInt = &n
	}
	planVersion := int64(dc.Plan.Version)
	deciderRow := int64(dc.DeciderRow)

	bind := func(tx *sql.Tx, slotID int64) (int64, error) {
		cur, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return 0, err
		}
		if cur.Status != StatusInProgress {
			return 0, errDelegationNotEligible
		}
		if cycleIDInt != nil {
			if err := evalCycleBudgetTx(ctx, tx, *cycleIDInt, dc.estimateMicros()); err != nil {
				return 0, err
			}
		}
		row, err := insertRun(ctx, tx, cid, insertRunInput{
			CycleID: cycleIDInt, Kind: runKindDelegate, ChallengeVersion: int64(cur.Version),
			PlanVersion: &planVersion, SessionID: sessionID, ResumedFromRunID: resumedFrom, PID: pid, Host: host,
			HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: dc.ImplBudgetMicros, BudgetBucket: budgetBucketImpl,
			SlotID: &slotID, Decider: dc.Decider, DeciderRow: &deciderRow,
		})
		if err != nil {
			return 0, err
		}
		n, ok := parseRunID(row.ID)
		if !ok {
			return 0, fmt.Errorf("core: malformed run id %q", row.ID)
		}
		return n, nil
	}
	assignment, err := s.acquireDelegationSlot(ctx, in, dc, bind)
	adm.markInserted() // run が記録された（または割り当てに失敗した）。以後は DB の数に入る。
	switch {
	case errors.Is(err, ErrSlotUnavailable):
		if jc != nil {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedSlotUnavailable}, nil
		}
		return nil, nil, err
	case errors.Is(err, ErrBudgetExceeded):
		if jc != nil {
			return nil, &NotStarted{ChallengeID: ch.ID, Reason: NotStartedCycleBudget}, nil
		}
		return nil, nil, err
	case errors.Is(err, errActiveRunExists):
		return nil, nil, ErrRunInProgress
	case err != nil:
		return nil, nil, err
	}
	runIDInt, _ := parseRunID(assignment.RunID)

	launch := DelegateLaunchInput{
		SessionID:      sessionID,
		Workspace:      s.workspace,
		WorkDir:        assignment.Path,
		RunDir:         runDirPath(s.workspace, assignment.RunID),
		OutputSchema:   delegationReportSchema(in.ConnDecl.HumanQuestionKinds),
		MaxBudgetUSD:   microsToUSD(dc.ImplBudgetMicros),
		TimeoutSec:     in.AgentDecl.TimeoutSec.Delegate,
		PermissionMode: dc.Connector.PermissionMode,
		Decider:        dc.Decider,
		DeciderRow:     dc.DeciderRow,
		Brief:          brief,
		Invocation:     dc.Invocation,
	}
	if dc.Binding != nil {
		if _, n, ok := parseExternalKey(dc.Binding.ExternalKey); ok {
			launch.SourceIssueNumber, launch.SourceIssueURL = n, dc.Binding.URL
		}
	}
	if dc.Resume != nil {
		launch.IsResume = true
		launch.ResumeKind = dc.Resume.Kind
		launch.ResumeAnswer = dc.Resume.Answer
		launch.ResumeQuestion = dc.Resume.Question
		launch.ResumeFeedback = dc.Resume.Feedback
		launch.ResumeBranch = dc.Resume.Branch
	}
	output, invokeErr := s.invokeFnWithHeartbeat(ctx, runIDInt, func(ctx context.Context) (JudgmentLaunchOutput, error) {
		return in.Delegate.InvokeDelegation(ctx, launch)
	})

	result, errSummary := output.Result, output.ErrorSummary
	if invokeErr != nil {
		if errSummary == "" {
			errSummary = invokeErr.Error()
		}
		if result == "" {
			result = RunResultLaunchFailed
		}
	}
	if !result.valid() {
		result = RunResultErrored
	}
	var report *DelegationReport
	if result == RunResultSucceeded {
		r, ok := validateDelegationReport(output.StructuredOutput, in.ConnDecl.HumanQuestionKinds)
		if !ok {
			result = RunResultInvalidOutput
		}
		report = r
	}
	// 照合（launch_failed を除くすべての結果。報告に依らない）。スロットを解放する前に、
	// 作業ツリーの検査と gh の GET を行う。ctx が取り消されても照合する。
	recordCtx := context.WithoutCancel(ctx)
	var recon *reconciliation
	if result != RunResultLaunchFailed {
		var reportBranch *string
		if report != nil {
			reportBranch = report.Branch
		}
		recon = s.gatherReconciliation(recordCtx, in, dc, assignment.Path, assignment.BaseBranch, reportBranch)
	}
	mismatch := output.SessionIDReturned != "" && output.SessionIDReturned != sessionID
	costMicros, costSource := computeRunCost(result, output.ReportedTotalCostUSD, dc.Resume != nil, prevReportedMicros, dc.ImplBudgetMicros)
	var reportedPtr *int64
	if output.ReportedTotalCostUSD != nil {
		v := usdToMicros(*output.ReportedTotalCostUSD)
		reportedPtr = &v
	}

	// 子を起動して費用が発生しうる以上、結果の記録とスロットの解放は ctx の取り消しに
	// 関わらず必ず行う（RunJudgment の③と同じ理由）。
	var reaped *runRow
	err = s.mutateSlots(recordCtx, func(tx *sql.Tx) error {
		// 起動の最中に回収（interrupted）された run は上書きせず、割り当て直されたかもしれない
		// スロットも解放しない。
		cur, err := loadRunByID(recordCtx, tx, runIDInt)
		if err != nil {
			return err
		}
		if cur == nil || cur.EndedAt != nil {
			reaped = cur
			return nil
		}
		if mismatch {
			if _, err := tx.ExecContext(recordCtx,
				`UPDATE run SET session_id = ?, session_id_mismatch = 1 WHERE id = ?`, output.SessionIDReturned, runIDInt); err != nil {
				return err
			}
		}
		if _, err := updateRunEnd(recordCtx, tx, runIDInt, updateRunEndInput{
			EndedAt: s.currentTime(), Result: result, RateLimited: output.RateLimited,
			CostUSD: &costMicros, CostSource: costSource, ReportedTotalCostUSD: reportedPtr,
			Output: string(output.StructuredOutput), Error: errSummary,
		}); err != nil {
			return err
		}
		if recon != nil {
			verifiedAt := s.currentTime()
			for _, a := range recon.artifactInputs(&verifiedAt) {
				if _, err := insertRunArtifact(recordCtx, tx, runIDInt, a); err != nil {
					return err
				}
			}
		}
		sid, _ := parseSlotID(assignment.SlotID)
		if recon != nil && recon.AttentionReason != "" {
			// 未コミットの変更などが残っている作業ツリーは、人が slot clear するまで
			// 次の委譲に割り当てない（変更は消さない）。
			cur, err := loadSlotByID(recordCtx, tx, sid)
			if err != nil {
				return err
			}
			if cur == nil {
				return ErrNotFound
			}
			_, err = updateSlotState(recordCtx, tx, sid, slotStateNeedsAttention, nil, recon.AttentionReason)
			return err
		}
		return releaseSlot(recordCtx, tx, sid)
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, nil, err
	}
	if output.RateLimited && jc != nil {
		jc.rateLimited.Store(true)
	}

	if reaped != nil {
		// 起動の最中に回収された run は、ストアの記録（interrupted）を結果にする。
		return &JudgmentAutoItem{ChallengeID: ch.ID, RunID: assignment.RunID, Result: reaped.Result}, nil, nil
	}
	item := &JudgmentAutoItem{ChallengeID: ch.ID, RunID: assignment.RunID, Result: result}
	if report != nil {
		item.Outcome = string(report.Outcome)
	}
	if recon != nil {
		status, note, err := s.applyReconciliation(recordCtx, dc, assignment.RunID, recon, result, report)
		if err != nil {
			return nil, nil, err
		}
		item.Status, item.Note = status, note
	}
	return item, nil, nil
}

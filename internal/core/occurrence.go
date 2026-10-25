package core

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// このファイルは発生（occurrence。マイグレーション 0011）の閉集合の定数と、
// 発生を 1 行作る内部ヘルパを持つ。閉集合は DB の CHECK 制約ではなく core の
// 定数で検査する（0004・0009 の流儀）。後続のチケットは定数を足さずに使う。

// OccurrenceKind は発生の種類。core が記録する 12 種類（M45P5 の 12 の出来事に 1 対 1）と、
// J6 が足す発生の j6_observed。
type OccurrenceKind string

// OccurrenceKind の13値。
const (
	OccurrenceKindPlanRejected          OccurrenceKind = "plan_rejected"
	OccurrenceKindCompletionRejected    OccurrenceKind = "completion_rejected"
	OccurrenceKindCompletionApproved    OccurrenceKind = "completion_approved"
	OccurrenceKindVerificationNotMet    OccurrenceKind = "verification_not_met"
	OccurrenceKindVerificationUncertain OccurrenceKind = "verification_uncertain"
	OccurrenceKindDelegationFailed      OccurrenceKind = "delegation_failed"
	OccurrenceKindJudgmentFailed        OccurrenceKind = "judgment_failed"
	OccurrenceKindFailureLimitHold      OccurrenceKind = "failure_limit_hold"
	OccurrenceKindBudgetExhausted       OccurrenceKind = "budget_exhausted"
	OccurrenceKindDelegationStopped     OccurrenceKind = "delegation_stopped"
	OccurrenceKindUnapprovedMerge       OccurrenceKind = "unapproved_merge"
	OccurrenceKindReleaseWithdrawn      OccurrenceKind = "release_withdrawn"
	OccurrenceKindJ6Observed            OccurrenceKind = "j6_observed"
)

var coreOccurrenceKindValues = []OccurrenceKind{
	OccurrenceKindPlanRejected, OccurrenceKindCompletionRejected, OccurrenceKindCompletionApproved,
	OccurrenceKindVerificationNotMet, OccurrenceKindVerificationUncertain, OccurrenceKindDelegationFailed,
	OccurrenceKindJudgmentFailed, OccurrenceKindFailureLimitHold, OccurrenceKindBudgetExhausted,
	OccurrenceKindDelegationStopped, OccurrenceKindUnapprovedMerge, OccurrenceKindReleaseWithdrawn,
}

// ParseOccurrenceKind は s を発生の種類（j6_observed を含む 13 値）として解釈する。
// 前後の空白を除いた完全一致のみ受理する。
func ParseOccurrenceKind(s string) (OccurrenceKind, bool) {
	trimmed := strings.TrimSpace(s)
	for _, v := range coreOccurrenceKindValues {
		if string(v) == trimmed {
			return v, true
		}
	}
	if trimmed == string(OccurrenceKindJ6Observed) {
		return OccurrenceKindJ6Observed, true
	}
	return "", false
}

// OccurrenceSource は発生の出どころ。
type OccurrenceSource string

// OccurrenceSource の2値。
const (
	OccurrenceSourceCore OccurrenceSource = "core"
	OccurrenceSourceJ6   OccurrenceSource = "j6"
)

// ParseOccurrenceSource は s を発生の出どころとして解釈する。前後の空白を除いた完全一致のみ受理する。
func ParseOccurrenceSource(s string) (OccurrenceSource, bool) {
	switch trimmed := strings.TrimSpace(s); OccurrenceSource(trimmed) {
	case OccurrenceSourceCore, OccurrenceSourceJ6:
		return OccurrenceSource(trimmed), true
	}
	return "", false
}

// OccurrenceOutcome は発生の良し悪し。作成の時点に呼び出し側が決めた値を書き、後から書き換えない。
type OccurrenceOutcome string

// OccurrenceOutcome の2値。
const (
	OccurrenceOutcomeGood OccurrenceOutcome = "good"
	OccurrenceOutcomeBad  OccurrenceOutcome = "bad"
)

// ParseOccurrenceOutcome は s を発生の良し悪しとして解釈する。前後の空白を除いた完全一致のみ受理する。
func ParseOccurrenceOutcome(s string) (OccurrenceOutcome, bool) {
	switch trimmed := strings.TrimSpace(s); OccurrenceOutcome(trimmed) {
	case OccurrenceOutcomeGood, OccurrenceOutcomeBad:
		return OccurrenceOutcome(trimmed), true
	}
	return "", false
}

// OccurrenceCause は J6 が分類する発生の原因。none は原因の分類の対象外。
type OccurrenceCause string

// OccurrenceCause の4値。
const (
	OccurrenceCauseContext     OccurrenceCause = "context"
	OccurrenceCausePermission  OccurrenceCause = "permission"
	OccurrenceCausePerformance OccurrenceCause = "performance"
	OccurrenceCauseNone        OccurrenceCause = "none"
)

// ParseOccurrenceCause は s を発生の原因として解釈する。前後の空白を除いた完全一致のみ受理する。
func ParseOccurrenceCause(s string) (OccurrenceCause, bool) {
	switch trimmed := strings.TrimSpace(s); OccurrenceCause(trimmed) {
	case OccurrenceCauseContext, OccurrenceCausePermission, OccurrenceCausePerformance, OccurrenceCauseNone:
		return OccurrenceCause(trimmed), true
	}
	return "", false
}

// OccurrenceLayer は J6 が分類する改修の対象の層。none は層の対象外。
type OccurrenceLayer string

// OccurrenceLayer の5値。
const (
	OccurrenceLayerPrompt         OccurrenceLayer = "prompt"
	OccurrenceLayerConnectorSkill OccurrenceLayer = "connector_skill"
	OccurrenceLayerEntrySkill     OccurrenceLayer = "entry_skill"
	OccurrenceLayerKnowledge      OccurrenceLayer = "knowledge"
	OccurrenceLayerNone           OccurrenceLayer = "none"
)

// ParseOccurrenceLayer は s を改修の対象の層として解釈する。前後の空白を除いた完全一致のみ受理する。
func ParseOccurrenceLayer(s string) (OccurrenceLayer, bool) {
	switch trimmed := strings.TrimSpace(s); OccurrenceLayer(trimmed) {
	case OccurrenceLayerPrompt, OccurrenceLayerConnectorSkill, OccurrenceLayerEntrySkill,
		OccurrenceLayerKnowledge, OccurrenceLayerNone:
		return OccurrenceLayer(trimmed), true
	}
	return "", false
}

// OccurrenceRef は発生が指す出来事の記録への参照。承認・保留・run・実行の試行・作業ログの
// ちょうど 1 つを、内部整数 ID で指す（DB の CHECK 制約も同じ構造を強制する）。
type OccurrenceRef struct {
	ApprovalID  *int64
	HoldID      *int64
	RunID       *int64
	ExecutionID *int64
	ActivityID  *int64
}

func (r OccurrenceRef) count() int {
	n := 0
	for _, p := range []*int64{r.ApprovalID, r.HoldID, r.RunID, r.ExecutionID, r.ActivityID} {
		if p != nil {
			n++
		}
	}
	return n
}

// occurrenceInput は core が出来事と同じトランザクションで作る発生の入力。
type occurrenceInput struct {
	ChallengeID int64
	// CycleID は周の内部整数 ID。呼び出し側が周を持たないときは nil（NULL）。
	CycleID *int64
	// CauseRunID は原因の run の内部整数 ID（nil なら NULL）。
	CauseRunID *int64
	Kind       OccurrenceKind
	Ref        OccurrenceRef
	// Outcome は作成の時点に呼び出し側が決めた良し悪し。作った後に書き換えない。
	Outcome OccurrenceOutcome
	At      time.Time
}

// occurrenceRow は occurrence テーブルへ INSERT する 1 行分。出どころは常に core で、
// 分類の列（原因・層・対象・兆候・分類した run）と処理の印は NULL である。
type occurrenceRow struct {
	ChallengeID int64
	CycleID     *int64
	CauseRunID  *int64
	Kind        string
	Ref         OccurrenceRef
	Outcome     string
	Source      string
	CreatedAt   string
	// 以下は J6 が足す発生（出どころ j6。insertJ6OccurrenceRow）だけが持つ。core の発生では nil。
	J6Kind          *string
	Cause           *string
	Layer           *string
	Target          *string
	Signal          *string
	ClassifiedRunID *int64
	Handled         *string
}

func defaultInsertOccurrence(tx *sql.Tx, row occurrenceRow) error {
	_, err := tx.Exec(
		`INSERT INTO occurrence (challenge_id, cycle_id, cause_run_id, kind, j6_kind, approval_id, hold_id, run_id, execution_id, activity_id,
		   outcome, source, cause, layer, target, signal, classified_run_id, handled, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ChallengeID, int64PtrColumn(row.CycleID), int64PtrColumn(row.CauseRunID), row.Kind, nullableString(row.J6Kind),
		int64PtrColumn(row.Ref.ApprovalID), int64PtrColumn(row.Ref.HoldID), int64PtrColumn(row.Ref.RunID),
		int64PtrColumn(row.Ref.ExecutionID), int64PtrColumn(row.Ref.ActivityID),
		row.Outcome, row.Source, nullableString(row.Cause), nullableString(row.Layer), nullableString(row.Target),
		nullableString(row.Signal), int64PtrColumn(row.ClassifiedRunID), nullableString(row.Handled), row.CreatedAt,
	)
	return err
}

// insertOccurrenceRow は発生を 1 行作る、core 唯一の口。呼び出し側のトランザクション
// （出来事を書くのと同じもの）の中で呼ぶ。出どころは core、分類の列は NULL で書く。
// 出来事の書き込みが失敗すれば発生も残らず、ここが失敗すれば呼び出し側は
// エラーを返してトランザクションごと巻き戻す（記録の漏れを作らない）。
// 作った後に良し悪しなどを書き換える関数は持たない。
func (s *Store) insertOccurrenceRow(tx *sql.Tx, in occurrenceInput) error {
	kind, ok := ParseOccurrenceKind(string(in.Kind))
	if !ok || kind == OccurrenceKindJ6Observed {
		return fmt.Errorf("insert occurrence: invalid kind %q for a core occurrence", in.Kind)
	}
	outcome, ok := ParseOccurrenceOutcome(string(in.Outcome))
	if !ok {
		return fmt.Errorf("insert occurrence: invalid outcome %q", in.Outcome)
	}
	if in.Ref.count() != 1 {
		return fmt.Errorf("insert occurrence: exactly one reference is required, got %d", in.Ref.count())
	}
	row := occurrenceRow{
		ChallengeID: in.ChallengeID,
		CycleID:     in.CycleID,
		CauseRunID:  in.CauseRunID,
		Kind:        string(kind),
		Ref:         in.Ref,
		Outcome:     string(outcome),
		Source:      string(OccurrenceSourceCore),
		CreatedAt:   formatTimestamp(in.At),
	}
	insert := s.insertOccurrence
	if insert == nil {
		insert = defaultInsertOccurrence
	}
	return insert(tx, row)
}

// occurrenceHandledNotApplicable は発生の処理済みの印（handled）の値のうち、原因が none の
// 発生に分類の時点で付ける「対象外」。J8 の入力としきい値の件数に入れない。
const occurrenceHandledNotApplicable = "not_applicable"

// j6OccurrenceInput は J6 が足す発生の入力。出どころは j6・種類は j6_observed に固定し、
// 分類の列（原因・層・対象・兆候・分類した run）は作成の時点で埋める。
type j6OccurrenceInput struct {
	ChallengeID int64
	CycleID     *int64
	CauseRunID  *int64
	// J6Kind は J6 の出力の kind（自由記述。行の j6_kind に写す）。
	J6Kind  string
	Ref     OccurrenceRef
	Outcome OccurrenceOutcome
	Cause   OccurrenceCause
	Layer   OccurrenceLayer
	Target  string
	Signal  string
	// ClassifiedRunID は分類した J6 の run の内部整数 ID。
	ClassifiedRunID int64
	At              time.Time
}

// insertJ6OccurrenceRow は J6 が足す発生を 1 行作る口。insertOccurrenceRow と同じく呼び出し側の
// トランザクションの中で呼び、同じ挿入の関数（テストの失敗注入のフック insertOccurrence を含む）を通す。
// 原因が none の発生は分類の時点で処理済み（対象外）にする。
func (s *Store) insertJ6OccurrenceRow(tx *sql.Tx, in j6OccurrenceInput) error {
	outcome, ok := ParseOccurrenceOutcome(string(in.Outcome))
	if !ok {
		return fmt.Errorf("insert j6 occurrence: invalid outcome %q", in.Outcome)
	}
	cause, ok := ParseOccurrenceCause(string(in.Cause))
	if !ok {
		return fmt.Errorf("insert j6 occurrence: invalid cause %q", in.Cause)
	}
	layer, ok := ParseOccurrenceLayer(string(in.Layer))
	if !ok {
		return fmt.Errorf("insert j6 occurrence: invalid layer %q", in.Layer)
	}
	if in.Ref.count() != 1 {
		return fmt.Errorf("insert j6 occurrence: exactly one reference is required, got %d", in.Ref.count())
	}
	causeS, layerS, targetS, signalS, j6Kind := string(cause), string(layer), in.Target, in.Signal, in.J6Kind
	row := occurrenceRow{
		ChallengeID: in.ChallengeID, CycleID: in.CycleID, CauseRunID: in.CauseRunID,
		Kind: string(OccurrenceKindJ6Observed), Ref: in.Ref, Outcome: string(outcome),
		Source: string(OccurrenceSourceJ6), CreatedAt: formatTimestamp(in.At),
		J6Kind: &j6Kind, Cause: &causeS, Layer: &layerS, Target: &targetS, Signal: &signalS,
		ClassifiedRunID: &in.ClassifiedRunID,
	}
	if cause == OccurrenceCauseNone {
		h := occurrenceHandledNotApplicable
		row.Handled = &h
	}
	insert := s.insertOccurrence
	if insert == nil {
		insert = defaultInsertOccurrence
	}
	return insert(tx, row)
}

func formatOccurrenceID(id int64) string { return "O-" + strconv.FormatInt(id, 10) }

func parseOccurrenceID(s string) (int64, bool) {
	rest, ok := strings.CutPrefix(s, "O-")
	if !ok || rest == "" || rest[0] == '0' || rest[0] == '+' || rest[0] == '-' {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// holdReason は保留（hold の行）を作る理由の種別。発生の種類を決める。
type holdReason string

const (
	holdReasonNone            holdReason = ""
	holdReasonFailureLimit    holdReason = "failure_limit"
	holdReasonReworkLimit     holdReason = "rework_limit"
	holdReasonQuestions       holdReason = "questions"
	holdReasonBlocked         holdReason = "blocked"
	holdReasonUnapprovedMerge holdReason = "unapproved_merge"
	// holdReasonArtifactMissing は成果物が見つからない保留。M45P5 の 12 の出来事に無いので発生を作らない。
	holdReasonArtifactMissing holdReason = "artifact_missing"
)

// occurrenceKind は保留の理由に対応する発生の種類。発生を作らない理由は false。
func (r holdReason) occurrenceKind() (OccurrenceKind, bool) {
	switch r {
	case holdReasonFailureLimit, holdReasonReworkLimit:
		return OccurrenceKindFailureLimitHold, true
	case holdReasonQuestions, holdReasonBlocked:
		return OccurrenceKindDelegationStopped, true
	case holdReasonUnapprovedMerge:
		return OccurrenceKindUnapprovedMerge, true
	}
	return "", false
}

// delegationEndOccurrenceKind は委譲の run の終了結果に対応する発生の種類。
// 数える失敗（枠超過の run を除く）は delegation_failed、budget_exhausted は budget_exhausted
// （delegation_failed は作らない）。それ以外は発生を作らない。
func delegationEndOccurrenceKind(result RunResult, rateLimited bool) (OccurrenceKind, bool) {
	if result == RunResultBudgetExhausted {
		return OccurrenceKindBudgetExhausted, true
	}
	if !rateLimited && isCountedFailure(result) {
		return OccurrenceKindDelegationFailed, true
	}
	return "", false
}

// insertHoldWithOccurrence は保留の行を作り、理由に対応する発生があれば同じトランザクションで
// 作る（参照は保留の行、原因の run は遷移の run、周は cycleID）。
func (s *Store) insertHoldWithOccurrence(ctx context.Context, tc *transitionCtx, question string, reason holdReason, cycleID string) error {
	holdID, err := insertHoldID(ctx, tc, question)
	if err != nil {
		return err
	}
	kind, ok := reason.occurrenceKind()
	if !ok {
		return nil
	}
	var cyc *int64
	if n, ok := parseCycleID(cycleID); ok {
		cyc = &n
	}
	return s.insertOccurrenceRow(tc.tx, occurrenceInput{
		ChallengeID: tc.id, CycleID: cyc, CauseRunID: tc.runID, Kind: kind,
		Ref: OccurrenceRef{HoldID: &holdID}, Outcome: OccurrenceOutcomeBad, At: tc.now,
	})
}

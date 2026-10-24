// このファイルは `flywheel status` の実コマンドが呼ぶ読み取り専用の問い合わせ
// API（GetOverview）を持つ（Issue #14）。区分の判定（どの状態の課題・どの状態の
// 不可逆操作がどの区分に属するか）は core が持ち、internal/cli には持たせない
// （親要件チケット #4 の指示。docs/features/m1-core.md §状況の表示）。
//
// 状態を変えず、作業ログにも書かない（GetChallenge・ListChallenges・
// ListActivities と同じく s.db.Read 経由の読み取りだけで完結する）。

package core

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

// needsHumanStatuses は「人間の操作を待っているもの」に出す課題の状態
// （計画承認待ち・完了確認待ち・人間対応待ち）。
var needsHumanStatuses = []Status{
	StatusAwaitingPlanApproval,
	StatusAwaitingCompletionApproval,
	StatusAwaitingHuman,
}

// actionableStatuses は「システムが次に進められるもの」に出す課題の状態
// （未分類・分類済・着手中・検証中）。
var actionableStatuses = []Status{
	StatusUnclassified,
	StatusClassified,
	StatusInProgress,
	StatusVerifying,
}

// Overview は GetOverview の結果（`flywheel status` の3区分）。
type Overview struct {
	// NeedsHumanChallenges は計画承認待ち・完了確認待ち・人間対応待ちの課題
	// （id 昇順）。
	NeedsHumanChallenges []Challenge
	// NeedsHumanOperations は未承認（pending）の不可逆操作（id 昇順）。
	NeedsHumanOperations []IrreversibleOperation
	// ActionableChallenges は未分類・分類済・着手中・検証中の課題（id 昇順）。
	ActionableChallenges []Challenge
	// ApprovedOperations は承認済み（approved）の不可逆操作（id 昇順）。
	ApprovedOperations []IrreversibleOperation
	// Discrepancies は食い違いのある課題（id 昇順。docs/features/
	// m2-github-issue-ingest.md §食い違いの表示）。種類は DiscrepancyKind の
	// 閉集合（#58 の 3 種類に、#72 が upstream_commented・upstream_updated を
	// 足した）。
	Discrepancies []Discrepancy
	// NeedsHumanTriage は §J1「判定が not_mine で自動の対象から外れている
	// 課題は、status の needs_human.triage に、理由と run の ID つきで出る」
	// の一覧（challenge_id 昇順。#84）。無ければ空スライス（AC「triage の
	// 無いワークスペースでは [] である」）。
	NeedsHumanTriage []TriageItem
	// NeedsHumanSlots は needs_attention のスロット（slot の id 昇順。
	// docs/features/m3-invoker-delegation.md §IF / API の
	// status.needs_human.slots。S2）。無ければ空スライス。
	NeedsHumanSlots []SlotAttention
	// NeedsHumanBudgetExhausted は実装枠の残りが 1 USD 未満の着手中の課題（challenge_id 昇順。
	// status.needs_human.budget_exhausted。S2）。無ければ空スライス。
	NeedsHumanBudgetExhausted []BudgetExhausted
	// WaitingExternal は CI の完了を待っている検証中の課題（status.waiting_external.challenges。S2）。
	// GetOverview は GitHub を呼ばないため空のまま返し、呼び出し元が ListWaitingExternal の結果を
	// 入れる。
	WaitingExternal []WaitingExternal
	// UnmetDependencies は ActionableChallenges の課題 ID ごとの、満たされていない依存先（S2。依存先の ID の昇順）。
	// 満たされていない依存先が無い課題はキーを持たない。
	UnmetDependencies map[string][]DependencyView
}

// DiscrepancyKind は Discrepancy.Kinds の値（docs/features/
// m2-github-issue-ingest.md §食い違いの表示・§IF / API「`status` と `show` の
// 拡張」の閉集合の部分集合。列挙の順で並べる）。
type DiscrepancyKind string

// DiscrepancyKind の値（仕様の閉集合の順。upstream_commented・upstream_updated
// は 2026-09-26 にオーナー QH10 で足した＝§上流の更新の観測と既読）。
const (
	DiscrepancyKindUpstreamClosed    DiscrepancyKind = "upstream_closed"
	DiscrepancyKindUpstreamMissing   DiscrepancyKind = "upstream_missing"
	DiscrepancyKindOutOfPolicy       DiscrepancyKind = "out_of_policy"
	DiscrepancyKindUpstreamCommented DiscrepancyKind = "upstream_commented"
	DiscrepancyKindUpstreamUpdated   DiscrepancyKind = "upstream_updated"
	// DiscrepancyKindMergedUnapproved は、直前の試行の結果が merged_unapproved の
	// release がある（承認なしの本番反映。M3 の検出と同じ事象）。
	DiscrepancyKindMergedUnapproved DiscrepancyKind = "merged_unapproved"
	// DiscrepancyKindDependencyCycle は、ストアの依存の循環に含まれる課題（S2）。
	DiscrepancyKindDependencyCycle DiscrepancyKind = "dependency_cycle"
)

// discrepancyKindValues は DiscrepancyKind の閉集合（仕様の列挙の順）。
var discrepancyKindValues = []DiscrepancyKind{
	DiscrepancyKindUpstreamClosed,
	DiscrepancyKindUpstreamMissing,
	DiscrepancyKindOutOfPolicy,
	DiscrepancyKindUpstreamCommented,
	DiscrepancyKindUpstreamUpdated,
	DiscrepancyKindMergedUnapproved,
	DiscrepancyKindDependencyCycle,
}

// DiscrepancyKindValues は DiscrepancyKind の閉集合の写しを返す（CLI のテストが
// 仕様の列挙と双方向に照合するため。IngestOutcomeValues と同じ形）。
func DiscrepancyKindValues() []DiscrepancyKind {
	return append([]DiscrepancyKind(nil), discrepancyKindValues...)
}

// Discrepancy は食い違いのある課題 1 件（challenge_id・その種類）。
type Discrepancy struct {
	ChallengeID string
	Kinds       []DiscrepancyKind
	// OperationID は merged_unapproved の release の ID（"OP-<n>"）。上流の食い違いだけの
	// 要素では空。
	OperationID string
	// ChallengeIDs は dependency_cycle の輪の課題の ID（昇順）。それ以外の要素では空。
	ChallengeIDs []string
}

// listChallengesInStatuses は statuses のいずれかに一致する課題を id 昇順で返す
// （ListChallenges と異なり、複数状態の OR 条件を1クエリで扱う）。
func listChallengesInStatuses(ctx context.Context, tx *sql.Tx, statuses []Status) ([]Challenge, error) {
	placeholders := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, st := range statuses {
		placeholders[i] = "?"
		args[i] = string(st)
	}
	query := challengeSelectColumns + " WHERE status IN (" + strings.Join(placeholders, ",") + ") ORDER BY id ASC"

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make([]Challenge, 0)
	for rows.Next() {
		c, err := scanChallengeRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *c)
	}
	return result, rows.Err()
}

// listOperationsInState は state に一致する不可逆操作を、課題をまたいで id 昇順で
// 返す（loadOperations は1課題に従属する一覧を返すのに対し、こちらはストア全体を
// 対象にする）。行の変換は challenge.go の operationSelectColumns・scanOperationRow を
// 共有する。
func listOperationsInState(ctx context.Context, tx *sql.Tx, state OperationState) ([]IrreversibleOperation, error) {
	rows, err := tx.QueryContext(ctx, operationSelectColumns+` WHERE state = ? ORDER BY id ASC`, string(state))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make([]IrreversibleOperation, 0)
	for rows.Next() {
		op, err := scanOperationRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *op)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := fillOperationExecutionInfo(ctx, tx, result); err != nil {
		return nil, err
	}
	return result, nil
}

// unreadKinds は観測値と読んだ時点の値から未読の更新の種類を導く
// （§上流の更新の観測と既読「未読の更新は、記録から導き、別に保存しない」）。
// ingest の結果（IngestItemResult.Unread）と status の discrepancyKinds が
// 同じ判定を共有する（§IF / API「`unread` は…`status` の `kinds` と同じ判定」）。
//
//   - upstream_commented: 観測値のコメント数が読んだ時点のコメント数より大きい
//     （`>`。コメントの削除による件数の減少では出さない）
//   - upstream_updated: 観測値の更新日時が読んだ時点の更新日時と違う（`!=`。
//     前後は比べない）。どちらも "" なら「まだ観測していない」ことを表し、
//     0003 の前に作られた対応が初めて観測値を得るまでは差が生じない
//     （§クリティカル設計決定 1）
func unreadKinds(commentsCount, readCommentsCount int, upstreamUpdatedAt, readUpstreamUpdatedAt string) []DiscrepancyKind {
	var kinds []DiscrepancyKind
	if commentsCount > readCommentsCount {
		kinds = append(kinds, DiscrepancyKindUpstreamCommented)
	}
	if upstreamUpdatedAt != readUpstreamUpdatedAt {
		kinds = append(kinds, DiscrepancyKindUpstreamUpdated)
	}
	return kinds
}

// discrepancyKinds は、対応 1 件の上流の状態・ポリシーの状態・観測値と読んだ
// 時点の値から食い違いの種類を導く（§食い違いの表示「食い違いは記録から導き、
// 別に保存しない」）。
func discrepancyKinds(upstream upstreamState, policy policyState, commentsCount, readCommentsCount int, upstreamUpdatedAt, readUpstreamUpdatedAt string) []DiscrepancyKind {
	var kinds []DiscrepancyKind
	if upstream == upstreamStateClosed {
		kinds = append(kinds, DiscrepancyKindUpstreamClosed)
	}
	if upstream == upstreamStateMissing {
		kinds = append(kinds, DiscrepancyKindUpstreamMissing)
	}
	if policy == policyStateOutOfPolicy {
		kinds = append(kinds, DiscrepancyKindOutOfPolicy)
	}
	kinds = append(kinds, unreadKinds(commentsCount, readCommentsCount, upstreamUpdatedAt, readUpstreamUpdatedAt)...)
	return kinds
}

// listDiscrepancies は完了していない課題のうち、対応がある（source_binding を
// 持つ）ものを challenge.id 昇順に読み、discrepancyKinds が非空を返すものだけを
// Discrepancy として返す（§食い違いの表示）。対応の無い課題（`create` で作った
// 課題）は source_binding が無いので対象にならない。
func listDiscrepancies(ctx context.Context, tx *sql.Tx) ([]Discrepancy, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT c.id, c.status, sb.upstream_state, sb.policy_state,
		       sb.comments_count, sb.upstream_updated_at, sb.read_comments_count, sb.read_upstream_updated_at
		FROM source_binding sb
		JOIN challenge c ON c.id = sb.challenge_id
		ORDER BY c.id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make([]Discrepancy, 0)
	for rows.Next() {
		var id int64
		var status, upstream, policy string
		var commentsCount, readCommentsCount int
		var upstreamUpdatedAt, readUpstreamUpdatedAt sql.NullString
		if err := rows.Scan(&id, &status, &upstream, &policy, &commentsCount, &upstreamUpdatedAt, &readCommentsCount, &readUpstreamUpdatedAt); err != nil {
			return nil, err
		}
		if IsTerminal(Table, StatusVocabulary, Status(status)) {
			continue
		}
		kinds := discrepancyKinds(upstreamState(upstream), policyState(policy), commentsCount, readCommentsCount, upstreamUpdatedAt.String, readUpstreamUpdatedAt.String)
		if len(kinds) == 0 {
			continue
		}
		result = append(result, Discrepancy{ChallengeID: formatChallengeID(id), Kinds: kinds})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result, err = addMergedUnapproved(ctx, tx, result)
	if err != nil {
		return nil, err
	}
	return addDependencyCycles(ctx, tx, result)
}

// addDependencyCycles は循環に含まれる課題を食い違いに足す。課題に既に要素があれば、その先頭の要素の
// kinds の末尾に dependency_cycle を足して ChallengeIDs を持たせ、無ければ新しい要素を足す。
// 課題が完了していても出す。結果は課題 ID の昇順。
func addDependencyCycles(ctx context.Context, tx *sql.Tx, result []Discrepancy) ([]Discrepancy, error) {
	cycles, err := dependencyCycleDiscrepancies(ctx, tx)
	if err != nil {
		return nil, err
	}
	if len(cycles) == 0 {
		return result, nil
	}
	for _, c := range cycles {
		merged := false
		for i := range result {
			if result[i].ChallengeID == c.ChallengeID {
				result[i].Kinds = append(result[i].Kinds, DiscrepancyKindDependencyCycle)
				result[i].ChallengeIDs = c.ChallengeIDs
				merged = true
				break
			}
		}
		if !merged {
			result = append(result, c)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, _ := parseChallengeID(result[i].ChallengeID)
		b, _ := parseChallengeID(result[j].ChallengeID)
		return a < b
	})
	return result, nil
}

// addMergedUnapproved は、直前の試行（operation_execution で id が最大の行）の結果が
// merged_unapproved で、取り消されて pending に戻っている release を食い違いに足す。
// 課題に上流の食い違いの要素があれば、その kinds の末尾に足して operation_id を持たせる。
// 同じ課題に該当する release がほかにもあれば、その分は kinds が merged_unapproved だけの
// 要素を足す。課題が完了していても出す（承認なしの反映は完了後も人が見る）。結果は課題 ID
// の昇順、同じ課題の中では操作 ID の昇順。
func addMergedUnapproved(ctx context.Context, tx *sql.Tx, result []Discrepancy) ([]Discrepancy, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT o.challenge_id, o.id FROM operation o
		WHERE o.kind = ? AND o.state = ?
		  AND (SELECT e.result FROM operation_execution e WHERE e.operation_id = o.id ORDER BY e.id DESC LIMIT 1) = ?
		ORDER BY o.challenge_id ASC, o.id ASC`,
		string(OperationKindRelease), string(OperationStatePending), string(ExecutionResultMergedUnapproved))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid, oid int64
		if err := rows.Scan(&cid, &oid); err != nil {
			return nil, err
		}
		cidStr, opStr := formatChallengeID(cid), formatOperationID(oid)
		merged := false
		for i := range result {
			if result[i].ChallengeID == cidStr && result[i].OperationID == "" {
				result[i].Kinds = append(result[i].Kinds, DiscrepancyKindMergedUnapproved)
				result[i].OperationID = opStr
				merged = true
				break
			}
		}
		if !merged {
			result = append(result, Discrepancy{ChallengeID: cidStr, Kinds: []DiscrepancyKind{DiscrepancyKindMergedUnapproved}, OperationID: opStr})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, _ := parseChallengeID(result[i].ChallengeID)
		b, _ := parseChallengeID(result[j].ChallengeID)
		return a < b
	})
	return result, nil
}

// listUnmetDependencies は課題ごとに、依存の評価（#325）で満たされていない依存先を返す。
func listUnmetDependencies(ctx context.Context, tx *sql.Tx, challenges []Challenge) (map[string][]DependencyView, error) {
	out := map[string][]DependencyView{}
	if len(challenges) == 0 {
		return out, nil
	}
	eval, err := evaluateDependencies(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, c := range challenges {
		cid, ok := parseChallengeID(c.ID)
		if !ok {
			continue
		}
		views, err := dependencyViews(ctx, tx, eval, cid)
		if err != nil {
			return nil, err
		}
		for _, v := range views {
			if !v.Satisfied {
				out[c.ID] = append(out[c.ID], v)
			}
		}
	}
	return out, nil
}

// GetOverview は `flywheel status` の本体。課題を「人間の操作を待っているもの」
// （計画承認待ち・完了確認待ち・人間対応待ち）と「システムが次に進められるもの」
// （未分類・分類済・着手中・検証中）に、不可逆操作を「未承認（pending）」と
// 「承認済み（approved）」に分ける。完了（done）の課題と差し戻し済み（rejected）の
// 不可逆操作はどちらの結果にも含めない（docs/features/m1-core.md §状況の表示）。
//
// 読み取り専用: 状態・版・作業ログを一切変えない（s.db.Read を使い、mutate は
// 呼ばない）。
func (s *Store) GetOverview(ctx context.Context) (*Overview, error) {
	return s.GetOverviewFor(ctx, nil)
}

// GetOverviewFor は GetOverview に、計画の枠の額を引く宣言（サイズの既定。nil 可）を渡す版。
// agent が nil のときの needs_human.budget_exhausted は、計画の出力が枠の額を明記する課題だけを
// 判定できる。
func (s *Store) GetOverviewFor(ctx context.Context, agent *AgentDeclaration) (*Overview, error) {
	result := Overview{WaitingExternal: []WaitingExternal{}}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		humanChallenges, err := listChallengesInStatuses(ctx, tx, needsHumanStatuses)
		if err != nil {
			return err
		}
		result.NeedsHumanChallenges = humanChallenges

		actionable, err := listChallengesInStatuses(ctx, tx, actionableStatuses)
		if err != nil {
			return err
		}
		result.ActionableChallenges = actionable

		unmet, err := listUnmetDependencies(ctx, tx, actionable)
		if err != nil {
			return err
		}
		result.UnmetDependencies = unmet

		pendingOps, err := listOperationsInState(ctx, tx, OperationStatePending)
		if err != nil {
			return err
		}
		result.NeedsHumanOperations = pendingOps

		approvedOps, err := listOperationsInState(ctx, tx, OperationStateApproved)
		if err != nil {
			return err
		}
		result.ApprovedOperations = approvedOps

		discrepancies, err := listDiscrepancies(ctx, tx)
		if err != nil {
			return err
		}
		result.Discrepancies = discrepancies

		triage, err := listJ1Triage(ctx, tx)
		if err != nil {
			return err
		}
		result.NeedsHumanTriage = triage

		slots, err := listSlotsNeedingAttention(ctx, tx)
		if err != nil {
			return err
		}
		result.NeedsHumanSlots = slots

		exhausted, err := listBudgetExhausted(ctx, tx, agent)
		if err != nil {
			return err
		}
		result.NeedsHumanBudgetExhausted = exhausted
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &result, nil
}

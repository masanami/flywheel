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
	// m2-github-issue-ingest.md §食い違いの表示）。#58 は
	// DiscrepancyKindUpstreamClosed・DiscrepancyKindUpstreamMissing・
	// DiscrepancyKindOutOfPolicy の 3 種類だけを導く（upstream_commented・
	// upstream_updated は #65 系のチケットが discrepancyKinds を拡張して足す）。
	Discrepancies []Discrepancy
}

// DiscrepancyKind は Discrepancy.Kinds の値（docs/features/
// m2-github-issue-ingest.md §食い違いの表示・§IF / API「`status` と `show` の
// 拡張」の閉集合の部分集合。列挙の順で並べる）。
type DiscrepancyKind string

// DiscrepancyKind の値（#58 が導く 3 種類だけ。仕様の閉集合の順）。
const (
	DiscrepancyKindUpstreamClosed  DiscrepancyKind = "upstream_closed"
	DiscrepancyKindUpstreamMissing DiscrepancyKind = "upstream_missing"
	DiscrepancyKindOutOfPolicy     DiscrepancyKind = "out_of_policy"
)

// discrepancyKindValues は DiscrepancyKind の閉集合（#58 が導く分だけ。仕様の
// 列挙の順）。
var discrepancyKindValues = []DiscrepancyKind{
	DiscrepancyKindUpstreamClosed,
	DiscrepancyKindUpstreamMissing,
	DiscrepancyKindOutOfPolicy,
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
	return result, rows.Err()
}

// discrepancyKinds は、対応 1 件の上流の状態・ポリシーの状態から食い違いの
// 種類を導く（§食い違いの表示「食い違いは記録から導き、別に保存しない」）。
// この関数だけに閉じておくことで、#65 系のチケットが未読の更新
// （upstream_commented・upstream_updated）を足すときにここへ追記できる
// （複数箇所に判定ロジックが散らばらない）。
func discrepancyKinds(upstream upstreamState, policy policyState) []DiscrepancyKind {
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
	return kinds
}

// listDiscrepancies は完了していない課題のうち、対応がある（source_binding を
// 持つ）ものを challenge.id 昇順に読み、discrepancyKinds が非空を返すものだけを
// Discrepancy として返す（§食い違いの表示）。対応の無い課題（`create` で作った
// 課題）は source_binding が無いので対象にならない。
func listDiscrepancies(ctx context.Context, tx *sql.Tx) ([]Discrepancy, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT c.id, c.status, sb.upstream_state, sb.policy_state
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
		if err := rows.Scan(&id, &status, &upstream, &policy); err != nil {
			return nil, err
		}
		if IsTerminal(Table, StatusVocabulary, Status(status)) {
			continue
		}
		kinds := discrepancyKinds(upstreamState(upstream), policyState(policy))
		if len(kinds) == 0 {
			continue
		}
		result = append(result, Discrepancy{ChallengeID: formatChallengeID(id), Kinds: kinds})
	}
	return result, rows.Err()
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
	var result Overview
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
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &result, nil
}

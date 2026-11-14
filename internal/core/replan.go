package core

// このファイルは、保留への回答に付ける「範囲が変わる」印（T12・docs/features/m1-core.md §状態機械）の
// 後続の流れを持つ。印の付いた回答は課題を分類済へ戻す（transition.go の IfReplan）。計画を作る判断点（J2）が
// 回答の文と前の計画を読んで計画を作り直し、計画の承認待ちへ進める（初回の計画と同じゲート）。承認の後の委譲は
// 前の計画の成果物（PR・ブランチ）を引き継ぎ、使った費用は計画の版をまたいで積み上がる（loadBucketSpend）。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// labelJ2Replan は J2 の入力で、計画の作り直しの材料を渡すデータの区画の名前。
const labelJ2Replan = "計画の作り直し"

// labelPreviousDeliverable は J3 の入力で、前の計画の成果物を渡すデータの区画の名前。
const labelPreviousDeliverable = "前の計画の成果物"

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ReplanAllowedFrom は、保留の直前の状態 from の保留へ、印の付いた回答ができるか。
func ReplanAllowedFrom(from Status) bool {
	for _, s := range replanSources {
		if s == from {
			return true
		}
	}
	return false
}

// replanNote は計画の作り直しの材料。
type replanNote struct {
	// Question・Answer は、印の付いた最後の回答の保留の問いと回答。
	Question string
	Answer   string
	// PlanVersion・PlanBody は、課題の最新の計画の版と本文（前の計画）。
	PlanVersion int
	PlanBody    string
	// PlanCreatedAt は最新の計画の作成時刻（回答より後の計画は、回答を踏まえて作り直した計画）。
	PlanCreatedAt string
	// AnsweredAt は回答の時刻。
	AnsweredAt string
}

// loadReplanNote は、課題に印の付いた回答済みの保留があるとき、その最後のものと最新の計画を返す。無ければ nil。
func loadReplanNote(ctx context.Context, tx *sql.Tx, challengeID int64) (*replanNote, error) {
	var n replanNote
	var answeredAt string
	err := tx.QueryRowContext(ctx,
		`SELECT question, answer, answered_at FROM hold WHERE challenge_id = ? AND replan = 1 AND answer IS NOT NULL ORDER BY id DESC LIMIT 1`,
		challengeID).Scan(&n.Question, &n.Answer, &answeredAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var planCreatedAt string
	err = tx.QueryRowContext(ctx,
		`SELECT version, body, created_at FROM task_plan WHERE challenge_id = ? ORDER BY version DESC LIMIT 1`, challengeID).Scan(&n.PlanVersion, &n.PlanBody, &planCreatedAt)
	if err == sql.ErrNoRows {
		n.AnsweredAt = answeredAt
		return &n, nil
	}
	if err != nil {
		return nil, err
	}
	n.PlanCreatedAt = planCreatedAt
	n.AnsweredAt = answeredAt
	return &n, nil
}

// formatReplanSection は J2 の入力の区画「計画の作り直し」の本文。
func formatReplanSection(n *replanNote) string {
	var b strings.Builder
	b.WriteString("人間が保留への回答で、範囲・方針・達成条件が変わると印を付けた。この課題の計画を、回答に合わせて作り直す。\n")
	b.WriteString("回答は人間の決定であり、前の計画と食い違う点は回答に従う。前の計画の作業はすでに一部または全部が行われている（成果物は引き継がれ、新しい計画の達成条件に合わせて直される）。\n\n")
	b.WriteString("保留の問い:\n" + n.Question + "\n\n")
	b.WriteString("回答:\n" + n.Answer + "\n")
	if n.PlanBody != "" {
		fmt.Fprintf(&b, "\n前の計画（版 %d）の本文:\n%s\n", n.PlanVersion, n.PlanBody)
	}
	return strings.TrimRight(b.String(), "\n")
}

// previousDeliverable は前の計画の成果物（PR・ブランチ）。
type previousDeliverable struct {
	PRs      []string
	Branches []string
}

func (d *previousDeliverable) empty() bool { return len(d.PRs) == 0 && len(d.Branches) == 0 }

// loadPreviousDeliverable は、課題に印の付いた回答があり、いまの計画の版（planVersion）の委譲の run がまだ
// 無いとき、それより前の版の委譲の run の成果物（PR・ブランチ）を返す。該当しなければ nil。
func loadPreviousDeliverable(ctx context.Context, tx *sql.Tx, challengeID, planVersion int64) (*previousDeliverable, error) {
	note, err := loadReplanNote(ctx, tx, challengeID)
	if err != nil || note == nil {
		return nil, err
	}
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run WHERE challenge_id = ? AND kind IN ('delegate', 'contract') AND plan_version = ?`,
		challengeID, planVersion).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT a.kind, a.ref FROM run_artifact a JOIN run r ON r.id = a.run_id
		 WHERE r.challenge_id = ? AND r.kind IN ('delegate', 'contract') AND r.plan_version < ? AND a.kind IN (?, ?)
		 ORDER BY a.id ASC`, challengeID, planVersion, string(artifactKindPR), string(artifactKindBranch))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	d := &previousDeliverable{}
	seen := map[string]bool{}
	for rows.Next() {
		var kind, ref string
		if err := rows.Scan(&kind, &ref); err != nil {
			return nil, err
		}
		if seen[kind+"\x00"+ref] {
			continue
		}
		seen[kind+"\x00"+ref] = true
		if kind == string(artifactKindPR) {
			d.PRs = append(d.PRs, ref)
		} else {
			d.Branches = append(d.Branches, ref)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if d.empty() {
		return nil, nil
	}
	return d, nil
}

// formatPreviousDeliverableSection は J3 の入力の区画「前の計画の成果物」の本文。
func formatPreviousDeliverableSection(d *previousDeliverable) string {
	var b strings.Builder
	b.WriteString("この課題は計画を作り直して承認を受けた。前の計画の作業の成果物が残っている。\n")
	if len(d.PRs) > 0 {
		b.WriteString("PR: " + strings.Join(d.PRs, ", ") + "\n")
	}
	if len(d.Branches) > 0 {
		b.WriteString("ブランチ: " + strings.Join(d.Branches, ", ") + "\n")
	}
	b.WriteString("やること: 新しい計画の達成条件に合わせて、この成果物（同じ PR・同じブランチ）を直す。新しい PR・新しいブランチを作らない。")
	return b.String()
}

// このファイルは #99（親要件チケット #98 §クリティカル設計決定 1・
// docs/features/m3-invoker-delegation.md）が足すマイグレーション 0005 の
// slot・run_artifact 表に対する、非公開の行レベルの読み書きと、計画の枠の
// 上書き（flywheel budget）の記録を持つ。
//
// slot・run_artifact の書き込みは作業ログ（activity）に載せない（M3P11）。
// 閉集合（slot.provider・slot.state・run_artifact.kind・run_artifact.state）は
// 0001 以降の流儀どおりスキーマに CHECK 制約を付けず、このファイルで検証する。

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// --- slot ---

var slotIDPattern = regexp.MustCompile(`^SL-[1-9][0-9]*$`)

func formatSlotID(id int64) string { return fmt.Sprintf("SL-%d", id) }

// parseSlotID は s を slot の内部整数 ID として解釈する。形式不正は ok=false。
func parseSlotID(s string) (int64, bool) {
	if !slotIDPattern.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "SL-"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// slotProvider は slot.provider（スロットの作業ツリーの出所）。
type slotProvider string

const (
	slotProviderClone    slotProvider = "clone"
	slotProviderWorktree slotProvider = "worktree"
)

func (p slotProvider) valid() bool { return p == slotProviderClone || p == slotProviderWorktree }

// slotState は slot.state。
type slotState string

const (
	slotStateIdle           slotState = "idle"
	slotStateBusy           slotState = "busy"
	slotStateNeedsAttention slotState = "needs_attention"
)

func (s slotState) valid() bool {
	switch s {
	case slotStateIdle, slotStateBusy, slotStateNeedsAttention:
		return true
	}
	return false
}

// slotRow は slot 表の 1 行。ID・RunID は表示形（"SL-<n>"・"R-<n>"）。
// AttentionReason は state が needs_attention の理由（払い出しの失敗・
// パスが無い・未コミットの変更など。FR64・M3P45。自由記述）で、"" が NULL。
type slotRow struct {
	ID              string
	Repo            string
	Provider        slotProvider
	Path            string
	State           slotState
	RunID           *string
	AttentionReason string
}

const slotSelectColumns = `SELECT id, repo, provider, path, state, run_id, attention_reason FROM slot`

func scanSlotRow(row interface{ Scan(dest ...any) error }) (*slotRow, error) {
	var (
		id                          int64
		repo, provider, path, state string
		runID                       sql.NullInt64
		reason                      sql.NullString
	)
	if err := row.Scan(&id, &repo, &provider, &path, &state, &runID, &reason); err != nil {
		return nil, err
	}
	r := &slotRow{
		ID: formatSlotID(id), Repo: repo, Provider: slotProvider(provider), Path: path,
		State: slotState(state), AttentionReason: reason.String,
	}
	if runID.Valid {
		v := formatRunID(runID.Int64)
		r.RunID = &v
	}
	return r, nil
}

// insertSlotInput は insertSlot の入力。新しいスロットは idle で作る。
type insertSlotInput struct {
	Repo     string
	Provider slotProvider
	Path     string
}

func (in insertSlotInput) valid() bool {
	return in.Repo != "" && in.Provider.valid() && in.Path != ""
}

// insertSlot は idle のスロットを作る。入力が不正なら ErrValidation。
func insertSlot(ctx context.Context, tx *sql.Tx, in insertSlotInput) (*slotRow, error) {
	if !in.valid() {
		return nil, ErrValidation
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO slot (repo, provider, path, state, run_id, attention_reason) VALUES (?, ?, ?, 'idle', NULL, NULL)`,
		in.Repo, string(in.Provider), in.Path)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return loadSlotByID(ctx, tx, id)
}

// loadSlotByID は id のスロットを tx から読む。無ければ (nil, nil)。
func loadSlotByID(ctx context.Context, tx *sql.Tx, id int64) (*slotRow, error) {
	r, err := scanSlotRow(tx.QueryRowContext(ctx, slotSelectColumns+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// updateSlotState は id のスロットの state・run_id・attention_reason を更新する。
// 整合の規則: busy は runID を必須とし、idle・needs_attention は runID を
// 持たない。attention_reason は needs_attention のときだけ必須（非空）で、
// それ以外では NULL にする。スロットが無ければ (nil, nil)。
func updateSlotState(ctx context.Context, tx *sql.Tx, id int64, state slotState, runID *int64, reason string) (*slotRow, error) {
	if !state.valid() {
		return nil, ErrValidation
	}
	if (state == slotStateBusy) != (runID != nil) {
		return nil, ErrValidation
	}
	if (state == slotStateNeedsAttention) != (reason != "") {
		return nil, ErrValidation
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE slot SET state = ?, run_id = ?, attention_reason = ? WHERE id = ?`,
		string(state), int64PtrColumn(runID), nullableTextColumn(reason), id)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, nil
	}
	return loadSlotByID(ctx, tx, id)
}

// --- run_artifact ---

// artifactKind は run_artifact.kind。
type artifactKind string

const (
	artifactKindBranch artifactKind = "branch"
	artifactKindPR     artifactKind = "pr"
	artifactKindCommit artifactKind = "commit"
)

func (k artifactKind) valid() bool {
	return k == artifactKindBranch || k == artifactKindPR || k == artifactKindCommit
}

// artifactState は run_artifact.state（PR のときだけ持つ）。
type artifactState string

const (
	artifactStateOpen   artifactState = "open"
	artifactStateClosed artifactState = "closed"
	artifactStateMerged artifactState = "merged"
)

func (s artifactState) valid() bool {
	return s == artifactStateOpen || s == artifactStateClosed || s == artifactStateMerged
}

// runArtifactRow は run_artifact 表の 1 行。RunID は表示形（"R-<n>"）。
// State・Base は "" が NULL、VerifiedAt は nil が NULL。
type runArtifactRow struct {
	ID         int64
	RunID      string
	Kind       artifactKind
	Ref        string
	State      artifactState
	Base       string
	VerifiedAt *time.Time
}

const runArtifactSelectColumns = `SELECT id, run_id, kind, ref, state, base, verified_at FROM run_artifact`

func scanRunArtifactRow(row interface{ Scan(dest ...any) error }) (*runArtifactRow, error) {
	var (
		id, runID       int64
		kind, ref       string
		state, base, at sql.NullString
	)
	if err := row.Scan(&id, &runID, &kind, &ref, &state, &base, &at); err != nil {
		return nil, err
	}
	r := &runArtifactRow{
		ID: id, RunID: formatRunID(runID), Kind: artifactKind(kind), Ref: ref,
		State: artifactState(state.String), Base: base.String,
	}
	if at.Valid {
		t, err := parseTimestamp(at.String)
		if err != nil {
			return nil, err
		}
		r.VerifiedAt = &t
	}
	return r, nil
}

// insertRunArtifactInput は insertRunArtifact の入力。State は PR のときだけ
// 指定でき（open | closed | merged）、PR 以外では "" でなければならない。
type insertRunArtifactInput struct {
	Kind       artifactKind
	Ref        string
	State      artifactState
	Base       string
	VerifiedAt *time.Time
}

func (in insertRunArtifactInput) valid() bool {
	if !in.Kind.valid() || in.Ref == "" {
		return false
	}
	if in.Kind == artifactKindPR {
		return in.State == "" || in.State.valid()
	}
	return in.State == ""
}

// insertRunArtifact は runID の run の成果物を 1 件記録する。入力が不正なら
// ErrValidation。runID の run が無ければ外部キー違反の生のエラーが返る。
func insertRunArtifact(ctx context.Context, tx *sql.Tx, runID int64, in insertRunArtifactInput) (*runArtifactRow, error) {
	if !in.valid() {
		return nil, ErrValidation
	}
	var verified any
	if in.VerifiedAt != nil {
		verified = formatTimestamp(*in.VerifiedAt)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO run_artifact (run_id, kind, ref, state, base, verified_at) VALUES (?, ?, ?, ?, ?, ?)`,
		runID, string(in.Kind), in.Ref, nullableTextColumn(string(in.State)), nullableTextColumn(in.Base), verified)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	r, err := scanRunArtifactRow(tx.QueryRowContext(ctx, runArtifactSelectColumns+" WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return r, nil
}

// listRunArtifacts は runID の run の成果物を id の昇順で返す。
func listRunArtifacts(ctx context.Context, tx *sql.Tx, runID int64) ([]runArtifactRow, error) {
	rows, err := tx.QueryContext(ctx, runArtifactSelectColumns+" WHERE run_id = ? ORDER BY id", runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []runArtifactRow
	for rows.Next() {
		r, err := scanRunArtifactRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// --- 計画の枠の上書き（flywheel budget） ---

// planBudgetOverride は task_plan の実装枠・レビュー対応枠の上書き額（USD の
// 100 万分の 1 を単位とする整数。nil は上書きなし）。
type planBudgetOverride struct {
	ImplBudgetUSD   *int64
	ReviewBudgetUSD *int64
}

// setPlanBudgetOverride は challengeID の計画の版 version の上書きを記録する
// （nil を渡した枠は NULL に戻す）。額は正でなければならず、不正なら
// ErrValidation。その版の計画が無ければ (false, nil)。
func setPlanBudgetOverride(ctx context.Context, tx *sql.Tx, challengeID, version int64, o planBudgetOverride) (bool, error) {
	for _, p := range []*int64{o.ImplBudgetUSD, o.ReviewBudgetUSD} {
		if p != nil && *p <= 0 {
			return false, ErrValidation
		}
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE task_plan SET impl_budget_usd = ?, review_budget_usd = ? WHERE challenge_id = ? AND version = ?`,
		int64PtrColumn(o.ImplBudgetUSD), int64PtrColumn(o.ReviewBudgetUSD), challengeID, version)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// loadPlanBudgetOverride は challengeID の計画の版 version の上書きを読む。
// その版の計画が無ければ (nil, nil)。
func loadPlanBudgetOverride(ctx context.Context, tx *sql.Tx, challengeID, version int64) (*planBudgetOverride, error) {
	var impl, review sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT impl_budget_usd, review_budget_usd FROM task_plan WHERE challenge_id = ? AND version = ?`,
		challengeID, version).Scan(&impl, &review)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o := &planBudgetOverride{}
	if impl.Valid {
		v := impl.Int64
		o.ImplBudgetUSD = &v
	}
	if review.Valid {
		v := review.Int64
		o.ReviewBudgetUSD = &v
	}
	return o, nil
}

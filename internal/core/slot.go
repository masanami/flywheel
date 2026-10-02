package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// このファイルは実行スロットの規則（親要件チケット #98 §実行スロット・
// §プロバイダ worktree。M3H7・M3H8・M3P37・M3P45・M3P47）を持つ: 行の作成・
// 割り当てと解放（ストアのトランザクション）・割り当て前の検査・払い出し・
// needs_attention と `slot clear`・status の一覧。
//
// slot の書き込みは作業ログ（activity）に載せない（M3P11）。flywheel は
// スロットの作業ツリーの変更を消さない（reset・clean・checkout -- をしない）。
// git の起動は SlotGit（internal/adapters/git）の口を通す。

// worktreeSlotsDir はプロバイダ worktree のスロットの置き場（ワークスペースからの
// 相対。`<リポジトリ名>/<SL-ID>` をその下に作る。起草者の仮定）。
var worktreeSlotsDir = filepath.Join(".flywheel", "worktrees")

// Slot は slot 表の 1 行の公開形。ID・RunID は表示形（"SL-<n>"・"R-<n>"）。
// AttentionReason は State が needs_attention の理由（"" は無し）。
type Slot struct {
	ID              string
	Repo            string
	Provider        string
	Path            string
	State           string
	RunID           *string
	AttentionReason string
}

func slotFromRow(r *slotRow) Slot {
	return Slot{
		ID: r.ID, Repo: r.Repo, Provider: string(r.Provider), Path: r.Path,
		State: string(r.State), RunID: r.RunID, AttentionReason: r.AttentionReason,
	}
}

// SlotAssignment は AcquireSlot が割り当てたスロット。RunID は bind が返した run
// の表示形。
type SlotAssignment struct {
	SlotID string
	Repo   string
	Path   string
	RunID  string
}

// SlotBinder は AcquireSlot が、スロットを busy にするのと同じトランザクションの
// 中で呼ぶ。スロットの内部整数 ID を受け取り、そのスロットを使う run を作って
// （insertRun の SlotID に渡す）その run の内部整数 ID を返す。
type SlotBinder func(tx *sql.Tx, slotID int64) (runID int64, err error)

// slotCandidates は repo の宣言に従って使いうるスロットの行を id 昇順で返す
// （宣言の provider と一致する行のうち、clone は宣言のパスの行、worktree は
// 先頭の count 本）。宣言から外れた行は選ばない（消さない）。
func slotCandidates(rows []slotRow, repo ConnectorRepo, workspace string) []slotRow {
	p := slotProvider(repo.Slots.Provider)
	var out []slotRow
	switch p {
	case slotProviderClone:
		declared := map[string]bool{}
		for _, rel := range repo.Slots.Paths {
			declared[filepath.Join(workspace, rel)] = true
		}
		for _, r := range rows {
			if r.Repo == repo.Name && r.Provider == p && declared[r.Path] {
				out = append(out, r)
			}
		}
	case slotProviderWorktree:
		for _, r := range rows {
			if r.Repo == repo.Name && r.Provider == p && len(out) < repo.Slots.Count {
				out = append(out, r)
			}
		}
	}
	return out
}

func loadSlotRows(ctx context.Context, tx *sql.Tx) ([]slotRow, error) {
	rows, err := tx.QueryContext(ctx, slotSelectColumns+" ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []slotRow
	for rows.Next() {
		r, err := scanSlotRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ensureRepoSlots は repo の宣言に足りないスロットの行を idle で作る
// （clone: 宣言のパスごとに 1 行。worktree: count に足りない分。既にある行・
// 宣言から外れた行は変えない）。
func ensureRepoSlots(ctx context.Context, tx *sql.Tx, repo ConnectorRepo, workspace string) error {
	rows, err := loadSlotRows(ctx, tx)
	if err != nil {
		return err
	}
	switch slotProvider(repo.Slots.Provider) {
	case slotProviderClone:
		have := map[string]bool{}
		for _, r := range rows {
			if r.Repo == repo.Name && r.Provider == slotProviderClone {
				have[r.Path] = true
			}
		}
		for _, rel := range repo.Slots.Paths {
			p := filepath.Join(workspace, rel)
			if have[p] {
				continue
			}
			have[p] = true
			if _, err := insertSlot(ctx, tx, insertSlotInput{Repo: repo.Name, Provider: slotProviderClone, Path: p}); err != nil {
				return err
			}
		}
	case slotProviderWorktree:
		have := 0
		for _, r := range rows {
			if r.Repo == repo.Name && r.Provider == slotProviderWorktree {
				have++
			}
		}
		for ; have < repo.Slots.Count; have++ {
			// パスに SL-ID を含めるため、行の作成後に確定する（UNIQUE(repo, path) に
			// 当たらないよう、仮のパスは 1 行ごとに確定してから次の行を作る）。
			r, err := insertSlot(ctx, tx, insertSlotInput{Repo: repo.Name, Provider: slotProviderWorktree, Path: "pending"})
			if err != nil {
				return err
			}
			path := filepath.Join(workspace, worktreeSlotsDir, repo.Name, r.ID)
			if _, err := tx.ExecContext(ctx, `UPDATE slot SET path = ? WHERE id = ?`, path, mustParseSlotID(r.ID)); err != nil {
				return err
			}
		}
	}
	return nil
}

func mustParseSlotID(s string) int64 {
	n, _ := parseSlotID(s)
	return n
}

// EnsureSlots は repo の宣言に足りないスロットの行を idle で作る（委譲の段・
// run の開始時に呼ぶ。clone: 宣言のパスごと。worktree: count 本。起草者の仮定）。
// clone のパスにディレクトリは作らない。
func (s *Store) EnsureSlots(ctx context.Context, repo ConnectorRepo) error {
	if slotProvider(repo.Slots.Provider) == slotProviderWorktree {
		// 置き場 .flywheel/worktrees/<リポジトリ名>/ の外へ払い出さない。
		if !filepath.IsLocal(repo.Name) || strings.ContainsAny(repo.Name, `/\`) {
			return fmt.Errorf("%w: repo name %q is not usable as a worktree directory name", ErrValidation, repo.Name)
		}
		if err := ensureWorktreesIgnored(s.workspace); err != nil {
			return err
		}
	}
	err := s.mutateSlots(ctx, func(tx *sql.Tx) error {
		return ensureRepoSlots(ctx, tx, repo, s.workspace)
	})
	return classifyReadWriteErr(err)
}

// ensureWorktreesIgnored は .flywheel/.gitignore に worktrees/ の行が無ければ足す
// （払い出した作業ツリーを Git で追跡させない。.gitignore が無ければ core の既定の
// 内容で作る）。
func ensureWorktreesIgnored(workspace string) error {
	if err := EnsureWorkspaceGitignore(workspace); err != nil {
		return err
	}
	path := filepath.Join(workspace, flywheelDirName, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: read %s: %w", ErrStoreError, path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if t := strings.TrimSpace(line); t == "worktrees/" || t == "/worktrees/" || t == "worktrees" {
			return nil
		}
	}
	add := "worktrees/\n"
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		add = "\n" + add
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("%w: open %s: %w", ErrStoreError, path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(add); err != nil {
		return fmt.Errorf("%w: write %s: %w", ErrStoreError, path, err)
	}
	return nil
}

// mutateSlots は slot の書き込みトランザクション（作業ログに載せない）。
func (s *Store) mutateSlots(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return s.db.Write(ctx, fn)
}

// ListSlots は全スロットを id 昇順で返す。
func (s *Store) ListSlots(ctx context.Context) ([]Slot, error) {
	var out []Slot
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := loadSlotRows(ctx, tx)
		if err != nil {
			return err
		}
		for i := range rows {
			out = append(out, slotFromRow(&rows[i]))
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

// prepareSlot は割り当て前の検査（払い出しを含む）を行い、満たさない理由を返す
// （"" は割り当てられる）。git は SlotGit だけを通して呼ぶ。
// git を起動できない（ErrGitUnavailable）・ctx の取り消しのように、スロットの
// 作業ツリーに原因が無い失敗は理由にせず err で返す（スロットを needs_attention
// にしない）。
func (s *Store) prepareSlot(ctx context.Context, git SlotGit, repo ConnectorRepo, slot slotRow) (string, error) {
	tree := SlotTree{Path: slot.Path}
	if slot.Provider == slotProviderWorktree {
		base := filepath.Join(s.workspace, repo.Slots.Base)
		tree.BaseClone = base
		if _, err := git.EnsureWorktree(ctx, WorktreeRequest{
			BaseClone: base, Path: slot.Path, Ref: "refs/heads/" + repo.DefaultBranch,
		}); err != nil {
			if isEnvironmentFailure(ctx, err) {
				return "", err
			}
			return "provisioning the worktree failed: " + oneLine(err.Error()), nil
		}
	}
	st, err := git.Inspect(ctx, tree)
	if err != nil && isEnvironmentFailure(ctx, err) {
		return "", err
	}
	switch {
	case err != nil:
		return "inspecting the working tree failed: " + oneLine(err.Error()), nil
	case !st.Exists:
		return "the slot path does not exist: " + slot.Path, nil
	case !st.PointerOK:
		return "the working tree's .git pointer does not point at this worktree of the base clone: " + oneLine(st.PointerProblem), nil
	case st.Dirty:
		return "the working tree has uncommitted changes", nil
	case !remoteMatches(st.OriginURL, repo.Remote):
		return fmt.Sprintf("origin (%q) is not the declared remote %s", st.OriginURL, repo.Remote), nil
	}
	return "", nil
}

func isEnvironmentFailure(ctx context.Context, err error) bool {
	return errors.Is(err, ErrGitUnavailable) || ctx.Err() != nil
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// AcquireSlot は repo の使えるスロットを 1 本選び、割り当て前の検査（worktree は
// 初回の払い出しを含む）を通ったものを、bind が返す run に割り当てて busy にする
// （M3P45・M3P47・M3H8）。割り当ては 1 つのストアのトランザクションの中で行い、
// 2 つのプロセスが同じスロットを取らない。検査に失敗したスロットは割り当てずに
// needs_attention にして理由を記録し、次の idle のスロットを試す。needs_attention・
// busy のスロットは選ばない。使えるスロットが無ければ ErrSlotUnavailable。
func (s *Store) AcquireSlot(ctx context.Context, git SlotGit, repo ConnectorRepo, bind SlotBinder) (*SlotAssignment, error) {
	if git == nil || bind == nil {
		return nil, ErrValidation
	}
	if err := s.EnsureSlots(ctx, repo); err != nil {
		return nil, err
	}
	tried := map[string]bool{}
	for {
		next, err := s.nextIdleSlot(ctx, repo, tried)
		if err != nil {
			return nil, err
		}
		if next == nil {
			return nil, ErrSlotUnavailable
		}
		tried[next.ID] = true

		reason, err := s.prepareSlot(ctx, git, repo, *next)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			if err := s.markSlotNeedsAttention(ctx, *next, reason); err != nil {
				return nil, err
			}
			continue
		}
		a, err := s.assignSlot(ctx, *next, bind)
		if err != nil {
			return nil, err
		}
		if a != nil {
			return a, nil
		}
	}
}

func (s *Store) nextIdleSlot(ctx context.Context, repo ConnectorRepo, tried map[string]bool) (*slotRow, error) {
	var found *slotRow
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := loadSlotRows(ctx, tx)
		if err != nil {
			return err
		}
		for _, r := range slotCandidates(rows, repo, s.workspace) {
			if r.State == slotStateIdle && !tried[r.ID] {
				r := r
				found = &r
				return nil
			}
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return found, nil
}

// markSlotNeedsAttention は slot がまだ idle なら needs_attention にして理由を
// 記録する（別のプロセスが割り当て済みのスロットは変えない）。
func (s *Store) markSlotNeedsAttention(ctx context.Context, slot slotRow, reason string) error {
	err := s.mutateSlots(ctx, func(tx *sql.Tx) error {
		id, _ := parseSlotID(slot.ID)
		cur, err := loadSlotByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur == nil || cur.State != slotStateIdle {
			return nil
		}
		_, err = updateSlotState(ctx, tx, id, slotStateNeedsAttention, nil, reason)
		return err
	})
	return classifyReadWriteErr(err)
}

// assignSlot はトランザクションの中で slot がまだ idle であることを確かめ、bind が
// 作る run に割り当てて busy にする。他のプロセスが先に取っていれば (nil, nil)。
func (s *Store) assignSlot(ctx context.Context, slot slotRow, bind SlotBinder) (*SlotAssignment, error) {
	var out *SlotAssignment
	err := s.mutateSlots(ctx, func(tx *sql.Tx) error {
		id, _ := parseSlotID(slot.ID)
		cur, err := loadSlotByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur == nil || cur.State != slotStateIdle {
			return nil
		}
		runID, err := bind(tx, id)
		if err != nil {
			return err
		}
		if _, err := updateSlotState(ctx, tx, id, slotStateBusy, &runID, ""); err != nil {
			return err
		}
		out = &SlotAssignment{SlotID: cur.ID, Repo: cur.Repo, Path: cur.Path, RunID: formatRunID(runID)}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		if errors.Is(err, errActiveSlotRunExists) {
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}

// ReleaseSlot は busy のスロットを idle に戻す（委譲の run が終わった時点で呼ぶ。
// run の終了（result の確定）が先で、終了していない run が残ったままでは同じ
// スロットを次の run に割り当てられない〈idx_run_active_slot〉。
// 子が問いを出して終わった後、回答を待つ間は保持しない）。busy でなければ
// 何もしない。スロットが無い・形式不正は ErrNotFound。
func (s *Store) ReleaseSlot(ctx context.Context, slotID string) error {
	id, ok := parseSlotID(slotID)
	if !ok {
		return ErrNotFound
	}
	err := s.mutateSlots(ctx, func(tx *sql.Tx) error {
		return releaseSlot(ctx, tx, id)
	})
	return classifyReadWriteErr(err)
}

// releaseSlot は run を終わらせるトランザクションの中から呼べる形。
func releaseSlot(ctx context.Context, tx *sql.Tx, id int64) error {
	cur, err := loadSlotByID(ctx, tx, id)
	if err != nil {
		return err
	}
	if cur == nil {
		return ErrNotFound
	}
	if cur.State != slotStateBusy {
		return nil
	}
	_, err = updateSlotState(ctx, tx, id, slotStateIdle, nil, "")
	return err
}

// ClearSlot は needs_attention のスロットを idle に戻す（`flywheel slot clear`。
// 人が作業ツリーを確かめた後に使う。本人確認も端末も要らず、作業ログに載せない）。
// 形式不正・スロットが無いは ErrNotFound（課題の ID と同じく fail-closed）、
// needs_attention でなければ ErrInvalidTransition（使用中のスロットを idle にしない）。
func (s *Store) ClearSlot(ctx context.Context, slotID string) (*Slot, error) {
	id, ok := parseSlotID(slotID)
	if !ok {
		return nil, ErrNotFound
	}
	var out Slot
	err := s.mutateSlots(ctx, func(tx *sql.Tx) error {
		cur, err := loadSlotByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur == nil {
			return ErrNotFound
		}
		if cur.State != slotStateNeedsAttention {
			return ErrInvalidTransition
		}
		r, err := updateSlotState(ctx, tx, id, slotStateIdle, nil, "")
		if err != nil {
			return err
		}
		out = slotFromRow(r)
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &out, nil
}

// SlotAttention は status の needs_human.slots の 1 件（needs_attention のスロット）。
// RunID は使用中の run で、needs_attention のスロットは使用中でないため通常 nil。
type SlotAttention struct {
	SlotID string
	Repo   string
	Path   string
	RunID  *string
	Reason string
}

func listSlotsNeedingAttention(ctx context.Context, tx *sql.Tx) ([]SlotAttention, error) {
	rows, err := tx.QueryContext(ctx, slotSelectColumns+" WHERE state = 'needs_attention' ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]SlotAttention, 0)
	for rows.Next() {
		r, err := scanSlotRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, SlotAttention{SlotID: r.ID, Repo: r.Repo, Path: r.Path, RunID: r.RunID, Reason: r.AttentionReason})
	}
	return out, rows.Err()
}

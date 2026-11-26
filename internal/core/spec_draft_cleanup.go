package core

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"
)

// このファイルは、終わった課題の仕様の下書きの削除を持つ。消すのは、課題が取り下げになったときと、完了した課題に
// まだ実行していない不可逆操作が無くなったとき（完了の後も、反映の前に検証中へ戻して計画を作り直すことがある）。
// 呼ぶのは、遷移・不可逆操作の実行の記録・不可逆操作の単独の差し戻し・承認待ちや承認済みの不可逆操作を実行せずに閉じる書き込みのトランザクションが成立した後で、失敗しても課題の進みは止めない。

var (
	errSpecDraftOutside    = errors.New("the draft resolves outside the draft directory")
	errSpecDraftNotRegular = errors.New("the draft is not a regular file")
)

// cleanupSpecDraft は課題 cid の仕様の文書が下書きで、消してよければ、下書きのファイルを消す。消せなければ、作業ログに
// spec_draft_cleanup_failed を残す。呼び出し元の操作はもう成立しているので、誤りは返さない（ctx の取り消しにも従わない）。
func (s *Store) cleanupSpecDraft(ctx context.Context, cid int64, actor string) {
	ctx = context.WithoutCancel(ctx)
	// 消してよいかの判定と削除は、1 つの書き込みのトランザクションの中で行う。課題を作る・直す書き込みと排他になるので、
	// 判定の後に、同じ下書きを指す課題が足されてから消す、という順にはならない。
	var draft string
	var removeErr error
	err := s.db.Write(ctx, func(tx *sql.Tx) (err error) {
		if draft, err = s.removableSpecDraft(ctx, tx, cid); err != nil || draft == "" {
			return err
		}
		removeErr = removeSpecDraft(s.workspace, draft)
		return nil
	})
	// 消してよいかを読めないのはストアの障害で、作業ログも書けないので、何もしない。
	if err != nil || draft == "" || removeErr == nil {
		return
	}
	err = removeErr
	reason := err.Error()
	// 作業ログも書けなければ、残す先が無い。
	_ = s.mutateAs(ctx, actor, ChannelCLI, VerificationNone, func(_ *sql.Tx, rec *activityRecorder) error {
		return rec.record("challenge", cid, ActivityActionSpecDraftCleanupFailed, nil,
			map[string]any{"path": draft, "reason": reason})
	})
}

// removableSpecDraft は、課題 cid の終わりで消してよい下書きの場所を返す（消さないなら空）。同じ下書きを指すほかの課題が、
// まだ消してよい状態でなければ消さない。
func (s *Store) removableSpecDraft(ctx context.Context, tx *sql.Tx, cid int64) (string, error) {
	c, err := loadChallenge(ctx, tx, cid)
	if err != nil || !c.SpecIsDraft() {
		return "", err
	}
	if ok, err := specDraftReleased(ctx, tx, cid, c.Status); err != nil || !ok {
		return "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, status, spec_path FROM challenge WHERE id <> ? AND spec_path IS NOT NULL`, cid)
	if err != nil {
		return "", err
	}
	type other struct {
		id     int64
		status Status
		spec   string
	}
	var others []other
	for rows.Next() {
		var o other
		if err := rows.Scan(&o.id, &o.status, &o.spec); err != nil {
			_ = rows.Close()
			return "", err
		}
		others = append(others, o)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return "", err
	}
	for _, o := range others {
		if !sameSpecFile(s.workspace, *c.SpecPath, o.spec) {
			continue
		}
		if ok, err := specDraftReleased(ctx, tx, o.id, o.status); err != nil || !ok {
			return "", err
		}
	}
	return *c.SpecPath, nil
}

// specDraftReleased は、状態 status の課題 cid が下書きをもう使わないか: 取り下げ、または、完了していて、まだ実行していない
// （承認待ち・承認済みの）不可逆操作が無い。
func specDraftReleased(ctx context.Context, tx *sql.Tx, cid int64, status Status) (bool, error) {
	switch status {
	case StatusWithdrawn:
		return true, nil
	case StatusDone:
		var n int
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operation WHERE challenge_id = ? AND state IN (?, ?)`,
			cid, string(OperationStatePending), string(OperationStateApproved)).Scan(&n)
		return n == 0, err
	}
	return false, nil
}

// sameSpecFile は 2 つの場所が同じ文書を指すか。正規化した形が同じか、リンクをたどった先が同じファイル。
func sameSpecFile(workspace, a, b string) bool {
	if path.Clean(a) == path.Clean(b) {
		return true
	}
	stat := func(p string) (os.FileInfo, error) {
		resolved, err := resolveSpecPath(workspace, p)
		if err != nil {
			return nil, err
		}
		return os.Stat(resolved)
	}
	x, errX := stat(a)
	y, errY := stat(b)
	return errX == nil && errY == nil && os.SameFile(x, y)
}

// removeSpecDraft は下書き p のファイルを消す。消すのは、場所のリンクをたどった先が、下書きの置き場の実体の下にある
// 通常のファイルのときだけ。ファイルがすでに無ければ何もしない。
func removeSpecDraft(workspace, p string) error {
	resolved, err := resolveSpecPath(workspace, p)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return err
	}
	dir, err := followPath(root, specDraftDir)
	if err != nil {
		return err
	}
	if resolved == dir || !pathInside(dir, resolved) {
		return errSpecDraftOutside
	}
	info, err := os.Lstat(resolved)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errSpecDraftNotRegular
	}
	if err := os.Remove(resolved); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

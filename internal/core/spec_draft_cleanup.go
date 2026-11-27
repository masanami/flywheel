package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"
)

// このファイルは、仕様の下書きの削除（終わった課題の後始末と、下書きづくりの記録の下書きを消す共通の処理）を持つ。終わった課題の後始末で消すのは、課題が取り下げになったときと、完了した課題に
// まだ実行していない不可逆操作が無くなったとき（完了の後も、反映の前に検証中へ戻して計画を作り直すことがある）。
// 呼ぶのは、遷移・不可逆操作の実行の記録・不可逆操作の単独の差し戻し・承認待ちや承認済みの不可逆操作を実行せずに閉じる書き込みのトランザクションが成立した後で、失敗しても課題の進みは止めない。

var (
	errSpecDraftOutside    = errors.New("the draft resolves outside the draft directory")
	errSpecDraftNotRegular = errors.New("the draft is not a regular file")
	errSpecDraftShared     = errors.New("another unfinished challenge points at the draft")
)

// cleanupSpecDraft は課題 cid の仕様の文書が下書きで、消してよければ、下書きのファイルを消す。消せなければ、作業ログに
// spec_draft_cleanup_failed を残す。呼び出し元の操作はもう成立しているので、誤りは返さない（ctx の取り消しにも従わない）。
func (s *Store) cleanupSpecDraft(ctx context.Context, cid int64, actor string) {
	s.removeDraftAfterWrite(ctx, cid, actor, func(ctx context.Context, tx *sql.Tx) (string, error, error) {
		draft, err := s.removableSpecDraft(ctx, tx, cid)
		return draft, nil, err
	})
}

// discardSpecDraft は課題 cid の下書きづくりの記録の場所のファイルを消す（記録が無ければ何もしない。記録は変えない）。
// 終わっていないほかの課題が同じ文書を仕様の文書として指していれば消さない。課題 cid 自身の仕様の文書の場所は見ないので、
// 消してよいかは呼び出し元が決める。呼ぶのは、呼び出し元の書き込みが成立した後。消さなかった・消せなかったときは、
// 作業ログに spec_draft_cleanup_failed を残す。誤りは返さない（ctx の取り消しにも従わない）。
func (s *Store) discardSpecDraft(ctx context.Context, cid int64, actor string) {
	s.removeDraftAfterWrite(ctx, cid, actor, func(ctx context.Context, tx *sql.Tx) (string, error, error) {
		d, err := loadSpecDraft(ctx, tx, cid)
		if err != nil || d == nil {
			return "", nil, err
		}
		shared, err := s.specDraftHeldByOther(ctx, tx, cid, d.Path)
		if err != nil || !shared {
			return d.Path, nil, err
		}
		return d.Path, errSpecDraftShared, nil
	})
}

// removeDraftAfterWrite は pick が選んだ下書き draft を消す。pick の keep が非 nil なら、消さずに理由として作業ログに残す。
// draft が空なら何もしない。
func (s *Store) removeDraftAfterWrite(ctx context.Context, cid int64, actor string,
	pick func(ctx context.Context, tx *sql.Tx) (draft string, keep error, err error)) {
	ctx = context.WithoutCancel(ctx)
	// 消してよいかの判定と削除は、1 つの書き込みのトランザクションの中で行う。課題を作る・直す書き込みと排他になるので、
	// 判定の後に、同じ下書きを指す課題が足されてから消す、という順にはならない。
	var draft string
	var kept error
	err := s.db.Write(ctx, func(tx *sql.Tx) (err error) {
		if draft, kept, err = pick(ctx, tx); err != nil || draft == "" || kept != nil {
			return err
		}
		kept = removeSpecDraft(s.workspace, draft)
		return nil
	})
	// 消してよいかを読めないのはストアの障害で、作業ログも書けないので、何もしない。
	if err != nil || draft == "" || kept == nil {
		return
	}
	reason := kept.Error()
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
	if held, err := s.specDraftHeldByOther(ctx, tx, cid, *c.SpecPath); err != nil || held {
		return "", err
	}
	return *c.SpecPath, nil
}

// specDraftHeldByOther は、課題 cid のほかに、下書き draft と同じ文書を仕様の文書として指し、まだ使っている課題があるか。
func (s *Store) specDraftHeldByOther(ctx context.Context, tx *sql.Tx, cid int64, draft string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, status, spec_path FROM challenge WHERE id <> ? AND spec_path IS NOT NULL`, cid)
	if err != nil {
		return false, err
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
			return false, err
		}
		others = append(others, o)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	for _, o := range others {
		if !sameSpecFile(s.workspace, draft, o.spec) {
			continue
		}
		if released, err := specDraftReleased(ctx, tx, o.id, o.status); err != nil || !released {
			return !released, err
		}
	}
	return false, nil
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

// removeSpecDraft は下書き p のファイルを消す。消すのは、下書きの置き場の実体がワークスペースの実体の .flywheel/ の下にあり、
// 場所のリンクをたどった先がその下にある通常のファイルのときだけ。ファイルがすでに無ければ何もしない。
func removeSpecDraft(workspace, p string) error {
	return removeRegularFileUnder(workspace, p, specDraftDir, errSpecDraftOutside, errSpecDraftNotRegular)
}

// flywheelPlacement は .flywheel の下の置き場 dir（ワークスペースから見た相対のパス）の実体を返す。置き場がまだ無くてもよい。
// 実体がワークスペースの実体の .flywheel/ の下に無ければ（置き場かその親がシンボリックリンクで外を指すとき）、outside を
// 含む誤りを返す。
func flywheelPlacement(workspace, dir string, outside error) (string, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return "", err
	}
	base, err := followPath(root, dir)
	if err != nil {
		return "", err
	}
	if top := filepath.Join(root, flywheelDirName); base == top || !pathInside(top, base) {
		return "", fmt.Errorf("%w: the directory %s is not under %s of the workspace", outside, dir, flywheelDirName)
	}
	return base, nil
}

// removeRegularFileUnder はワークスペースから見た相対のパス p のファイルを消す。消すのは、置き場 dir の実体がワークスペースの
// 実体の .flywheel/ の下にあり、p のリンクをたどった先が、その置き場の実体の下にある通常のファイルのときだけ（置き場か p が
// 外なら outside を含む誤り、通常のファイルでなければ notRegular を返す）。ファイルがすでに無ければ何もしない。
func removeRegularFileUnder(workspace, p, dir string, outside, notRegular error) error {
	base, err := flywheelPlacement(workspace, dir, outside)
	if err != nil {
		return err
	}
	resolved, err := resolveSpecPath(workspace, p)
	if err != nil {
		return err
	}
	if resolved == base || !pathInside(base, resolved) {
		return outside
	}
	info, err := os.Lstat(resolved)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return notRegular
	}
	if err := os.Remove(resolved); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

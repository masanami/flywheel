package core

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
)

// このファイルは `cycle` の最後の段 backup（docs/features/m5-tom-migration-and-switchover.md
// §書き出しのコミット・M5P3）を持つ。書き出し（.flywheel/export.json）と memory_dir だけを
// パス指定でコミットする。push しない。LLM を呼ばない。

// BackupOutcome は backup の段の結果（outcome）の閉集合。
type BackupOutcome string

// BackupOutcome の 5 値。
const (
	BackupCommitted BackupOutcome = "committed"
	BackupUnchanged BackupOutcome = "unchanged"
	BackupNotGit    BackupOutcome = "not_git"
	BackupIgnored   BackupOutcome = "ignored"
	BackupFailed    BackupOutcome = "failed"
)

// backupExportRel は書き出しの置き場（ワークスペースからの相対パス）。
const backupExportRel = ".flywheel/export.json"

// BackupResult は backup の段の結果。Commit が空ならコミットを作っていない。
// Detail は failed と ignored で理由を持ち（failed では空にしない）、ほかは空。
type BackupResult struct {
	Outcome BackupOutcome
	Commit  string
	Detail  string
}

// BackupWorkspace は backup の段の本体。書き出しをワークスペースへ書き、Git の作業ツリーなら
// 書き出しと（宣言されていれば）memory_dir だけをコミットする。ストアを書かない（読むだけ）。
// 失敗はエラーにせず outcome=failed の結果で返す（cycle の終了コードとほかの段を変えない）。
func (s *Store) BackupWorkspace(ctx context.Context, git WorkspaceGit, agent *AgentDeclaration, cycleID string) BackupResult {
	ws := s.Workspace()
	exportAbs := filepath.Join(ws, filepath.FromSlash(backupExportRel))
	if err := os.MkdirAll(filepath.Dir(exportAbs), 0o755); err != nil {
		return backupFailed("export: " + err.Error())
	}
	if _, err := s.ExportToFile(ctx, exportAbs); err != nil {
		return backupFailed("export: " + err.Error())
	}

	isTree, err := git.IsWorkTree(ctx, ws)
	if err != nil {
		return backupFailed("git: " + err.Error())
	}
	if !isTree {
		return BackupResult{Outcome: BackupNotGit}
	}
	ignored, err := git.IsIgnored(ctx, ws, exportAbs)
	if err != nil {
		return backupFailed("git: " + err.Error())
	}

	var paths []string
	if !ignored {
		paths = append(paths, exportAbs)
	}
	// memory_dir がワークスペース自身・.flywheel を含む場所を指すときは対象にしない
	// （ワークスペース全体〈オーナーの未コミットの変更を含む〉を巻き込まないため）。
	if agent != nil && agent.MemoryDirDeclared && agent.MemoryDir != "" {
		mem := filepath.Join(ws, agent.MemoryDir)
		rel, err := filepath.Rel(mem, filepath.Join(ws, ".flywheel"))
		insideMem := err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
		if !insideMem {
			paths = append(paths, mem)
		}
	}

	skipped := BackupResult{Outcome: BackupUnchanged}
	if ignored {
		skipped = BackupResult{Outcome: BackupIgnored, Detail: "export path is ignored by git: " + backupExportRel}
	}
	if len(paths) == 0 {
		return skipped
	}
	changed, err := git.HasChanges(ctx, ws, paths)
	if err != nil {
		return backupFailed("git: " + err.Error())
	}
	if !changed {
		return skipped
	}
	sha, err := git.CommitPaths(ctx, ws, "chore(flywheel): backup "+cycleID, paths)
	if err != nil {
		return backupFailed("git commit: " + err.Error())
	}
	if ignored {
		skipped.Commit = sha
		return skipped
	}
	return BackupResult{Outcome: BackupCommitted, Commit: sha}
}

func backupFailed(detail string) BackupResult {
	if strings.TrimSpace(detail) == "" {
		detail = "unknown error"
	}
	return BackupResult{Outcome: BackupFailed, Detail: detail}
}

// recordBackup は backup の結果を作業ログ（entity=cycle・action=backup）に残す。
// after は outcome・commit・detail を持つ（version は持たない。空は null）。
func (s *Store) recordBackup(ctx context.Context, cycleID string, res BackupResult) error {
	id, ok := parseCycleID(cycleID)
	if !ok {
		return ErrValidation
	}
	var commit, detail any
	if res.Commit != "" {
		commit = res.Commit
	}
	if res.Detail != "" {
		detail = res.Detail
	}
	return s.mutate(ctx, ChannelCLI, func(_ *sql.Tx, rec *activityRecorder) error {
		return rec.record("cycle", id, ActivityActionBackup, nil, map[string]any{
			"outcome": string(res.Outcome),
			"commit":  commit,
			"detail":  detail,
		})
	})
}

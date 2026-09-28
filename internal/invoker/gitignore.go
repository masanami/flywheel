package invoker

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// runsGitignoreLine は .flywheel/.gitignore へ足す、runs/ を除外する行
// （§invoker の共通の規則「run の保存先を作るとき、.flywheel/.gitignore に
// runs/を除外する行が無ければ足す」。D10と同じ扱い＝ストアと同じくGitで
// 追跡させない）。
const runsGitignoreLine = "runs/"

// ensureRunsGitignoreEntry は workspace/.flywheel/.gitignore に runsGitignoreLine
// が無ければ足す（冪等）。通常の運用では core.Init が既に runs/ を含む完全な
// 内容（store.go の gitignoreContents）で .gitignore を作っているため、ここは
// 「利用者が .gitignore を削除した後に run が起きた」場合の保険にすぎない。
//
// .gitignore が無い場合は、狭い内容（runs/ だけ）で新規作成しない
// （self-review 指摘 round2, CONFIRMED）: それをすると「ファイルが存在
// する」状態になり、以後 core.Init の writeGitignoreIfMissing が完全な
// 内容（flywheel.db・-wal・-shm を含む）へ復元する機会を失う
// （writeGitignoreIfMissing は既存ファイルに一切触れない）。
// 代わりに core.EnsureWorkspaceGitignore を呼び、core が持つ完全な内容
// （runs/ を含む）で作らせる（self-review 指摘 round3, PLAUSIBLE: 「何も
// しない」だと§invoker の共通の規則「run の保存先を作るとき…runs/を
// 除外する行が無ければ足す」を.gitignoreが元から無いケースで満たせない。
// D10の保護とrunsの除外を両方満たすには、invoker が狭い内容を作るのでは
// なく、core が持つ正本の内容を使わせればよい）。
func ensureRunsGitignoreEntry(workspace string) error {
	path := filepath.Join(workspace, ".flywheel", ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return core.EnsureWorkspaceGitignore(workspace)
		}
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == runsGitignoreLine {
			return nil
		}
	}
	content := string(data)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += runsGitignoreLine + "\n"
	return os.WriteFile(path, []byte(content), 0o644)
}

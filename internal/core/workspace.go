package core

import (
	"os"
	"path/filepath"
)

// WorkspaceEnvVar はワークスペースを指定する環境変数名
// （--workspace の次に優先される。docs/features/m1-core.md §ワークスペースとストア）。
const WorkspaceEnvVar = "FLYWHEEL_WORKSPACE"

// flywheelDirName は各ワークスペースの下に作るディレクトリ名。
const flywheelDirName = ".flywheel"

// dbFileName はストアのファイル名。
const dbFileName = "flywheel.db"

// resolveBaseDir は --workspace → 環境変数 FLYWHEEL_WORKSPACE → カレントディレクトリの
// 順で基点のディレクトリを決める（init・init 以外のコマンドで共通の優先順位）。
// workspaceFlag が空文字なら --workspace は指定されていないものとして扱う。
func resolveBaseDir(workspaceFlag string) (string, error) {
	dir, _, err := resolveBaseDirExplicit(workspaceFlag)
	return dir, err
}

// resolveBaseDirExplicit は resolveBaseDir と同じ優先順位で基点のディレクトリを
// 決めるが、その基点が --workspace か FLYWHEEL_WORKSPACE によって明示的に
// 指定されたのか（true）、カレントディレクトリへのフォールバックなのか（false）
// も返す。OpenWorkspace はこれを使って、明示された基点では親へ遡らない
// （docs/features/m1-core.md AC「--workspace <dir> を指定したコマンドは、
// カレントディレクトリに関係なく <dir>/.flywheel/flywheel.db を使う」）。
func resolveBaseDirExplicit(workspaceFlag string) (dir string, explicit bool, err error) {
	if workspaceFlag != "" {
		dir, err = filepath.Abs(workspaceFlag)
		return dir, true, err
	}
	if v := os.Getenv(WorkspaceEnvVar); v != "" {
		dir, err = filepath.Abs(v)
		return dir, true, err
	}
	dir, err = os.Getwd()
	return dir, false, err
}

// storeDBPath は base の下のストアファイルの絶対パスを返す。
func storeDBPath(base string) string {
	return filepath.Join(base, flywheelDirName, dbFileName)
}

// findWorkspace は base から親ディレクトリへ向かって、最初に .flywheel/flywheel.db が
// 見つかったディレクトリを返す（init 以外のコマンドの探索規則で、基点が
// カレントディレクトリのときだけ使う。親へ遡る。D13: 環境固有のパスはコードに
// 埋め込まず、実行時に決めた絶対パスのみを扱う）。見つからなければ ok=false を
// 返す（ストアは作らない＝fail-closed）。
func findWorkspace(base string) (dir string, ok bool) {
	dir = base
	for {
		if storeExistsAt(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// storeExistsAt は dir/.flywheel/flywheel.db がファイルとして存在するかを返す。
func storeExistsAt(dir string) bool {
	info, err := os.Stat(storeDBPath(dir))
	return err == nil && !info.IsDir()
}

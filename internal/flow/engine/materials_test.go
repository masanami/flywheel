package engine

import (
	"path/filepath"
	"testing"

	"github.com/masanami/flywheel/internal/flow/assets"
)

// 埋め込んだ資材（internal/flow/assets）を試験ごとの一時ディレクトリへ展開し、その workflows/ の場所を返す。
func bundledWorkflowDir(t *testing.T) string {
	t.Helper()
	l, err := assets.Ensure(assets.FS, filepath.Join(t.TempDir(), "runtime", "test"))
	if err != nil {
		t.Fatal(err)
	}
	return l.WorkflowDir
}

// needPluginScripts は、プラグインのスクリプト（plugin/scripts）を読む試験を飛ばす。
// プラグインのスクリプトは埋め込まず、試験は写した元のリポジトリを読まない。
func needPluginScripts(t *testing.T) {
	t.Helper()
	t.Skip("plugin/scripts is not embedded and the tests do not read the source repository")
}

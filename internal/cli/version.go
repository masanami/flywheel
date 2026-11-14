package cli

import (
	"fmt"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// runVersion は `flywheel version` の実装。ストアを開かず、ネットワークにも触れない。
func runVersion(_ Args) (any, error) {
	v, err := core.ReadVersion()
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.FromVersion(v), text: versionText(v)}, nil
}

func versionText(v core.VersionInfo) string {
	return fmt.Sprintf("commit: %s\ntime: %s\nschema_version: %d\n", commitText(v), v.Time, v.SchemaVersion)
}

// commitText はコミットの表示。未コミットの変更つきのビルドは、run の flywheel_version と同じく
// 末尾に +dirty を付ける。
func commitText(v core.VersionInfo) string {
	if v.Modified {
		return v.Commit + "+dirty"
	}
	return v.Commit
}

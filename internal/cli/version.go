package cli

import (
	"fmt"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは `flywheel version`（Issue #593）の実コマンドを持つ。版の読み取りは core.ReadVersion、
// JSON の形は internal/view が持ち、ここでは整形するだけ。

// runVersion は `flywheel version` の実装。ストアを開かず、ネットワークにも触れない。
func runVersion(a Args) (any, error) {
	v, err := a.readVersion()
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.FromVersion(v), text: versionText(v)}, nil
}

// versionText は --json 無しの version の表示。コミット（未コミットの変更つきは +dirty）・ビルドの時刻・
// 未コミットの変更の有無・ストアのスキーマの版を 1 行ずつ出す。
func versionText(v core.VersionInfo) string {
	return fmt.Sprintf("commit: %s\ntime: %s\nmodified: %t\nschema_version: %d\n", v.CommitLabel(), v.Time, v.Modified, v.SchemaVersion)
}

// binaryCommit は status に出す、動いているバイナリのコミットの表示。読み取りに失敗したら UnknownBuildValue。
func binaryCommit(a Args) string {
	v, err := a.readVersion()
	if err != nil {
		return core.UnknownBuildValue
	}
	return v.CommitLabel()
}

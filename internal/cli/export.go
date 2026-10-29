package cli

import (
	"context"
	"fmt"

	"github.com/masanami/flywheel/internal/view"
)

// runExport は `flywheel export --out <path>` の実装（docs/features/m5-tom-migration-and-switchover.md
// §書き出し）。core.Store.ExportToFile を呼ぶだけで、書き出しの規則は core が持つ。ストアを変えず、
// 作業ログに載せず、LLM も呼ばない。
func runExport(a Args) (any, error) {
	res, err := a.Store.ExportToFile(context.Background(), a.Values["out"])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.FromExportResult(res),
		text: fmt.Sprintf("書き出し: %s\nスキーマの版: %d\n", res.Path, res.SchemaVersion),
	}, nil
}

// runImport は `flywheel import --in <path>` の実装（同 §再構築）。すべての表に行が無いストアへだけ
// 読み込む規則は core.Store.ImportFromFile が持つ。ストアは変えるが作業ログには載せず、LLM も呼ばない。
func runImport(a Args) (any, error) {
	res, err := a.Store.ImportFromFile(context.Background(), a.Values["in"])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.FromImportResult(res),
		text: fmt.Sprintf("読み込み: %s\nスキーマの版: %d\n", res.Path, res.SchemaVersion),
	}, nil
}

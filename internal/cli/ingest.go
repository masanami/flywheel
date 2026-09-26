package cli

import (
	"fmt"

	"github.com/masanami/flywheel/internal/core"
)

// runIngest は `flywheel ingest [--source <id>]` の実装（Issue #54）。
//
// 取り込みの本体（`gh` による取得・冪等な作成と更新・上流の close の検出。
// docs/features/m2-github-issue-ingest.md §機能要件）は #59 が結線する。
// このチケットの範囲は、宣言の読み込みと検証（#53 の core.LoadSourcesDeclaration）・
// `--source` の対象の絞り込み（core.SelectSources）の結果を、終了コードと
// エラーコードへ写すことだけ（P2「規則は core、CLI は写すだけ」）。
//
// 宣言ファイルが無ければ core.ErrConfigNotFound（→ config_not_found・終了 2）、
// 解釈できない・規則に反する宣言なら core.ErrConfigInvalid（→ config_invalid・
// 終了 2）。`--source` が宣言に無い id なら core.ErrValidation
// （→ validation_failed・終了 1）。いずれの場合もストアを変えず、`gh` を呼ばない
// （取り込み元の取得自体をまだ行わないため）。
//
// RequiresStore: true（commands.go）により、ここへ到達する前に
// core.OpenWorkspace がストアを開いている。ストアの版が新しすぎる場合は
// store_too_new でその時点で終わり、宣言の読み込み・`--source` の判定・`gh` の
// 呼び出しのいずれにも進まない（AC-98「gh が呼ばれない」は、この事前チェックが
// Run より前に決着することで自動的に成り立つ）。
func runIngest(a Args) (any, error) {
	decl, err := core.LoadSourcesDeclaration(a.Store.Workspace())
	if err != nil {
		return nil, mapCoreErr(err)
	}

	// --source の指定の有無を区別する（self-review 指摘: 文字列の値だけで
	// 判定すると `--source ""`・`--source=` が「省略」と同じ「全件選択」に化け、
	// AC-13 の fail-closed から外れる）。既存の任意フラグ（runList の
	// `--status`）と同じ形。
	var sourceID *string
	if v, ok := a.Values["source"]; ok {
		sourceID = &v
	}
	selected, err := core.SelectSources(decl, sourceID)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	// 取り込みの本体が未結線の間の成功時の振る舞い（#54 の仮定。完了条件
	// 「取り込みの本体が未結線の間の成功時の振る舞い（例: 宣言が妥当なら空の
	// 結果を返す）を決める」）: 宣言と --source の検証を通ったら、常に空の結果を
	// 返す。docs/features/m1-core.md §成功時の JSON 出力の規約 ##### `ingest`。
	return textOutput{
		json: map[string]any{"sources": []any{}},
		text: fmt.Sprintf("宣言を検証しました（対象の取り込み元 %d 件。取り込みの本体は未結線です）\n", len(selected)),
	}, nil
}

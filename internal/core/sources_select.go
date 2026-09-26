package core

import "fmt"

// SelectSources は `flywheel ingest [--source <id>]` が処理すべき取り込み元を
// decl（LoadSourcesDeclaration が返した検証済みの宣言）から選ぶ。
//
// sourceID が nil（--source 省略）なら、宣言の全取り込み元を宣言の順に返す
// （docs/features/m2-github-issue-ingest.md §実行の起点「flywheel ingest は、
// 宣言のすべての取り込み元を宣言の順に処理する」）。
//
// sourceID が非 nil で、宣言のどの id とも一致しなければ（*sourceID が空文字列の
// 場合を含む。sourceIDPattern は空文字列を許さないため、どの宣言の id とも
// 一致しえない）ErrValidation を返す（同節「flywheel ingest --source <id> は、
// 指定した取り込み元だけを処理する。宣言に無い id は validation_failed」。
// #54 AC-13）。
//
// sourceID をポインタで受け取るのは、「--source 省略」と「--source に明示的に
// 空文字列を渡した」を区別するため（self-review 指摘: 文字列の等値だけで
// 判定すると、`--source ""`・`--source=` が省略と同じ「全件選択」に化け、
// AC-13 の fail-closed から外れる。internal/cli/challenge.go の runList が
// `--status` を `*string` で受け取る既存パターンと同じ形）。
func SelectSources(decl *SourcesDeclaration, sourceID *string) ([]SourceEntry, error) {
	if sourceID == nil {
		return decl.Sources, nil
	}
	for _, e := range decl.Sources {
		if e.ID == *sourceID {
			return []SourceEntry{e}, nil
		}
	}
	return nil, fmt.Errorf("%w: source %q is not declared", ErrValidation, *sourceID)
}

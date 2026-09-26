package core

import (
	"errors"
	"testing"
)

// strPtr は internal/core/challenge_test.go で定義済み（同一パッケージ）。

// TestSelectSources_OmittedSourceIDReturnsAllInDeclarationOrder は sourceID が
// nil（--source 省略）の場合、宣言の全取り込み元を宣言の順に返すことを検証する
// （docs/features/m2-github-issue-ingest.md §実行の起点「flywheel ingest は、
// 宣言のすべての取り込み元を宣言の順に処理する」）。
func TestSelectSources_OmittedSourceIDReturnsAllInDeclarationOrder(t *testing.T) {
	decl := &SourcesDeclaration{
		Version: 1,
		Sources: []SourceEntry{
			{ID: "a", Type: SourceTypeGitHubIssue, Repos: []string{"o/a"}},
			{ID: "b", Type: SourceTypeGitHubIssue, Repos: []string{"o/b"}},
		},
	}
	got, err := SelectSources(decl, nil)
	if err != nil {
		t.Fatalf("SelectSources() error = %v, want nil", err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("SelectSources() = %+v, want [a, b] in order", got)
	}
}

// TestSelectSources_KnownIDReturnsOnlyThatSource は #54 の完了条件
// 「flywheel ingest --source <id> は、指定した取り込み元のリポジトリだけを
// 取得する」の前段（対象の絞り込み）を検証する。
func TestSelectSources_KnownIDReturnsOnlyThatSource(t *testing.T) {
	decl := &SourcesDeclaration{
		Version: 1,
		Sources: []SourceEntry{
			{ID: "a", Type: SourceTypeGitHubIssue, Repos: []string{"o/a"}},
			{ID: "b", Type: SourceTypeGitHubIssue, Repos: []string{"o/b"}},
		},
	}
	got, err := SelectSources(decl, strPtr("b"))
	if err != nil {
		t.Fatalf("SelectSources() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("SelectSources() = %+v, want [b]", got)
	}
}

// TestSelectSources_UnknownIDReturnsErrValidation は AC-13「flywheel ingest
// --source <宣言に無い id> は、終了コード 1・validation_failed で終わり、
// ストアが変わらない」の core 側の判定を検証する。
func TestSelectSources_UnknownIDReturnsErrValidation(t *testing.T) {
	decl := &SourcesDeclaration{
		Version: 1,
		Sources: []SourceEntry{
			{ID: "a", Type: SourceTypeGitHubIssue, Repos: []string{"o/a"}},
		},
	}
	_, err := SelectSources(decl, strPtr("does-not-exist"))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("SelectSources() error = %v, want ErrValidation", err)
	}
}

// TestSelectSources_ExplicitEmptyStringReturnsErrValidation は self-review 指摘
// （code-reviewer・design-reviewer が独立に検出）の再発防止テスト:
// `--source ""`（明示的に空文字列を指定）を「省略」と混同して全件選択すると、
// AC-13 の fail-closed から外れる。sourceIDPattern（core/sources.go）は空文字列を
// 許さないため、空文字列はどの宣言の id とも一致しえず、ErrValidation になる
// べきである。
func TestSelectSources_ExplicitEmptyStringReturnsErrValidation(t *testing.T) {
	decl := &SourcesDeclaration{
		Version: 1,
		Sources: []SourceEntry{
			{ID: "a", Type: SourceTypeGitHubIssue, Repos: []string{"o/a"}},
		},
	}
	_, err := SelectSources(decl, strPtr(""))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf(`SelectSources(decl, strPtr("")) error = %v, want ErrValidation (must not be treated as "omitted")`, err)
	}
}

// TestSelectSources_EmptyDeclarationAndOmittedIDReturnsEmptySlice は、宣言に
// 取り込み元が1つも無いワークスペース（`{"version":1,"sources":[]}`）で
// --source を省略したときに、空の一覧（エラーではない）を返すことを検証する。
func TestSelectSources_EmptyDeclarationAndOmittedIDReturnsEmptySlice(t *testing.T) {
	decl := &SourcesDeclaration{Version: 1, Sources: []SourceEntry{}}
	got, err := SelectSources(decl, nil)
	if err != nil {
		t.Fatalf("SelectSources() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("SelectSources() = %+v, want empty", got)
	}
}

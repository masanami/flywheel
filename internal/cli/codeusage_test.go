package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// codeUsageViolation は NewError(...) の第 1 引数が、既知の ErrorCode 定数の
// 参照であると静的に確認できなかった箇所を表す。
type codeUsageViolation struct {
	Pos     string
	Message string
}

// collectErrorCodeConstants はファイル群から `Name ErrorCode = "value"` 形式の
// package-level const 宣言を集め、定数名 → コード値 の対応表を作る。
func collectErrorCodeConstants(files []*ast.File) map[string]ErrorCode {
	consts := map[string]ErrorCode{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				typeIdent, ok := vs.Type.(*ast.Ident)
				if !ok || typeIdent.Name != "ErrorCode" {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					val, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					consts[name.Name] = ErrorCode(val)
				}
			}
		}
	}
	return consts
}

// calleeName は呼び出し式から関数名（パッケージ修飾子を除く）を取り出す。
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	default:
		return ""
	}
}

// scanErrorCodeUsage は NewError(...) 呼び出しの第 1 引数を検査する。
// 許可されるのは既知の ErrorCode 定数への直接参照だけであり、文字列リテラルや
// `ErrorCode("...")` のような型変換によるすり抜けは違反として報告する
// （表に無いコード文字列リテラルでエラーを作れないことの担保）。
// 戻り値 used は、静的に追跡できた「実際に使用されているエラーコード」の集合。
func scanErrorCodeUsage(files []*ast.File, fset *token.FileSet, consts map[string]ErrorCode) (used map[ErrorCode]bool, violations []codeUsageViolation) {
	used = map[ErrorCode]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if calleeName(call.Fun) != "NewError" {
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			pos := fset.Position(call.Pos()).String()
			switch arg := call.Args[0].(type) {
			case *ast.Ident:
				code, ok := consts[arg.Name]
				if !ok {
					violations = append(violations, codeUsageViolation{
						Pos:     pos,
						Message: "NewError の第1引数が既知の ErrorCode 定数を参照していない: " + arg.Name,
					})
					return true
				}
				used[code] = true
			case *ast.SelectorExpr:
				code, ok := consts[arg.Sel.Name]
				if !ok {
					violations = append(violations, codeUsageViolation{
						Pos:     pos,
						Message: "NewError の第1引数が既知の ErrorCode 定数を参照していない: " + arg.Sel.Name,
					})
					return true
				}
				used[code] = true
			default:
				violations = append(violations, codeUsageViolation{
					Pos:     pos,
					Message: "NewError の第1引数が定数参照ではない（文字列リテラルや型変換によるコード生成は禁止）",
				})
			}
			return true
		})
	}
	return used, violations
}

func mustParseAll(t *testing.T, fset *token.FileSet, sources map[string]string) []*ast.File {
	t.Helper()
	var files []*ast.File
	for name, src := range sources {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, f)
	}
	return files
}

func TestScanErrorCodeUsage_AcceptsConstantReference(t *testing.T) {
	fset := token.NewFileSet()
	files := mustParseAll(t, fset, map[string]string{
		"const.go": "package cli\nconst (\n\tCodeFoo ErrorCode = \"foo\"\n)\n",
		"use.go":   "package cli\nfunc f() error { return NewError(CodeFoo, \"boom\") }\n",
	})
	consts := collectErrorCodeConstants(files)
	used, violations := scanErrorCodeUsage(files, fset, consts)
	if len(violations) != 0 {
		t.Fatalf("unexpected violations: %+v", violations)
	}
	if !used[ErrorCode("foo")] {
		t.Fatalf("expected code %q to be recorded as used, got %v", "foo", used)
	}
}

func TestScanErrorCodeUsage_RejectsRawStringLiteral(t *testing.T) {
	fset := token.NewFileSet()
	files := mustParseAll(t, fset, map[string]string{
		"use.go": "package cli\nfunc f() error { return NewError(\"bogus\", \"boom\") }\n",
	})
	_, violations := scanErrorCodeUsage(files, fset, map[string]ErrorCode{})
	if len(violations) != 1 {
		t.Fatalf("want 1 violation for raw string literal, got %d: %+v", len(violations), violations)
	}
}

func TestScanErrorCodeUsage_RejectsTypeConversionBypass(t *testing.T) {
	fset := token.NewFileSet()
	files := mustParseAll(t, fset, map[string]string{
		"use.go": "package cli\nfunc f() error { return NewError(ErrorCode(\"bogus\"), \"boom\") }\n",
	})
	_, violations := scanErrorCodeUsage(files, fset, map[string]ErrorCode{})
	if len(violations) != 1 {
		t.Fatalf("want 1 violation for ErrorCode(...) conversion bypass, got %d: %+v", len(violations), violations)
	}
}

func TestScanErrorCodeUsage_RejectsUnknownIdentifier(t *testing.T) {
	fset := token.NewFileSet()
	files := mustParseAll(t, fset, map[string]string{
		"use.go": "package cli\nfunc f(x ErrorCode) error { return NewError(x, \"boom\") }\n",
	})
	_, violations := scanErrorCodeUsage(files, fset, map[string]ErrorCode{})
	if len(violations) != 1 {
		t.Fatalf("want 1 violation for unresolvable identifier, got %d: %+v", len(violations), violations)
	}
}

// scanDirectErrorConstruction は Error{Code: …} / &Error{Code: …} の複合リテラルに
// よる直接構築を検出する。NewError は内部で正にこの形を使うため、NewError 自身の
// 関数本体だけは対象から除外する（唯一許可された構築箇所）。
// code-reviewer の指摘: NewError(...) 呼び出しだけを見る scanErrorCodeUsage は、
// 複合リテラルでの直接構築（表に無いコードを Code フィールドへ直接代入する形）を
// 検出できず、AC-76 が担保したい「エラーの生成は定数型経由だけ」を静かにすり抜けられる。
func scanDirectErrorConstruction(files []*ast.File, fset *token.FileSet) (violations []codeUsageViolation) {
	isErrorTypeExpr := func(expr ast.Expr) bool {
		switch t := expr.(type) {
		case *ast.Ident:
			return t.Name == "Error"
		case *ast.SelectorExpr:
			return t.Sel.Name == "Error"
		default:
			return false
		}
	}
	check := func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CompositeLit:
			if !isErrorTypeExpr(node.Type) || len(node.Elts) == 0 {
				return true
			}
			// キー付き（Error{Code: ...}）・キー無し（Error{code, msg} の位置指定）の
			// いずれも、NewError 以外からの Error{...} 構築そのものを禁止する
			// （位置指定は Code を検査せずすり抜けるため、要素があれば無条件で違反にする）。
			violations = append(violations, codeUsageViolation{
				Pos:     fset.Position(node.Pos()).String(),
				Message: "Error{...} の直接構築は禁止（NewError を経由すること）",
			})
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Code" {
					continue
				}
				violations = append(violations, codeUsageViolation{
					Pos:     fset.Position(node.Pos()).String(),
					Message: "<expr>.Code への直接代入は禁止（NewError を経由すること）",
				})
			}
		}
		return true
	}
	for _, f := range files {
		filename := fset.Position(f.Pos()).Filename
		// NewError 自身の構築だけを許可する。この例外はファイル名が厳密に
		// errors.go であるものに限る（"not_errors.go" のような他ファイルを
		// HasSuffix で誤って一致させないよう basename の完全一致で判定する）。
		allowNewErrorHere := filepath.Base(filename) == "errors.go"
		for _, decl := range f.Decls {
			fd, isFunc := decl.(*ast.FuncDecl)
			if isFunc && allowNewErrorHere && fd.Name.Name == "NewError" {
				continue
			}
			if isFunc {
				if fd.Body != nil {
					ast.Inspect(fd.Body, check)
				}
				continue
			}
			ast.Inspect(decl, check)
		}
	}
	return violations
}

func TestScanDirectErrorConstruction_RejectsCompositeLiteral(t *testing.T) {
	fset := token.NewFileSet()
	files := mustParseAll(t, fset, map[string]string{
		"use.go": "package cli\nfunc f() error { return &Error{Code: CodeUsageError, Message: \"x\"} }\n",
	})
	violations := scanDirectErrorConstruction(files, fset)
	if len(violations) != 1 {
		t.Fatalf("want 1 violation for direct composite-literal construction, got %d: %+v", len(violations), violations)
	}
}

func TestScanDirectErrorConstruction_AllowsInsideNewError(t *testing.T) {
	fset := token.NewFileSet()
	files := mustParseAll(t, fset, map[string]string{
		"errors.go": "package cli\nfunc NewError(code ErrorCode, message string) *Error {\n\treturn &Error{Code: code, Message: message}\n}\n",
	})
	violations := scanDirectErrorConstruction(files, fset)
	if len(violations) != 0 {
		t.Fatalf("NewError's own construction should be allowed, got violations: %+v", violations)
	}
}

// 確認モードのラウンド2で code-reviewer が発見: キー無し（位置指定）の複合リテラルは
// scanDirectErrorConstruction をすり抜けていた（KeyValueExpr だけを見ていたため）。
func TestScanDirectErrorConstruction_RejectsUnkeyedCompositeLiteral(t *testing.T) {
	fset := token.NewFileSet()
	files := mustParseAll(t, fset, map[string]string{
		"use.go": "package cli\nfunc f() error { return &Error{\"bogus_code\", \"boom\"} }\n",
	})
	violations := scanDirectErrorConstruction(files, fset)
	if len(violations) != 1 {
		t.Fatalf("want 1 violation for unkeyed composite-literal construction, got %d: %+v", len(violations), violations)
	}
}

// 同じくラウンド2の指摘: フィールドへの直接代入（e.Code = ...）もすり抜けていた。
func TestScanDirectErrorConstruction_RejectsFieldAssignment(t *testing.T) {
	fset := token.NewFileSet()
	files := mustParseAll(t, fset, map[string]string{
		"use.go": "package cli\nfunc f(e *Error) { e.Code = ErrorCode(\"bogus\") }\n",
	})
	violations := scanDirectErrorConstruction(files, fset)
	if len(violations) != 1 {
		t.Fatalf("want 1 violation for direct field assignment, got %d: %+v", len(violations), violations)
	}
}

// NewError の例外は errors.go にだけ適用されるべき（他ファイル・他パッケージの
// 同名関数 NewError が誤って除外対象になっていた、というラウンド2の指摘）。
func TestScanDirectErrorConstruction_NewErrorExceptionOnlyAppliesInErrorsGo(t *testing.T) {
	fset := token.NewFileSet()
	files := mustParseAll(t, fset, map[string]string{
		"not_errors.go": "package cli\nfunc NewError(x string) *Error {\n\treturn &Error{Code: ErrorCode(x)}\n}\n",
	})
	violations := scanDirectErrorConstruction(files, fset)
	if len(violations) != 1 {
		t.Fatalf("want 1 violation: NewError defined outside errors.go should not be exempted, got %d: %+v", len(violations), violations)
	}
}

// TestErrorCodeUsageInSourcesIsWithinSpec は、現時点で実装が実際に使用している
// エラーコード（NewError 呼び出しから静的に追跡できるもの）が、仕様のエラーコード表の
// 部分集合であることを検査する（AC-76 の枠組み）。
//
// 本チケット（#5）はスタブのみで usage_error・internal_error しか使っていない。
// 全コマンドが実装され出そろった後の「表と実装が完全に一致する」という
// 最終閉包の検査は #15 で行う（このテストはその前段の枠組み）。
func TestErrorCodeUsageInSourcesIsWithinSpec(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var files []*ast.File
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	consts := collectErrorCodeConstants(files)
	used, violations := scanErrorCodeUsage(files, fset, consts)
	violations = append(violations, scanDirectErrorConstruction(files, fset)...)
	if len(violations) != 0 {
		for _, v := range violations {
			t.Errorf("%s: %s", v.Pos, v.Message)
		}
		t.FailNow()
	}

	spec := loadSpecErrorCodeTable(t)
	specCodes := map[ErrorCode]bool{}
	for _, e := range spec {
		specCodes[e.Code] = true
	}
	for code := range used {
		if !specCodes[code] {
			t.Errorf("used error code %q is not present in the spec table", code)
		}
	}
	if len(used) == 0 {
		t.Error("no NewError usage detected at all; expected at least usage_error/internal_error from the command stubs")
	}
}

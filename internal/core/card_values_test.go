package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"testing"
)

// constStrings は card.go の const 宣言のうち型 typ の文字列値を宣言順に返す。
func constStrings(t *testing.T, typ string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "card.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != typ || len(vs.Values) != 1 {
				continue
			}
			s, err := strconv.Unquote(vs.Values[0].(*ast.BasicLit).Value)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
	}
	return out
}

func TestCardActionValues_MatchConsts(t *testing.T) {
	var got []string
	for _, v := range CardActionValues() {
		got = append(got, string(v))
	}
	if want := constStrings(t, "CardAction"); !reflect.DeepEqual(got, want) {
		t.Errorf("CardActionValues = %v, const 宣言 = %v", got, want)
	}
}

func TestCardModifierValues_MatchConsts(t *testing.T) {
	var got []string
	for _, v := range CardModifierValues() {
		got = append(got, string(v))
	}
	if want := constStrings(t, "CardModifier"); !reflect.DeepEqual(got, want) {
		t.Errorf("CardModifierValues = %v, const 宣言 = %v", got, want)
	}
}

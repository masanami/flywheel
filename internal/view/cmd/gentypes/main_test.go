package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// AC-125: 生成し直した結果とコミット済みの types.gen.ts に差があれば落ちる。
func TestCommittedTypesAreUpToDate(t *testing.T) {
	want, err := Generate(roots)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "..", "..", "web", "src", "api", "types.gen.ts")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v（`go generate ./internal/view` で生成する）", err)
	}
	if string(got) != string(want) {
		t.Errorf("web/src/api/types.gen.ts が生成結果と違う。`go generate ./internal/view` で生成し直す")
	}
}

type inner struct {
	A int `json:"a"`
}

type sample struct {
	Name    string            `json:"name"`
	Opt     string            `json:"opt,omitempty"`
	Ptr     *string           `json:"ptr"`
	List    []inner           `json:"list"`
	PtrList []*float64        `json:"ptr_list"`
	Nested  *inner            `json:"nested"`
	Raw     any               `json:"raw"`
	RawJSON json.RawMessage   `json:"raw_json"`
	Bare    int               `json:",omitempty"`
	M       map[string]string `json:"m"`
	Dash    string            `json:"-"`
}

func TestGenerateMapsTagsPointersSlicesNested(t *testing.T) {
	src, err := Generate([]reflect.Type{reflect.TypeOf(sample{})})
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{
		"export interface sample {",
		"  name: string;",
		"  opt?: string;",
		"  ptr: string | null;",
		"  list: inner[];",
		"  ptr_list: (number | null)[];",
		"  nested: inner | null;",
		"  raw: unknown;",
		"  raw_json: unknown;",
		"  Bare?: number;",
		"  m: Record<string, string>;",
		"export interface inner {\n  a: number;",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("出力に %q が無い:\n%s", want, s)
		}
	}
	if strings.Contains(s, "  -:") || strings.Contains(s, "Dash") {
		t.Errorf("タグの無い・`-` のフィールドが出ている:\n%s", s)
	}
}

func TestGenerateRejectsExportedFieldWithoutTag(t *testing.T) {
	type bad struct{ X int }
	if _, err := Generate([]reflect.Type{reflect.TypeOf(bad{})}); err == nil {
		t.Error("json タグの無い公開フィールドはエラーにする")
	}
}

func TestRootsCoverCLIResponses(t *testing.T) {
	var names []string
	for _, r := range roots {
		names = append(names, r.Name())
	}
	for _, w := range []string{"StatusResponse", "ListResponse", "ShowResponse", "LogResponse", "RunsResponse"} {
		found := false
		for _, n := range names {
			found = found || n == w
		}
		if !found {
			t.Errorf("%s が生成の起点に無い", w)
		}
	}
}

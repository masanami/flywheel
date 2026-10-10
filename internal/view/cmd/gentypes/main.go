// gentypes は internal/view の型から TypeScript の型（web/src/api/types.gen.ts）を書き出す。
// 生成専用・テスト専用で、本番のバイナリには含めない。標準ライブラリだけを使う。
//
// 使い方: go generate ./internal/view
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/masanami/flywheel/internal/view"
)

// roots は生成の起点。CLI と同じ形の既存の応答（status・list・show・log・runs）。
// server が新しく定める応答の型は、それを足すチケットが view に型を置いてからここへ足す。
var roots = []reflect.Type{
	reflect.TypeOf(view.StatusResponse{}),
	reflect.TypeOf(view.ListResponse{}),
	reflect.TypeOf(view.ShowResponse{}),
	reflect.TypeOf(view.LogResponse{}),
	reflect.TypeOf(view.RunsResponse{}),
}

const header = "// このファイルは生成物。編集しない。`go generate ./internal/view` で書き出す。\n" +
	"// 正本は internal/view の構造体（json タグ）。\n"

func main() {
	out := "web/src/api/types.gen.ts"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	src, err := Generate(roots)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gentypes:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, src, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gentypes:", err)
		os.Exit(1)
	}
}

type generator struct {
	seen  map[reflect.Type]bool
	order []reflect.Type
}

// Generate は roots から到達できる構造体を TypeScript の interface として書き出す。
// 出力は決定的（起点の順に深さ優先で、先に見つけた型から並べる）。
func Generate(rootTypes []reflect.Type) ([]byte, error) {
	g := &generator{seen: map[reflect.Type]bool{}}
	var buf bytes.Buffer
	buf.WriteString(header)
	for _, r := range rootTypes {
		if err := g.visit(r); err != nil {
			return nil, err
		}
	}
	for _, t := range g.order {
		body, err := g.iface(t)
		if err != nil {
			return nil, err
		}
		buf.WriteString("\n")
		buf.WriteString(body)
	}
	return buf.Bytes(), nil
}

func (g *generator) visit(t reflect.Type) error {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() == reflect.Map {
		return g.visit(t.Elem())
	}
	if t.Kind() != reflect.Struct || g.seen[t] {
		return nil
	}
	if t.Name() == "" {
		return fmt.Errorf("無名の構造体は写せない: %s", t)
	}
	g.seen[t] = true
	g.order = append(g.order, t)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if _, _, skip := jsonName(f); skip {
			continue
		}
		if err := g.visit(f.Type); err != nil {
			return err
		}
	}
	return nil
}

// jsonName は json タグからキー名と omitempty を返す。タグが無い・`-` のフィールドは skip。
func jsonName(f reflect.StructField) (name string, omit, skip bool) {
	if !f.IsExported() {
		return "", false, true
	}
	tag, ok := f.Tag.Lookup("json")
	if !ok || tag == "-" {
		return "", false, true
	}
	parts := strings.Split(tag, ",")
	for _, p := range parts[1:] {
		if p == "omitempty" || p == "omitzero" {
			omit = true
		}
	}
	if parts[0] == "" {
		// encoding/json は名前が空なら Go のフィールド名をキーにする。
		return f.Name, omit, false
	}
	return parts[0], omit, false
}

func (g *generator) iface(t reflect.Type) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "export interface %s {\n", t.Name())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, omit, skip := jsonName(f)
		if skip {
			if _, ok := f.Tag.Lookup("json"); f.IsExported() && !ok {
				return "", fmt.Errorf("%s.%s: json タグが無い", t.Name(), f.Name)
			}
			continue
		}
		ts, err := tsType(f.Type)
		if err != nil {
			return "", fmt.Errorf("%s.%s: %w", t.Name(), f.Name, err)
		}
		opt := ""
		if omit {
			opt = "?"
		}
		fmt.Fprintf(&b, "  %s%s: %s;\n", name, opt, ts)
	}
	b.WriteString("}\n")
	return b.String(), nil
}

var rawMessage = reflect.TypeOf(json.RawMessage(nil))

func tsType(t reflect.Type) (string, error) {
	if t == rawMessage {
		return "unknown", nil
	}
	switch t.Kind() {
	case reflect.Ptr:
		inner, err := tsType(t.Elem())
		if err != nil {
			return "", err
		}
		return inner + " | null", nil
	case reflect.String:
		return "string", nil
	case reflect.Bool:
		return "boolean", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number", nil
	case reflect.Interface:
		return "unknown", nil
	case reflect.Slice, reflect.Array:
		inner, err := tsType(t.Elem())
		if err != nil {
			return "", err
		}
		if strings.Contains(inner, " ") {
			inner = "(" + inner + ")"
		}
		return inner + "[]", nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return "", fmt.Errorf("キーが文字列でない map は写せない: %s", t)
		}
		inner, err := tsType(t.Elem())
		if err != nil {
			return "", err
		}
		return "Record<string, " + inner + ">", nil
	case reflect.Struct:
		return t.Name(), nil
	}
	return "", fmt.Errorf("写せない型: %s", t)
}

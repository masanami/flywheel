package flow

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/masanami/flywheel/internal/flow/assets"
	"github.com/masanami/flywheel/internal/flow/workflow"
)

// ParseModelFlags は flow start の --model <変数>=<値>（複数可）を、変数の名前 → 値の写像に読む。
// `=` が無い・変数の名前か値が空・同じ変数を 2 度渡すのは誤り。渡されなければ nil を返す。
func ParseModelFlags(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for _, kv := range raw {
		name, val, ok := strings.Cut(kv, "=")
		switch {
		case !ok:
			return nil, fmt.Errorf("--model %q must be <variable>=<value>", kv)
		case name == "":
			return nil, fmt.Errorf("--model %q has an empty variable name", kv)
		case val == "":
			return nil, fmt.Errorf("--model %q has an empty value", kv)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("--model variable %q given twice", name)
		}
		out[name] = val
	}
	return out, nil
}

// CheckModelFlags は flow start に渡された --model が使えるかを、始める前（実行のフォルダも資材の展開も無し）に確かめる:
// 形が <変数>=<値> であること、埋め込んだフロー name が定義している変数であること。埋め込んだ定義を読めなければ
// （定義が無い・読めない）ここでは誤りにせず、Start が理由を報告する。
func CheckModelFlags(name string, raw []string) error {
	given, err := ParseModelFlags(raw)
	if err != nil || len(given) == 0 {
		return err
	}
	data, err := fs.ReadFile(assets.FS, "workflows/"+name+".yaml")
	if err != nil {
		return nil
	}
	wf, err := workflow.Parse(name+".yaml", data)
	if err != nil {
		return nil
	}
	_, err = workflow.ResolveModels(wf, given)
	return err
}

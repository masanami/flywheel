package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestUIGuidanceTemplatesExistInCLI は UI の案内の雛形（web/src/guidance.json）の
// サブコマンドとフラグが、CLI のコマンドの定義に実在することを照合する（AC-109）。
func TestUIGuidanceTemplatesExistInCLI(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "web", "src", "guidance.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Templates []struct {
			ID   string `json:"id"`
			When struct {
				Actions []string `json:"actions"`
				Context string   `json:"context"`
			} `json:"when"`
			Command  []string `json:"command"`
			Flags    []string `json:"flags"`
			Template string   `json:"template"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Templates) == 0 {
		t.Fatal("雛形が 1 つも無い")
	}
	flagRe := regexp.MustCompile(`--[a-z][a-z-]*`)
	for _, tpl := range g.Templates {
		// 出す場面は、課題の操作（actions）か画面の名前（context）のどちらか一方だけで書く。
		if (len(tpl.When.Actions) > 0) == (tpl.When.Context != "") {
			t.Errorf("%s: when は actions か context のどちらか一方だけを持つ: %+v", tpl.ID, tpl.When)
		}
		var cmd *Command
		for _, c := range defaultCommands() {
			c := c
			if strings.Join(c.Path, " ") == strings.Join(tpl.Command, " ") {
				cmd = &c
			}
		}
		if cmd == nil {
			t.Errorf("%s: コマンド %v が CLI に無い", tpl.ID, tpl.Command)
			continue
		}
		known := map[string]bool{}
		for _, f := range commonFlags {
			known[f.Name] = true
		}
		for _, f := range cmd.Flags {
			known[f.Name] = true
		}
		for _, f := range tpl.Flags {
			if !known[f] {
				t.Errorf("%s: フラグ --%s が CLI に無い", tpl.ID, f)
			}
		}
		// 雛形の文字列に出るフラグと宣言したフラグが一致し、必須のフラグがすべて含まれる。
		var inTpl []string
		for _, m := range flagRe.FindAllString(tpl.Template, -1) {
			inTpl = append(inTpl, strings.TrimPrefix(m, "--"))
		}
		declared := append([]string(nil), tpl.Flags...)
		sort.Strings(inTpl)
		sort.Strings(declared)
		if strings.Join(inTpl, ",") != strings.Join(declared, ",") {
			t.Errorf("%s: 雛形のフラグ %v と宣言 %v が違う", tpl.ID, inTpl, declared)
		}
		for _, f := range cmd.Flags {
			if f.Required && !strings.Contains(tpl.Template, "--"+f.Name) {
				t.Errorf("%s: 必須フラグ --%s が雛形に無い", tpl.ID, f.Name)
			}
		}
		if !strings.HasPrefix(tpl.Template, "flywheel "+strings.Join(tpl.Command, " ")+" ") {
			t.Errorf("%s: 雛形がサブコマンドで始まらない: %q", tpl.ID, tpl.Template)
		}
	}
}

// TestUIGuidanceHasNowScreenContexts は「いま進められること」の画面が出す端末のコマンドの案内
// （取り込み・サイクル・スロットの片付け）が、画面の名前（context）つきで雛形にあることを確かめる（Issue #805）。
func TestUIGuidanceHasNowScreenContexts(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "web", "src", "guidance.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Templates []struct {
			When struct {
				Context string `json:"context"`
			} `json:"when"`
			Template string `json:"template"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ingest": "flywheel ingest ",
		"cycle":  "flywheel cycle ",
		"slot":   "flywheel slot clear ",
	}
	for ctx, prefix := range want {
		found := false
		for _, tpl := range g.Templates {
			if tpl.When.Context == ctx && strings.HasPrefix(tpl.Template, prefix) {
				found = true
			}
		}
		if !found {
			t.Errorf("context %q の案内（%q で始まる雛形）が無い", ctx, prefix)
		}
	}
}

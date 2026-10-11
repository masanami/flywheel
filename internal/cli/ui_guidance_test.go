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
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "web", "src", "guidance.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Templates []struct {
			ID       string   `json:"id"`
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

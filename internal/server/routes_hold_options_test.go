package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// S4b-1: 保留へ選択肢で答える口（画面のボタンの経路）は、S4d-2 が足すまで、サーバーの経路の一覧に無い。
func TestRoutesHaveNoWayToAnswerHoldByOption(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(routes) == 0 {
		t.Fatal("経路の一覧を server.go から読めない（検査が空振りしている）")
	}
	for _, m := range routes {
		p := strings.ToLower(m[1])
		for _, banned := range []string{"answer", "option", "hold", "question/"} {
			if strings.Contains(p, banned) {
				t.Errorf("経路 %q は保留へ答える口に見える（%q を含む）。S4d-2 が足すまで置かない", m[1], banned)
			}
		}
	}
}

// 経路の名前だけでなく、サーバーのソースが回答の口（ExecuteAnswer・PrepareAnswer・AnswerRequest）を呼ばないことも固定する
// （既存の経路が選択肢で答える口に化けても検出する）。
func TestServerSourcesDoNotCallCoreAnswer(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("ソースを列挙できない: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"ExecuteAnswer", "PrepareAnswer", "AnswerRequest"} {
			if strings.Contains(string(b), banned) {
				t.Errorf("%s が %s を参照している。保留へ答える口は S4d-2 が足すまで置かない", f, banned)
			}
		}
	}
}

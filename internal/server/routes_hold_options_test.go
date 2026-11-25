package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 保留へ選択肢で答える口（画面のボタンの経路）は、S4d-2 が足した 1 つだけ（holdAnswerRoute）。ほかの経路は、回答に見える名前を持たない。
// holdAnswerRoute は、画面のボタンで選ぶだけの回答を受ける、唯一の経路。
const holdAnswerRoute = "/api/v1/workspaces/{name}/challenges/{id}/holds/{hold}/answer"

// chatPromptAnswerRoute は、対話の欄の確認（許可の確認と問い）への答えの口。保留の口ではない（呼ぶのは確認の答えの API だけで、
// 保留の回答の API を呼ばないことは、下のソースの検査が確かめる）。
const chatPromptAnswerRoute = "/api/v1/workspaces/{name}/chat/prompts/{id}/answer"

// coreAnswerRefs は保留の回答の API の参照。AnswerRequest は識別子の全体で照合する（view.ChatPromptAnswerRequest は別の型）。
var coreAnswerRefs = []*regexp.Regexp{regexp.MustCompile(`ExecuteAnswer`), regexp.MustCompile(`PrepareAnswer`), regexp.MustCompile(`\bAnswerRequest\b`)}

func TestRoutesHaveNoWayToAnswerHoldByOption(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(routes) == 0 {
		t.Fatal("経路の一覧を server.go から読めない（検査が空振りしている）")
	}
	found := false
	for _, m := range routes {
		if m[1] == holdAnswerRoute {
			found = true
			continue
		}
		if m[1] == chatPromptAnswerRoute {
			continue
		}
		p := strings.ToLower(m[1])
		for _, banned := range []string{"answer", "option", "hold", "question/"} {
			if strings.Contains(p, banned) {
				t.Errorf("経路 %q は保留へ答える口に見える（%q を含む）。画面のボタンの回答の口は holdAnswerRoute だけ", m[1], banned)
			}
		}
	}
	if !found {
		t.Errorf("経路 %q が server.go に無い", holdAnswerRoute)
	}
}

// 経路の名前だけでなく、サーバーのソース（hold_answer.go を除く）が回答の口（ExecuteAnswer・PrepareAnswer・AnswerRequest）を呼ばないことも固定する
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
		if f == "hold_answer.go" {
			continue // 画面のボタンで選ぶだけの回答の口だけが、core の回答を呼ぶ
		}
		for _, banned := range coreAnswerRefs {
			if banned.Match(b) {
				t.Errorf("%s が %s を参照している。保留へ答える口は hold_answer.go だけ", f, banned)
			}
		}
	}
}

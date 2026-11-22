package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
)

// AC-248: 指示文 sv.md が、埋め込みの指示文の一覧にある。
func TestSVPrompt_IsInTheEmbeddedInstructions(t *testing.T) {
	t.Parallel()
	body, err := invoker.Instructions(core.JudgmentSV)
	if err != nil || strings.TrimSpace(body) == "" {
		t.Fatalf("invoker.Instructions(SV) = %d bytes, %v; want the embedded sv.md", len(body), err)
	}
}

// AC-249: 指示文 sv.md は、禁止語の照合を通る（開発に固有の語・状態の語彙・コマンド名・設定のキー・判断点の出力の閉集合の値〔手の名前を含む〕・金額が無い）。
func TestSVPrompt_ContainsNoForbiddenTerms(t *testing.T) {
	t.Parallel()
	body, err := invoker.Instructions(core.JudgmentSV)
	if err != nil {
		t.Fatal(err)
	}
	terms := invoker.ForbiddenTerms(realForbiddenSources())
	if hits := invoker.FindForbiddenTerms(body, terms); len(hits) != 0 {
		t.Errorf("sv.md contains forbidden term(s): %v", hits)
	}
}

// 手の名前は判断点の出力の閉集合の値で、禁止語の生成元に入っている（指示文へ書くと照合が失敗する）。
func TestForbiddenTerms_DetectsEachSupervisorHand(t *testing.T) {
	t.Parallel()
	terms := invoker.ForbiddenTerms(realForbiddenSources())
	for _, h := range core.SupervisorHandValues() {
		hits := invoker.FindForbiddenTerms("この節では手 "+string(h)+" を選ぶ。", terms)
		found := false
		for _, v := range hits {
			found = found || v == string(h)
		}
		if !found {
			t.Errorf("FindForbiddenTerms did not detect the hand %q (hits %v)", h, hits)
		}
	}
}

// AC-250: 指示文 sv.md に、できないこと（承認・反映・撤回〔状態の語彙の「取り下げ」は禁止語のため言い換える〕・予算の増額・範囲の変更・成果物の手直し）の記述がある。
func TestSVPrompt_StatesWhatTheSupervisorCannotDo(t *testing.T) {
	t.Parallel()
	body, err := invoker.Instructions(core.JudgmentSV)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(body, "## できないこと")
	if i < 0 {
		t.Fatal("sv.md has no section 「できないこと」")
	}
	section := body[i:]
	if j := strings.Index(section[len("## できないこと"):], "\n## "); j >= 0 {
		section = section[:len("## できないこと")+j]
	}
	for _, want := range []string{"承認", "反映", "撤回", "予算を増や", "範囲を変え", "手直し"} {
		if !strings.Contains(section, want) {
			t.Errorf("the 「できないこと」 section of sv.md lacks %q:\n%s", want, section)
		}
	}
	// 調べてよい範囲と、根拠を示せないときは上げることも書く。
	for _, want := range []string{"Read・Grep・Glob", "根拠を示せないときは、オーナーへ上げる"} {
		if !strings.Contains(body, want) {
			t.Errorf("sv.md lacks %q", want)
		}
	}
}

package invoker

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// 委譲の指示文は、作業を始める前のブランチの確認と切り替えの規律を持つ
// （新規の依頼も、種類ごとの再開の固定の文面も）。
func TestDelegationPromptsRequireBranchCheckBeforeWork(t *testing.T) {
	want := []string{
		"現在のブランチと未コミットの変更の有無",
		"未コミットの変更が無いことを確かめてから切り替える",
		"origin/main",
		"merge・rebase・reset をしない",
		"切り替えに失敗したら、その後の git 操作をせず",
		"assumptions",
	}
	stdin, err := BuildDelegationStdin(DelegationBrief{Brief: "b", Decider: "child", DeciderRow: 1})
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]string{"brief": string(stdin)}
	for _, k := range []core.ResumeKind{core.ResumeKindAnswer, core.ResumeKindInterrupted, core.ResumeKindRework} {
		p, err := ResumePrompt(k)
		if err != nil {
			t.Fatal(err)
		}
		texts["resume:"+string(k)] = p
	}
	for name, text := range texts {
		for _, w := range want {
			if name != "brief" && w == "origin/main" {
				continue // 再開は既存のブランチの続き。無ければの規則は新規の依頼だけ
			}
			if !strings.Contains(text, w) {
				t.Errorf("%s lacks %q", name, w)
			}
		}
	}
}

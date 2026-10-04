package cli

import (
	"fmt"
	"slices"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
)

// このファイルは #82（親要件 #77・docs/features/m3-invoker-delegation.md
// §指示文の歯止め）の禁止語検査のうち、4つの生成元（状態の語彙・
// flywheel のサブコマンド名・.flywheel/agent.json のキー・判断点の出力
// スキーマの閉集合の値）をすべて見られる internal/cli にだけ置ける検査を持つ:
//
//   - 埋め込みの実物（指示文・ブリーフの固定の節）が禁止語を含まないこと
//     （AC「指示文に…が現れると、禁止語のテストが失敗する」の裏付け＝
//     実物は現れていないことの確認）
//   - 各カテゴリの語を差し込んだフィクスチャで実際に失敗する（検出される）
//     ことの確認（AC-117〜120）
//   - 禁止の一覧がコードの定義から生成されること（AC-122）
//
// フィクスチャの検査は invoker.FindForbiddenTerms を実物の検査と共有する
// （検査関数を2つ持たない）。

// subcommandTokens は defaultCommands() の全 Path のトークンを重複無く返す
// （§指示文の歯止め「禁止語…flywheelのサブコマンド名…はコマンドの一覧から
// 生成される」の生成元。internal/cli が正本を持つ）。
func subcommandTokens() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range defaultCommands() {
		for _, tok := range c.Path {
			if tok == "" || seen[tok] {
				continue
			}
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out
}

// stateCodesAndLabels は core.StatusVocabulary の Code・Label の全値を返す
// （§指示文の歯止め「状態のコードと表示名」の生成元）。
func stateCodesAndLabels(vocab []core.StatusVocabEntry) []string {
	var out []string
	for _, e := range vocab {
		out = append(out, string(e.Code), e.Label)
	}
	return out
}

// realForbiddenSources は、実際にバイナリへ埋め込む禁止語の一覧をコードの
// 定義から組み立てる（#84以降がCLIへ結線するときの生成の実例でもある）。
func realForbiddenSources() invoker.ForbiddenSources {
	return invoker.ForbiddenSources{
		StateCodesAndLabels: stateCodesAndLabels(core.StatusVocabulary),
		Subcommands:         subcommandTokens(),
		ConfigKeys:          core.AgentDeclarationKeys(),
		OutputClosedValues:  core.AllJudgmentOutputClosedValues(),
	}
}

// allEmbeddedPromptContents は J1〜J5 の指示文とブリーフの固定の節5つ、
// 再開の固定の文面2つ、計12件の埋め込みの実物の中身を、名前つきで返す。
func allEmbeddedPromptContents(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, j := range []core.JudgmentPoint{core.JudgmentJ1, core.JudgmentJ2, core.JudgmentJ3, core.JudgmentJ4, core.JudgmentJ5} {
		body, err := invoker.Instructions(j)
		if err != nil {
			t.Fatalf("invoker.Instructions(%s): %v", j, err)
		}
		out[fmt.Sprintf("instructions:%s", j)] = body
	}
	sections, err := invoker.BriefFixedSections()
	if err != nil {
		t.Fatalf("invoker.BriefFixedSections(): %v", err)
	}
	for _, s := range sections {
		out["brief:"+s.ID] = s.Body
	}
	for _, k := range []core.ResumeKind{core.ResumeKindAnswer, core.ResumeKindInterrupted} {
		body, err := invoker.ResumePrompt(k)
		if err != nil {
			t.Fatalf("invoker.ResumePrompt(%s): %v", k, err)
		}
		out["resume:"+string(k)] = body
	}
	return out
}

// TestRealPrompts_ContainNoForbiddenTerms は、実際に埋め込まれている指示文と
// ブリーフの固定の節が、コードの定義から生成した禁止語を1つも含まないことを
// 検証する。
func TestRealPrompts_ContainNoForbiddenTerms(t *testing.T) {
	terms := invoker.ForbiddenTerms(realForbiddenSources())
	for name, content := range allEmbeddedPromptContents(t) {
		if hits := invoker.FindForbiddenTerms(content, terms); len(hits) != 0 {
			t.Errorf("%s contains forbidden term(s): %v\ncontent:\n%s", name, hits, content)
		}
	}
}

// TestForbiddenTerms_DetectsEachStateVocabularyValue は AC-117
// 「指示文に状態のコードか表示名が現れると、禁止語のテストが失敗する
// （語彙の8値のそれぞれを差し込んだフィクスチャで検証する）」を、
// 8値それぞれのコードと表示名の両方で検証する。
func TestForbiddenTerms_DetectsEachStateVocabularyValue(t *testing.T) {
	terms := invoker.ForbiddenTerms(realForbiddenSources())
	for _, e := range core.StatusVocabulary {
		t.Run(string(e.Code)+"/code", func(t *testing.T) {
			fixture := "この節の本文は、後続のチケットで書く。状態は " + string(e.Code) + " である。"
			hits := invoker.FindForbiddenTerms(fixture, terms)
			if !slices.Contains(hits, string(e.Code)) {
				t.Errorf("FindForbiddenTerms(...) = %v, want it to contain the inserted state code %q", hits, e.Code)
			}
		})
		t.Run(string(e.Code)+"/label", func(t *testing.T) {
			fixture := "この節の本文は、後続のチケットで書く。状態は「" + e.Label + "」である。"
			hits := invoker.FindForbiddenTerms(fixture, terms)
			if !slices.Contains(hits, e.Label) {
				t.Errorf("FindForbiddenTerms(...) = %v, want it to contain the inserted state label %q", hits, e.Label)
			}
		})
	}
}

// TestForbiddenTerms_DetectsEachSubcommandName は AC-118
// 「指示文にflywheelのサブコマンド名が現れると、禁止語のテストが失敗する」を
// 検証する。
func TestForbiddenTerms_DetectsEachSubcommandName(t *testing.T) {
	terms := invoker.ForbiddenTerms(realForbiddenSources())
	for _, tok := range subcommandTokens() {
		t.Run(tok, func(t *testing.T) {
			fixture := "この節の本文は、後続のチケットで書く。実行するコマンドは " + tok + " である。"
			hits := invoker.FindForbiddenTerms(fixture, terms)
			if !slices.Contains(hits, tok) {
				t.Errorf("FindForbiddenTerms(...) = %v, want it to contain the inserted subcommand %q", hits, tok)
			}
		})
	}
}

// TestForbiddenTerms_DetectsEachAgentDeclarationKey は AC-119
// 「指示文に.flywheel/agent.jsonのキー名が現れると、禁止語のテストが失敗する」
// を検証する（S/M/L・J1〜J5のような1〜2文字のキーも含む）。
func TestForbiddenTerms_DetectsEachAgentDeclarationKey(t *testing.T) {
	terms := invoker.ForbiddenTerms(realForbiddenSources())
	for _, key := range core.AgentDeclarationKeys() {
		t.Run(key, func(t *testing.T) {
			fixture := "この節の本文は、後続のチケットで書く。キーは " + key + " である。"
			hits := invoker.FindForbiddenTerms(fixture, terms)
			if !slices.Contains(hits, key) {
				t.Errorf("FindForbiddenTerms(...) = %v, want it to contain the inserted config key %q", hits, key)
			}
		})
	}
}

// TestForbiddenTerms_DetectsEachOutputClosedValue は AC-120
// 「指示文に判断点の出力スキーマの閉集合の値が現れると、禁止語のテストが
// 失敗する」を検証する。
func TestForbiddenTerms_DetectsEachOutputClosedValue(t *testing.T) {
	terms := invoker.ForbiddenTerms(realForbiddenSources())
	for _, v := range core.AllJudgmentOutputClosedValues() {
		t.Run(v, func(t *testing.T) {
			fixture := "この節の本文は、後続のチケットで書く。値は " + v + " である。"
			hits := invoker.FindForbiddenTerms(fixture, terms)
			if !slices.Contains(hits, v) {
				t.Errorf("FindForbiddenTerms(...) = %v, want it to contain the inserted output closed value %q", hits, v)
			}
		})
	}
}

// TestForbiddenTerms_GeneratedFromCode_PicksUpNewStateVocabularyValue は
// AC-122「禁止語の一覧は…コードの定義から生成される（状態の語彙に値を1つ
// 足したテスト用の定義で、一覧にその値が現れることで検証する）」を、
// 実際の生成経路（core.StatusVocabulary→stateCodesAndLabels）に1件だけ
// テスト用の値を加えた複製で検証する（core.StatusVocabulary 自体は
// 変更しない）。
func TestForbiddenTerms_GeneratedFromCode_PicksUpNewStateVocabularyValue(t *testing.T) {
	extendedVocab := make([]core.StatusVocabEntry, len(core.StatusVocabulary), len(core.StatusVocabulary)+1)
	copy(extendedVocab, core.StatusVocabulary)
	extendedVocab = append(extendedVocab, core.StatusVocabEntry{
		Code:  core.Status("test_added_state_for_ac122"),
		Label: "テスト追加状態",
	})

	src := realForbiddenSources()
	src.StateCodesAndLabels = stateCodesAndLabels(extendedVocab)
	terms := invoker.ForbiddenTerms(src)

	found := false
	for _, term := range terms {
		if term == "test_added_state_for_ac122" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ForbiddenTerms(...) does not contain the newly added state vocabulary value; the generation does not follow the code definition")
	}
}

package invoker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/masanami/flywheel/internal/core"
)

// TestInstructions_ReturnsEmbeddedContentForEachJudgmentPoint は
// invoker.Instructions が J1〜J5 それぞれに対応する埋め込みファイルの中身を
// 返すことを検証する（#82・§指示文の歯止め「判断点ごとの指示文は別ファイルと
// し、バイナリに埋め込む」）。
func TestInstructions_ReturnsEmbeddedContentForEachJudgmentPoint(t *testing.T) {
	for _, j := range []core.JudgmentPoint{core.JudgmentJ1, core.JudgmentJ2, core.JudgmentJ3, core.JudgmentJ4, core.JudgmentJ5, core.JudgmentJ6, core.JudgmentJ8} {
		got, err := Instructions(j)
		if err != nil {
			t.Fatalf("Instructions(%s): %v", j, err)
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("Instructions(%s) is empty", j)
		}
	}
}

// TestInstructions_DifferentJudgmentPointsHaveDifferentContent は、5つの
// ファイルがそれぞれ別ファイルであり、中身も別であることを検証する
// （1つのファイルへの読み違いを防ぐ回帰）。
func TestInstructions_DifferentJudgmentPointsHaveDifferentContent(t *testing.T) {
	seen := map[string]core.JudgmentPoint{}
	for _, j := range []core.JudgmentPoint{core.JudgmentJ1, core.JudgmentJ2, core.JudgmentJ3, core.JudgmentJ4, core.JudgmentJ5, core.JudgmentJ6, core.JudgmentJ8} {
		got, err := Instructions(j)
		if err != nil {
			t.Fatalf("Instructions(%s): %v", j, err)
		}
		if prev, ok := seen[got]; ok {
			t.Errorf("Instructions(%s) and Instructions(%s) return identical content", j, prev)
		}
		seen[got] = j
	}
}

// TestInstructions_UnknownJudgmentPointReturnsError は、閉集合の外の値を
// 渡すとエラーになることを検証する（fail-closed。将来 core 側に判断点が
// 増えたときの空回り防止）。
func TestInstructions_UnknownJudgmentPointReturnsError(t *testing.T) {
	_, err := Instructions(core.JudgmentPoint("J7"))
	if err == nil {
		t.Fatal("Instructions(\"J7\") err = nil, want an error")
	}
}

// TestBriefFixedSections_ReturnsAllSectionsInFixedOrder は
// BriefFixedSections が §J3 の固定の節5つを、常に同じ順序で返すことを検証する
// （§J3「固定の節を…雛形から差し込んで、ブリーフを組み立てる」）。
func TestBriefFixedSections_ReturnsAllSectionsInFixedOrder(t *testing.T) {
	got, err := BriefFixedSections()
	if err != nil {
		t.Fatalf("BriefFixedSections(): %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("len(BriefFixedSections()) = %d, want 5", len(got))
	}
	for i, s := range got {
		if strings.TrimSpace(s.ID) == "" {
			t.Errorf("section[%d].ID is empty", i)
		}
		if strings.TrimSpace(s.Body) == "" {
			t.Errorf("section[%d].Body is empty", i)
		}
	}
	// 2回呼んでも同じ順序であることを固定する（マップ由来の非決定順を防ぐ）。
	got2, err := BriefFixedSections()
	if err != nil {
		t.Fatalf("BriefFixedSections() (2nd call): %v", err)
	}
	for i := range got {
		if got[i].ID != got2[i].ID {
			t.Errorf("order is not stable: [%d] = %q then %q", i, got[i].ID, got2[i].ID)
		}
	}
}

// TestCheckPromptSizes_RealEmbedIsWithinLimits は AC「J1〜J5の指示文とブリーフの
// 固定の節の雛形は、それぞれ定めた分量の上限以下である」の実物側を検証する。
func TestCheckPromptSizes_RealEmbedIsWithinLimits(t *testing.T) {
	fsys, err := promptsSubFS()
	if err != nil {
		t.Fatalf("promptsSubFS(): %v", err)
	}
	if err := CheckPromptSizes(fsys); err != nil {
		t.Errorf("CheckPromptSizes(real embed) = %v, want nil", err)
	}
}

// j2HeadroomBytes は J2 の指示文に残しておく余り（バイト）。新しい規則を足す
// 課題（例: #460）が上限に当たって止まらないよう、実物の j2.md が上限より
// この値以上小さいことを固定する（#462）。
const j2HeadroomBytes = 300

// TestJ2Prompt_HasHeadroomBelowLimit は、埋め込みの j2.md のバイト数が J2 の
// 上限より j2HeadroomBytes 以上小さいことを検証する（#462）。
func TestJ2Prompt_HasHeadroomBelowLimit(t *testing.T) {
	fsys, err := promptsSubFS()
	if err != nil {
		t.Fatalf("promptsSubFS(): %v", err)
	}
	info, err := fs.Stat(fsys, "j2.md")
	if err != nil {
		t.Fatalf("fs.Stat(j2.md): %v", err)
	}
	limit := judgmentPromptLimit["j2.md"]
	if headroom := int64(limit) - info.Size(); headroom < j2HeadroomBytes {
		t.Errorf("j2.md is %d bytes, limit %d bytes: headroom %d bytes, want >= %d", info.Size(), limit, headroom, j2HeadroomBytes)
	}
}

// j5HeadroomBytes は J5 の指示文に残しておく余り（バイト）。J5 に規則を足す課題が上限に当たって
// 止まらないよう、実物の j5.md が上限よりこの値以上小さいことを固定する（#466。J2 の #462 と同じやり方）。
const j5HeadroomBytes = 300

// TestJ5Prompt_HasHeadroomBelowLimit は、埋め込みの j5.md のバイト数が J5 の上限より
// j5HeadroomBytes 以上小さいことを検証する（#466）。
func TestJ5Prompt_HasHeadroomBelowLimit(t *testing.T) {
	fsys, err := promptsSubFS()
	if err != nil {
		t.Fatalf("promptsSubFS(): %v", err)
	}
	info, err := fs.Stat(fsys, "j5.md")
	if err != nil {
		t.Fatalf("fs.Stat(j5.md): %v", err)
	}
	limit := judgmentPromptLimit["j5.md"]
	if headroom := int64(limit) - info.Size(); headroom < j5HeadroomBytes {
		t.Errorf("j5.md is %d bytes, limit %d bytes: headroom %d bytes, want >= %d", info.Size(), limit, headroom, j5HeadroomBytes)
	}
}

// TestJ5Prompt_TaskSectionRequiresBothTheChallengeAndTheTaskDoneCriteria は、j5.md が区画「タスク」を読むものに挙げ、
// 区画があるときは課題の達成条件とタスクの満たす条件（完了条件。指示文は禁止語「完了」を避けてこう書く）の両方を満たしたときだけ「満たしている」を選ぶと述べることを確かめる。
func TestJ5Prompt_TaskSectionRequiresBothTheChallengeAndTheTaskDoneCriteria(t *testing.T) {
	ins, err := Instructions(core.JudgmentJ5)
	if err != nil {
		t.Fatalf("Instructions(J5): %v", err)
	}
	var line string
	for _, l := range strings.Split(ins, "\n") {
		if strings.HasPrefix(l, "- 「タスク」:") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("j5.md has no bullet for the 「タスク」 section")
	}
	for _, want := range []string{"この区画があるとき", "「達成条件」", "タスクの満たす条件", "両方を満たしている", "満たしているを選ぶ", "どちらか一方でも満たしていなければ満たしていないを選び"} {
		if !strings.Contains(line, want) {
			t.Errorf("the 「タスク」 bullet lacks %q:\n%s", want, line)
		}
	}
}

// TestCheckPromptSizes_ExactLimitPasses_OneByteOverFails は AC「上限を1バイト
// 超えるフィクスチャで失敗することも検証する」を、J1〜J5 の5ファイルすべての
// 上限で固定する（三角測量: ちょうど上限は通り、+1バイトで落ちることの両方を
// 同じフィクスチャの土台から確認する）。
//
// self-review 指摘（round1, code-reviewer CONFIRMED）: 当初は J1（4096バイト）
// の境界だけを検証しており、judgmentPromptLimit の J2〜J5 の値
// （当時 8192・8192・4096・6144。J2 は #462 で 12288、#749 で 12482、J5 は #466 で 8192、#565 で 9216、#563 で 9728、#654 で 10752、#629 で 11354）を緩めても検出するテストが無かった。5ファイル
// 分をテーブル駆動で回す形に直した。
func TestCheckPromptSizes_ExactLimitPasses_OneByteOverFails(t *testing.T) {
	limits := map[string]int{
		"j1.md": 4096,
		"j2.md": 13312,
		"j3.md": 8192,
		"j4.md": 4096,
		"j5.md": 11354,
		"j6.md": 6144,
		"j8.md": 8192,
	}

	mkFS := func(sizes map[string]int) fstest.MapFS {
		fsys := fstest.MapFS{
			"brief/decider.md":                 &fstest.MapFile{Data: repeatBytes(1)},
			"brief/decider_discipline.md":      &fstest.MapFile{Data: repeatBytes(1)},
			"brief/completion_report_style.md": &fstest.MapFile{Data: repeatBytes(1)},
			"brief/forbidden_operations.md":    &fstest.MapFile{Data: repeatBytes(1)},
			"brief/delegation_output_shape.md": &fstest.MapFile{Data: repeatBytes(1)},
			"resume/answer.md":                 &fstest.MapFile{Data: repeatBytes(1)},
			"resume/interrupted.md":            &fstest.MapFile{Data: repeatBytes(1)},
			"resume/budget.md":                 &fstest.MapFile{Data: repeatBytes(1)},
			"resume/rework.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		}
		for name := range limits {
			size := 1
			if s, ok := sizes[name]; ok {
				size = s
			}
			fsys[name] = &fstest.MapFile{Data: repeatBytes(size)}
		}
		return fsys
	}

	for name, limit := range limits {
		name, limit := name, limit
		t.Run(name, func(t *testing.T) {
			if err := CheckPromptSizes(mkFS(map[string]int{name: limit})); err != nil {
				t.Errorf("CheckPromptSizes(%s at exactly %d bytes) = %v, want nil", name, limit, err)
			}
			if err := CheckPromptSizes(mkFS(map[string]int{name: limit + 1})); err == nil {
				t.Errorf("CheckPromptSizes(%s at %d bytes) = nil, want an error", name, limit+1)
			}
		})
	}
}

// TestCheckPromptSizes_BriefTotal_ExactLimitPasses_OneByteOverFails は
// ブリーフの固定の節の雛形の合計12288バイトの境界を検証する。
func TestCheckPromptSizes_BriefTotal_ExactLimitPasses_OneByteOverFails(t *testing.T) {
	const totalLimit = 12288

	mkFS := func(lastFileSize int) fstest.MapFS {
		return fstest.MapFS{
			"j1.md":                            &fstest.MapFile{Data: repeatBytes(1)},
			"j2.md":                            &fstest.MapFile{Data: repeatBytes(1)},
			"j3.md":                            &fstest.MapFile{Data: repeatBytes(1)},
			"j4.md":                            &fstest.MapFile{Data: repeatBytes(1)},
			"j5.md":                            &fstest.MapFile{Data: repeatBytes(1)},
			"j6.md":                            &fstest.MapFile{Data: repeatBytes(1)},
			"j8.md":                            &fstest.MapFile{Data: repeatBytes(1)},
			"brief/decider.md":                 &fstest.MapFile{Data: repeatBytes(1)},
			"brief/decider_discipline.md":      &fstest.MapFile{Data: repeatBytes(1)},
			"brief/completion_report_style.md": &fstest.MapFile{Data: repeatBytes(1)},
			"brief/forbidden_operations.md":    &fstest.MapFile{Data: repeatBytes(1)},
			"brief/delegation_output_shape.md": &fstest.MapFile{Data: repeatBytes(lastFileSize)},
			"resume/answer.md":                 &fstest.MapFile{Data: repeatBytes(1)},
			"resume/interrupted.md":            &fstest.MapFile{Data: repeatBytes(1)},
			"resume/budget.md":                 &fstest.MapFile{Data: repeatBytes(1)},
			"resume/rework.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		}
	}
	// 4つの brief ファイルが1バイトずつ(計4)。残りを totalLimit-4 にすれば
	// ちょうど上限、+1すれば超過。
	if err := CheckPromptSizes(mkFS(totalLimit - 4)); err != nil {
		t.Errorf("CheckPromptSizes(brief total exactly %d) = %v, want nil", totalLimit, err)
	}
	if err := CheckPromptSizes(mkFS(totalLimit - 4 + 1)); err == nil {
		t.Errorf("CheckPromptSizes(brief total %d) = nil, want an error", totalLimit+1)
	}
}

// TestCheckPromptSizes_MissingJudgmentFileFails は、上限表にある判断点
// ファイルが1つでも欠けていたら失敗することを検証する（空回り防止）。
func TestCheckPromptSizes_MissingJudgmentFileFails(t *testing.T) {
	fsys := fstest.MapFS{
		"j1.md": &fstest.MapFile{Data: repeatBytes(1)},
		"j2.md": &fstest.MapFile{Data: repeatBytes(1)},
		"j3.md": &fstest.MapFile{Data: repeatBytes(1)},
		"j4.md": &fstest.MapFile{Data: repeatBytes(1)},
		// j5.md が無い
		"j6.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j8.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"brief/decider.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"brief/decider_discipline.md":      &fstest.MapFile{Data: repeatBytes(1)},
		"brief/completion_report_style.md": &fstest.MapFile{Data: repeatBytes(1)},
		"brief/forbidden_operations.md":    &fstest.MapFile{Data: repeatBytes(1)},
		"brief/delegation_output_shape.md": &fstest.MapFile{Data: repeatBytes(1)},
		"resume/answer.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"resume/interrupted.md":            &fstest.MapFile{Data: repeatBytes(1)},
		"resume/budget.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"resume/rework.md":                 &fstest.MapFile{Data: repeatBytes(1)},
	}
	if err := CheckPromptSizes(fsys); err == nil {
		t.Error("CheckPromptSizes(missing j5.md) = nil, want an error")
	}
}

// TestCheckPromptSizes_MissingBriefFileFails は、固定の節の雛形が1つでも
// 欠けていたら失敗することを検証する。
func TestCheckPromptSizes_MissingBriefFileFails(t *testing.T) {
	fsys := fstest.MapFS{
		"j1.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j2.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j3.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j4.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j5.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j6.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j8.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"brief/decider.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"brief/decider_discipline.md":      &fstest.MapFile{Data: repeatBytes(1)},
		"brief/completion_report_style.md": &fstest.MapFile{Data: repeatBytes(1)},
		"brief/forbidden_operations.md":    &fstest.MapFile{Data: repeatBytes(1)},
		// delegation_output_shape.md が無い
	}
	if err := CheckPromptSizes(fsys); err == nil {
		t.Error("CheckPromptSizes(missing a brief section) = nil, want an error")
	}
}

// TestCheckPromptSizes_UnexpectedFileFails は、上限表に無い判断点ファイル・
// 想定外のファイルが混入したら失敗することを検証する（§指示文の歯止め
// 「上限表に無い判断点ファイル・想定外のファイルが embed に入ったら失敗、など
// 空回り防止も入れる」）。
func TestCheckPromptSizes_UnexpectedFileFails(t *testing.T) {
	fsys := fstest.MapFS{
		"j1.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j2.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j3.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j4.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j5.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j6.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j8.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j7.md":                            &fstest.MapFile{Data: repeatBytes(1)}, // 上限表に無い
		"brief/decider.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"brief/decider_discipline.md":      &fstest.MapFile{Data: repeatBytes(1)},
		"brief/completion_report_style.md": &fstest.MapFile{Data: repeatBytes(1)},
		"brief/forbidden_operations.md":    &fstest.MapFile{Data: repeatBytes(1)},
		"brief/delegation_output_shape.md": &fstest.MapFile{Data: repeatBytes(1)},
		"resume/answer.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"resume/interrupted.md":            &fstest.MapFile{Data: repeatBytes(1)},
		"resume/budget.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"resume/rework.md":                 &fstest.MapFile{Data: repeatBytes(1)},
	}
	if err := CheckPromptSizes(fsys); err == nil {
		t.Error("CheckPromptSizes(with unexpected j7.md) = nil, want an error")
	}
}

// TestCheckPromptSizes_UnexpectedFileInBriefDirFails は brief/ 配下に
// 想定外のファイルが混じっても失敗することを検証する。
func TestCheckPromptSizes_UnexpectedFileInBriefDirFails(t *testing.T) {
	fsys := fstest.MapFS{
		"j1.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j2.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j3.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j4.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j5.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j6.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"j8.md":                            &fstest.MapFile{Data: repeatBytes(1)},
		"brief/decider.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"brief/decider_discipline.md":      &fstest.MapFile{Data: repeatBytes(1)},
		"brief/completion_report_style.md": &fstest.MapFile{Data: repeatBytes(1)},
		"brief/forbidden_operations.md":    &fstest.MapFile{Data: repeatBytes(1)},
		"brief/delegation_output_shape.md": &fstest.MapFile{Data: repeatBytes(1)},
		"resume/answer.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"resume/interrupted.md":            &fstest.MapFile{Data: repeatBytes(1)},
		"resume/budget.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"resume/rework.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"brief/extra_unexpected.md":        &fstest.MapFile{Data: repeatBytes(1)},
	}
	if err := CheckPromptSizes(fsys); err == nil {
		t.Error("CheckPromptSizes(with unexpected brief/extra_unexpected.md) = nil, want an error")
	}
}

func repeatBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return b
}

// AC-275: J2・J3・J5 の指示文は、入力の節と同じラベル（core.MemoryIndexSectionLabel）で記憶の索引を指し、
// 「関係する記憶を読んでよい」と述べる（入力の区画の中の指示は打ち消されるため、指示は指示文に置く）。
// J1・J4 の指示文は索引を指さない。
func TestInstructions_MemoryIndexIsReferencedByJ2J3J5Only(t *testing.T) {
	label := "「" + core.MemoryIndexSectionLabel + "」"
	for _, j := range []core.JudgmentPoint{core.JudgmentJ2, core.JudgmentJ3, core.JudgmentJ5} {
		got, err := Instructions(j)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, label) || !strings.Contains(got, "読んでよい") {
			t.Errorf("Instructions(%s) lacks %s or 読んでよい", j, label)
		}
	}
	for _, j := range []core.JudgmentPoint{core.JudgmentJ1, core.JudgmentJ4} {
		got, err := Instructions(j)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, label) {
			t.Errorf("Instructions(%s) must not refer to the memory index", j)
		}
	}
}

// AC-243a: j6.md は起票済みのフォローアップの区画を説明し、それと重複させない指示を持つ。
// ラベルは internal/core/judgment_j6.go の j6FollowupsLabel と同じ文字列（core の非公開の定数のため写す）。
func TestInstructions_J6ForbidsDuplicateFollowups(t *testing.T) {
	label := "「起票済みのフォローアップ」"
	got, err := Instructions(core.JudgmentJ6)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "- "+label+":") {
		t.Errorf("Instructions(J6) lacks the description of the section %s", label)
	}
	if !strings.Contains(got, label+"にあるものと同じ内容のものは重複させない") {
		t.Errorf("Instructions(J6) lacks the instruction not to duplicate %s", label)
	}
}

// AC-28（#772）: j6.md に「新しい課題の案」の語が無い。
func TestInstructions_J6HasNoNewChallengeProposalWord_AC28(t *testing.T) {
	got, err := Instructions(core.JudgmentJ6)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "新しい課題の案") {
		t.Error("Instructions(J6) still contains 「新しい課題の案」")
	}
}

// AC-29（#772）: j6.md に「達成条件」の語が無い。
func TestInstructions_J6HasNoDoneCriteriaWord_AC29(t *testing.T) {
	got, err := Instructions(core.JudgmentJ6)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "達成条件") {
		t.Error("Instructions(J6) still contains 「達成条件」")
	}
}

// AC-30（#772）: j6.md の、見出しに「フォローアップ」を含む節に「1 行」の語がある。
func TestInstructions_J6FollowupSectionSaysOneLine_AC30(t *testing.T) {
	got, err := Instructions(core.JudgmentJ6)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sec := range strings.Split("\n"+got, "\n#") {
		head, body, _ := strings.Cut(sec, "\n")
		if strings.HasPrefix(head, "#") && !strings.HasPrefix(head, "##") {
			continue
		}
		if strings.Contains(head, "フォローアップ") && !strings.Contains(head, "「") {
			found = true
			if !strings.Contains(body, "1 行") {
				t.Errorf("section %q lacks 「1 行」", head)
			}
		}
	}
	if !found {
		t.Error("no section whose heading contains フォローアップ")
	}
}

// J8 の指示文は、やめる・減らす候補の検討と、指標の取り方の穴の点検を求める。
func TestInstructions_J8AsksForReductionAndMetricHoleCheck(t *testing.T) {
	got, err := Instructions(core.JudgmentJ8)
	if err != nil {
		t.Fatal(err)
	}
	// 見出し（### 3・### 4）にも出る語ではなく、本文の句で照合する。
	for _, want := range []string{
		"足す改修だけを考えない",
		"やめる・減らす提案の候補として検討する",
		"選んだ指標で効果を本当に測れるかを疑う",
		"指標を選び直すか、その提案を出さない",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Instructions(J8) lacks %q", want)
		}
	}
	if len(got) > 8192 {
		t.Errorf("Instructions(J8) is %d bytes, over 8192", len(got))
	}
}

// #508: j8.md は、達成の条件の各手順が委譲の子か判断点の出力で実行できるかを確かめ、
// 実行できない手順を親かオーナーの段に分けることと、判断点の指示文へ書き足す文言に
// 特定のリポジトリやツールの語を入れないことを求める。
func TestInstructions_J8CriteriaExecutableAndNoToolSpecificTerms(t *testing.T) {
	got, err := Instructions(core.JudgmentJ8)
	if err != nil {
		t.Fatal(err)
	}
	// 見出しにも出る語ではなく、本文の句で照合する。
	for _, want := range []string{
		"委譲の子か判断点の出力で実行できるかを確かめる",
		"親かオーナーの段に分けて書く",
		"特定のリポジトリやツールの語",
		"一般的な言葉で書く",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Instructions(J8) lacks %q", want)
		}
	}
	if strings.Contains(got, "応答の本文") {
		t.Errorf("Instructions(J8) must put the withheld evidence in the withheld field, not the response body")
	}
}

// #444: j2.md の達成条件の項は、達成の確認がこの課題の記録だけで判定できる条件にすることと、
// 他の課題の将来の実行に依存する確認をテストでの固定かフォローアップの課題へ回すことを求める。
func TestInstructions_J2CriteriaJudgeableFromOwnRecord(t *testing.T) {
	got, err := Instructions(core.JudgmentJ2)
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "- 達成条件（課題の達成条件）:") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("Instructions(J2) lacks the item of 達成条件")
	}
	for _, want := range []string{
		"達成の確認がこの課題の記録だけで判定できる条件にする",
		"他の課題の将来の実行に依存する確認は、テストでの固定に代えるか、フォローアップの課題に分ける",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the item of 達成条件 in Instructions(J2) lacks %q", want)
		}
	}
}

// #451: j2.md は、達成条件をこの課題の本番反映の一歩手前までにすること・人の操作を達成条件に入れないこと・
// 本番反映の後に人が行う作業を手順の末尾に『本番反映の後に人が行うこと』として書くことを求める。
func TestInstructions_J2CriteriaStopBeforeTheReleaseAndHumanActionsGoToTheSteps(t *testing.T) {
	got, err := Instructions(core.JudgmentJ2)
	if err != nil {
		t.Fatal(err)
	}
	item := func(prefix string) string {
		for _, l := range strings.Split(got, "\n") {
			if strings.Contains(l, prefix) {
				return l
			}
		}
		t.Fatalf("Instructions(J2) lacks the item %q", prefix)
		return ""
	}
	criteria := item("- 達成条件（課題の達成条件）:")
	for _, want := range []string{
		"達成条件は、この課題の本番反映の一歩手前まで（例: 成果物が揃い、自動の検査が通る）にする",
		"人の操作は達成条件に入れない",
	} {
		if !strings.Contains(criteria, want) {
			t.Errorf("the item of 達成条件 in Instructions(J2) lacks %q", want)
		}
	}
	if tasks := item("- タスク（tasks）:"); !strings.Contains(tasks, "本番反映の後に人が行う作業は、最後のタスクの body の末尾に『本番反映の後に人が行うこと』として書く") {
		t.Errorf("the item of タスク in Instructions(J2) lacks where to write the human work after the release: %q", tasks)
	}
}

// #451: j5.md は、本番反映より前には満たせない条件に付ける 2 つの区分と、その使い方を書く。
func TestInstructions_J5DescribesTheTwoConditionCategories(t *testing.T) {
	got, err := Instructions(core.JudgmentJ5)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## 本番反映より前には満たせない条件",
		"- 本番反映で満たされる見込み:",
		"- 人の操作の後でしか満たせない:",
		"判断の 3 つは、区分を付けた条件を除いた残りの条件で選ぶ",
		"区分は、いま確かめられる条件には付けない",
		"保留の記録で人がその条件に答えている（済ませた・本番反映の後に行う等）ときは、回答に従い、同じ区分を付け直さない",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Instructions(J5) lacks %q", want)
		}
	}
}

// #466: j5.md は、「本番反映が無いので条件を満たせない」保留への人の回答ごとの扱い（マージ済み・PR が無い・
// 条件が不要）を書く。
func TestInstructions_J5DescribesTheAnswersToTheNoReleaseHold(t *testing.T) {
	got, err := Instructions(core.JudgmentJ5)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(got, "### 「本番反映が無いので条件を満たせない」保留への回答")
	end := strings.Index(got, "## 守ること")
	if start < 0 || end < start {
		t.Fatalf("Instructions(J5) lacks the subsection about the answers to the no-release hold: %d %d", start, end)
	}
	sub := got[start:end]
	for _, want := range []string{
		"- マージ済み: 既定ブランチ（main）で取り込まれたことを確かめてから、その条件は満たしているとして判定する",
		"- PR が無い: その条件は満たしていない。満たしていないを選び、差し戻しの指摘に、本番反映する PR を子に作らせることを書く",
		"- 条件が不要: その条件を達成条件から除いて判定する",
		"同じ区分を付け直さない",
	} {
		if !strings.Contains(sub, want) {
			t.Errorf("the subsection lacks %q", want)
		}
	}
}

// j2.md は、論点と問いを計画と同じ 1 回の出力で返すこと・タスク・統合先を使うかを求める。先に論点だけを返す段・
// 論点不要の根拠・問い直しの区画・論点と回答の区画は持たない（core の見直し S2a。AC-53 は no_issues_needed の文字列が無いこと）。
func TestInstructions_J2DescribesTopicsTasksAndNoRemovedSections(t *testing.T) {
	got, err := Instructions(core.JudgmentJ2)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"論点（topics）",
		"問い（0〜3 件）",
		"論点と問いは、計画と同じ 1 回の出力で返す",
		"タスク（tasks）: 1〜12 件",
		"前提のタスクの識別子の配列（depends_on",
		"対象リポジトリ（repo",
		"統合先を使うか（uses_integration）",
		"タスクが 2 件以上なら真にする",
		"タスクが 1 件の計画では、課題の達成条件とタスクの達成条件が食い違わないように書く",
		"課題の全体について書く",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Instructions(J2) lacks %q", want)
		}
	}
	for _, gone := range []string{"no_issues_needed", "論点不要", "「論点と回答」", "「問い直し」", "判断の前の確認", "手順:", "steps"} {
		if strings.Contains(got, gone) {
			t.Errorf("Instructions(J2) still contains %q", gone)
		}
	}
}

// #472: j2.md は、計画を出す前の点検として、作業のセッションの中で進められる手順にすること
// （周を回すことが要る段は親の段に分ける・実物を起動する段に権限と後片付けを書く）、
// 着手の門を課題の間の依存で表し外部の Issue の開閉を門にしないこと、
// パスを実物で確かめ渡す文が渡す先の禁止語を含まないかを確かめることを、例とともに求める。
func TestInstructions_J2ChecksThePlanBeforeOutput(t *testing.T) {
	got, err := Instructions(core.JudgmentJ2)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(got, "## 計画の点検\n")
	if start < 0 {
		t.Fatal("Instructions(J2) lacks the section 計画の点検")
	}
	section := got[start+len("## 計画の点検\n"):]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	for _, want := range []string{
		"周を回すことが要る段は、親が行う段として手順に分ける",
		"与える権限と、確かめた後の停止・削除の後片付けを書く",
		"着手の門や順序は、課題の間の依存で表す",
		"外部の Issue の開閉などの状態を、着手や続行の門にしない",
		"『子の課題に分けて依存でつなぐ』と答えたときは、課題の依存として扱う",
		"読み取りの道具で実物を確かめてから書き、確かめられなければ『未確認』と添える",
		"渡す先の指示文が禁じる語を含まないかを確かめ",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the section 計画の点検 in Instructions(J2) lacks %q", want)
		}
	}
	// 各点に要旨の例を 1 行ずつ添える。
	if n := strings.Count(section, "例:"); n < 3 {
		t.Errorf("the section 計画の点検 in Instructions(J2) has %d examples, want >= 3", n)
	}
}

// #500: 人や子に問い・選択肢・理由・推奨・差し戻しの指摘を出す判断点の指示文は、
// 入力の区画が英語でも日本語で書くことを明記する（文を消すとこのテストが失敗する）。
//
// 足す前の指示文ごとの判定（2026-11-09 に既定ブランチの ec3f1ce で確認）:
//
//	指示文 | 人へ出す文                       | 言語を指定する文 | 英語の部分
//	j1.md  | 理由・headline・位置づけ         | 無し             | 項目名の headline と例の識別子（AC-12 等）
//	j2.md  | 問い・論点・理由                 | 無し             | 出力のキー名の括弧書き（expected・non_goals 等）
//	j3.md  | 無し（委譲先への brief。問いを出さない） | 無し     | 無し
//	j4.md  | 雛形のみ（本文は後続のチケット） | 無し             | 無し
//	j5.md  | 理由・差し戻しの指摘・問い       | 無し             | 「実物（actual）」・`{scene, after}`・main
//	j6.md  | 学び・記憶・フォローアップ       | 無し             | 出力のキー名の括弧書き（classifications 等）
//	j8.md  | 改修の提案と理由                 | 無し             | 無し
//
// J3 は #628 で子への報告の言語を指定する別の文を足した（共通の文ではないので、検査は TestInstructions_J3RequiresChildReportInJapanese）。
//
// 出力の型（internal/core の各 jN の出力のスキーマ）はキー名・閉集合の値が英語で、
// description は J1 の context（日本語）以外に無い。言語を指定する文はどこにも無かった。
// 共通の規律を置く仕組み（判断点の指示文の共通の部品）は無いので、j3・j4 以外の各指示文に足した。
//
// 英語の問いになった原因の候補（未確認）: J5 の入力には PR の差分の区画が最大 200,000 バイト
// （maxPullRequestDiffBytes）入り、コードの識別子・英語のコメントが大半を占めうる。出力の型の
// キー名も英語で、指示文に言語の指定が無かったため、PR の中身を読めずにオーナーへ差分と本文の
// 確認を頼む場面（C-439 の H-91・C-451 の H-122 に共通）で、日本語の文脈が薄いまま英語に
// 引きずられた見込み。実際の入力（その時の差分の区画の中身）は確かめていない。
func TestInstructions_RequireJapaneseForHumanFacingText(t *testing.T) {
	const want = "人や子に出す問い・選択肢・理由・推奨・差し戻しの指摘の文は日本語で書く。データの区画（差分・課題・コメント）が英語でも同じ。コードの識別子・パス・コマンド・引用は原文のままでよい。"
	for _, jp := range []core.JudgmentPoint{core.JudgmentJ1, core.JudgmentJ2, core.JudgmentJ5, core.JudgmentJ6, core.JudgmentJ8} {
		got, err := Instructions(jp)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, want) {
			t.Errorf("Instructions(%v) lacks the rule to write human-facing text in Japanese", jp)
		}
	}
}

// J3 は人に問いを出さないので共通の文は求めず、子への報告の言語の指定を検査する（文を消すと失敗する）。
func TestInstructions_J3RequiresChildReportInJapanese(t *testing.T) {
	const want = "委譲先が返す報告（要約・前提・未検証の項目など）は日本語で書くよう、ブリーフに明記する。入力が英語でも同じ。コードの識別子・パス・コマンド・引用は原文のままでよい。"
	got, err := Instructions(core.JudgmentJ3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, want) {
		t.Error("Instructions(J3) lacks the rule to have the child report in Japanese")
	}
}

// #511: j2.md の手順の項は、委譲の子が使えない操作（この仕組みのコマンド・サーバの起動）を
// 計画の子の手順や論点の選択肢に入れず、親かオーナーの段か PR の CI へ分けることを求める。
// 規則の行は特定のリポジトリの名前を含まない（汎用の道具として一般の言い方で書く）。
func TestInstructions_J2StepsExcludeOperationsDelegatedChildCannotUse(t *testing.T) {
	got, err := Instructions(core.JudgmentJ2)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"この仕組みのコマンド（CLI・サーバの起動を含む）を help 以外使えない",
		"タスクの手順にも論点の選択肢にも入れない",
		"親かオーナーの段",
		"PR の CI で確かめる",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Instructions(J2) lacks %q", want)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "この仕組みのコマンド") && strings.Contains(strings.ToLower(line), "flywheel") {
			t.Errorf("the rule line names a specific repository: %q", line)
		}
	}
}

// #538（#542 で「計画の点検」へ統合）: j2.md は、達成条件を書く前の確認として、(1) 上流の達成条件を写す前に
// 関係する PR がマージ済みか・Issue が開いているかを確かめ、マージ済みなら成果物の形を選び直すこと、
// (2) 子の課題に分けるという回答のときは取りまとめの Issue 自身を委譲しないこと、
// (3) 「CI で通る」と書く前にその検査が CI にあるかを確かめることを求める（文を消すとこのテストが失敗する）。
func TestInstructions_J2VerifiesPlanPremisesBeforeCriteria(t *testing.T) {
	got, err := Instructions(core.JudgmentJ2)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(got, "## 計画の点検\n")
	end := strings.Index(got, "## 守ること")
	if start < 0 || end < start {
		t.Fatalf("Instructions(J2) lacks the section 計画の点検 before 守ること: %d %d", start, end)
	}
	item := got[start:end]
	for _, c := range []struct {
		name string
		keys []string
	}{
		{"(1) upstream PR merged / Issue open", []string{"上流の達成条件を写す前に", "PR がマージ済みか", "Issue が開いているか", "マージ済みなら成果物の形", "選び直す"}},
		{"(2) do not delegate the umbrella Issue", []string{"子の課題に分けるという回答のとき", "取りまとめの Issue 自身を委譲しない"}},
		{"(3) confirm the check exists in CI", []string{"「CI で通る」と書く前に", "その検査が CI にあるか確かめる"}},
	} {
		for _, want := range c.keys {
			if !strings.Contains(item, want) {
				t.Errorf("the section 計画の点検 in Instructions(J2) lacks %q for %s", want, c.name)
			}
		}
	}
}

// #542: j2.md は、(規則 1) 子の課題に分けると決まった取りまとめの課題には委譲の計画を出さず、人への問いを 1 つ返し、
// 選択肢に「親が子の Issue を起票して取り込み、依存でつなぐ」と「親が直接行う種類の保留にする」を入れること、
// (規則 2) 論点の回答で選ばれた選択肢の中身を計画の手順と達成条件に全部写し、写せなければ問いにすること、
// (b) 手元での実行に書き換えたときは結果を PR の説明に残させることを求める。規則の行は特定の名前を含まない。
func TestInstructions_J2AggregateIssueQuestionAndChosenOptionCoverage(t *testing.T) {
	got, err := Instructions(core.JudgmentJ2)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(got, "## 計画の点検\n")
	end := strings.Index(got, "## 守ること")
	if start < 0 || end < start {
		t.Fatalf("Instructions(J2) lacks the section 計画の点検 before 守ること: %d %d", start, end)
	}
	section := got[start:end]
	for _, want := range []string{
		"達成条件を書く前と出力の前に次を確かめ",
		"委譲の計画（1）を出さず、人への問い（2）を 1 つ返す",
		"保留の記録にこの問いの回答があるときは、その回答に従い問い直さない",
		"「親が子の Issue を起票して取り込み、依存でつなぐ」",
		"「取りまとめの課題は、親が直接行う種類の保留にする」",
		"どちらかを推奨にする",
		"ラベルだけでなく、それが何を確かめる／何を作るためのものかを、タスクの body と達成条件に全部写す",
		"写せない部分があれば計画に入れず、2 で問う",
		"手元での実行に書き換え、実行したコマンドと成否の要旨を PR の説明に残させる",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the section 計画の点検 in Instructions(J2) lacks %q", want)
		}
	}
	for _, l := range strings.Split(section, "\n") {
		if low := strings.ToLower(l); strings.Contains(low, "flywheel") || strings.Contains(low, "claude-harness") {
			t.Errorf("the rule line names a specific repository: %q", l)
		}
	}
}

// #563: j5.md は、委譲の報告がレビュー（自己レビュー・独立レビュー）の未実施・不明を申告しているときの扱い
// （満たしているを選ばず判断できないを選ぶ・理由の文言・ほかの未検証へ広げない・満たしていないになるときはそちらを優先）を書く。
func TestInstructions_J5DescribesTheUnreviewedReportRule(t *testing.T) {
	got, err := Instructions(core.JudgmentJ5)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"子の作業の中で行うべき自己レビュー・独立レビューの未実施・不明の申告があるときは、満たしているを選ばず、判断できないを選ぶ",
		"レビュー未実施・不明の申告あり。親の独立レビューを推奨",
		"次はこの扱いにしない:",
		"ほかの未検証の申告",
		"人による PR のレビューやマージの承認の待ち",
		"直した後の再レビューは無い",
		"レビューの実施状況（実施・未実施・不明と、その根拠）の記載が無い・空のときは、不明として扱う",
		"保留の記録に、レビューの実施を確かめた、またはレビュー無しで進めることを認めた人の回答（例: 親がレビューして問題なし・レビューを省いて進めてよい）があるときも、この扱いをせず回答に従う",
		"その回答の後に来た最初の検証（同じ委譲の報告・同じ PR の head）だけで、差し戻しの後の新しい差分には効かせない",
		"判断できないの理由が複数あるときは、問いは 1 つのまま、すべての理由をその 1 つの問いに並べる",
		"満たしていないになるときはそちらを選び、差し戻しの指摘にレビューの実施を含める",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Instructions(J5) lacks %q", want)
		}
	}
}

// #654: 委譲の指示文（J3 ブリーフの固定の節）は、報告にレビューの実施状況を必ず含め、空にしないことを求める。
func TestBriefFixedSections_RequireReviewStatusInTheReport(t *testing.T) {
	fixed, err := BriefFixedSections()
	if err != nil {
		t.Fatal(err)
	}
	var all string
	for _, f := range fixed {
		all += f.Body
	}
	for _, want := range []string{
		"レビューの実施状況（実施・未実施・不明のどれかと、その根拠）。要約か確かめていないことに必ず書く。書かなければ、受け取った側は不明として扱う",
		"レビューの実施状況（実施・未実施・不明と、その根拠）: 要約か確かめていないことに必ず書く",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("brief fixed sections lack %q", want)
		}
	}
}

// TestJ2Prompt_MentionsParentDirectVerdict は、J2 の指示文が parent_direct の判定の意味（委譲の計画が立たず
// 親が直接行うのが適切なときに、理由と親の達成条件を書く）を
// 持つことを固定する（#536）。
func TestJ2Prompt_MentionsParentDirectVerdict(t *testing.T) {
	fsys, err := promptsSubFS()
	if err != nil {
		t.Fatalf("promptsSubFS(): %v", err)
	}
	b, err := fs.ReadFile(fsys, "j2.md")
	if err != nil {
		t.Fatalf("fs.ReadFile(j2.md): %v", err)
	}
	for _, want := range []string{"委譲の計画が立たず、親が直接行うのが適切", "親が何を満たせば終わりか"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("j2.md does not contain %q", want)
		}
	}
}

// TestJ2Prompt_ListsReplanSection は、J2 の指示文が区画「計画の作り直し」と、前の計画より回答を優先する扱いを
// 持つことを固定する（#642）。
func TestJ2Prompt_ListsReplanSection(t *testing.T) {
	got, err := Instructions(core.JudgmentJ2)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"「" + core.ReplanSectionLabel + "」", "前の計画より回答を優先して計画を作り直す"} {
		if !strings.Contains(got, want) {
			t.Errorf("Instructions(J2) lacks %q", want)
		}
	}
}

// #571: j8.md は、達成の条件を対象のいまの中身と課題のいまの状態に照らし、満たし済みの項目を外すこと・
// 区画に無い課題は「状態は未確認」と書くこと・全項目が満たし済みなら提案を出さないことを求める。
func TestInstructions_J8CriteriaChecksCurrentState(t *testing.T) {
	got, err := Instructions(core.JudgmentJ8)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"満たし済みの項目は条件から外し",
		"後を継いだ課題に読み替え",
		"状態は未確認",
		"その提案は出さない",
		"見送りの欄に",
		"明細の課題の ID と過去の改修課題の区画には、課題の状態が付き",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Instructions(J8) lacks %q", want)
		}
	}
}

// core の見直しの区切り S1（docs/features/core-redesign.md）の受入基準のうち、J9 の指示文が無いこと。
// サブテスト名の先頭が基準の番号（masanami/flywheel#732 の受入基準の並び順。ほかの仕様の同じ番号とは無関係）。
func TestPrompts_J9IsGone(t *testing.T) {
	t.Run("AC-56 internal/invoker/prompts/j9.md が無い", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join("prompts", "j9.md")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("prompts/j9.md on disk: err = %v, want not exist", err)
		}
		fsys, err := promptsSubFS()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fs.Stat(fsys, "j9.md"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("embedded j9.md: err = %v, want not exist", err)
		}
	})
	t.Run("AC-57 指示文の分量の上限の表に j9.md の行が無い", func(t *testing.T) {
		if limit, ok := judgmentPromptLimit["j9.md"]; ok {
			t.Errorf("judgmentPromptLimit has j9.md = %d, want no entry", limit)
		}
	})
}

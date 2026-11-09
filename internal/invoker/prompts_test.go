package invoker

import (
	"io/fs"
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

// TestCheckPromptSizes_ExactLimitPasses_OneByteOverFails は AC「上限を1バイト
// 超えるフィクスチャで失敗することも検証する」を、J1〜J5 の5ファイルすべての
// 上限で固定する（三角測量: ちょうど上限は通り、+1バイトで落ちることの両方を
// 同じフィクスチャの土台から確認する）。
//
// self-review 指摘（round1, code-reviewer CONFIRMED）: 当初は J1（4096バイト）
// の境界だけを検証しており、judgmentPromptLimit の J2〜J5 の値
// （当時 8192・8192・4096・6144。J2 は #462 で 12288、J5 は #466 で 8192）を緩めても検出するテストが無かった。5ファイル
// 分をテーブル駆動で回す形に直した。
func TestCheckPromptSizes_ExactLimitPasses_OneByteOverFails(t *testing.T) {
	limits := map[string]int{
		"j1.md": 4096,
		"j2.md": 12288,
		"j3.md": 8192,
		"j4.md": 4096,
		"j5.md": 8192,
		"j6.md": 6144,
		"j8.md": 6144,
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
	if !strings.Contains(got, label+"にある課題と同じ内容のものは重複させない") {
		t.Errorf("Instructions(J6) lacks the instruction not to duplicate %s", label)
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
	if len(got) > 6144 {
		t.Errorf("Instructions(J8) is %d bytes, over 6144", len(got))
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
		if strings.Contains(l, "- 達成条件:") {
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
	criteria := item("- 達成条件:")
	for _, want := range []string{
		"達成条件は、この課題の本番反映の一歩手前まで（例: 成果物が揃い、自動の検査が通る）にする",
		"人の操作は達成条件に入れない",
	} {
		if !strings.Contains(criteria, want) {
			t.Errorf("the item of 達成条件 in Instructions(J2) lacks %q", want)
		}
	}
	if steps := item("- 手順:"); !strings.Contains(steps, "本番反映の後に人が行う作業があれば、手順の末尾に『本番反映の後に人が行うこと』として書く") {
		t.Errorf("the item of 手順 in Instructions(J2) lacks where to write the human work after the release: %q", steps)
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

// #450: j2.md は、中以上で論点の記録が無いとき計画ではなく論点を選ぶ確認の段（判断の前）と、
// 論点不要を返す条件（決まっていることごとの引用）、問い直しの区画、出力の型の説明の行を持つ。
func TestInstructions_J2AsksForQuotesBeforeSkippingIssues(t *testing.T) {
	got, err := Instructions(core.JudgmentJ2)
	if err != nil {
		t.Fatal(err)
	}
	confirm := strings.Index(got, "## 判断の前の確認")
	judge := strings.Index(got, "## 判断\n")
	if confirm < 0 || judge < 0 || confirm > judge {
		t.Fatalf("the confirmation step must come before the judgment section: %d %d", confirm, judge)
	}
	step := got[confirm:judge]
	for _, want := range []string{
		"区画「論点と回答」が無く、見込みのサイズが中以上なら、1 は選ばず 3 を選ぶ",
		"決まったことすべてを区画の文で引用できるときだけ、1 に論点不要（no_issues_needed）と引用を付けてよい",
		"引用できない決定が 1 つでもあれば 3 を選ぶ",
	} {
		if !strings.Contains(step, want) {
			t.Errorf("the confirmation step lacks %q", want)
		}
	}
	// 1 の項目に論点不要の意味、読むものに問い直しの区画、出力の型の説明の行にサイズと記録の関係。
	var item, shape string
	for _, l := range strings.Split(got, "\n") {
		switch {
		case strings.Contains(l, "- 論点不要（no_issues_needed）:"):
			item = l
		case strings.Contains(l, "出力の形式（どの項目にどんな値を入れるか）"):
			shape = l
		}
	}
	for _, want := range []string{"確認の条件を満たすときだけ", "中身・引用元の種別", "引用の文"} {
		if !strings.Contains(item, want) {
			t.Errorf("the item of no_issues_needed lacks %q", want)
		}
	}
	if !strings.Contains(shape, "計画のサイズが中・大なら、論点の記録（区画「論点と回答」）か論点不要の引用が要る") {
		t.Errorf("the line about the output shape lacks the size rule: %q", shape)
	}
	if !strings.Contains(got, "- 「問い直し」:") {
		t.Error("the reading list lacks the re-ask section")
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

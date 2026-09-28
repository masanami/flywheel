package invoker

import (
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
	for _, j := range []core.JudgmentPoint{core.JudgmentJ1, core.JudgmentJ2, core.JudgmentJ3, core.JudgmentJ4, core.JudgmentJ5} {
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
	for _, j := range []core.JudgmentPoint{core.JudgmentJ1, core.JudgmentJ2, core.JudgmentJ3, core.JudgmentJ4, core.JudgmentJ5} {
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
	_, err := Instructions(core.JudgmentPoint("J6"))
	if err == nil {
		t.Fatal("Instructions(\"J6\") err = nil, want an error")
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

// TestCheckPromptSizes_ExactLimitPasses_OneByteOverFails は AC「上限を1バイト
// 超えるフィクスチャで失敗することも検証する」を、J1〜J5 の5ファイルすべての
// 上限で固定する（三角測量: ちょうど上限は通り、+1バイトで落ちることの両方を
// 同じフィクスチャの土台から確認する）。
//
// self-review 指摘（round1, code-reviewer CONFIRMED）: 当初は J1（4096バイト）
// の境界だけを検証しており、judgmentPromptLimit の J2〜J5 の値
// （8192・8192・4096・6144）を緩めても検出するテストが無かった。5ファイル
// 分をテーブル駆動で回す形に直した。
func TestCheckPromptSizes_ExactLimitPasses_OneByteOverFails(t *testing.T) {
	limits := map[string]int{
		"j1.md": 4096,
		"j2.md": 8192,
		"j3.md": 8192,
		"j4.md": 4096,
		"j5.md": 6144,
	}

	mkFS := func(sizes map[string]int) fstest.MapFS {
		fsys := fstest.MapFS{
			"brief/decider.md":                 &fstest.MapFile{Data: repeatBytes(1)},
			"brief/decider_discipline.md":      &fstest.MapFile{Data: repeatBytes(1)},
			"brief/completion_report_style.md": &fstest.MapFile{Data: repeatBytes(1)},
			"brief/forbidden_operations.md":    &fstest.MapFile{Data: repeatBytes(1)},
			"brief/delegation_output_shape.md": &fstest.MapFile{Data: repeatBytes(1)},
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
			"brief/decider.md":                 &fstest.MapFile{Data: repeatBytes(1)},
			"brief/decider_discipline.md":      &fstest.MapFile{Data: repeatBytes(1)},
			"brief/completion_report_style.md": &fstest.MapFile{Data: repeatBytes(1)},
			"brief/forbidden_operations.md":    &fstest.MapFile{Data: repeatBytes(1)},
			"brief/delegation_output_shape.md": &fstest.MapFile{Data: repeatBytes(lastFileSize)},
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
		"brief/decider.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"brief/decider_discipline.md":      &fstest.MapFile{Data: repeatBytes(1)},
		"brief/completion_report_style.md": &fstest.MapFile{Data: repeatBytes(1)},
		"brief/forbidden_operations.md":    &fstest.MapFile{Data: repeatBytes(1)},
		"brief/delegation_output_shape.md": &fstest.MapFile{Data: repeatBytes(1)},
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
		"j6.md":                            &fstest.MapFile{Data: repeatBytes(1)}, // 上限表に無い
		"brief/decider.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"brief/decider_discipline.md":      &fstest.MapFile{Data: repeatBytes(1)},
		"brief/completion_report_style.md": &fstest.MapFile{Data: repeatBytes(1)},
		"brief/forbidden_operations.md":    &fstest.MapFile{Data: repeatBytes(1)},
		"brief/delegation_output_shape.md": &fstest.MapFile{Data: repeatBytes(1)},
	}
	if err := CheckPromptSizes(fsys); err == nil {
		t.Error("CheckPromptSizes(with unexpected j6.md) = nil, want an error")
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
		"brief/decider.md":                 &fstest.MapFile{Data: repeatBytes(1)},
		"brief/decider_discipline.md":      &fstest.MapFile{Data: repeatBytes(1)},
		"brief/completion_report_style.md": &fstest.MapFile{Data: repeatBytes(1)},
		"brief/forbidden_operations.md":    &fstest.MapFile{Data: repeatBytes(1)},
		"brief/delegation_output_shape.md": &fstest.MapFile{Data: repeatBytes(1)},
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

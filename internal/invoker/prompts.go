package invoker

// このファイルは #82（親要件 #77・docs/features/m3-invoker-delegation.md
// §指示文の歯止め）が定める、判断点ごとの指示文と J3 ブリーフの固定の節の
// 雛形の置き場・embed・分量の検査を持つ。
//
//   - 判断点ごとの指示文と固定の節の雛形は internal/invoker/prompts/ 配下の
//     ファイルとして置き、embed でバイナリに埋め込む（実行時にワークスペースや
//     プラグインから読まない。AC「実行時に指示文をワークスペースから読まない」）。
//   - J1・J2 の指示文の本文は #84・#85、J3〜J5・固定の節の本文は S2・S3 が書く。
//     本チケットは「検査を通る最小限の雛形」だけを置く。
//   - 分量の上限・置き場の空回り防止（想定外のファイル混入・欠落の検出）は
//     CheckPromptSizes が担う。fs.FS を受け取る形にし、本物の embed でも
//     フィクスチャ（fstest.MapFS）でも同じ関数を通す（検査関数を2つ持たない）。
//
// self-review 指摘（round1, design-reviewer PLAUSIBLE・未解決のまま持ち越し）:
// Instructions・BriefFixedSections を実際に呼んで判断の呼び出しの標準入力を
// 組み立てるのは誰か（#84・#85 以降）が、本チケットの時点では未確定である。
// CLAUDE.md の import の向きの規約は「internal/cli が internal/invoker を
// import してよいのは New・NewLauncher の組み立てと ErrClaudeNotFound の
// 写像だけ」としており、internal/core は internal/invoker を import しない。
// したがって Instructions・BuildStdin を呼んで core.RunJudgmentInput.Stdin を
// 組み立てる処理を internal/cli に書くなら上の規約を広げる決定が要り、
// internal/core 側に置くなら import の向きの規約自体を見直す決定が要る。
// このファイルはどちらか一方を先取りして決めず、後続チケットが規約と併せて
// 決定することを前提にしている（本チケットの範囲は置き場・embed・検査だけ）。
// 本チケットの internal/cli/prompts_guard_test.go は _test.go からの検査
// 目的の呼び出しであり、上の「cli が呼んでよい範囲」は本番コードパスに
// ついての規約であるため、テストからの呼び出しはこの規約の対象外である。

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

//go:embed prompts
var promptsFS embed.FS

// promptsSubFS は promptsFS を "prompts" 配下にルートし直した fs.FS を返す
// （返り値のパスは "j1.md"・"brief/decider.md" のように prompts/ を含まない。
// CheckPromptSizes・フィクスチャの fstest.MapFS と同じ形にそろえるため）。
func promptsSubFS() (fs.FS, error) {
	return fs.Sub(promptsFS, "prompts")
}

// judgmentPromptFile は判断点ごとの指示文ファイル名（prompts/ 直下、
// promptsSubFS からの相対パス）。
var judgmentPromptFile = map[core.JudgmentPoint]string{
	core.JudgmentJ1: "j1.md",
	core.JudgmentJ2: "j2.md",
	core.JudgmentJ3: "j3.md",
	core.JudgmentJ4: "j4.md",
	core.JudgmentJ5: "j5.md",
}

// judgmentPromptLimit は判断点ごとの指示文の分量の上限（バイト。UTF-8）
// （docs/features/m3-invoker-delegation.md §指示文の歯止め「J1: 4KiB・
// J2: 8KiB・J3: 8KiB・J4: 4KiB・J5: 6KiB」）。
var judgmentPromptLimit = map[string]int{
	"j1.md": 4096,
	"j2.md": 8192,
	"j3.md": 8192,
	"j4.md": 4096,
	"j5.md": 6144,
}

// briefFixedSectionsTotalLimit は J3 ブリーフの固定の節の雛形の合計の上限
// （バイト。§指示文の歯止め「ブリーフの固定の節の雛形の合計: 12 KiB」）。
const briefFixedSectionsTotalLimit = 12288

// briefSectionOrder は J3 が固定の節を差し込む順序（§J3「固定の節（意思決定者と
// 該当した行・意思決定者ごとの規律・完了報告の様式・禁止する操作と代替手段・
// 報告の形）を…組み立てる」）。ID は英字の識別子（表示用の日本語見出しは、
// 実際にブリーフへ差し込む文言の二重管理を避けるため、本チケットでは持たない。
// 差し込みの組み立ては S2 の J3 実装チケットが行う）。
var briefSectionOrder = []struct {
	ID   string
	File string
}{
	{ID: "decider", File: "brief/decider.md"},
	{ID: "decider_discipline", File: "brief/decider_discipline.md"},
	{ID: "completion_report_style", File: "brief/completion_report_style.md"},
	{ID: "forbidden_operations", File: "brief/forbidden_operations.md"},
	{ID: "delegation_output_shape", File: "brief/delegation_output_shape.md"},
}

// Instructions は j の指示文の本文を、embed から読んで返す
// （ワークスペースからは一切読まない）。後続チケット（J1: #84、J2: #85、
// J3〜J5: S2・S3）は、この関数の戻り値を invoker.BuildStdin の instructions
// 引数へそのまま渡す入口として使う想定。
func Instructions(j core.JudgmentPoint) (string, error) {
	file, ok := judgmentPromptFile[j]
	if !ok {
		return "", fmt.Errorf("invoker: unknown judgment point %q", j)
	}
	fsys, err := promptsSubFS()
	if err != nil {
		return "", fmt.Errorf("invoker: prompts fs: %w", err)
	}
	b, err := fs.ReadFile(fsys, file)
	if err != nil {
		return "", fmt.Errorf("invoker: read instructions for %s: %w", j, err)
	}
	return string(b), nil
}

// BriefFixedSection は J3 ブリーフの固定の節1件（雛形）。
type BriefFixedSection struct {
	ID   string
	Body string
}

// BriefFixedSections は J3 ブリーフの固定の節の雛形を、差し込む順序のまま
// 返す（§J3「固定の節を…雛形から差し込んで、ブリーフを組み立てる」）。
// 本チケットは雛形の中身と置き場だけを用意し、実際の見出しの組み立て・
// 差し込みは後続チケット（S2 の J3 実装）が行う。
func BriefFixedSections() ([]BriefFixedSection, error) {
	fsys, err := promptsSubFS()
	if err != nil {
		return nil, fmt.Errorf("invoker: prompts fs: %w", err)
	}
	sections := make([]BriefFixedSection, 0, len(briefSectionOrder))
	for _, s := range briefSectionOrder {
		b, err := fs.ReadFile(fsys, s.File)
		if err != nil {
			return nil, fmt.Errorf("invoker: read brief fixed section %s: %w", s.ID, err)
		}
		sections = append(sections, BriefFixedSection{ID: s.ID, Body: string(b)})
	}
	return sections, nil
}

// CheckPromptSizes は fsys（prompts/ 直下をルートとする fs.FS。本物の embed
// （promptsSubFS）でもフィクスチャ（fstest.MapFS）でも同じ関数を通す）が、
// 判断点ごとの指示文とブリーフの固定の節の雛形の分量の上限
// （§指示文の歯止め）を守っているかを検査する。
//
// 次のいずれでもエラーを返す（fail-closed。空回り防止）:
//   - 判断点ごとの指示文が上限を超える
//   - ブリーフの固定の節の雛形の合計が上限を超える
//   - 上限表にある判断点ファイル・固定の節ファイルが1つでも欠けている
//   - 上限表・固定の節の一覧に無いファイルが混入している
func CheckPromptSizes(fsys fs.FS) error {
	expectedBrief := make(map[string]bool, len(briefSectionOrder))
	for _, s := range briefSectionOrder {
		expectedBrief[s.File] = true
	}

	seenJudgment := map[string]bool{}
	seenBrief := map[string]bool{}
	var briefTotal int64

	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case judgmentPromptLimit[path] > 0:
			seenJudgment[path] = true
			if info.Size() > int64(judgmentPromptLimit[path]) {
				return fmt.Errorf("invoker: %s is %d bytes, exceeds the limit of %d bytes", path, info.Size(), judgmentPromptLimit[path])
			}
		case expectedBrief[path]:
			seenBrief[path] = true
			briefTotal += info.Size()
		default:
			return fmt.Errorf("invoker: unexpected prompt file %q (not in the size limit table)", path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	var missing []string
	for f := range judgmentPromptLimit {
		if !seenJudgment[f] {
			missing = append(missing, f)
		}
	}
	for f := range expectedBrief {
		if !seenBrief[f] {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		// self-review 指摘（round1, code-reviewer CONFIRMED）: judgmentPromptLimit・
		// expectedBrief はマップであり、range の順序は実行のたびに変わる。
		// エラーメッセージの再現性のためソートする（検査結果そのものには
		// 影響しないが、CI ログの diff が意味なく変わるのを防ぐ）。
		sort.Strings(missing)
		return fmt.Errorf("invoker: missing expected prompt file(s): %s", strings.Join(missing, ", "))
	}

	if briefTotal > briefFixedSectionsTotalLimit {
		return fmt.Errorf("invoker: brief fixed sections total %d bytes, exceeds the limit of %d bytes", briefTotal, briefFixedSectionsTotalLimit)
	}
	return nil
}

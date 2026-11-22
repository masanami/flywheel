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
// 【決定 2026-09-28 親（#84）】Instructions・BuildStdin を呼んで標準入力を
// 組み立てるのは internal/invoker.Launcher.InvokeJudgment 自身である
// （launch.go の buildJudgmentStdin）。core.RunJudgmentInput／
// JudgmentLaunchInput は「判断点の種類」（Judgment）と「データの区画の一覧」
// （Sections []core.JudgmentDataSection。ラベルと本文の組）だけを invoker へ
// 渡し、invoker が in.Judgment から Instructions(j) を読み、
// core.JudgmentDataSection を DataSection へ写して BuildStdin(instructions,
// sections) を呼ぶ。この形なら CLAUDE.md の import の向きの規約
// （internal/cli が internal/invoker を import してよいのは New・
// NewLauncher の組み立てと ErrClaudeNotFound の写像だけ・internal/core は
// internal/invoker を import しない）をどちらも変えずに済む。呼び出し元
// （#84 の J1・将来の J2 以降）は Sections を組み立てて RunJudgmentInput へ渡す
// だけでよい。Sections が空のとき（#81 時点の呼び出し元・テストとの互換）は
// 既存の Stdin をそのまま使う経路を残す（buildJudgmentStdin 参照）。

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
	core.JudgmentJ6: "j6.md",
	core.JudgmentJ8: "j8.md",
}

// judgmentPromptLimit は判断点ごとの指示文の分量の上限（バイト。UTF-8）
// （J1: 4KiB・J2: 13312B（13KiB）・J3: 8KiB・J4: 4KiB・J5: 11717B・J6: 6KiB・J8: 6KiB。
// J5 は #466 で 8KiB、#565 で 9KiB、#563 で 9.5KiB、#654 で 10.5KiB、#629 で 11354B（#654 の 10752 に、#629 で j5.md へ足した 602 バイトを足した値）へ引き上げた。
// J5 は core の見直し（区切り S3a）で、11354 に、j5.md へ足した区画「委譲先が値で返したこと」の説明の 363 バイトを足した値へ引き上げた。
// J5 は S3b のタスクの検証で、j5.md へ足した区画「対応する取り込み元」の説明とタスクの検証の扱いの 629 バイトを足した 12346 へ引き上げた。
// J5 は S3b の課題の単位の確かめで、j5.md へ足した課題の単位の確かめの扱い（692 バイト）を足した 13038 へ引き上げた。
// J5 は S3c のコンフリクトの解消の確かめで、j5.md へ足した節（746 バイト）を足した 13784 へ引き上げた。
// J5 は S3c のコンフリクトの解消の確かめの入力の区画の名前を j5.md へ足した（689 バイト）ので、14473 へ引き上げた。
// J2 は S3b の完了の差し戻しで、j2.md へ足した区画「人間の成果物の差し戻し」の説明（287 バイト）を足した 13599 へ引き上げた。
// J2 は S3b の計画の作り直しで、j2.md へ足した区画「課題の単位の確かめの指摘」「済みのタスク」の説明（区画の説明と、作り直し全体で済みのタスクをやり直さない旨の 1 文の計 579 バイト。完了の差し戻しの区画にあった同じ旨の文は、この 1 文へ整理した）を足した 14178 へ引き上げた。
// J2 は S3b の仕上げ 3 で、j2.md へ足した、統合先を使う計画のタスクのリポジトリを 1 つにそろえる旨（219 バイト）を足した 14397 へ引き上げた。
// J2 は core の見直し（区切り S2a。flywheel#749）で、12288 に、j2.md へ足した区画「問いと回答」の説明の 194 バイトを足した値へ引き上げた。
// J2 は S2b の取り込み元の子の区画と source の書き方（j2.md へ 511 バイト）で、13312（13KiB）へ引き上げた。
// J2 は S4c-1 の「足す」の形の呼び出しで、j2.md へ足した節（825 バイト）に、歯止めの余白 300 バイトを足した 15221 へ引き上げた。
// J2 は S4c-1 の仕上げで、j2.md へ足した上限と理由・expand_after の書き方・「足す」の形の規律（794 バイト）に、歯止めの余白 300 バイトを足した 16315 へ引き上げた（足した節の分だけ）。
// 仕様は docs/features/m3-invoker-delegation.md §指示文の歯止め）。
var judgmentPromptLimit = map[string]int{
	"j1.md": 4096,
	"j2.md": 16315,
	"j3.md": 8192,
	"j4.md": 4096,
	"j5.md": 14473,
	"j6.md": 6144,
	"j8.md": 8192,
	// chat.md は対話の欄の実行エンジンの追加の指示（判断点ではない）。
	"chat.md": 4096,
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

// resumePromptFile は `--resume` の再開で渡す固定の文面の雛形（種類ごと）。
// 分量の上限は resumePromptLimit（バイト）。
var resumePromptFile = map[core.ResumeKind]string{
	core.ResumeKindAnswer:      "resume/answer.md",
	core.ResumeKindInterrupted: "resume/interrupted.md",
	core.ResumeKindBudget:      "resume/budget.md",
	core.ResumeKindRework:      "resume/rework.md",
}

const resumePromptLimit = 2048

// ResumePrompt は kind の再開の固定の文面を、embed から読んで返す。
func ResumePrompt(kind core.ResumeKind) (string, error) {
	file, ok := resumePromptFile[kind]
	if !ok {
		return "", fmt.Errorf("invoker: unknown resume kind %q", kind)
	}
	fsys, err := promptsSubFS()
	if err != nil {
		return "", fmt.Errorf("invoker: prompts fs: %w", err)
	}
	b, err := fs.ReadFile(fsys, file)
	if err != nil {
		return "", fmt.Errorf("invoker: read resume prompt %s: %w", kind, err)
	}
	return string(b), nil
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

// chatPromptFile は対話の欄の実行エンジンへ渡す指示文のファイル名。
const chatPromptFile = "chat.md"

// ChatInstructions は対話の欄の指示文を、embed から読んで返す。
func ChatInstructions() (string, error) {
	fsys, err := promptsSubFS()
	if err != nil {
		return "", fmt.Errorf("invoker: prompts fs: %w", err)
	}
	b, err := fs.ReadFile(fsys, chatPromptFile)
	if err != nil {
		return "", fmt.Errorf("invoker: read chat instructions: %w", err)
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

	expectedResume := map[string]bool{}
	for _, f := range resumePromptFile {
		expectedResume[f] = true
	}
	seenResume := map[string]bool{}
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
		case expectedResume[path]:
			seenResume[path] = true
			if info.Size() > resumePromptLimit {
				return fmt.Errorf("invoker: %s is %d bytes, exceeds the limit of %d bytes", path, info.Size(), resumePromptLimit)
			}
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
	for f := range expectedResume {
		if !seenResume[f] {
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

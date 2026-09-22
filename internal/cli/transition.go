package cli

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/masanami/flywheel/internal/core"
)

// runClassify は `flywheel classify <ID> --priority <P0|P1|P2>` の実装（T2）。
// 閉集合の検査・遷移の可否・作業ログの記録は core が持つ（core.ClassifyChallenge）。
func runClassify(a Args) (any, error) {
	c, err := a.Store.ClassifyChallenge(context.Background(), core.ChannelCLI, a.Positional[0], core.ClassifyInput{
		Priority: a.Values["priority"],
	})
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: map[string]any{"challenge": challengeJSON(*c)}, text: challengeText(*c)}, nil
}

// runPlan は `flywheel plan <ID> (--file <path> | --stdin)` の実装（T3・T4）。
// --file はファイルを読んで本文にする（パスが無ければ usage_error。他の
// 入出力の失敗は internal_error）。--stdin は標準入力を全部読む。ちょうど 1 つの指定は commands.go の
// OneOfGroups が既に保証している。
func runPlan(a Args) (any, error) {
	body, cliErr := readPlanBody(a)
	if cliErr != nil {
		return nil, cliErr
	}

	c, plan, err := a.Store.PlanChallenge(context.Background(), core.ChannelCLI, a.Positional[0], core.PlanInput{Body: body})
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: map[string]any{"challenge": challengeJSON(*c), "plan": planJSON(*plan)},
		text: challengeText(*c),
	}, nil
}

// readPlanBody は計画の本文を --stdin または --file から読む。--file の
// パスが存在しない場合だけ usage_error（引数の誤り）。それ以外の入出力の失敗
// （読み取り中のエラー・権限・ディレクトリ指定など）は「想定外の失敗」として
// internal_error にする（エラーコード表の意味に合わせる。code-reviewer 指摘）。
// いずれも終了コードは 2。
func readPlanBody(a Args) (string, *Error) {
	if a.Bools["stdin"] {
		data, err := io.ReadAll(a.Stdin)
		if err != nil {
			return "", NewError(CodeInternalError, "標準入力の読み取りに失敗しました: "+err.Error())
		}
		return string(data), nil
	}
	path := a.Values["file"]
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", NewError(CodeUsageError, "--file が見つかりません: "+err.Error())
		}
		return "", NewError(CodeInternalError, "--file の読み取りに失敗しました: "+err.Error())
	}
	return string(data), nil
}

// runSubmit は `flywheel submit <ID>` の実装（T7）。操作固有の入力は無い。
func runSubmit(a Args) (any, error) {
	c, err := a.Store.SubmitChallenge(context.Background(), core.ChannelCLI, a.Positional[0])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: map[string]any{"challenge": challengeJSON(*c)}, text: challengeText(*c)}, nil
}

// runVerify は `flywheel verify <ID> --result <met|not_met|uncertain> [--question <q>]`
// の実装（T8・T9・T10）。--result met/not_met に --question を添えると
// usage_error（AC-30）。それ以外の入力規則（閉集合・問いの必須）は core が持つ。
func runVerify(a Args) (any, error) {
	result := a.Values["result"]
	question, hasQuestion := a.Values["question"]

	if hasQuestion {
		if r, ok := core.ParseVerifyResult(result); ok && r != core.VerifyResultUncertain {
			return nil, NewError(CodeUsageError, "--result met / --result not_met と --question は同時に指定できません")
		}
	}

	in := core.VerifyInput{Result: result}
	if hasQuestion {
		in.Question = &question
	}

	c, err := a.Store.VerifyChallenge(context.Background(), core.ChannelCLI, a.Positional[0], in)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: map[string]any{"challenge": challengeJSON(*c)}, text: challengeText(*c)}, nil
}

// runHold は `flywheel hold <ID> [--question <q>]` の実装（T11）。--question の
// 省略は空の問いとして core へ渡し、core が validation_failed にする
// （usage_error にはしない＝仕様）。
func runHold(a Args) (any, error) {
	c, err := a.Store.HoldChallenge(context.Background(), core.ChannelCLI, a.Positional[0], core.HoldInput{
		Question: a.Values["question"],
	})
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: map[string]any{"challenge": challengeJSON(*c)}, text: challengeText(*c)}, nil
}

// planJSON は core.Plan を「成功時の JSON 出力の規約」の plan オブジェクトの形へ
// 変換する（plan の単発の成功出力・plansJSON の要素と同じ形）。
func planJSON(p core.Plan) map[string]any {
	return map[string]any{
		"version":    p.Version,
		"body":       p.Body,
		"created_at": FormatTimestamp(p.CreatedAt),
	}
}

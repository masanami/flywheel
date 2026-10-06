// Package cli は flywheel CLI の共通層である。コマンドの宣言的な定義・
// フラグと位置引数の解析・JSON／テキストでの出力・終了コードとエラーコードの
// 写像を持つ。core（状態機械・承認・作業ログ）の規則はここに書かない
// （docs/features/m1-core.md 「Go のモジュール構成」）。
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// Run は cmd/flywheel/main.go から呼ばれる唯一の入口。
// os.Exit にそのまま渡せる終了コード（0/1/2）を返す。
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return run(args, stdin, stdout, stderr, defaultCommands())
}

// run は Run の内部実装。commands を外から差し込めるようにして、
// テストからテスト専用コマンドを注入できるようにする
// （成功経路 AC-72 の検証は、この関数へ直接テスト用のコマンド表を渡して行う）。
func run(rawArgs []string, stdin io.Reader, stdout, stderr io.Writer, commands []Command) int {
	jsonMode := scanJSONFlag(rawArgs)

	if len(rawArgs) > 0 && isHelpInvocation(rawArgs[0]) {
		_, _ = fmt.Fprint(stdout, usageText)
		return 0
	}

	// 委譲の子の環境（core.DelegatedRunEnvVar が、空文字でも設定されている）からは、`help` を
	// 除くすべてのコマンドを、コマンドの照合より前に拒否する（読み取りも含む。fail-closed）。
	// 値はメッセージに出すだけで、判定には使わない。
	if runID, delegated := os.LookupEnv(core.DelegatedRunEnvVar); delegated {
		return failErr(stderr, jsonMode, NewError(CodeVerificationRejected, fmt.Sprintf(
			"委譲の子の環境（%s=%q）からは flywheel のコマンドを実行できない", core.DelegatedRunEnvVar, runID)))
	}

	if len(rawArgs) == 0 {
		return failUsage(stderr, jsonMode, "コマンドを指定してください")
	}

	cmd, rest, ok := matchCommand(rawArgs, commands)
	if !ok {
		return failUsage(stderr, jsonMode, fmt.Sprintf("未知のコマンド: %s", strings.Join(rawArgs, " ")))
	}

	parsed, cliErr := parseArgs(rest, cmd)
	if cliErr != nil {
		return failErr(stderr, jsonMode, cliErr)
	}
	parsed.Stdin = stdin

	// 解析が成功した後は解析結果を正とする（事前走査はフラグの値としての
	// "--json" も拾うため、解析に失敗したときの出力形式の判定にだけ使う）。
	jsonMode = parsed.Bools["json"]

	// RequiresStore なコマンドは、Run を呼ぶ前にワークスペースのストアを開く。
	// これにより init を含む全コマンドが store_not_found・store_too_new・
	// store_busy・store_error を一様に返せる（Run 自体がまだスタブでも、この
	// 事前チェックだけは適用される）。
	if cmd.RequiresStore {
		store, storeErr := core.OpenWorkspace(parsed.Values["workspace"])
		if storeErr != nil {
			return failErr(stderr, jsonMode, mapCoreErr(storeErr))
		}
		defer func() { _ = store.Close() }()
		parsed.Store = store
	}

	data, err := cmd.Run(parsed)
	if err != nil {
		var asErr *Error
		matched := errors.As(err, &asErr)
		switch {
		case matched && asErr != nil:
			return failErr(stderr, jsonMode, asErr)
		case matched:
			// err の動的型は *Error だが値そのものが nil（型付き nil）。
			// コマンド実装の誤りであり、err.Error() の呼び出しは nil レシーバで
			// panic するため、固定メッセージで internal_error にする。
			return failErr(stderr, jsonMode, NewError(CodeInternalError, "command returned a nil *cli.Error"))
		default:
			return failErr(stderr, jsonMode, NewError(CodeInternalError, err.Error()))
		}
	}

	return succeed(stdout, stderr, jsonMode, data)
}

func isHelpInvocation(first string) bool {
	return first == "--help" || first == "-h" || first == "help"
}

// scanJSONFlag は引数の解釈（フラグ解析）が成功するかどうかに関わらず、
// --json の有無を事前に判定する。フラグ解析に失敗した場合でも、
// --json が引数中にあれば JSON でエラーを出すため。
func scanJSONFlag(args []string) bool {
	for _, a := range args {
		if a == "--json" {
			return true
		}
		if strings.HasPrefix(a, "--json=") {
			return strings.TrimPrefix(a, "--json=") != "false"
		}
	}
	return false
}

// matchCommand は rawArgs の先頭トークンから登録済みコマンドを探す。
// `op add` のような 2 トークンのコマンドを 1 トークンのコマンドより優先して
// 照合する。
func matchCommand(rawArgs []string, commands []Command) (Command, []string, bool) {
	if len(rawArgs) >= 2 {
		for _, c := range commands {
			if len(c.Path) == 2 && c.Path[0] == rawArgs[0] && c.Path[1] == rawArgs[1] {
				return c, rawArgs[2:], true
			}
		}
	}
	for _, c := range commands {
		if len(c.Path) == 1 && c.Path[0] == rawArgs[0] {
			return c, rawArgs[1:], true
		}
	}
	return Command{}, nil, false
}

// parseArgs はコマンド固有のフラグ・共通フラグを、位置引数の前後どちらに
// あっても解釈する。未知のフラグ・必須引数の欠落は usage_error にする。
func parseArgs(rawArgs []string, cmd Command) (Args, *Error) {
	allFlags := map[string]flagDef{}
	for _, f := range commonFlags {
		allFlags[f.Name] = f
	}
	for _, f := range cmd.Flags {
		allFlags[f.Name] = f
	}

	values := map[string]string{}
	bools := map[string]bool{}
	var positional []string

	i := 0
	for i < len(rawArgs) {
		tok := rawArgs[i]
		if len(tok) > 1 && tok[0] == '-' {
			// 先頭のハイフンは 1 つか 2 つだけを受け付ける（`---json` は未知のフラグ）。
			name := strings.TrimPrefix(strings.TrimPrefix(tok, "-"), "-")
			hasInline := false
			var inlineValue string
			if idx := strings.Index(name, "="); idx >= 0 {
				inlineValue = name[idx+1:]
				name = name[:idx]
				hasInline = true
			}
			def, ok := allFlags[name]
			if !ok {
				return Args{}, NewError(CodeUsageError, fmt.Sprintf("未知のフラグ: --%s", name))
			}
			if def.HasValue {
				var val string
				if hasInline {
					val = inlineValue
				} else {
					if i+1 >= len(rawArgs) {
						return Args{}, NewError(CodeUsageError, fmt.Sprintf("--%s には値が必要です", name))
					}
					val = rawArgs[i+1]
					i++
				}
				values[name] = val
			} else {
				bools[name] = !hasInline || inlineValue != "false"
			}
			i++
			continue
		}
		positional = append(positional, tok)
		i++
	}

	if len(positional) < cmd.MinPositional || len(positional) > cmd.MaxPositional {
		return Args{}, NewError(CodeUsageError, fmt.Sprintf("引数の数が正しくありません（%d 個必要、%d 個指定）", cmd.MinPositional, len(positional)))
	}

	for _, f := range cmd.Flags {
		if !f.Required {
			continue
		}
		if f.HasValue {
			if _, ok := values[f.Name]; !ok {
				return Args{}, NewError(CodeUsageError, fmt.Sprintf("--%s は必須です", f.Name))
			}
		} else if !bools[f.Name] {
			return Args{}, NewError(CodeUsageError, fmt.Sprintf("--%s は必須です", f.Name))
		}
	}

	for _, group := range cmd.OneOfGroups {
		count := 0
		for _, name := range group {
			if _, ok := values[name]; ok {
				count++
			} else if bools[name] {
				count++
			}
		}
		if count != 1 {
			names := make([]string, len(group))
			for i, n := range group {
				names[i] = "--" + n
			}
			return Args{}, NewError(CodeUsageError, fmt.Sprintf("%s のうちちょうど 1 つを指定してください", strings.Join(names, " / ")))
		}
	}

	return Args{Positional: positional, Values: values, Bools: bools}, nil
}

func failErr(stderr io.Writer, jsonMode bool, cliErr *Error) int {
	if jsonMode {
		if err := writeJSONError(stderr, cliErr.Code, cliErr.Message); err != nil {
			// 固定形の envelope のエンコードは通常失敗しない。最後の砦としてテキストへ。
			writeTextError(stderr, cliErr.Code, cliErr.Message)
		}
	} else {
		writeTextError(stderr, cliErr.Code, cliErr.Message)
	}
	return ExitCodeFor(cliErr.Code)
}

func failUsage(stderr io.Writer, jsonMode bool, message string) int {
	return failErr(stderr, jsonMode, NewError(CodeUsageError, message))
}

func succeed(stdout, stderr io.Writer, jsonMode bool, data any) int {
	if to, ok := data.(textOutput); ok {
		if !jsonMode {
			_, _ = fmt.Fprint(stdout, to.text)
			return 0
		}
		data = to.json
	}
	if !jsonMode {
		if data == nil {
			_, _ = fmt.Fprintln(stdout, "ok")
		} else {
			_, _ = fmt.Fprintf(stdout, "%v\n", data)
		}
		return 0
	}
	if data == nil {
		data = map[string]any{}
	}
	if err := writeJSONSuccess(stdout, data); err != nil {
		// data が最上位オブジェクトへマーシャルできない場合はコマンド実装の誤りであり、
		// 呼び出し側からは internal_error として観測させる。
		return failErr(stderr, true, NewError(CodeInternalError, err.Error()))
	}
	return 0
}

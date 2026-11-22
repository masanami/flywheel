// Package cli は flywheel CLI の共通層である。コマンドの宣言的な定義・
// フラグと位置引数の解析・JSON／テキストでの出力・終了コードとエラーコードの
// 写像を持つ。core（状態機械・承認・作業ログ）の規則はここに書かない
// （docs/features/m1-core.md 「Go のモジュール構成」）。
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/server"
)

// Run は cmd/flywheel/main.go から呼ばれる唯一の入口。
// os.Exit にそのまま渡せる終了コード（0/1/2）を返す。
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runWithDeps(args, stdin, stdout, stderr, defaultCommands(), runDeps{CycleStart: server.StartCycle})
}

// run は Run の内部実装。commands を外から差し込めるようにして、
// テストからテスト専用コマンドを注入できるようにする
// （成功経路 AC-72 の検証は、この関数へ直接テスト用のコマンド表を渡して行う）。
func run(rawArgs []string, stdin io.Reader, stdout, stderr io.Writer, commands []Command) int {
	return runWithLookPath(rawArgs, stdin, stdout, stderr, commands, nil)
}

// runWithLookPath は run に gh・claude の実行ファイルの探し方を渡せる形（nil なら exec.LookPath＝PATH 上）。
// テストが、プロセス全体の PATH を書き換えずに、テストごとの偽の gh・claude を使うために使う。
func runWithLookPath(rawArgs []string, stdin io.Reader, stdout, stderr io.Writer, commands []Command, lookPath lookPathFunc) int {
	return runWithDeps(rawArgs, stdin, stdout, stderr, commands, runDeps{LookPath: lookPath})
}

// runDeps は run に外から渡す、環境に触れる口。ゼロ値は、gh・claude を PATH から探し、サイクルを切り離して始められない扱い。
type runDeps struct {
	LookPath   lookPathFunc
	CycleStart server.CycleStarter
}

func runWithDeps(rawArgs []string, stdin io.Reader, stdout, stderr io.Writer, commands []Command, deps runDeps) int {
	lookPath := deps.LookPath
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

	// 対話の欄の印がある環境からは、道具の範囲が full でなければ、欄から受けるコマンドの一覧にあるコマンドだけを受ける
	// （委譲の子の印の検査の後。名前の照合は全体のフラグを除いた後）。
	chat := chatMarkFromEnv(rawArgs)
	needChatMark, chatKnd, gateErr := chatGate(chat, rawArgs)
	if gateErr != nil {
		return failErr(stderr, jsonMode, gateErr)
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
	parsed.Stderr = stderr
	parsed.Stdout = stdout
	parsed.LookPath = lookPath
	parsed.Chat = chat
	// 道具の範囲は、解析が決めたワークスペース（コマンドが実際に開くもの）の宣言で決め直し、一覧の検査をかけ直す
	// （事前の走査と解析で読み方が食い違っても、restricted の宣言の検査を外せないようにする）。
	if chat != nil {
		chat.Tools = chatToolsForWorkspace(parsed.Values["workspace"])
		need, kind, regateErr := chatGate(chat, rawArgs)
		if regateErr != nil {
			return failErr(stderr, jsonMode, regateErr)
		}
		needChatMark, chatKnd = need, kind
	}
	parsed.CycleStart = deps.CycleStart

	// 解析が成功した後は解析結果を正とする（事前走査はフラグの値としての
	// "--json" も拾うため、解析に失敗したときの出力形式の判定にだけ使う）。
	jsonMode = parsed.Bools["json"]

	if chat != nil && chatKnd == chatKindCycle {
		need, cycleErr := chatGateCycle(chat, parsed.Bools["detach"])
		if cycleErr != nil {
			return failErr(stderr, jsonMode, cycleErr)
		}
		needChatMark = need
	}
	if chat != nil && chatKnd == chatKindIngest {
		need, ingestErr := chatGateIngest(chat, len(parsed.Positional))
		if ingestErr != nil {
			return failErr(stderr, jsonMode, ingestErr)
		}
		needChatMark = need
	}

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
		// 記録するコマンドには、印の検査をかける（印の会話が開いている・最後のオーナーの発言・返事が進行中）。
		// 本人確認を通る操作は、core が操作の中でも同じ検査を行う。
		if needChatMark {
			if err := checkChatMark(parsed); err != nil {
				var asErr *Error
				if errors.As(err, &asErr) && asErr != nil {
					return failErr(stderr, jsonMode, asErr)
				}
				return failErr(stderr, jsonMode, NewError(CodeInternalError, err.Error()))
			}
		}
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

	// 操作が成立した後で、サイクルを始めるかを決めて始める。結果は操作の出力に足すだけで、終了コードは変えない。
	if cmd.StartsCycle != nil {
		if trigger := cmd.StartsCycle(parsed); trigger != "" {
			cyc := server.StartCycleAfterOperation(context.Background(), parsed.Store, trigger, parsed.Bools["no-cycle"], parsed.CycleStart)
			data = withCycleOutput(data, cyc)
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
	if cmd.StartsCycle != nil {
		allFlags[noCycleFlag.Name] = noCycleFlag
	}

	values := map[string]string{}
	multi := map[string][]string{}
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
				if def.Repeatable {
					multi[name] = append(multi[name], val)
				}
			} else {
				bools[name] = !hasInline || inlineValue != "false"
			}
			i++
			continue
		}
		positional = append(positional, tok)
		i++
	}

	if len(positional) < cmd.MinPositional || (cmd.MaxPositional != UnlimitedPositional && len(positional) > cmd.MaxPositional) {
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

	return Args{Positional: positional, Values: values, Multi: multi, Bools: bools}, nil
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
	if cliErr.ExitCode != 0 {
		return cliErr.ExitCode
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

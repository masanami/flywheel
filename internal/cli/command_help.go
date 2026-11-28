package cli

import (
	"strings"
)

// commandHelp は `flywheel <コマンド> --help`（`-h` も）の使い方の文を返す。flow のコマンドは flowHelp が扱う。
// 使い方を求める呼び出しでなければ ok は偽。必須の引数が欠けていても、ほかのフラグがあっても、使い方を優先する。
// 文は usageText（`flywheel --help` の一覧）のそのコマンドの項目をそのまま使う。
func commandHelp(rawArgs []string, commands []Command) (string, bool) {
	if text, ok := flowHelp(rawArgs); ok {
		return text, true
	}
	if len(rawArgs) == 0 {
		return "", false
	}
	cmd, rest, ok := matchCommand(rawArgs, commands)
	if !ok || !asksHelp(rest, cmd) {
		return "", false
	}
	entries := commandUsageEntries(cmd, commands)
	if len(entries) == 0 {
		return "", false
	}
	return renderHelp("flywheel "+strings.Join(cmd.Path, " "), entries), true
}

// commandUsageEntries は usageText の cmd の項目を集める。項目がどのコマンドのものかは、引数の解釈と同じ
// matchCommand で決める（`cycle status` の行は `cycle` の項目にしない）。
func commandUsageEntries(cmd Command, commands []Command) []string {
	path := strings.Join(cmd.Path, " ")
	return usageEntries(func(item string) bool {
		got, _, ok := matchCommand(strings.Fields(item), commands)
		return ok && strings.Join(got.Path, " ") == path
	})
}

// usageContinuation は usageText の項目の続きの行の字下げ。
const usageContinuation = "            "

// usageEntries は usageText の項目（字下げ 2 つの行と、その続きの字下げした行）のうち、
// match が真を返すものを集める。match には項目の 1 行目を字下げを除いて渡す。
func usageEntries(match func(item string) bool) []string {
	var out []string
	in := false
	for _, line := range strings.Split(usageText, "\n") {
		if item, ok := strings.CutPrefix(line, "  "); ok && !strings.HasPrefix(item, " ") {
			in = match(item)
			if in {
				out = append(out, line)
			}
			continue
		}
		if in && strings.HasPrefix(line, usageContinuation) {
			out = append(out, line)
			continue
		}
		in = false
	}
	return out
}

func isHelpFlag(tok string) bool { return tok == "--help" || tok == "-h" }

// asksHelp は引数に --help か -h があるかを見る。値を取るフラグの次の語は値なので見ない（`--note --help` は助けを求めていない）。
func asksHelp(args []string, cmd Command) bool {
	hasValue := map[string]bool{}
	for _, f := range append(append([]flagDef{}, commonFlags...), cmd.Flags...) {
		hasValue[f.Name] = f.HasValue
	}
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if isHelpFlag(tok) {
			return true
		}
		if len(tok) > 1 && tok[0] == '-' && !strings.Contains(tok, "=") {
			if hasValue[strings.TrimPrefix(strings.TrimPrefix(tok, "-"), "-")] {
				i++
			}
		}
	}
	return false
}

// commonFlagsUsage は usageText の「共通フラグ:」の節の行。
func commonFlagsUsage() []string {
	var out []string
	in := false
	for _, line := range strings.Split(usageText, "\n") {
		switch {
		case line == "共通フラグ:":
			in = true
		case in && strings.TrimSpace(line) == "":
			return out
		case in:
			out = append(out, line)
		}
	}
	return out
}

// renderHelp は title の使い方の文を組み立てる。entries の項目の 1 行目には flywheel を添える。
func renderHelp(title string, entries []string) string {
	var b strings.Builder
	b.WriteString(title + " - 使い方\n\n")
	for _, e := range entries {
		if !strings.HasPrefix(e, usageContinuation) {
			e = "  flywheel " + strings.TrimPrefix(e, "  ")
		}
		b.WriteString(e + "\n")
	}
	b.WriteString("\n共通フラグ:\n")
	for _, l := range commonFlagsUsage() {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n一覧は flywheel --help を参照。\n")
	return b.String()
}

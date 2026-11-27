package cli

import (
	"strings"
)

// flowHelp は `flywheel flow --help`・`flywheel flow <サブコマンド> --help`（`-h` も）の使い方の文を返す。
// 使い方を求める呼び出しでなければ ok は偽。文は usageText（`flywheel --help` の一覧）の flow の行をそのまま使い、
// 一覧と食い違わないようにする。一覧に載せていないサブコマンド（内部の入口）は扱わない。
func flowHelp(rawArgs []string) (string, bool) {
	if len(rawArgs) < 2 || rawArgs[0] != "flow" {
		return "", false
	}
	if isHelpFlag(rawArgs[1]) {
		return renderFlowHelp("flywheel flow", flowUsageEntries("")), true
	}
	var cmd *Command
	for _, c := range flowCommands() {
		if c.Path[1] == rawArgs[1] {
			c := c
			cmd = &c
			break
		}
	}
	if cmd == nil {
		return "", false
	}
	entries := flowUsageEntries(rawArgs[1])
	if len(entries) == 0 || !asksHelp(rawArgs[2:], *cmd) {
		return "", false
	}
	return renderFlowHelp("flywheel flow "+rawArgs[1], entries), true
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

// flowUsageEntries は usageText の flow の項目（`  flow <サブコマンド> …` の行と、その続きの字下げした行）を集める。
// sub が空ならすべて、そうでなければそのサブコマンドの項目だけ。
func flowUsageEntries(sub string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(usageText, "\n") {
		if rest, ok := strings.CutPrefix(line, "  flow "); ok {
			name, _, _ := strings.Cut(rest, " ")
			in = sub == "" || name == sub
			if in {
				out = append(out, line)
			}
			continue
		}
		if in && strings.HasPrefix(line, "            ") {
			out = append(out, line)
			continue
		}
		in = false
	}
	return out
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

func renderFlowHelp(title string, entries []string) string {
	var b strings.Builder
	b.WriteString(title + " - 使い方\n\n")
	for _, e := range entries {
		if strings.HasPrefix(e, "  flow ") {
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

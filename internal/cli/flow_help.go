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
		return renderHelp("flywheel flow", flowUsageEntries("")), true
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
	return renderHelp("flywheel flow "+rawArgs[1], entries), true
}

// flowUsageEntries は usageText の flow の項目を集める。sub が空ならすべて、そうでなければそのサブコマンドの項目だけ。
func flowUsageEntries(sub string) []string {
	return usageEntries(func(item string) bool {
		rest, ok := strings.CutPrefix(item, "flow ")
		if !ok {
			return false
		}
		name, _, _ := strings.Cut(rest, " ")
		return sub == "" || name == sub
	})
}

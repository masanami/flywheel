package invoker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// buildChatArgs は対話の欄の実行エンジンの起動の引数を組み立てる。判断の呼び出しの引数（buildArgs）にならい、
// 違いは次のとおり。
//
//   - 出力は "--output-format stream-json --verbose"（--verbose が無いと stream-json は始まらない。発言の途中の
//     断片を流す --include-partial-messages は付けない）。
//   - "--max-turns" にターン数の上限を渡す。
//   - restricted: 許す道具は Read・Grep・Glob・Bash(flywheel:*) の 4 つだけ、禁じる道具は Edit・Write・
//     NotebookEdit・WebFetch・WebSearch・Task。許可の扱いは判断の呼び出しと同じ default（尋ねる相手がいないので、
//     許していない道具は断られる）。書き込む・実行するシェルのコマンドを断るのは、この許可の設定だけが頼り
//     （読み取りだけと見なされるコマンドは、実行エンジンが尋ねずに通す）。"--setting-sources" に空を渡し、利用者の設定の
//     許可が加わらないようにする。
//   - full: 許す道具・禁じる道具の指定を付けず、許可の扱いはすべて許す。
//   - 指示文を "--append-system-prompt" で追加の指示に入れる。
//
// オーナーの発言・いま見ているものは引数に現れない（標準入力で渡す）。
func buildChatArgs(in core.ChatLaunchInput, instructions string) []string {
	sessionFlag := "--session-id"
	if in.Resume {
		sessionFlag = "--resume"
	}
	args := []string{
		"-p",
		sessionFlag, in.SessionID,
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", strconv.Itoa(in.MaxTurns),
	}
	if in.Tools == core.ChatToolsFull {
		args = append(args, "--permission-mode", "bypassPermissions")
	} else {
		args = append(args,
			"--allowedTools", "Read", "Grep", "Glob", "Bash(flywheel:*)",
			"--disallowedTools", "Edit", "Write", "NotebookEdit", "WebFetch", "WebSearch", "Task",
			"--permission-mode", "default",
			// 設定の出どころを空にする。利用者の設定（~/.claude など）の許可が上の許可に加わらないようにする。
			"--setting-sources", "",
		)
	}
	return append(args, "--append-system-prompt", instructions)
}

// chatViewingSectionLabel は「いま見ているもの」のデータの区画の名前。
const chatViewingSectionLabel = "いま見ているもの"

// buildChatStdin は対話の実行エンジンの標準入力を組み立てる: 道具の範囲・いま見ているもの（外部由来の題名を含む
// ので、データの区画に入れる）・オーナーの発言の順。
func buildChatStdin(in core.ChatLaunchInput) []byte {
	var b strings.Builder
	tools := in.Tools
	if tools != core.ChatToolsFull {
		tools = core.ChatToolsRestricted
	}
	fmt.Fprintf(&b, "道具の範囲: %s\n\n", tools)
	if v := in.Viewing; v != nil && (v.ChallengeID != "" || v.TaskID != "" || v.Title != "" || v.TaskTitle != "") {
		var c strings.Builder
		if v.ChallengeID != "" || v.Title != "" {
			fmt.Fprintf(&c, "課題: %s %s\n", v.ChallengeID, v.Title)
		}
		if v.TaskID != "" || v.TaskTitle != "" {
			fmt.Fprintf(&c, "タスク: %s %s\n", v.TaskID, v.TaskTitle)
		}
		b.WriteString(dataSectionGuardNote)
		b.WriteString("\n\n")
		b.WriteString(WrapDataSection(chatViewingSectionLabel, c.String()))
		b.WriteString("\n")
	}
	b.WriteString("# オーナーの発言\n\n")
	b.WriteString(in.Text)
	b.WriteString("\n")
	return []byte(b.String())
}

package invoker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// buildChatArgs は返事ごとに起動する実行エンジンの起動の引数を組み立てる。判断の呼び出しの引数（buildArgs）にならい、
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
	args := []string{
		"-p",
		chatSessionFlag(in.Resume), in.SessionID,
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

// buildChatStdin は返事ごとに起動する実行エンジンの標準入力を組み立てる。
func buildChatStdin(in core.ChatLaunchInput) []byte {
	return []byte(chatUtteranceText(in.Text, in.Viewing))
}

// chatOwnerHeading は、実行エンジンへ送る文の中で、オーナーの発言の前に置く見出し。
const chatOwnerHeading = "# オーナーの発言\n\n"

// chatUtteranceText は実行エンジンへ送る文を組み立てる: いま見ているもの（外部由来の題名を含むので、データの
// 区画に入れる。無ければ省く）・オーナーの発言の順。全文の読み取りは、この形からオーナーの発言を取り出す
// （ownerUtterance）。
func chatUtteranceText(text string, v *core.ChatViewing) string {
	var b strings.Builder
	if v != nil && (v.ChallengeID != "" || v.TaskID != "" || v.Title != "" || v.TaskTitle != "") {
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
	b.WriteString(chatOwnerHeading)
	b.WriteString(text)
	b.WriteString("\n")
	return b.String()
}

// ownerUtterance は chatUtteranceText の形の文からオーナーの発言だけを取り出す。その形でなければ、そのまま返す。
// 区画の中の区切りに見える行は無害にしてあるので、最初の区画の終わりの行が、いま見ているものの区画の終わりになる。
func ownerUtterance(sent string) string {
	rest := sent
	if strings.HasPrefix(sent, dataSectionGuardNote+"\n\n"+dataSectionBeginPrefix+chatViewingSectionLabel+dataSectionBeginSuffix+"\n") {
		_, after, found := strings.Cut(sent, "\n"+dataSectionEnd+"\n\n")
		if !found {
			return sent
		}
		rest = after
	}
	body, ok := strings.CutPrefix(rest, chatOwnerHeading)
	if !ok {
		return sent
	}
	return strings.TrimSuffix(body, "\n")
}

// buildConversationArgs は会話ごとに起動したまま続ける実行エンジンの起動の引数を組み立てる。入力も出力も
// stream-json（--verbose が無いと出力は始まらない）で、許可の確認と問いは標準入出力で呼び出し元へ渡させる。
// 許可の正本は実行エンジンの設定のファイルなので、設定の出どころ・道具の許可と禁止・許可の扱いは指定しない。
// ターン数の上限も指定しない。発言は引数に現れない（標準入力で送る）。
func buildConversationArgs(in core.ChatStartInput, instructions string) []string {
	return []string{
		"-p",
		chatSessionFlag(in.Resume), in.SessionID,
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-prompts", "host",
		"--permission-prompt-tool", "stdio",
		"--append-system-prompt", instructions,
	}
}

// chatSessionFlag は会話の識別を渡す引数の名前（続きなら --resume、新しい会話なら --session-id）。
func chatSessionFlag(resume bool) string {
	if resume {
		return "--resume"
	}
	return "--session-id"
}

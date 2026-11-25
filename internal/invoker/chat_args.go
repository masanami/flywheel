package invoker

import (
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// chatViewingSectionLabel は「いま見ているもの」のデータの区画の名前。
const chatViewingSectionLabel = "いま見ているもの"

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

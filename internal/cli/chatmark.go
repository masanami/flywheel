package cli

import (
	"context"
	"os"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは、画面の対話の欄から起動された flywheel のコマンドの扱い（docs/features/core-redesign-s4d.md
// §欄から受けるコマンドの一覧・§欄から受ける承認・回答）を持つ。CLI は印（環境変数）を読み、コマンドの名前で
// 一覧の検査をかけ、経路 chat・方式 relayed を core へ渡すだけで、印の検査の中身・承認の成立条件は core が持つ。

// chatCommandRefusal は一覧に無いコマンドを断るときの文言。
const chatCommandRefusal = "対話の欄からは、このコマンドを受けません"

// chatMark は環境の印。Tools は道具の範囲（決まった値でなければ restricted）。
type chatMark struct {
	Session, Message string
	Tools            core.ChatTools
}

// chatMarkFromEnv は環境から印を読む。会話の識別・発言の識別のどちらかが（空文字でも）設定されていれば、印がある
// ものとして扱う（半端な印も、印の検査で断る側に倒す）。
func chatMarkFromEnv() *chatMark {
	session, hasSession := os.LookupEnv(core.ChatEnvSession)
	message, hasMessage := os.LookupEnv(core.ChatEnvMessage)
	if !hasSession && !hasMessage {
		return nil
	}
	return &chatMark{Session: session, Message: message, Tools: core.ChatToolsFromEnv(os.Getenv(core.ChatEnvTools))}
}

// chatKind は欄から受けるコマンドの分け。
type chatKind int

const (
	chatKindNone   chatKind = iota // 一覧に無い
	chatKindRead                   // 読む（印の検査をかけない）
	chatKindRecord                 // 記録する・サイクル（印の検査をかける）
	// chatKindIngest は ingest。取り込み元を 1 つ以上指定した形だけを受ける（指定の有無は解析の後に見る）。
	chatKindIngest
	// chatKindCycle は cycle（status・stop 以外）。--detach が付いた形だけを受ける。--detach が値を取るフラグの値に
	// なっていないかは、解析の後に見る。
	chatKindCycle
)

// chatReadCommands・chatRecordCommands は一覧の名前（followup・cycle は次の語まで）。
var (
	chatReadCommands = [][]string{
		{"status"}, {"show"}, {"list"}, {"log"}, {"runs"}, {"metrics"}, {"version"}, {"help"},
		{"followup", "list"}, {"cycle", "status"},
	}
	chatRecordCommands = [][]string{
		{"approve"}, {"reject"}, {"answer"}, {"withdraw"}, {"budget"}, {"create"},
		{"followup", "drop"}, {"followup", "promote"}, {"followup", "done"},
		{"cycle", "stop"},
	}
)

// chatCommandWords は全体のフラグ（--json・--workspace）を除いた引数から、コマンドの名前（先頭の語と、その直後の語）
// を返す。直後がフラグなら 2 つ目は空。
func chatCommandWords(rawArgs []string) (first, second string) {
	var words []string
	for i := 0; i < len(rawArgs); i++ {
		tok := rawArgs[i]
		switch {
		case tok == "--workspace":
			i++
			continue
		case strings.HasPrefix(tok, "--workspace="), tok == "--json", strings.HasPrefix(tok, "--json="):
			continue
		case strings.HasPrefix(tok, "-") && len(tok) > 1:
			if len(words) > 0 && len(words) < 2 {
				// 先頭の語の直後がコマンド固有のフラグなら、2 つ目の語は無い。
				words = append(words, "")
			}
			continue
		}
		words = append(words, tok)
	}
	if len(words) > 0 {
		first = words[0]
	}
	if len(words) > 1 {
		second = words[1]
	}
	return first, second
}

// classifyChatCommand は rawArgs のコマンドを、欄から受けるコマンドの一覧に照らして分ける。
func classifyChatCommand(rawArgs []string) chatKind {
	first, second := chatCommandWords(rawArgs)
	has := func(list [][]string) bool {
		for _, n := range list {
			if n[0] != first {
				continue
			}
			if len(n) == 1 || n[1] == second {
				return true
			}
		}
		return false
	}
	switch {
	case has(chatReadCommands):
		return chatKindRead
	case has(chatRecordCommands):
		return chatKindRecord
	case first == "cycle":
		return chatKindCycle
	case first == "ingest":
		return chatKindIngest
	}
	return chatKindNone
}

// chatGate はコマンドの照合より前の、一覧の検査。断るなら *Error を返す。mark が nil（印が無い）なら何もしない。
// 道具の範囲が full のときは一覧の検査をかけない。戻り値の needMark は、ストアを開いた後に印の検査をかけるか。
func chatGate(mark *chatMark, rawArgs []string) (needMark bool, kind chatKind, err *Error) {
	if mark == nil {
		return false, chatKindNone, nil
	}
	kind = classifyChatCommand(rawArgs)
	if mark.Tools != core.ChatToolsFull && kind == chatKindNone {
		return false, kind, NewError(CodeVerificationRejected, chatCommandRefusal)
	}
	return kind == chatKindRecord, kind, nil
}

// chatGateCycle は解析の後に、cycle の --detach の有無で断るかと印の検査の要否を決める（--detach の無い cycle は、
// 道具の範囲が restricted のとき断る）。
func chatGateCycle(mark *chatMark, detach bool) (needMark bool, err *Error) {
	if detach {
		return true, nil
	}
	if mark.Tools != core.ChatToolsFull {
		return false, NewError(CodeVerificationRejected, chatCommandRefusal)
	}
	return false, nil
}

// chatGateIngest は解析の後に、ingest の指定の有無で断るかと印の検査の要否を決める。取り込み元を 1 つ以上指定した形だけが
// 記録するコマンド（印の検査をかける）。引数の無い形は、道具の範囲が restricted のとき断る。
func chatGateIngest(mark *chatMark, positional int) (needMark bool, err *Error) {
	if positional > 0 {
		return true, nil
	}
	if mark.Tools != core.ChatToolsFull {
		return false, NewError(CodeVerificationRejected, chatCommandRefusal)
	}
	return false, nil
}

// checkChatMark はストアを開いた後に、記録するコマンドへ印の検査をかける（core の検査）。
func checkChatMark(a Args) error {
	if a.Chat == nil || a.Store == nil {
		return nil
	}
	if err := a.Store.CheckChatMark(context.Background(), a.Chat.Session, a.Chat.Message); err != nil {
		return mapCoreErr(err)
	}
	return nil
}

// channelFor は本人確認を通る操作を core へ渡す経路。印があれば chat、無ければ cli。経路を決める唯一の所。
func channelFor(a Args) core.Channel {
	if a.Chat != nil {
		return core.ChannelChat
	}
	return core.ChannelCLI
}

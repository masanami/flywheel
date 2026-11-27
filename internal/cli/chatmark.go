package cli

import (
	"context"
	"os"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは、画面の対話の欄から起動された flywheel のコマンドの扱い（docs/features/chat-session.md）を持つ。
// CLI は印（環境変数）を読み、経路 chat・方式 relayed を core へ渡すだけで、印の検査の中身・承認の成立条件・
// 会話と課題の紐づけは core が持つ。受けるコマンドは絞らない（欄の許可の正本は Claude Code の設定）。

// chatMark は環境の印。
type chatMark struct {
	Session string
}

// chatMarkFromEnv は環境から印を読む。会話の識別が（空文字でも）設定されていれば、印があるものとして扱う
// （空の印は、印の検査で断る側に倒す）。
func chatMarkFromEnv() *chatMark {
	session, ok := os.LookupEnv(core.ChatEnvSession)
	if !ok {
		return nil
	}
	return &chatMark{Session: session}
}

// coreMark は本人確認を通らない記録の口（create・指定つきの ingest）へ渡す印を返す。印が無ければ nil。
func (m *chatMark) coreMark() *core.ChatSessionMark {
	if m == nil {
		return nil
	}
	return &core.ChatSessionMark{Session: m.Session}
}

// needsChatMarkCheck は、印があるときに、コマンドを実行する前に印の検査をかける「記録するコマンド」かを返す。
// 読むコマンドと、下に無いコマンドにはかけない。ingest は取り込み元を 1 つ以上指定した形、cycle は --detach の
// 形だけが記録するコマンド。
func needsChatMarkCheck(cmd Command, a Args) bool {
	switch strings.Join(cmd.Path, " ") {
	case "approve", "reject", "answer", "withdraw", "budget", "create", "requirement create",
		"followup drop", "followup promote", "followup done", "cycle stop":
		return true
	case "ingest":
		return len(a.Positional) > 0
	case "cycle":
		return a.Bools["detach"]
	}
	return false
}

// checkChatMark はストアを開いた後に、記録するコマンドへ印の検査をかける（core の検査）。
func checkChatMark(a Args) error {
	if a.Chat == nil || a.Store == nil {
		return nil
	}
	if err := a.Store.CheckChatMark(context.Background(), a.Chat.Session); err != nil {
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

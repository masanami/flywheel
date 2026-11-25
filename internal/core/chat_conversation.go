package core

import (
	"context"
	"errors"
	"time"
)

// 会話の単位の対話の実行のインターフェース（docs/features/chat-session.md「機能全体の設計」）。会話ごとに実行エンジンを
// 起動したまま続ける。置き場は core で、internal/invoker が実装する。実行エンジンに固有の形（起動の引数・入出力の形・
// 会話のファイルの置き場所と形）は invoker の中に閉じ、ここへは持ち込まない。

// ErrChatEngineNotFound は実行エンジンが見つからず起動できないことを表す。
var ErrChatEngineNotFound = errors.New("core: the chat engine is not found")

// ErrChatTranscriptUnavailable は会話の全文のファイルが見つからない・読めないことを表す。
var ErrChatTranscriptUnavailable = errors.New("core: the chat transcript is unavailable")

// ChatStartInput は会話の実行エンジンの起動の入力。
type ChatStartInput struct {
	// SessionID は会話の識別（ChatSession.EngineSession）。Resume が真なら続き、偽なら新しい会話。
	SessionID string
	Resume    bool
	// WorkDir は実行エンジンの作業フォルダ（ワークスペース）。
	WorkDir string
	// Env は実行エンジンの環境へ足す KEY=VALUE の一覧（ChatSessionMarkEnv の印と、呼び出し元が用意した PATH）。
	Env []string
}

// ChatUtterance は実行エンジンへ送る発言。
type ChatUtterance struct {
	Text string
	// Viewing は、そのとき欄が見せていたもの（無ければ nil）。
	Viewing *ChatViewing
}

// ChatReplyEnd は返事の終わり。
type ChatReplyEnd struct {
	// State は終わり方（replying 以外の値。新しい返事に limit は付けない）。
	State ChatMessageState
	// CostTotalUSD は実行エンジンが返した費用の累計（USD）。分からなければ nil
	// （FinishChatReplyWithTotal へそのまま渡す）。
	CostTotalUSD *float64
	// ErrorSummary は失敗の要約（無ければ空）。
	ErrorSummary string
}

// ChatExit は実行エンジンのプロセスの終わり。
type ChatExit struct {
	// ErrorSummary は失敗の要約（正常に終わったなら空）。
	ErrorSummary string
}

// ChatConversationEvents は途中の分を受ける呼び出し元の関数。nil の関数は呼ばない。実装は、実行エンジンの出力を
// 読む順に、1 つずつ（重ねずに）呼ぶ。StartConversation が戻る前にも呼びうるが、StartConversation の中から
// 同期では呼ばない。サブエージェントの中の発言と道具の使用は渡さない。道具の結果の中身は渡さない。
// 関数の中から、同じ会話の ChatConversation.Stop を呼ばない。
type ChatConversationEvents struct {
	// Started は、出力に会話の始まりの行が現れたとき。
	Started func()
	// Speech は、発言を 1 つ出し終えるたび。
	Speech func(text string)
	// ToolStarted は、道具を使い始めるたび。
	ToolStarted func(use ChatToolUse)
	// ToolFinished は、道具の結果が届くたび。toolUseID は ChatToolUse.ID。
	ToolFinished func(toolUseID string, ok bool)
	// Prompt は、許可の確認か問いを求められたとき。答えは ChatConversation.Answer で返す。
	Prompt func(req ChatPromptRequest)
	// ReplyEnded は、返事の終わりの行が届いたとき（プロセスは動いたまま次の発言を待てる）。
	ReplyEnded func(end ChatReplyEnd)
	// Exited は、プロセスが終わったとき。起動した会話につき 1 回で、その後はどの関数も呼ばない。
	Exited func(exit ChatExit)
}

// ChatConversation は動いている会話の手。
type ChatConversation interface {
	// Send は発言を送る。送れなければ誤り。
	Send(ctx context.Context, u ChatUtterance) error
	// Answer は確認へ答える。requestID は ChatPromptRequest.RequestID。許可はその 1 回だけを許し、
	// 以後も許す答えは返さない。送れなければ（プロセスが終わっていた・書き込めなかった）誤り。
	Answer(ctx context.Context, requestID string, answer ChatPromptAnswer) error
	// Stop はプロセスとその子を止める（返事だけを割り込みで止める形ではない）。終わった後に呼んでも誤りにしない。
	// 誤りなしで返るのは、Exited を含む途中の分の関数がすべて返った後。
	Stop(ctx context.Context) error
}

// ChatTranscriptRef は全文を指す入力。
type ChatTranscriptRef struct {
	// SessionID は会話の識別。WorkDir はその会話を起動した作業フォルダ。
	SessionID string
	WorkDir   string
}

// ChatTranscriptKind は全文の項目の種類。
type ChatTranscriptKind string

// ChatTranscriptKind の 4 値。
const (
	ChatTranscriptOwner      ChatTranscriptKind = "owner"
	ChatTranscriptAssistant  ChatTranscriptKind = "assistant"
	ChatTranscriptToolUse    ChatTranscriptKind = "tool_use"
	ChatTranscriptToolResult ChatTranscriptKind = "tool_result"
)

// ChatTranscriptKinds は全文の項目の種類の閉集合を返す。
func ChatTranscriptKinds() []ChatTranscriptKind {
	return []ChatTranscriptKind{ChatTranscriptOwner, ChatTranscriptAssistant, ChatTranscriptToolUse, ChatTranscriptToolResult}
}

// ChatTranscriptEntry は全文の項目 1 つ。
type ChatTranscriptEntry struct {
	Kind ChatTranscriptKind
	// Text は発言と返事の文・道具の使用の入力・道具の結果。
	Text string
	// Tool は道具の名前（道具の使用と結果だけ。ほかは空）。
	Tool string
	// Truncated は結果を切ったか。
	Truncated bool
	// At は項目の時刻（分からなければゼロ値）。
	At time.Time
}

// ChatConversationInvoker は会話の単位の対話の実行の IF。
type ChatConversationInvoker interface {
	// Available は起動できるか（実行エンジンがあるか）を確かめる。
	Available(ctx context.Context) error
	// StartConversation は会話の実行エンジンを起動する。ctx が及ぶのは起動の間だけで、起動した後に取り消しても
	// プロセスは止まらない（止めるのは ChatConversation.Stop）。1 回の返事のターン数と時間の上限は置かない。
	// 起動できないときは誤り（実行エンジンが見つからなければ errors.Is(err, ErrChatEngineNotFound)）。
	StartConversation(ctx context.Context, in ChatStartInput, ev ChatConversationEvents) (ChatConversation, error)
	// ReadTranscript は会話の全文をファイルの順に返す。見つからない・読めなければ
	// errors.Is(err, ErrChatTranscriptUnavailable)。
	ReadTranscript(ctx context.Context, ref ChatTranscriptRef) ([]ChatTranscriptEntry, error)
	// HasTranscript は全文のファイルが見つかるかを、中身を読まずに返す。
	HasTranscript(ctx context.Context, ref ChatTranscriptRef) bool
}

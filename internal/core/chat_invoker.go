package core

import "context"

// 対話の実行のインターフェース（docs/features/core-redesign-s4d.md S4d-1「対話の実行」）。判断点の呼び出し
// （JudgmentInvoker。1 回で結果を返す）と同じく core が持ち、internal/invoker が実装する。判断点と違い、
// 途中の分を ChatEvents の関数で 1 つずつ渡す。実行エンジンに固有の形（コマンドの引数・stream-json の形）は
// invoker の中に閉じ、ここへは持ち込まない。止めるのは ctx の取り消し。

// ChatLaunchInput は返事 1 回の起動の入力。
type ChatLaunchInput struct {
	// Text はオーナーの発言。Viewing は、そのとき欄が見せていたもの（無ければ nil）。
	Text    string
	Viewing *ChatViewing
	// SessionID は会話の識別（ChatSession.EngineSession）。Resume が真なら続き、偽なら新しい会話。
	SessionID string
	Resume    bool
	// WorkDir は実行エンジンの作業ディレクトリ（ワークスペース）。
	WorkDir string
	// Env は実行エンジンの環境へ足す KEY=VALUE の一覧（ChatMarkEnv の印と、呼び出し元が用意した PATH など）。
	// PATH の先頭へ置くディレクトリの用意と後始末は呼び出し元（サーバ）が行い、invoker は Env をそのまま足すだけにする。
	Env []string
	// Tools は許す道具の範囲。
	Tools ChatTools
	// MaxTurns・TimeoutSec は 1 回の返事のターン数と時間（秒）の上限。
	MaxTurns   int
	TimeoutSec int
}

// ChatEvents は途中の分を受ける呼び出し元の関数。nil の関数は呼ばない。いずれも、実行エンジンの出力を読む
// 順に、1 回ずつ呼ばれる。
type ChatEvents struct {
	// Started は、出力に会話の始まりの行が現れたとき。
	Started func()
	// Speech は、発言を 1 つ出し終えるたび。
	Speech func(text string)
	// Command は、コマンドを 1 つ実行し終えるたび（実行した行と、成功したか）。
	Command func(cmd ChatCommand)
}

// ChatLaunchOutput は返事 1 回の終わり方と費用。
type ChatLaunchOutput struct {
	// State は終わり方（replying 以外の 6 値。ctx の取り消しで止めたときは stopped、ターン数か時間の上限は limit）。
	State ChatMessageState
	// CostUSD は返った費用（USD。分からなければ 0）。
	CostUSD float64
	// ErrorSummary は失敗の要約（無ければ空）。
	ErrorSummary string
}

// ChatInvoker は対話の実行の IF（internal/invoker が実装する）。
type ChatInvoker interface {
	// Available は起動できるか（PATH に実行エンジンがあるか）を確かめる。
	Available(ctx context.Context) error
	// InvokeChat は返事を 1 回作る。途中の分を ev へ渡し、終わり方と費用を返す。実行エンジンを起動できなかった・
	// 出力を読めなかった場合は、error を返す（このとき呼び出し元は failed として終える。出力の State・費用は使わない）。
	// ctx の取り消し・時間の上限は error ではなく、出力の State（stopped・limit）で返す。
	InvokeChat(ctx context.Context, in ChatLaunchInput, ev ChatEvents) (ChatLaunchOutput, error)
}

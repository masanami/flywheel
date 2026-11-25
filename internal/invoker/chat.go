package invoker

// このファイルは core.ChatInvoker（対話の欄の、返事ごとに起動する実行の IF。internal/core/chat_invoker.go）の実装を持つ。
// 実行エンジンに固有の形（引数・stream-json の形）はここと chat_args.go に閉じ、規則（枠超過の判定・
// 費用の記録・発言の保存）は持たない。1 回の返事ごとにプロセスを 1 つ起動し、常駐させない。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// defaultChatTimeout は TimeoutSec が 0 以下のときの最後の砦の上限（宣言の既定と同じ 600 秒）。
const defaultChatTimeout = 600 * time.Second

// maxChatStderr は保持する標準エラーの上限（バイト）。
const maxChatStderr = 64 << 10

// sessionLostMarker は、続きの会話が実行エンジンに無いときの出力に含まれる文言。
const sessionLostMarker = "No conversation found"

// chatStreamLine は stream-json の 1 行から、読むものだけを取り出す。
type chatStreamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// ParentToolUseID は、子のエージェント（Task の道具）の出力の行で非 null になる。
	ParentToolUseID *string `json:"parent_tool_use_id"`
	Message         struct {
		Content []struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			Name      string `json:"name"`
			ID        string `json:"id"`
			ToolUseID string `json:"tool_use_id"`
			IsError   bool   `json:"is_error"`
			Input     struct {
				Command string `json:"command"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
	IsError      bool     `json:"is_error"`
	Result       string   `json:"result"`
	TerminalRsn  string   `json:"terminal_reason"`
	TotalCostUSD *float64 `json:"total_cost_usd"`
}

// InvokeChat は core.ChatInvoker の実装。claude を起動し、標準出力の stream-json を 1 行ずつ読んで ev を呼び、
// 終わり方と費用を返す。error を返すのは、実行エンジンを起動できなかったときだけ（ErrClaudeNotFound を含む）。
// ctx の取り消しは State が stopped、時間の上限は limit で返す。止めるときは子のプロセスのグループごと止める。
func (l *Launcher) InvokeChat(ctx context.Context, in core.ChatLaunchInput, ev core.ChatEvents) (core.ChatLaunchOutput, error) {
	claudePath, err := l.look("claude")
	if err != nil {
		return core.ChatLaunchOutput{}, fmt.Errorf("%w: %s", ErrClaudeNotFound, err.Error())
	}
	instructions, err := ChatInstructions()
	if err != nil {
		return core.ChatLaunchOutput{}, err
	}
	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultChatTimeout
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, claudePath, buildChatArgs(in, instructions)...)
	cmd.Dir = in.WorkDir
	cmd.Env = chatEnv(os.Environ(), in.Env)
	cmd.Stdin = bytes.NewReader(buildChatStdin(in))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		} else if err != nil {
			return err
		}
		return nil
	}
	cmd.WaitDelay = waitDelayAfterCancel
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return core.ChatLaunchOutput{}, fmt.Errorf("invoker: chat stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return core.ChatLaunchOutput{}, fmt.Errorf("invoker: start chat engine: %w", err)
	}

	res := scanChatStream(stdout, ev)
	waitErr := cmd.Wait()

	switch {
	case ctx.Err() != nil:
		return core.ChatLaunchOutput{State: core.ChatStateStopped, CostUSD: reportedCost(res)}, nil
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		return core.ChatLaunchOutput{
			State:        core.ChatStateLimit,
			CostUSD:      reportedCost(res),
			ErrorSummary: fmt.Sprintf("timed out after %s", timeout),
		}, nil
	}
	return classifyChat(in, res, waitErr, stderr.String()), nil
}

// chatEnv は base に add を重ねた環境を返す（add にある名前は base のものを置き換える）。委譲の目印は、
// 親の環境にあっても子へ渡さない。
func chatEnv(base, add []string) []string {
	override := make(map[string]bool, len(add))
	for _, kv := range add {
		if i := strings.IndexByte(kv, '='); i > 0 {
			override[kv[:i]] = true
		}
	}
	env := make([]string, 0, len(base)+len(add))
	for _, kv := range withoutDelegatedMarker(base) {
		if i := strings.IndexByte(kv, '='); i > 0 && override[kv[:i]] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, add...)
}

// chatStreamResult は出力を読み終えた結果。
type chatStreamResult struct {
	hasResult bool
	last      chatStreamLine
	rawResult string
}

// scanChatStream は r の stream-json を 1 行ずつ読み、ev を呼ぶ。JSON でない行は読み飛ばす。
func scanChatStream(r io.Reader, ev core.ChatEvents) chatStreamResult {
	var out chatStreamResult
	pending := map[string]string{} // tool_use の id → 実行した行
	started := false
	br := bufio.NewReader(r)
	for {
		raw, readErr := br.ReadBytes('\n')
		if line := bytes.TrimSpace(raw); len(line) > 0 {
			var l chatStreamLine
			if json.Unmarshal(line, &l) == nil {
				switch l.Type {
				case "system":
					if l.Subtype == "init" && !started {
						started = true
						if ev.Started != nil {
							ev.Started()
						}
					}
				case "assistant":
					if l.ParentToolUseID != nil {
						break // 子のエージェントの発言・コマンドは、オーケストレーターのものとして渡さない
					}
					for _, c := range l.Message.Content {
						switch c.Type {
						case "text":
							if strings.TrimSpace(c.Text) != "" && ev.Speech != nil {
								ev.Speech(c.Text)
							}
						case "tool_use":
							if c.Name == "Bash" {
								pending[c.ID] = c.Input.Command
							}
						}
					}
				case "user":
					for _, c := range l.Message.Content {
						if c.Type != "tool_result" {
							continue
						}
						cmdLine, ok := pending[c.ToolUseID]
						if !ok {
							continue
						}
						delete(pending, c.ToolUseID)
						if ev.Command != nil {
							ev.Command(core.ChatCommand{Command: cmdLine, OK: !c.IsError})
						}
					}
				case "result":
					out.hasResult = true
					out.last = l
					out.rawResult = string(line)
				}
			}
		}
		if readErr != nil {
			return out
		}
	}
}

// classifyChat は終わり方を決める。判別には終わりの行の subtype・is_error・terminal_reason と終了コードを使い、
// 自由記述は、続きの会話が無いことの見分けと枠超過の判定（core.IsRateLimited へそのまま渡す）にだけ使う。
func classifyChat(in core.ChatLaunchInput, res chatStreamResult, waitErr error, stderr string) core.ChatLaunchOutput {
	out := core.ChatLaunchOutput{CostUSD: reportedCost(res)}
	text := res.last.Result
	failedRun := waitErr != nil || (res.hasResult && (res.last.IsError || res.last.Subtype != "success"))

	switch {
	case res.hasResult && (res.last.Subtype == "error_max_turns" || res.last.TerminalRsn == "max_turns"):
		out.State = core.ChatStateLimit
	case in.Resume && failedRun && (strings.Contains(res.rawResult, sessionLostMarker) || strings.Contains(stderr, sessionLostMarker)):
		out.State = core.ChatStateSessionLost
		out.ErrorSummary = sessionLostMarker
	case failedRun && (core.IsRateLimited(text) || core.IsRateLimited(stderr)):
		out.State = core.ChatStateRateLimited
		out.ErrorSummary = firstNonEmpty(text, stderr)
	case failedRun:
		out.State = core.ChatStateFailed
		out.ErrorSummary = firstNonEmpty(text, stderr)
		if out.ErrorSummary == "" && waitErr != nil {
			out.ErrorSummary = waitErr.Error()
		}
	case !res.hasResult:
		out.State = core.ChatStateFailed
		out.ErrorSummary = "invoker: the chat engine output has no result line"
	default:
		out.State = core.ChatStateDone
	}
	return out
}

// reportedCost は終わりの行が返した費用（読めていなければ 0。費用を返す前に止めた返事は 0 になる）。
func reportedCost(res chatStreamResult) float64 {
	if res.hasResult && res.last.TotalCostUSD != nil {
		return *res.last.TotalCostUSD
	}
	return 0
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if t := strings.TrimSpace(s); t != "" {
			return t
		}
	}
	return ""
}

// limitedBuffer は先頭 maxChatStderr バイトだけを保持する io.Writer。
type limitedBuffer struct{ buf bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxChatStderr - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

var _ core.ChatInvoker = (*Launcher)(nil)

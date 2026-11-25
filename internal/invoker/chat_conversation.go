package invoker

// このファイルは core.ChatConversationInvoker（対話の欄の、会話の単位の実行の IF。internal/core/chat_conversation.go）
// のうち、プロセスの起動・発言と確認の答えの送り・停止を持つ。出力の読み取りは chat_conversation_stream.go、
// 全文の読み取りは chat_transcript.go。

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

const (
	// chatDrainHardLimit は、プロセスが終わった後に、孫のプロセスが管へ書き続けても読むのをやめる上限。
	chatDrainHardLimit = 6 * waitDelayAfterCancel
	// chatReplyWriteTimeout は、扱わない種類の制御の要求へ誤りを返す書き込みの上限。
	chatReplyWriteTimeout = 5 * time.Second
)

// errChatProcessExited は、プロセスが終わっていて送れないことを表す。
var errChatProcessExited = errors.New("invoker: the chat engine process has exited")

// conversation は動いている会話 1 つ（プロセス 1 つ）。
type conversation struct {
	cmd   *exec.Cmd
	stdin *os.File
	// writing は標準入力へ書く権利（1 行ずつ書く）。
	writing chan struct{}
	// exited はプロセスの本体が終わったら閉じる。finished はプロセスの終わりを呼び出し元へ渡し終えたら閉じる。
	exited   chan struct{}
	finished chan struct{}
	// waitErr・exitedAt は exited が閉じた後にだけ読む。
	waitErr  error
	exitedAt time.Time

	// awaiting は、送った発言の返事の終わりをまだ渡していないか。
	awaiting atomic.Bool

	mu sync.Mutex
	// pending は答えを待っている確認（確認の識別 → 要求の中身）。
	pending map[string]pendingRequest
}

// StartConversation は core.ChatConversationInvoker の実装。claude を起動したまま残し、標準出力の stream-json を
// 1 行ずつ読んで ev を呼ぶ。ctx が及ぶのは起動の間だけ。claude は自分のプロセスのグループで動かす。
func (l *Launcher) StartConversation(ctx context.Context, in core.ChatStartInput, ev core.ChatConversationEvents) (core.ChatConversation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	claudePath, err := l.look("claude")
	if err != nil {
		return nil, fmt.Errorf("%w: %w: %s", core.ErrChatEngineNotFound, ErrClaudeNotFound, err.Error())
	}
	instructions, err := ChatInstructions()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(claudePath, buildConversationArgs(in, instructions)...)
	cmd.Dir = in.WorkDir
	cmd.Env = chatEnv(os.Environ(), in.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// 管は自分で持つ。os/exec に写させると、孫のプロセスが管を持つ間、本体の終わりが分からない。
	pipes, err := newChatPipes()
	if err != nil {
		return nil, fmt.Errorf("invoker: chat pipes: %w", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pipes.inR, pipes.outW, pipes.errW
	err = cmd.Start()
	pipes.closeChildEnds()
	if err != nil {
		pipes.closeParentEnds()
		return nil, fmt.Errorf("invoker: start chat engine: %w", err)
	}

	c := &conversation{
		cmd:      cmd,
		stdin:    pipes.inW,
		writing:  make(chan struct{}, 1),
		exited:   make(chan struct{}),
		finished: make(chan struct{}),
		pending:  map[string]pendingRequest{},
	}
	go func() {
		c.waitErr = cmd.Wait()
		c.exitedAt = time.Now()
		close(c.exited)
		// いま待っている読み取りも、いつまでも待たないようにする。
		_ = pipes.outR.SetReadDeadline(c.exitedAt.Add(waitDelayAfterCancel))
		_ = pipes.errR.SetReadDeadline(c.exitedAt.Add(waitDelayAfterCancel))
	}()
	var stderr syncBuffer
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(&stderr, &drainReader{f: pipes.errR, c: c})
	}()
	go func() {
		defer close(c.finished)
		stream := &conversationStream{c: c, ev: ev, resume: in.Resume, stderr: &stderr, tools: map[string]bool{}}
		stream.read(bufio.NewReader(&drainReader{f: pipes.outR, c: c}))
		<-c.exited
		<-stderrDone
		pipes.closeParentEnds()
		c.dropPending()
		stream.exit(c.waitErr, stderr.String())
	}()
	return c, nil
}

// Send は core.ChatConversation の実装。発言を標準入力へ 1 行で送る。
func (c *conversation) Send(ctx context.Context, u core.ChatUtterance) error {
	line, err := utteranceLine(u)
	if err != nil {
		return err
	}
	// 書き終える前に返事の終わりが届きうるので、書く前に立てる。
	c.awaiting.Store(true)
	return c.write(ctx, line)
}

// Answer は core.ChatConversation の実装。答えを待っている確認にだけ、1 回だけ答えを送る。
func (c *conversation) Answer(ctx context.Context, requestID string, answer core.ChatPromptAnswer) error {
	c.mu.Lock()
	req, ok := c.pending[requestID]
	if !ok {
		c.mu.Unlock()
		return fmt.Errorf("invoker: no pending chat prompt %q", requestID)
	}
	line, err := answerLine(requestID, req, answer)
	if err == nil {
		delete(c.pending, requestID)
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.write(ctx, line)
}

// Stop は core.ChatConversation の実装。プロセスのグループへ SIGKILL を送り、プロセスの終わりを呼び出し元へ
// 渡し終えるまで待つ。終わった後に呼んでも何もしない。
func (c *conversation) Stop(ctx context.Context) error {
	select {
	case <-c.finished:
		return nil
	default:
	}
	if err := c.kill(); err != nil {
		return err
	}
	select {
	case <-c.finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// kill はプロセスのグループ（本体と、グループに残る子）へ SIGKILL を送る。本体が終わった後の誤りは見ない
// （macOS は、終わりを待つだけのプロセスしか残らないグループへの合図を誤りにする）。
func (c *conversation) kill() error {
	err := syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	select {
	case <-c.exited:
		return nil
	case <-time.After(waitDelayAfterCancel):
		return fmt.Errorf("invoker: stop chat engine: %w", err)
	}
}

// write は標準入力へ 1 行を書く。プロセスが終わっていた・書けなかった場合は誤り。書いている途中で ctx が
// 取り消されたら、行が途中で切れて後の入力が壊れるので、プロセスを止める。
func (c *conversation) write(ctx context.Context, line []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.writing <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.writing }()
	select {
	case <-c.exited:
		return errChatProcessExited
	default:
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.stdin.Write(line)
		done <- err
	}()
	var abort error
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("invoker: write to the chat engine: %w", err)
		}
		return nil
	case <-ctx.Done():
		abort = ctx.Err()
		_ = c.kill()
	case <-c.exited:
		abort = errChatProcessExited
	}
	_ = c.stdin.Close() // 待っている書き込みを戻す
	<-done
	return abort
}

func (c *conversation) addPending(requestID string, req pendingRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[requestID] = req
}

// dropRequest は答えを待っている確認から requestID を外し、あったかを返す。
func (c *conversation) dropRequest(requestID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.pending[requestID]
	delete(c.pending, requestID)
	return ok
}

func (c *conversation) dropPending() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.pending)
}

// drainReader は出力の管を読む。本体が終わった後は、新しい出力が waitDelayAfterCancel の間来なければ読むのを
// やめる（孫のプロセスが管を持ち続けると、終わりの印が来ない）。届いている出力は読み切る。
type drainReader struct {
	f *os.File
	c *conversation
}

func (r *drainReader) Read(p []byte) (int, error) {
	select {
	case <-r.c.exited:
		deadline := time.Now().Add(waitDelayAfterCancel)
		if hard := r.c.exitedAt.Add(chatDrainHardLimit); deadline.After(hard) {
			deadline = hard
		}
		_ = r.f.SetReadDeadline(deadline)
	default:
	}
	return r.f.Read(p)
}

// chatPipes は子の標準入力・標準出力・標準エラーの管。
type chatPipes struct {
	inR, inW, outR, outW, errR, errW *os.File
}

func newChatPipes() (*chatPipes, error) {
	p := &chatPipes{}
	var err error
	if p.inR, p.inW, err = os.Pipe(); err == nil {
		if p.outR, p.outW, err = os.Pipe(); err == nil {
			p.errR, p.errW, err = os.Pipe()
		}
	}
	if err != nil {
		p.closeChildEnds()
		p.closeParentEnds()
		return nil, err
	}
	return p, nil
}

func (p *chatPipes) closeChildEnds()  { closeFiles(p.inR, p.outW, p.errW) }
func (p *chatPipes) closeParentEnds() { closeFiles(p.inW, p.outR, p.errR) }

func closeFiles(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

// syncBuffer は先頭だけを保持する limitedBuffer を、書きながら読めるようにする。
type syncBuffer struct {
	mu  sync.Mutex
	buf limitedBuffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.buf.String())
}

// Reset は保持している分を捨てる。
func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.buf.Reset()
}

var (
	_ core.ChatConversationInvoker = (*Launcher)(nil)
	_ core.ChatConversation        = (*conversation)(nil)
)

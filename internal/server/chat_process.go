package server

// このファイルは、対話の欄の実行エンジンのプロセスの寿命を持つ（docs/features/chat-session.md「会話と Claude Code の
// プロセス」「機能全体の設計」）。起動・発言と確認の答えの送り・休ませる・止める・サーバの停止と起動の片付け。実行エンジンは
// core.ChatConversationInvoker（internal/invoker が実装する）で受け取る。返事の進み・道具の一覧・確認はストアへ書き、
// サーバがストアの外に持つのは、動いているプロセスと、その入出力の管、休ませるための時計だけ。
//
// 排他は 3 つ。chatAgent.op はエージェントごとの操作（発言・止める・新しい会話・続ける・休ませる・サーバの停止）を
// 直列にする。chatAgent.settle は、確認への答え（確かめる・送る・記録する）と返事の終わりの記録を直列にする
// （返事を終えると答えを待つ確認は expired になるので、送ってから記録するまでの間に返事を終えない）。
// chatAgent.mu はプロセスの状態を守る。実行エンジンの途中の分を受ける関数は op を取らず、mu を持ったまま
// プロセスを止めない（止める側は、途中の分を受ける関数がすべて返るのを待つため）。settle を持つ側は op を取らず、
// ストアを借りたまま settle を取らない。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

const (
	// chatIdleTimeout は、返事が終わってから次の発言が無いプロセスを止めるまでの時間（宣言の項目にしない）。
	chatIdleTimeout = 30 * time.Minute
	// chatStopWait は、プロセスを止めるのを待つ上限。
	chatStopWait = 10 * time.Second
	// chatAnswerWait は、実行エンジンへ答えを書くのを待つ上限。
	chatAnswerWait = 10 * time.Second
	// chatWriteAttempts と chatWriteRetryWait は、書けなかった書き込みをやり直す回数と間。
	chatWriteAttempts  = 5
	chatWriteRetryWait = 100 * time.Millisecond
	// chatBetweenRepliesDenyMessage は、返事の合間に届いた確認を断るときに添える文。
	chatBetweenRepliesDenyMessage = "いまは答えられません。次の返事の中で尋ね直してください。"
)

// errChatAnswerNotSent は、確認への答えを実行エンジンへ送れなかったことを表す。
var errChatAnswerNotSent = errors.New("答えを実行エンジンへ送れなかった")

// chatRunner は欄の実行エンジンのプロセスを管理する。
type chatRunner struct {
	fleet *Fleet

	mu      sync.Mutex
	invoker core.ChatConversationInvoker
	// executable は flywheel の名前で PATH に置くサーバの実行ファイルを返す（テストが差し替える）。
	executable func() (string, error)
	// idle は休ませるまでの時間。afterFunc は休ませるための時計で、返す関数は取り消し（テストが差し替える）。
	idle      time.Duration
	afterFunc func(d time.Duration, f func()) (cancel func())
	// agents はワークスペースの名前ごとの状態。
	agents  map[string]*chatAgent
	closing bool
}

// chatAgent はエージェント（ワークスペース）1 つの状態。動いているプロセスは高々 1 つ。
type chatAgent struct {
	op     sync.Mutex
	settle sync.Mutex

	mu   sync.Mutex
	proc *chatProcess
}

// chatProcess は動いている会話のプロセス 1 つ。conv と ready を除くフィールドは chatAgent.mu が守る。
type chatProcess struct {
	name string
	// sessionID は会話の行の ID、engineSession は会話の識別。
	sessionID     int64
	engineSession string
	// conv は ready が閉じた後にだけ読む。起動に失敗したら nil。
	conv  core.ChatConversation
	ready chan struct{}
	// cleanup は PATH の先頭に置いた一時のディレクトリを消す。
	cleanup func()

	// replyID は進行中の返事の行の ID（返事の合間は 0）。
	replyID int64
	// stopping は、サーバが自分で止めているか（返事の途中なら stopped にする）。
	stopping bool
	// exited は、プロセスが終わったか。
	exited bool
	// cancelIdle は休ませるための時計の取り消し。idleGeneration は、取り消しに間に合わなかった時計を見分ける。
	cancelIdle     func()
	idleGeneration int
}

func newChatRunner(fleet *Fleet) *chatRunner {
	return &chatRunner{
		fleet:      fleet,
		executable: os.Executable,
		idle:       chatIdleTimeout,
		afterFunc: func(d time.Duration, f func()) func() {
			t := time.AfterFunc(d, f)
			return func() { t.Stop() }
		},
		agents: map[string]*chatAgent{},
	}
}

func (c *chatRunner) engine() core.ChatConversationInvoker {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.invoker
}

func (c *chatRunner) agent(name string) *chatAgent {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.agents[name]
	if a == nil {
		a = &chatAgent{}
		c.agents[name] = a
	}
	return a
}

func (c *chatRunner) isClosing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closing
}

func (a *chatAgent) current() *chatProcess {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.proc
}

// replying は、返事が進行中のプロセスとその返事の行の ID を返す。無ければ nil。
func (a *chatAgent) replying() (*chatProcess, int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.proc == nil || a.proc.replyID == 0 {
		return nil, 0
	}
	return a.proc, a.proc.replyID
}

func (p *chatProcess) disarmIdleLocked() {
	p.idleGeneration++
	if p.cancelIdle != nil {
		p.cancelIdle()
		p.cancelIdle = nil
	}
}

// deliver は発言をプロセスへ送る。同じ会話のプロセスが動いていればそれへ送り、無ければ起動する。呼び出し元は
// a.op を持つ。送りは待たない（送れなければプロセスを止め、返事はプロセスの終わりで failed になる）。
func (c *chatRunner) deliver(a *chatAgent, name, workDir string, inv core.ChatConversationInvoker, turn *core.ChatTurn, u core.ChatUtterance) error {
	a.mu.Lock()
	p := a.proc
	if p != nil && p.engineSession == turn.EngineSession && !p.stopping && !p.exited {
		p.disarmIdleLocked()
		p.replyID = turn.ReplyMessageID
		a.mu.Unlock()
		go sendUtterance(p.conv, u)
		return nil
	}
	a.mu.Unlock()
	if p != nil {
		// 前の会話のプロセスが残っている。終わるまで次を起動しない。
		ctx, cancel := context.WithTimeout(context.Background(), chatStopWait)
		defer cancel()
		if err := c.stopProcess(ctx, a, p); err != nil {
			return err
		}
	}
	p, err := c.start(a, name, workDir, inv, turn)
	if err != nil {
		return err
	}
	go sendUtterance(p.conv, u)
	return nil
}

// sendUtterance は発言を送る。送れなかったプロセスは使えないので止める（サーバが止めた扱いにしないので、
// 返事はプロセスの終わりで failed になる。サーバが先に止めていれば stopped のまま）。
func sendUtterance(conv core.ChatConversation, u core.ChatUtterance) {
	if err := conv.Send(context.Background(), u); err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), chatStopWait)
		defer cancel()
		_ = conv.Stop(ctx)
	}
}

// start は会話のプロセスを起動する。呼び出し元は a.op を持ち、動いているプロセスは無い。
func (c *chatRunner) start(a *chatAgent, name, workDir string, inv core.ChatConversationInvoker, turn *core.ChatTurn) (*chatProcess, error) {
	binDir, cleanup, err := c.prepareChatBin()
	if err != nil {
		return nil, err
	}
	p := &chatProcess{
		name: name, sessionID: turn.SessionID, engineSession: turn.EngineSession,
		ready: make(chan struct{}), cleanup: cleanup, replyID: turn.ReplyMessageID,
	}
	// 途中の分は起動が戻る前にも届きうるので、先に登録する。
	a.mu.Lock()
	a.proc = p
	a.mu.Unlock()
	defer close(p.ready)
	conv, err := inv.StartConversation(context.Background(), core.ChatStartInput{
		SessionID: turn.EngineSession,
		Resume:    turn.Resume,
		WorkDir:   workDir,
		Env:       append(core.ChatSessionMarkEnv(turn.EngineSession), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH")),
	}, c.events(a, p))
	if err != nil {
		a.mu.Lock()
		if a.proc == p {
			a.proc = nil
		}
		a.mu.Unlock()
		cleanup()
		return nil, err
	}
	p.conv = conv
	return p, nil
}

// stopProcess はプロセスとその子を止め、終わりが記録されるまで待つ。進行中の返事は stopped になる。呼び出し元は
// a.op を持つ。止められなければ誤りを返し、プロセスは登録したままにする（2 つ目を起動しないため）。
func (c *chatRunner) stopProcess(ctx context.Context, a *chatAgent, p *chatProcess) error {
	a.mu.Lock()
	p.stopping = true
	p.disarmIdleLocked()
	a.mu.Unlock()
	if p.conv != nil {
		if err := p.conv.Stop(ctx); err != nil {
			return err
		}
	}
	a.mu.Lock()
	if a.proc == p {
		a.proc = nil
	}
	a.mu.Unlock()
	return nil
}

// events はプロセス 1 つの途中の分を、ストアへ書く関数にする。実行エンジンはこれらを 1 つずつ呼ぶ。
// 返事の合間に届いた発言と道具の使用は、結び付ける返事が無いので書かない。
func (c *chatRunner) events(a *chatAgent, p *chatProcess) core.ChatConversationEvents {
	bg := context.Background()
	// write は進行中の返事があるときだけ書く。
	write := func(fn func(st *core.Store, replyID int64)) {
		a.mu.Lock()
		replyID := p.replyID
		a.mu.Unlock()
		if replyID == 0 {
			return
		}
		c.withStore(p.name, func(st *core.Store) { fn(st, replyID) })
	}
	return core.ChatConversationEvents{
		Started: func() {
			c.withStore(p.name, func(st *core.Store) { _ = st.MarkChatEngineStarted(bg, p.sessionID) })
		},
		Speech: func(text string) {
			write(func(st *core.Store, id int64) { _ = st.AppendChatSpeech(bg, id, text) })
		},
		ToolStarted: func(use core.ChatToolUse) {
			write(func(st *core.Store, id int64) { _ = st.StartChatTool(bg, id, use) })
		},
		ToolFinished: func(toolUseID string, ok bool) {
			write(func(st *core.Store, id int64) { _ = st.FinishChatTool(bg, id, toolUseID, ok) })
		},
		Prompt: func(req core.ChatPromptRequest) { c.prompt(a, p, req) },
		ReplyEnded: func(end core.ChatReplyEnd) {
			c.replyEnded(a, p, end)
		},
		Exited: func(core.ChatExit) { c.exited(a, p) },
	}
}

// prompt は確認を受ける。進行中の返事があれば、答えを待つ確認として記録する。返事の合間なら、断る答えを返して、
// 会話の最後の返事の行に断った確認として記録する。
func (c *chatRunner) prompt(a *chatAgent, p *chatProcess, req core.ChatPromptRequest) {
	a.mu.Lock()
	replyID := p.replyID
	a.mu.Unlock()
	if replyID != 0 {
		c.withStore(p.name, func(st *core.Store) {
			if _, err := st.RecordChatPrompt(context.Background(), replyID, req); err != nil {
				// 記録できない確認には画面から答えられないので、断って実行エンジンを待たせない。
				_ = deny(p, req)
			}
		})
		return
	}
	if err := deny(p, req); err != nil {
		return // 送れなかった答えは記録しない
	}
	c.withStore(p.name, func(st *core.Store) {
		_ = st.RecordDeniedChatPrompt(context.Background(), p.sessionID, req, chatBetweenRepliesDenyMessage)
	})
}

func deny(p *chatProcess, req core.ChatPromptRequest) error {
	return answerEngine(p, req.RequestID, core.ChatPromptAnswer{Decision: core.ChatDecisionDeny, Message: chatBetweenRepliesDenyMessage})
}

// answerEngine は確認への答えをプロセスへ送る。
func answerEngine(p *chatProcess, requestID string, answer core.ChatPromptAnswer) error {
	<-p.ready
	if p.conv == nil {
		return errors.New("実行エンジンを起動できていない")
	}
	// 書き込みの途中で取り消すとプロセスが止まるので、要求の ctx は使わない。
	ctx, cancel := context.WithTimeout(context.Background(), chatAnswerWait)
	defer cancel()
	return p.conv.Answer(ctx, requestID, answer)
}

// answerPrompt は確認への答えを受ける。答えを待っていて形が合うことを core で確かめ、先に実行エンジンへ送り、
// 送れたら記録する。送れなければ、その返事の答えを待つ確認を expired にして errChatAnswerNotSent を返す
// （答えは記録しない）。ほかの誤りは core の誤り。プロセスを待つ間は、ストアを借りない。
func (c *chatRunner) answerPrompt(ctx context.Context, a *chatAgent, name string, promptID int64, answer core.ChatPromptAnswer) error {
	a.settle.Lock()
	defer a.settle.Unlock()
	st, release, le := c.fleet.Acquire(ctx, name)
	if le != nil {
		return le
	}
	prompt, err := st.CheckChatPromptAnswer(ctx, promptID, answer)
	release()
	if err != nil {
		return err
	}

	p, replyID := a.replying()
	if p == nil || replyID != prompt.MessageID {
		err = errors.New("確認を求めたプロセスが動いていない")
	} else {
		err = answerEngine(p, prompt.RequestID, answer)
	}
	if err != nil {
		if werr := c.writeWithRetry(name, func(st *core.Store) error {
			_, err := st.ExpireChatReplyPrompts(context.Background(), prompt.MessageID)
			return err
		}); werr != nil {
			return werr
		}
		return fmt.Errorf("%w: %s", errChatAnswerNotSent, err.Error())
	}
	return c.writeWithRetry(name, func(st *core.Store) error {
		_, err := st.AnswerChatPrompt(context.Background(), promptID, answer)
		return err
	})
}

// writeWithRetry はストアを借りて書く。ほかの書き込みとぶつかったときは、短く間を置いて数回やり直す。
func (c *chatRunner) writeWithRetry(name string, fn func(st *core.Store) error) error {
	var err error
	for attempt := 0; attempt < chatWriteAttempts; attempt++ {
		st, release, le := c.fleet.Acquire(context.Background(), name)
		if le != nil {
			err = le
		} else {
			err = fn(st)
			release()
			if !errors.Is(err, core.ErrStoreBusy) {
				return err
			}
		}
		time.Sleep(chatWriteRetryWait)
	}
	return err
}

// replyEnded は返事の終わりを記録し、休ませるための時計を始める。
func (c *chatRunner) replyEnded(a *chatAgent, p *chatProcess, end core.ChatReplyEnd) {
	a.mu.Lock()
	replyID, stopping := p.replyID, p.stopping
	a.mu.Unlock()
	if replyID == 0 {
		return
	}
	state := end.State
	if !validEndState(state) {
		state = core.ChatStateFailed
	}
	// サーバが止めている間に届いた終わりは、失敗にしない。
	if stopping && state != core.ChatStateSessionLost && state != core.ChatStateDone {
		state = core.ChatStateStopped
	}
	total := end.CostTotalUSD
	if total != nil && *total < 0 {
		total = nil
	}
	c.finishReply(a, p.name, replyID, state, total)

	a.mu.Lock()
	defer a.mu.Unlock()
	if p.replyID != replyID {
		return // 次の発言をもう受けた
	}
	p.replyID = 0
	if p.stopping || p.exited {
		return
	}
	// 続けられなかった会話は閉じたので、そのプロセスはすぐに止める。ほかは放置の時間の後に止める。
	wait := c.idle
	if state == core.ChatStateSessionLost {
		wait = 0
	}
	p.disarmIdleLocked()
	generation := p.idleGeneration
	p.cancelIdle = c.afterFunc(wait, func() { c.rest(a, p, generation) })
}

// exited はプロセスの終わり。返事の途中なら failed（サーバが止めた分は stopped）にする。返事の合間の終わりは
// 何も記録しない。
func (c *chatRunner) exited(a *chatAgent, p *chatProcess) {
	a.mu.Lock()
	replyID, stopping := p.replyID, p.stopping
	p.replyID, p.exited = 0, true
	p.disarmIdleLocked()
	a.mu.Unlock()
	if replyID != 0 {
		state := core.ChatStateFailed
		if stopping {
			state = core.ChatStateStopped
		}
		c.finishReply(a, p.name, replyID, state, nil)
	}
	p.cleanup()
	// 終わりを記録してから外す（外した後は、次の発言が新しいプロセスを起動する）。
	a.mu.Lock()
	if a.proc == p {
		a.proc = nil
	}
	a.mu.Unlock()
}

// rest は放置されたプロセスを止める（会話は閉じない）。
func (c *chatRunner) rest(a *chatAgent, p *chatProcess, generation int) {
	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	due := a.proc == p && p.idleGeneration == generation && p.replyID == 0
	a.mu.Unlock()
	if !due || c.isClosing() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatStopWait)
	defer cancel()
	_ = c.stopProcess(ctx, a, p)
}

// finishReply は返事の終わりを記録する。記録に失敗すると返事の行が replying のまま残り、次の発言も「新しい会話」も
// 409 になるので、短く間を置いて数回やり直す（それでも書けなければ、stop が行の側から止められる）。
func (c *chatRunner) finishReply(a *chatAgent, name string, replyID int64, state core.ChatMessageState, total *float64) {
	a.settle.Lock()
	defer a.settle.Unlock()
	for attempt := 0; attempt < chatWriteAttempts; attempt++ {
		done := false
		c.withStore(name, func(st *core.Store) {
			err := st.FinishChatReplyWithTotal(context.Background(), replyID, state, total)
			done = err == nil || errors.Is(err, core.ErrValidation) || errors.Is(err, core.ErrNotFound)
		})
		if done {
			return
		}
		time.Sleep(chatWriteRetryWait)
	}
}

func validEndState(s core.ChatMessageState) bool {
	for _, v := range core.ChatEndStates() {
		if v == s {
			return true
		}
	}
	return false
}

// recoverInterrupted は起動のとき、すべてのワークスペースのストアを 1 回ずつ見て、進行中のまま残った返事を
// stopped に、答えを待ったまま残った確認を expired にする。ストアの無い（開けない）ワークスペースは、作らずに
// 飛ばす。ストアは Fleet を通さず、見るだけ開いてすぐ閉じる（対象が無ければ書かず、-wal・-shm も残さない）。
func (c *chatRunner) recoverInterrupted(ctx context.Context) {
	for _, e := range c.fleet.entries {
		st, err := core.OpenWorkspace(e.path)
		if err != nil {
			continue
		}
		if found, err := st.HasInterruptedChat(ctx); err == nil && found {
			_, _ = st.StopInterruptedChatReplies(ctx)
		}
		_ = st.Close()
	}
}

// stopAll はすべてのプロセスを止め、終わりが記録されるまで待つ（進行中の返事は stopped、答えを待つ確認は
// expired になる）。以後は新しい発言を受けない。
func (c *chatRunner) stopAll() {
	c.mu.Lock()
	c.closing = true
	agents := make([]*chatAgent, 0, len(c.agents))
	for _, a := range c.agents {
		agents = append(agents, a)
	}
	c.mu.Unlock()
	var wg sync.WaitGroup
	for _, a := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.op.Lock()
			defer a.op.Unlock()
			if p := a.current(); p != nil {
				ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
				defer cancel()
				_ = c.stopProcess(ctx, a, p)
			}
		}()
	}
	// 実行エンジンが応じなくても、Shutdown を縛らない（待ちの上限は Shutdown と同じ）。
	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(shutdownTimeout):
	}
}

// withStore はバックグラウンドの書き込みのためにストアを借りる（借りられなければ何もしない）。
func (c *chatRunner) withStore(name string, fn func(st *core.Store)) {
	st, release, le := c.fleet.Acquire(context.Background(), name)
	if le != nil {
		return
	}
	defer release()
	fn(st)
}

// prepareChatBin は一時のディレクトリを作り、flywheel の名前でサーバの実行ファイルへのシンボリックリンクを置く。
// 戻り値は、そのディレクトリと、消す関数。
func (c *chatRunner) prepareChatBin() (string, func(), error) {
	exe, err := c.executable()
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "flywheel-chat-")
	if err != nil {
		return "", nil, err
	}
	remove := func() { _ = os.RemoveAll(dir) }
	if err := os.Symlink(exe, filepath.Join(dir, "flywheel")); err != nil {
		remove()
		return "", nil, err
	}
	return dir, remove, nil
}

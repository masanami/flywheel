package invoker

// このファイルは、会話の単位の実行の標準入出力の形（stream-json・control_request・control_response）を持つ。
// 形は実物の写し（testdata の chat_host_*）に合わせる。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

const (
	chatQuestionTool = "AskUserQuestion"
	// chatDefaultDenyMessage は、断るときに添える文が無い場合の文（実行エンジンは断りの文を求める）。
	chatDefaultDenyMessage = "オーナーがこの操作を断りました。"
)

// conversationLine は stream-json の 1 行から、読むものだけを取り出す。
type conversationLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// ParentToolUseID は、サブエージェントの中の行で非 null になる。
	ParentToolUseID *string `json:"parent_tool_use_id"`
	Message         struct {
		// Content は塊の配列か、文字列（文字列の行からは何も読まない）。
		Content json.RawMessage `json:"content"`
	} `json:"message"`

	// 終わりの行（type: result）。
	IsError      bool     `json:"is_error"`
	Result       string   `json:"result"`
	Errors       []string `json:"errors"`
	TotalCostUSD *float64 `json:"total_cost_usd"`

	// 制御の行（type: control_request・control_cancel_request）。
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype                 string          `json:"subtype"`
		ToolName                string          `json:"tool_name"`
		Input                   json.RawMessage `json:"input"`
		RequiresUserInteraction bool            `json:"requires_user_interaction"`
	} `json:"request"`
}

// contentBlock は発言の中身の塊 1 つ。
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	// Content は道具の結果の中身（全文の読み取りだけが使う。途中の分へは渡さない）。
	Content json.RawMessage `json:"content"`
}

func contentBlocks(raw json.RawMessage) []contentBlock {
	var out []contentBlock
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// pendingRequest は答えを待っている確認の要求。input は受けた道具の入力のままで、許すときはそのまま返す。
type pendingRequest struct {
	kind  core.ChatPromptKind
	input json.RawMessage
}

// conversationStream はプロセス 1 つの出力を読み、途中の分を順に呼び出し元へ渡す。1 つの goroutine だけが使う。
type conversationStream struct {
	c      *conversation
	ev     core.ChatConversationEvents
	resume bool
	stderr *syncBuffer

	started bool
	// ended は、返事の終わりを 1 回でも渡したか。
	ended bool
	// tools は渡した道具の使い始めのうち、結果をまだ渡していないもの。
	tools map[string]bool
}

// read は管が終わるまで 1 行ずつ読む。JSON でない行は読み飛ばす。
func (s *conversationStream) read(br *bufio.Reader) {
	for {
		raw, readErr := br.ReadBytes('\n')
		if line := bytes.TrimSpace(raw); len(line) > 0 {
			var l conversationLine
			if json.Unmarshal(line, &l) == nil {
				s.handle(l, string(line))
			}
		}
		if readErr != nil {
			return
		}
	}
}

func (s *conversationStream) handle(l conversationLine, raw string) {
	switch l.Type {
	case "system":
		if l.Subtype == "init" && !s.started {
			s.started = true
			if s.ev.Started != nil {
				s.ev.Started()
			}
		}
	case "assistant":
		if l.ParentToolUseID == nil {
			s.assistant(contentBlocks(l.Message.Content))
		}
	case "user":
		if l.ParentToolUseID == nil {
			s.toolResults(contentBlocks(l.Message.Content))
		}
	case "control_request":
		s.control(l)
	case "control_cancel_request":
		s.c.dropRequest(l.RequestID)
	case "result":
		s.replyEnded(s.classify(l, raw))
	}
}

func (s *conversationStream) assistant(blocks []contentBlock) {
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" && s.ev.Speech != nil {
				s.ev.Speech(b.Text)
			}
		case "tool_use":
			if b.ID == "" || b.Name == "" || s.tools[b.ID] {
				continue
			}
			s.tools[b.ID] = true
			if s.ev.ToolStarted != nil {
				s.ev.ToolStarted(core.ChatToolUse{ID: b.ID, Tool: b.Name, Target: toolTargetLine(b.Name, b.Input)})
			}
		}
	}
}

func (s *conversationStream) toolResults(blocks []contentBlock) {
	for _, b := range blocks {
		if b.Type != "tool_result" || !s.tools[b.ToolUseID] {
			continue
		}
		delete(s.tools, b.ToolUseID)
		if s.ev.ToolFinished != nil {
			s.ev.ToolFinished(b.ToolUseID, !b.IsError)
		}
	}
}

// control は実行エンジンからの要求を扱う。サブエージェントの中の道具の確認も、オーナーが答えるので渡す。
func (s *conversationStream) control(l conversationLine) {
	if l.RequestID == "" {
		return
	}
	if l.Request.Subtype != "can_use_tool" || l.Request.ToolName == "" {
		// 答えないと実行エンジンが待ち続けるので、扱えないと返す。
		line, err := jsonLine(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "error", "request_id": l.RequestID, "error": "unsupported control request",
		}})
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), chatReplyWriteTimeout)
		defer cancel()
		_ = s.c.write(ctx, line)
		return
	}
	req := promptRequest(l)
	s.c.addPending(req.RequestID, pendingRequest{kind: req.Kind, input: l.Request.Input})
	if s.ev.Prompt != nil {
		s.ev.Prompt(req)
	}
}

// promptRequest は確認の行を core の形にする。AskUserQuestion で requires_user_interaction が真のものは問い、
// ほかは許可の確認。問いの中身が読めない場合も、オーナーが断れるように許可の確認にする。
func promptRequest(l conversationLine) core.ChatPromptRequest {
	req := core.ChatPromptRequest{RequestID: l.RequestID, Kind: core.ChatPromptPermission, Tool: l.Request.ToolName}
	if l.Request.ToolName == chatQuestionTool && l.Request.RequiresUserInteraction {
		if qs := promptQuestions(l.Request.Input); len(qs) > 0 {
			req.Kind, req.Questions = core.ChatPromptQuestion, qs
			return req
		}
	}
	req.Target = promptTarget(l.Request.ToolName, l.Request.Input)
	return req
}

func promptQuestions(input json.RawMessage) []core.ChatQuestion {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
			MultiSelect bool `json:"multiSelect"`
		} `json:"questions"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	var out []core.ChatQuestion
	for _, q := range in.Questions {
		if q.Question == "" {
			return nil
		}
		cq := core.ChatQuestion{Question: q.Question, Options: []core.ChatQuestionOption{}, MultiSelect: q.MultiSelect}
		for _, o := range q.Options {
			cq.Options = append(cq.Options, core.ChatQuestionOption{Label: o.Label, Description: o.Description})
		}
		out = append(out, cq)
	}
	return out
}

// promptTarget は許可の確認の対象の全文。Bash はコマンドの全文、ほか（コマンドが読めない Bash を含む）は道具の
// 入力の JSON。オーナーが何を許すかを見て決めるので、切らない。
func promptTarget(tool string, input json.RawMessage) string {
	if tool == "Bash" {
		if cmd, ok := inputString(input, "command"); ok {
			return cmd
		}
	}
	return compactJSON(input)
}

// inputString は道具の入力から、名前が key とちょうど一致する文字列の値を返す。構造体へ読むと大文字と小文字の
// 違うキーも一致し、実行される値と違う値を見せうるので、map で読む。
func inputString(input json.RawMessage, key string) (string, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil {
		return "", false
	}
	var v string
	if raw, ok := m[key]; !ok || json.Unmarshal(raw, &v) != nil || v == "" {
		return "", false
	}
	return v, true
}

func compactJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

// toolTargetKeys は道具の一覧の対象の 1 行にする入力の項目。ここに無い道具の対象は空。
var toolTargetKeys = map[string]string{
	"Bash": "command",
	"Read": "file_path", "Write": "file_path", "Edit": "file_path", "MultiEdit": "file_path",
	"NotebookRead": "notebook_path", "NotebookEdit": "notebook_path",
	"Grep": "pattern", "Glob": "pattern", "WebSearch": "query", "ToolSearch": "query",
	"Skill": "skill",
	"Task":  "description", "Agent": "description",
}

var lineBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// toolTargetLine は道具の一覧の対象の 1 行（長さの切り詰めは core が行う）。
func toolTargetLine(tool string, input json.RawMessage) string {
	key, ok := toolTargetKeys[tool]
	if !ok {
		return ""
	}
	v, _ := inputString(input, key)
	return lineBreaks.Replace(v)
}

// classify は返事の終わりの行から終わり方を決める。判別には subtype と is_error を使い、自由記述は、続きの会話が
// 無いことの見分けと枠超過の判定（core.IsRateLimited へそのまま渡す）にだけ使う。ターン数の上限は指定しないので、
// limit にはしない。費用は実行エンジンが返した累計をそのまま渡す。
func (s *conversationStream) classify(l conversationLine, raw string) core.ChatReplyEnd {
	end := core.ChatReplyEnd{State: core.ChatStateDone, CostTotalUSD: l.TotalCostUSD}
	if !l.IsError && l.Subtype == "success" {
		return end
	}
	summary := firstNonEmpty(l.Result, strings.Join(l.Errors, "\n"), s.stderr.String(), l.Subtype, "invoker: the chat reply failed")
	switch {
	case s.sessionLost(raw):
		end.State, end.ErrorSummary = core.ChatStateSessionLost, sessionLostMarker
	case core.IsRateLimited(l.Result) || core.IsRateLimited(s.stderr.String()):
		end.State, end.ErrorSummary = core.ChatStateRateLimited, summary
	default:
		end.State, end.ErrorSummary = core.ChatStateFailed, summary
	}
	return end
}

// sessionLost は、続きの起動で会話が始まらないまま、会話が無い旨が text に出たかを返す。
func (s *conversationStream) sessionLost(text string) bool {
	return s.resume && !s.started && strings.Contains(text, sessionLostMarker)
}

func (s *conversationStream) replyEnded(end core.ChatReplyEnd) {
	s.ended = true
	s.c.awaiting.Store(false)
	// プロセスは返事をまたいで動くので、終わった返事の標準エラーを、後の返事の判定に使わない。
	s.stderr.Reset()
	if s.ev.ReplyEnded != nil {
		s.ev.ReplyEnded(end)
	}
}

// exit はプロセスの終わりを渡す。終わりの行が無いまま、会話が無いことか枠超過が標準エラーにだけ出て終わった
// 場合は、先に返事の終わりを渡す（プロセスの終わりだけでは、呼び出し元が見分けられない）。
func (s *conversationStream) exit(waitErr error, stderr string) {
	if waitErr != nil && (!s.ended || s.c.awaiting.Load()) {
		switch {
		case s.sessionLost(stderr):
			s.replyEnded(core.ChatReplyEnd{State: core.ChatStateSessionLost, ErrorSummary: sessionLostMarker})
		case core.IsRateLimited(stderr):
			s.replyEnded(core.ChatReplyEnd{State: core.ChatStateRateLimited, ErrorSummary: stderr})
		}
	}
	var exit core.ChatExit
	if waitErr != nil {
		exit.ErrorSummary = firstNonEmpty(stderr, waitErr.Error())
	}
	if s.ev.Exited != nil {
		s.ev.Exited(exit)
	}
}

// jsonLine は v を標準入力へ送る 1 行にする。
func jsonLine(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// utteranceLine は発言を送る行。
func utteranceLine(u core.ChatUtterance) ([]byte, error) {
	return jsonLine(map[string]any{"type": "user", "message": map[string]any{
		"role": "user", "content": chatUtteranceText(u.Text, u.Viewing),
	}})
}

// answerLine は確認への答えを送る行。許す答えは、受けた入力をそのまま（問いは answers を足して）返し、その 1 回
// だけを許す。許可の書き足し（updatedPermissions）は組み立てない。断る答えは、どちらの種類の確認にも返せる。
func answerLine(requestID string, req pendingRequest, a core.ChatPromptAnswer) ([]byte, error) {
	var response map[string]any
	switch {
	case a.Decision == core.ChatDecisionDeny && a.Answers == nil:
		message := a.Message
		if message == "" {
			message = chatDefaultDenyMessage
		}
		response = map[string]any{"behavior": "deny", "message": message}
	case a.Decision == core.ChatDecisionAllow && a.Answers == nil && req.kind == core.ChatPromptPermission:
		response = map[string]any{"behavior": "allow", "updatedInput": req.input}
	case a.Decision == "" && len(a.Answers) > 0 && req.kind == core.ChatPromptQuestion:
		var input map[string]any
		dec := json.NewDecoder(bytes.NewReader(req.input))
		dec.UseNumber()
		if err := dec.Decode(&input); err != nil || input == nil {
			return nil, errors.New("invoker: the question input is not a JSON object")
		}
		input["answers"] = a.Answers
		response = map[string]any{"behavior": "allow", "updatedInput": input}
	default:
		return nil, fmt.Errorf("invoker: the answer does not fit the %s prompt", req.kind)
	}
	return jsonLine(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": requestID, "response": response,
	}})
}

package invoker

// このファイルは会話の全文を、実行エンジン（Claude Code）の会話のファイルから読む。置き場所と形の決まりはここに
// 閉じる。読むたびにファイルを読み、どこにも写さない。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// chatTranscriptResultLimit は全文の道具の結果 1 つの長さの上限（文字数）。
const chatTranscriptResultLimit = 4000

// chatInterruptedPrefix は、実行エンジンが割り込みのときに利用者の行として書く文の始まり。
const chatInterruptedPrefix = "[Request interrupted"

var (
	regexpNonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)
	// safeSessionID は、ファイル名に使ってよい会話の識別。
	safeSessionID = regexp.MustCompile(`^[0-9a-zA-Z][0-9a-zA-Z-]*$`)
)

// transcriptPath は会話のファイルを探す。場所は <設定のフォルダ>/projects/<作業フォルダの名前>/<会話の識別>.jsonl。
// 設定のフォルダは CLAUDE_CONFIG_DIR、無ければ ~/.claude。作業フォルダの名前は、実パスの英数字でない文字を - に
// 替えたもの。名前の決まりが合わない場合（長いパスは短くされる）に備え、無ければ会話の識別（一意な UUID）を名前に
// 持つファイルをほかのフォルダからも探す。
func transcriptPath(ref core.ChatTranscriptRef) (string, bool) {
	if !safeSessionID.MatchString(ref.SessionID) {
		return "", false
	}
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		configDir = filepath.Join(home, ".claude")
	}
	projects := filepath.Join(configDir, "projects")
	file := ref.SessionID + ".jsonl"
	regular := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.Mode().IsRegular()
	}
	if ref.WorkDir != "" {
		dir, err := filepath.Abs(ref.WorkDir)
		if err == nil {
			if resolved, err := filepath.EvalSymlinks(dir); err == nil {
				dir = resolved
			}
			if path := filepath.Join(projects, regexpNonAlnum.ReplaceAllString(dir, "-"), file); regular(path) {
				return path, true
			}
		}
	}
	entries, err := os.ReadDir(projects)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if path := filepath.Join(projects, e.Name(), file); regular(path) {
			return path, true
		}
	}
	return "", false
}

// HasTranscript は core.ChatConversationInvoker の実装。
func (l *Launcher) HasTranscript(_ context.Context, ref core.ChatTranscriptRef) bool {
	_, ok := transcriptPath(ref)
	return ok
}

// ReadTranscript は core.ChatConversationInvoker の実装。
func (l *Launcher) ReadTranscript(ctx context.Context, ref core.ChatTranscriptRef) ([]core.ChatTranscriptEntry, error) {
	path, ok := transcriptPath(ref)
	if !ok {
		return nil, fmt.Errorf("%w: no session file for %q", core.ErrChatTranscriptUnavailable, ref.SessionID)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", core.ErrChatTranscriptUnavailable, err)
	}
	defer func() { _ = f.Close() }()

	out := []core.ChatTranscriptEntry{}
	toolNames := map[string]string{} // 道具の使用の識別 → 道具の名前
	br := bufio.NewReader(f)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, readErr := br.ReadBytes('\n')
		if line := bytes.TrimSpace(raw); len(line) > 0 {
			var r transcriptRecord
			// 読めない行（書きかけの行など）は飛ばす。
			if json.Unmarshal(line, &r) == nil {
				out = appendTranscriptEntries(out, r, toolNames)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return out, nil
		}
		if readErr != nil {
			return nil, fmt.Errorf("%w: %w", core.ErrChatTranscriptUnavailable, readErr)
		}
	}
}

// transcriptRecord は会話のファイルの 1 行から、読むものだけを取り出す。
type transcriptRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	// IsMeta は実行エンジンが差し込んだ行、IsSidechain はサブエージェントの行、IsCompactSummary は会話を縮めた要約。
	IsMeta           bool `json:"isMeta"`
	IsSidechain      bool `json:"isSidechain"`
	IsCompactSummary bool `json:"isCompactSummary"`
	// Origin は利用者の行の出どころ。裏の処理の知らせなどは human でない値を持つ。
	Origin *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func appendTranscriptEntries(out []core.ChatTranscriptEntry, r transcriptRecord, toolNames map[string]string) []core.ChatTranscriptEntry {
	if (r.Type != "user" && r.Type != "assistant") || r.IsMeta || r.IsSidechain || r.IsCompactSummary {
		return out
	}
	// 時刻が読めなければゼロ値。
	at, _ := time.Parse(time.RFC3339Nano, r.Timestamp)
	add := func(e core.ChatTranscriptEntry) {
		e.At = at
		out = append(out, e)
	}
	fromOwner := r.Type == "user" && (r.Origin == nil || r.Origin.Kind == "" || r.Origin.Kind == "human")
	ownerText := func(text string) {
		if fromOwner && strings.TrimSpace(text) != "" && !strings.HasPrefix(text, chatInterruptedPrefix) {
			add(core.ChatTranscriptEntry{Kind: core.ChatTranscriptOwner, Text: ownerUtterance(text)})
		}
	}
	var text string
	if json.Unmarshal(r.Message.Content, &text) == nil {
		ownerText(text)
		return out
	}
	for _, b := range contentBlocks(r.Message.Content) {
		switch {
		case b.Type == "text" && r.Type == "user":
			ownerText(b.Text)
		case b.Type == "text" && strings.TrimSpace(b.Text) != "":
			add(core.ChatTranscriptEntry{Kind: core.ChatTranscriptAssistant, Text: b.Text})
		case b.Type == "tool_use" && r.Type == "assistant":
			toolNames[b.ID] = b.Name
			add(core.ChatTranscriptEntry{Kind: core.ChatTranscriptToolUse, Tool: b.Name, Text: compactJSON(b.Input)})
		case b.Type == "tool_result" && r.Type == "user":
			result := toolResultText(b.Content)
			cut := cutTranscriptResult(result)
			add(core.ChatTranscriptEntry{Kind: core.ChatTranscriptToolResult, Tool: toolNames[b.ToolUseID], Text: cut, Truncated: len(cut) < len(result)})
		}
	}
	return out
}

// toolResultText は道具の結果の中身を文字列にする。中身は文字列か塊の配列で、文でない塊は種類の名前だけにする。
func toolResultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []string
	for _, b := range contentBlocks(raw) {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		} else {
			parts = append(parts, "["+b.Type+"]")
		}
	}
	return strings.Join(parts, "\n")
}

func cutTranscriptResult(s string) string {
	n := 0
	for i := range s {
		if n == chatTranscriptResultLimit {
			return s[:i]
		}
		n++
	}
	return s
}

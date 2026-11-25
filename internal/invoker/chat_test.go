package invoker

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const chatSession = "22222222-2222-2222-2222-222222222222"

func resultLine(subtype string, isErr bool, result string, cost string, terminal string) string {
	return `{"type":"result","subtype":"` + subtype + `","is_error":` + strconv.FormatBool(isErr) +
		`,"result":` + strconv.Quote(result) + `,"terminal_reason":"` + terminal + `","total_cost_usd":` + cost + `}` + "\n"
}

func assistantText(text string) string {
	b, _ := json.Marshal(text)
	return `{"type":"assistant","message":{"content":[{"type":"text","text":` + string(b) + `}]}}` + "\n"
}

const initLine = `{"type":"system","subtype":"init","session_id":"s"}` + "\n"

func readArgv(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var argv []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &argv); err != nil {
		t.Fatal(err)
	}
	return argv
}

// argsAfter は flag の後ろから、次の "--" で始まる引数の手前までを返す。
func argsAfter(argv []string, flag string) []string {
	i := slices.Index(argv, flag)
	if i < 0 {
		return nil
	}
	var out []string
	for _, a := range argv[i+1:] {
		if strings.HasPrefix(a, "--") {
			break
		}
		out = append(out, a)
	}
	return out
}

// processAlive は pid が動いているか（ゾンビでないか）を ps で確かめる。
func processAlive(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	s := strings.TrimSpace(string(out))
	return s != "" && !strings.HasPrefix(s, "Z")
}

// AC-143〜149: 指示文に決まった語句が入っている。
func TestChatInstructions_ContainRequiredPhrases(t *testing.T) {
	ins, err := ChatInstructions()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"オーナーの指示が無い承認を記録しない",
		"記録する前に、何が承認されるかの要約を示す",
		"記録した後に、何を記録したかを伝える",
		"ID を単独で出さない",
		"`answer --replan`",
		"課題にして委譲先に任せる",
	} {
		if !strings.Contains(ins, p) {
			t.Errorf("chat.md lacks %q", p)
		}
	}
}

// AC-151: chat.md は分量の上限の検査の対象で、実物は上限内。
func TestChatPrompt_IsInTheSizeCheck(t *testing.T) {
	if judgmentPromptLimit["chat.md"] <= 0 {
		t.Fatal("chat.md is not in the size limit table")
	}
	fsys, err := promptsSubFS()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPromptSizes(fsys); err != nil {
		t.Errorf("CheckPromptSizes(embedded) = %v", err)
	}
}

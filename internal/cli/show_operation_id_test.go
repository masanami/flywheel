package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは `show <OP-ID>`（不可逆操作の中身の表示）と、課題の ID の show が変わらないことを検証する。

const showOpRef = "https://github.com/o/r/pull/12"

// showOpWorkspace は課題 1 件と release 1 件（OP-1）を持つワークスペースを返す。
func showOpWorkspace(t *testing.T) (ws, id string) {
	t.Helper()
	ws = initializedWorkspace(t)
	id = runJSON(t, ws, "create", "--title", "反映する課題")["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "昇格の PR", "--ref", showOpRef)
	return ws, id
}

// showOpConnectors は o/r の既定ブランチを trunk にした宣言。
var showOpConnectors = strings.Replace(releaseConnectorsFixture, `"default_branch": "main"`, `"default_branch": "trunk"`, 1)

func TestShowOperation_TextHasKindChallengeTargetAndState(t *testing.T) {
	t.Parallel()
	ws, id := showOpWorkspace(t)
	writeConnectorsJSONForTest(t, ws, showOpConnectors)

	got := runText(t, ws, "show", "OP-1")
	for _, want := range []string{
		"操作 ID:      OP-1\n",
		"種類:         本番への反映 (release)\n",
		"課題 ID:      " + id + "\n",
		"課題タイトル: 反映する課題\n",
		"リポジトリ:   o/r\n",
		"反映先:       trunk\n",
		"PR:           #12 " + showOpRef + "\n",
		"参照:         " + showOpRef + "\n",
		"要約:         昇格の PR\n",
		"状態:         承認待ち (pending)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("show OP-1 lacks %q:\n%s", want, got)
		}
	}
}

func TestShowOperation_JSON(t *testing.T) {
	t.Parallel()
	ws, id := showOpWorkspace(t)
	writeConnectorsJSONForTest(t, ws, showOpConnectors)

	doc := runJSON(t, ws, "show", "OP-1")
	c := doc["challenge"].(map[string]any)
	if c["id"] != id || c["title"] != "反映する課題" {
		t.Errorf("challenge = %+v", c)
	}
	op := doc["operation"].(map[string]any)
	if op["id"] != "OP-1" || op["kind"] != "release" || op["state"] != "pending" || op["challenge_id"] != id || op["ref"] != showOpRef {
		t.Errorf("operation = %+v", op)
	}
	target := doc["target"].(map[string]any)
	if target["repo"] != "o/r" || target["base_branch"] != "trunk" || target["pr_number"] != float64(12) || target["pr_url"] != showOpRef {
		t.Errorf("target = %+v", target)
	}
}

// 参照も宣言も無い操作は、反映の対象を - と null で出す。
func TestShowOperation_WithoutRefShowsDashes(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := runJSON(t, ws, "create", "--title", "t")["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "op", "add", id, "--kind", "delete", "--summary", "消す")

	got := runText(t, ws, "show", "OP-1")
	for _, want := range []string{"種類:         削除 (delete)\n", "リポジトリ:   -\n", "反映先:       -\n", "PR:           -\n", "参照:         -\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("show OP-1 lacks %q:\n%s", want, got)
		}
	}
	target := runJSON(t, ws, "show", "OP-1")["target"].(map[string]any)
	for _, k := range []string{"repo", "base_branch", "pr_number", "pr_url"} {
		if v, ok := target[k]; !ok || v != nil {
			t.Errorf("target[%q] = %v (present=%v), want null", k, v, ok)
		}
	}
}

func TestShowOperation_ClosedStateShowsReason(t *testing.T) {
	t.Parallel()
	ws, _ := showOpWorkspace(t)
	coretest.CloseOperation(t, ws, 1, "pr_closed", "2026-01-01T00:00:00.000Z")

	if got := runText(t, ws, "show", "OP-1"); !strings.Contains(got, "状態:         閉じた (closed)\n閉じた理由:   pr_closed\n") {
		t.Errorf("closed state:\n%s", got)
	}
}

func TestShowOperation_EscapesControlCharacters(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := runJSON(t, ws, "create", "--title", "題\x1b[31m")["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "op", "add", id, "--kind", "other", "--summary", "要約\x1b[0m", "--ref", "参照\x1b[0m")

	if got := runText(t, ws, "show", "OP-1"); strings.Contains(got, "\x1b") {
		t.Errorf("control characters are not escaped: %q", got)
	}
}

func TestShowOperation_NotFoundMessageContainsGivenID(t *testing.T) {
	t.Parallel()
	ws, _ := showOpWorkspace(t)
	for _, id := range []string{"OP-99", "OP-0", "OP-01", "OP-abc", "OP-"} {
		code, stdout, stderr := runCLIRaw(t, "show", id, "--workspace", ws, "--json")
		if code != 1 || stdout != "" {
			t.Errorf("%s: exit=%d stdout=%q", id, code, stdout)
		}
		e := decodeJSON(t, stderr)["error"].(map[string]any)
		if e["code"] != string(CodeNotFound) || !strings.Contains(e["message"].(string), "不可逆操作 "+id+" ") {
			t.Errorf("%s: error = %+v", id, e)
		}
	}
}

// --plan は課題の ID だけで使える。操作があってもなくても同じ誤りにする。
func TestShowOperation_PlanFlagIsUsageError(t *testing.T) {
	t.Parallel()
	ws, _ := showOpWorkspace(t)
	for _, id := range []string{"OP-1", "OP-99"} {
		code, _, stderr := runCLIRaw(t, "show", id, "--plan", "--workspace", ws, "--json")
		if code != 2 {
			t.Errorf("%s: exit=%d, want 2 (%s)", id, code, stderr)
		}
		e := decodeJSON(t, stderr)["error"].(map[string]any)
		if e["code"] != string(CodeUsageError) || !strings.Contains(e["message"].(string), "課題の ID（C-<n>）だけで使えます") {
			t.Errorf("%s: error = %+v", id, e)
		}
	}
}

var showTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z`)

// 課題の ID の show は、テキストも not found の誤りも変わらない。
func TestShowChallenge_OutputIsUnchanged(t *testing.T) {
	t.Parallel()
	ws, id := showOpWorkspace(t)

	got := showTimestamp.ReplaceAllString(runText(t, ws, "show", id), "<time>")
	got = strings.ReplaceAll(got, resolveExpectedActor(t), "<actor>")
	if got != wantChallengeShowText {
		t.Errorf("show %s text changed:\n%q", id, got)
	}

	code, _, stderr := runCLIRaw(t, "show", "C-99", "--workspace", ws, "--json")
	e := decodeJSON(t, stderr)["error"].(map[string]any)
	if code != 1 || e["code"] != string(CodeNotFound) || e["message"] != "core: not found" {
		t.Errorf("show C-99: exit=%d error=%+v", code, e)
	}
}

func TestShowUsage_AcceptsChallengeAndOperationIDs(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"help"}, {"show", "--help"}} {
		code, stdout, _ := runCLIRaw(t, args...)
		var line string
		for _, l := range strings.Split(stdout, "\n") {
			if strings.Contains(l, "show <ID> [--plan]") {
				line = l
			}
		}
		if code != 0 || !strings.Contains(line, "課題の ID〔C-<n>〕と操作の ID〔OP-<n>〕の両方を受ける") || !strings.Contains(line, "--plan は課題の ID だけ") {
			t.Errorf("%v: exit=%d show line = %q", args, code, line)
		}
	}
}

const wantChallengeShowText = "ID:            C-1\nタイトル:      反映する課題\n状態:          未分類 (unclassified)\n版:            2\n起票者:        <actor>\n緊急度:        -\n優先度:        -\n説明:          \n完了条件:      \n作成:          <time>\n更新:          <time>\n依存先:        -\n依存元:        -\n統合先:        未決\n予算:          不明（枠を決められないタスクがある）\n仕様づくりの費用: 0 USD（予算には数えない）\n紐づいた会話:  0 件\nタスク:        0 件\n計画:          0 版\n承認・差し戻し: 0 件\n保留:          0 件\n不可逆操作:    1 件\n  OP-1 release 昇格の PR [pending]\n取り込み元:    -\n"

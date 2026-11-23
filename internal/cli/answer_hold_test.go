package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S4c-3: `answer --hold`（回答する保留の指定）。--relayed は端末が要らないので、本人確認の道は同じコードを通る。

const twoHoldsPlanSpec = taskAdditionPlanSpec

// twoHoldsWorkspace は、タスク a・b の保留（H-1・H-2。問いは「a への問い」「b への問い」）が開いている、人間対応待ちの課題を作る。
func twoHoldsWorkspace(t *testing.T) (ws, id string) {
	t.Helper()
	ws = initializedWorkspace(t)
	id = createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	coretest.InsertApprovedPlan(t, ws, n, "PLAN-BODY", twoHoldsPlanSpec)
	coretest.InsertTaskHold(t, ws, n, "a", "a への問い", "in_progress", "2026-11-22T00:00:00.000Z", `[{"id":"y","label":"Y 案","description":""}]`)
	coretest.InsertTaskHold(t, ws, n, "b", "b への問い", "in_progress", "2026-11-22T00:00:01.000Z", `[{"id":"x","label":"X 案","description":""}]`)
	coretest.SetChallengeStatus(t, ws, n, "awaiting_human")
	return ws, id
}

func holdRelayArgs() []string { return []string{"--relayed", "--session", "s", "--quote", "答える"} }

func holdAnswers(t *testing.T, ws string) [][]string {
	t.Helper()
	var out [][]string
	for _, r := range coretest.QueryRows(t, ws, `SELECT id, COALESCE(answer, '') FROM hold ORDER BY id`) {
		out = append(out, []string{fmt.Sprint(r[0]), fmt.Sprint(r[1])})
	}
	return out
}

func runAnswerCapture(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runWithLookPath(append(append([]string{}, args...), "--json"), strings.NewReader(""), &out, &errb, defaultCommands(), lookPathFor(t))
	return code, out.String(), errb.String()
}

// AC-78: --relayed で --hold 無しの回答は、開いている保留が 2 つなら検証の誤りで断る。何も書かず、出力に保留の一覧が出る。
func TestRunAnswer_RelayedWithoutHoldIsRefusedWhenTwoHoldsAreOpen(t *testing.T) {
	t.Parallel()
	ws, id := twoHoldsWorkspace(t)
	code, stdout, stderr := runAnswerCapture(t, append([]string{"answer", id, "--answer", "これで", "--workspace", ws}, holdRelayArgs()...)...)
	if code != 1 || stdout != "" {
		t.Fatalf("exit = %d stdout = %q, want 1 and empty", code, stdout)
	}
	var doc struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal([]byte(stderr), &doc); err != nil || doc.Error.Code != string(CodeValidationFailed) {
		t.Fatalf("stderr = %s (%v), want validation_failed", stderr, err)
	}
	for _, want := range []string{"H-1", "H-2", "a への問い", "b への問い"} {
		if !strings.Contains(doc.Error.Message, want) {
			t.Errorf("message lacks %q: %s", want, doc.Error.Message)
		}
	}
	for _, r := range holdAnswers(t, ws) {
		if r[1] != "" {
			t.Errorf("hold %s was answered: %v", r[0], r)
		}
	}
}

// AC-79: --relayed で --hold を付けた回答は、指定した保留に書く。もう一方は空のまま。課題は人間対応待ちのまま。
func TestRunAnswer_RelayedWithHoldAnswersOnlyThatHold(t *testing.T) {
	t.Parallel()
	ws, id := twoHoldsWorkspace(t)
	runJSON(t, ws, append([]string{"answer", id, "--hold", "H-2", "--answer", "b はこれで"}, holdRelayArgs()...)...)
	got := holdAnswers(t, ws)
	if got[0][1] != "" || got[1][1] != "b はこれで" {
		t.Errorf("holds = %v, want only H-2 answered", got)
	}
	if st := workStatus(t, ws, id); st != "awaiting_human" {
		t.Errorf("status = %q, want awaiting_human", st)
	}
	runJSON(t, ws, append([]string{"answer", id, "--hold", "H-1", "--answer", "a もこれで"}, holdRelayArgs()...)...)
	if st := workStatus(t, ws, id); st != "in_progress" {
		t.Errorf("status = %q, want in_progress", st)
	}
}

// AC-69〜71・81: 別の保留の識別・形の違う値は、検証の誤りで断る（何も書かない）。
func TestRunAnswer_HoldAndOptionAreCheckedAgainstTheSpecifiedHold(t *testing.T) {
	t.Parallel()
	ws, id := twoHoldsWorkspace(t)
	for _, hold := range []string{"H-999", "2", "h-1", "H-"} {
		requireErrorCode(t, append([]string{"answer", id, "--hold", hold, "--answer", "x", "--workspace", ws}, holdRelayArgs()...), 1, CodeValidationFailed)
	}
	// AC-81: H-1 にあるのは y。x は H-2 だけの識別。
	requireErrorCode(t, append([]string{"answer", id, "--hold", "H-1", "--option", "x", "--workspace", ws}, holdRelayArgs()...), 1, CodeValidationFailed)
	for _, r := range holdAnswers(t, ws) {
		if r[1] != "" {
			t.Errorf("hold %s was answered: %v", r[0], r)
		}
	}
	// AC-80: 指定した保留の識別なら成立する。
	runJSON(t, ws, append([]string{"answer", id, "--hold", "H-2", "--option", "x"}, holdRelayArgs()...)...)
	if got := holdAnswers(t, ws); got[1][1] != "X 案" {
		t.Errorf("holds = %v, want H-2 answered with the label", got)
	}
}

// AC-72〜74: 本人確認の前の要約に、答える保留の ID・タスクの題名・問いが出る。
func TestAnswerSummaryText_ShowsTheHoldIDTaskTitleAndQuestion(t *testing.T) {
	t.Parallel()
	p := &core.AnswerPreview{ChallengeID: "C-1", Title: "t", HoldID: "H-2", TaskTitle: "タスク b", Question: "b への問い", Answer: "これで"}
	got := answerSummaryText(p, false)
	for _, want := range []string{"H-2", "タスク b", "b への問い"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q: %q", want, got)
		}
	}
}

// AC-71: 空の --hold は「指定なし」に読み替えず、検証の誤りで断る（何も書かない）。
func TestRunAnswer_EmptyHoldIsRefused(t *testing.T) {
	t.Parallel()
	ws, id := twoHoldsWorkspace(t)
	code, _, stderr := runAnswerCapture(t, append([]string{"answer", id, "--hold", "", "--answer", "これで", "--workspace", ws}, holdRelayArgs()...)...)
	if code != 1 || !strings.Contains(stderr, string(CodeValidationFailed)) {
		t.Fatalf("exit = %d stderr = %s, want 1 and validation_failed", code, stderr)
	}
	for _, r := range holdAnswers(t, ws) {
		if r[1] != "" {
			t.Errorf("hold %s was answered: %v", r[0], r)
		}
	}
}

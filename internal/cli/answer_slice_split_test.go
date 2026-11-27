package cli

// 分けるかの保留への回答の CLI（docs/features/request-to-plan-s3.md）。AC の番号は親の Issue の受入基準の並び順。

import (
	"bytes"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

func sliceSplitWorkspace(t *testing.T) string {
	t.Helper()
	ws := initializedWorkspace(t)
	coretest.InsertChallenge(t, ws, "大きい課題")
	coretest.InsertSliceSplitHold(t, ws, 1, "o/app#1")
	return ws
}

// AC-141: 回答の本人確認（--relayed）だけで、split の回答が成立する。show の JSON は区切りごとの challenge_id を持つ。
func TestRunAnswer_SliceSplitNeedsOnlyTheAnswerVerification(t *testing.T) {
	t.Parallel()
	ws := sliceSplitWorkspace(t)
	runJSON(t, ws, "answer", "C-1", "--option", "split", "--relayed", "--session", "s", "--quote", "分けて")
	if got := workStatus(t, ws, "C-1"); got != "withdrawn" {
		t.Errorf("status = %q, want withdrawn", got)
	}
	split := runJSON(t, ws, "show", "C-1")["slice_split"].(map[string]any)
	slices := anySlice(t, split["slices"], "slices")
	for i, want := range []string{"C-2", "C-3"} {
		if got := slices[i].(map[string]any)["challenge_id"]; got != want {
			t.Errorf("slices[%d].challenge_id = %v, want %s", i, got, want)
		}
	}
}

// AC-131・134: 2 件目の区切りの課題の作成が失敗すると、終了コードが 0 でなく、元の課題は人間対応待ちのまま。
func TestRunAnswer_SliceSplitExitsNonZeroWhenAWriteFails(t *testing.T) {
	t.Parallel()
	ws := sliceSplitWorkspace(t)
	coretest.Exec(t, ws, `CREATE TRIGGER inject_failure BEFORE INSERT ON challenge WHEN NEW.title = '区切り B' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	var stdout, stderr bytes.Buffer
	code := runWithLookPath([]string{"answer", "C-1", "--option", "split", "--relayed", "--session", "s", "--quote", "分けて", "--workspace", ws}, strings.NewReader(""), &stdout, &stderr, defaultCommands(), lookPathFor(t))
	if code == 0 {
		t.Fatalf("exit code = 0: %s %s", stdout.String(), stderr.String())
	}
	if got := workStatus(t, ws, "C-1"); got != "awaiting_human" {
		t.Errorf("status = %q, want awaiting_human", got)
	}
	if n := coretest.CountChallenges(t, ws); n != 1 {
		t.Errorf("challenges = %d, want 1", n)
	}
}

// AC-136: 終わっていない委譲の実行があるときの split は、誤りのコード conflict。
func TestRunAnswer_SliceSplitIsConflictWhileDelegating(t *testing.T) {
	t.Parallel()
	ws := sliceSplitWorkspace(t)
	coretest.InsertContractRun(t, ws, 1, "c", "x", "working")
	requireErrorCode(t, []string{"answer", "C-1", "--option", "split", "--relayed", "--session", "s", "--quote", "分けて", "--workspace", ws}, 1, CodeConflict)
}

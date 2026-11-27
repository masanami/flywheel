package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは、flywheel show の文の出力の、要求と区切りを分ける記録の行を確かめる
// （docs/features/request-to-plan-s3.md「記録と表示」）。

const showAt = "2026-11-27T10:00:00.000Z"

// setupSplitRequirement は、取り下げた元の課題 C-1 と、要求 RQ-1 を指す区切りの課題 C-2・C-3 を作る。
func setupSplitRequirement(t *testing.T) string {
	t.Helper()
	ws := initializedWorkspace(t)
	unsetEnv(t, core.ChatEnvSession)
	createTitled(t, ws, "元の課題")
	runJSON(t, ws, "requirement", "create", "--title", "通知")
	for _, title := range []string{"通知の設定を保存する", "通知をまとめて送る"} {
		runJSON(t, ws, "create", "--title", title, "--requirement", "RQ-1")
	}
	coretest.Exec(t, ws, `UPDATE requirement SET origin = 'issue', external_key = 'o/app#41',
		url = 'https://github.com/o/app/issues/41', origin_challenge_id = 1 WHERE id = 1`)
	coretest.Exec(t, ws, `UPDATE challenge SET status = 'done' WHERE id = 2`)
	return ws
}

func assertContains(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Errorf("output does not contain %q:\n%s", want, text)
	}
}

func TestShowText_AC221_RequirementIssueAndTheChallengesOfIt(t *testing.T) {
	ws := setupSplitRequirement(t)
	want := "要求:          RQ-1（元の Issue: https://github.com/o/app/issues/41）\n" +
		"同じ要求の課題:\n" +
		"  C-2  通知の設定を保存する  完了 (done)\n" +
		"  C-3  通知をまとめて送る  未分類 (unclassified)\n"
	for _, id := range []string{"C-2", "C-3", "C-1"} { // C-1 は取り下げた元の課題
		assertContains(t, runText(t, ws, "show", id), want)
	}
}

func TestShowText_RequirementWithoutAnIssueShowsItsOrigin(t *testing.T) {
	ws := setupSplitRequirement(t)
	coretest.Exec(t, ws, `UPDATE requirement SET origin = 'manual', url = NULL, external_key = NULL, origin_challenge_id = NULL`)
	assertContains(t, runText(t, ws, "show", "C-2"), "要求:          RQ-1（出どころ: 手で作った）\n")
	coretest.Exec(t, ws, `UPDATE requirement SET origin = 'chat'`)
	assertContains(t, runText(t, ws, "show", "C-2"), "要求:          RQ-1（出どころ: チャットの欄の会話）\n")
}

func TestShowText_NoRequirementAndNoSliceSplitAddsNoLines(t *testing.T) {
	ws := initializedWorkspace(t)
	createTitled(t, ws, "t")
	text := runText(t, ws, "show", "C-1")
	for _, absent := range []string{"要求:", "同じ要求の課題", "区切りを分ける記録"} {
		if strings.Contains(text, absent) {
			t.Errorf("output contains %q:\n%s", absent, text)
		}
	}
}

func TestShowText_AC222_SliceSplitStateAndSliceNumbersAndTitles(t *testing.T) {
	ws := initializedWorkspace(t)
	createTitled(t, ws, "大きな課題")
	coretest.Exec(t, ws, `INSERT INTO slice_split (challenge_id, state, classify_reason, proposal, evidence_path, created_at, updated_at)
		VALUES (1, 'awaiting_owner', '理由', ?, '.flywheel/requirements/split-C-1.md', ?, ?)`,
		`{"reason":"r","slices":[{"title":"設定を保存する"},{"title":"まとめて\u001b[31m送る","depends_on":[1]}],"evidence":[]}`, showAt, showAt)

	text := runText(t, ws, "show", "C-1")
	assertContains(t, text, "区切りを分ける記録: awaiting_owner\n  1. 設定を保存する\n  2. まとめて")
	if strings.ContainsRune(text, '\x1b') {
		t.Errorf("a control character reaches the terminal:\n%q", text)
	}

	// 案の無い記録は、状態だけを出す。
	coretest.Exec(t, ws, `UPDATE slice_split SET state = 'pending', proposal = NULL, evidence_path = NULL`)
	assertContains(t, runText(t, ws, "show", "C-1"), "区切りを分ける記録: pending\n")
}

func TestCycleText_SettlePhaseShowsTheResolvedCount(t *testing.T) {
	text := cycleResultText(&core.CycleRunResult{
		Cycle:  &core.Cycle{ID: "Y-1"},
		Phases: []core.CyclePhaseResult{{Phase: core.CyclePhaseSettle, ResolvedRequirements: []string{"RQ-1", "RQ-2"}}},
	})
	assertContains(t, text, "  settle: 解決した要求 2 件\n")
}

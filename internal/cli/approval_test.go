package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは approve・reject・answer のうち、端末を開く前（core.Verify を
// 呼ぶ前）に決着する経路を in-process の run(...) で検証する。端末を要する
// 経路（成功・tty_required・confirmation_mismatch・conflict 等）は疑似端末を
// 使う approval_process_test.go が担う。

// --- --hold-release（#13 の先取りをしないための CLI 側の入力規則） ---

func TestRunApprove_HoldReleaseOnPlanApprovalIsUsageError(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	planPath := filepath.Join(t.TempDir(), "plan.txt")
	if err := os.WriteFile(planPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	runJSON(t, ws, "plan", id, "--file", planPath)

	requireErrorCode(t, []string{"approve", id, "--hold-release", "--workspace", ws}, 2, CodeUsageError)

	show := runJSON(t, ws, "show", id)
	c := show["challenge"].(map[string]any)
	// create=1 -> classify=2 -> plan=3。usage_error はここから変わらない。
	if c["status"] != "awaiting_plan_approval" || c["version"] != float64(3) {
		t.Errorf("challenge changed despite usage_error: %+v", c)
	}
}

func TestRunApprove_HoldReleaseOnCompletionApprovalIsInternalErrorNotYetImplemented(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "awaiting_completion_approval")

	requireErrorCode(t, []string{"approve", id, "--hold-release", "--workspace", ws}, 2, CodeInternalError)

	show := runJSON(t, ws, "show", id)
	c := show["challenge"].(map[string]any)
	if c["status"] != "awaiting_completion_approval" || c["version"] != float64(1) {
		t.Errorf("challenge changed despite internal_error stub: %+v", c)
	}
}

// --- AC-48: 理由が空の reject は validation_failed（端末を開く前に決着する） ---

func TestRunReject_EmptyReasonIsValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	planPath := filepath.Join(t.TempDir(), "plan.txt")
	if err := os.WriteFile(planPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	runJSON(t, ws, "plan", id, "--file", planPath)

	requireErrorCode(t, []string{"reject", id, "--reason", "   ", "--workspace", ws}, 1, CodeValidationFailed)
}

// --- 対象の ID が存在しない／終端状態（端末を開く前に決着する） ---

func TestRunApprove_NotFoundForMissingID(t *testing.T) {
	ws := initializedWorkspace(t)
	requireErrorCode(t, []string{"approve", "C-999", "--workspace", ws}, 1, CodeNotFound)
}

func TestRunAnswer_WrongStateIsInvalidTransition(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)

	requireErrorCode(t, []string{"answer", id, "--answer", "a", "--workspace", ws}, 1, CodeInvalidTransition)
}

// --- 要約テキストの組み立て（純粋関数の単体テスト） ---

func TestApprovalSummaryText_PlanIncludesBodyAndVersion(t *testing.T) {
	p := &core.ApprovalPreview{
		ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindPlan,
		PlanVersion: 2, PlanBody: "do the thing",
	}
	got := approvalSummaryText(p, nil)
	for _, want := range []string{"C-1", "t", "計画", "do the thing", "2"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "理由") {
		t.Errorf("summary = %q, must not contain a reason line for approve", got)
	}
}

func TestApprovalSummaryText_CompletionIncludesDoneCriteria(t *testing.T) {
	p := &core.ApprovalPreview{
		ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindCompletion,
		DoneCriteria: "it works",
	}
	got := approvalSummaryText(p, nil)
	for _, want := range []string{"C-1", "t", "完了", "it works"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary = %q, want it to contain %q", got, want)
		}
	}
}

func TestApprovalSummaryText_RejectIncludesReason(t *testing.T) {
	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindPlan, PlanVersion: 1, PlanBody: "x"}
	reason := "not ready"
	got := approvalSummaryText(p, &reason)
	if !strings.Contains(got, reason) {
		t.Errorf("summary = %q, want it to contain the reason %q", got, reason)
	}
}

// TestApprovalSummaryText_UnknownKindDoesNotLabelAsPlanOrCompletion は
// self-review 指摘の再発防止（ラウンド2）: PrepareApproval は現状 plan／
// completion の2値しか返さないが、ApprovalKind 自体は release を含む3値の
// 閉集合。未知の Kind を確信を持った誤ったラベル（「計画」）で見せてしまうと、
// 人間が何を承認しているかを誤認したまま確認入力を打ちかねない
// （runApprove の --hold-release 判定が同じ3値目を理由に fail-closed に
// 拒否しているのと非対称だった）。
func TestApprovalSummaryText_UnknownKindDoesNotLabelAsPlanOrCompletion(t *testing.T) {
	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindRelease}
	got := approvalSummaryText(p, nil)
	if strings.Contains(got, "種類:  計画") || strings.Contains(got, "種類:  完了") {
		t.Errorf("summary = %q, must not confidently mislabel an unknown kind as plan/completion", got)
	}
}

func TestAnswerSummaryText_IncludesQuestionAndAnswer(t *testing.T) {
	p := &core.AnswerPreview{ChallengeID: "C-1", Title: "t", Question: "why?", Answer: "because"}
	got := answerSummaryText(p)
	for _, want := range []string{"C-1", "t", "why?", "because"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary = %q, want it to contain %q", got, want)
		}
	}
}

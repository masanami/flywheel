package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S4c-2 の 4: followup promote --preview（AC-20〜AC-26）。

type previewDoc struct {
	BudgetImplUSD        *float64 `json:"budget_impl_usd"`
	BudgetReviewUSD      *float64 `json:"budget_review_usd"`
	Destination          string   `json:"destination"`
	EstimateAfterUSD     *float64 `json:"estimate_after_usd"`
	ExceedsByUSD         *float64 `json:"exceeds_by_usd"`
	FollowupID           string   `json:"followup_id"`
	LimitAfterUSD        *float64 `json:"limit_after_usd"`
	LimitUSD             *float64 `json:"limit_usd"`
	NewChallengeReason   *string  `json:"new_challenge_reason"`
	TargetChallengeID    *string  `json:"target_challenge_id"`
	TargetChallengeTitle *string  `json:"target_challenge_title"`
}

func previewRun(t *testing.T, ws, id string, extra ...string) previewDoc {
	t.Helper()
	// 本人確認の指定（--relayed）も、端末も使わない（標準入力は空）。
	code, stdout, stderr := fuRun(t, ws, append([]string{"followup", "promote", id, "--preview"}, extra...)...)
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	var doc previewDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	return doc
}

// AC-20〜AC-23・AC-26: 上げる先の種類・足すタスクの枠・足した後の見込み・上限を、本人確認なしで返す。
func TestFollowupPromotePreview_ReturnsKindFramesEstimateAndLimit_AC20_AC21_AC22_AC23_AC26(t *testing.T) {
	ws := addableFollowupWorkspace(t)
	doc := previewRun(t, ws, "F-1", "--budget-impl-usd", "1", "--budget-review-usd", "0.5")
	if doc.Destination != "task" || doc.FollowupID != "F-1" || doc.TargetChallengeID == nil || *doc.TargetChallengeID != "C-1" || doc.TargetChallengeTitle == nil || *doc.TargetChallengeTitle == "" { // AC-20
		t.Errorf("kind/target = %+v", doc)
	}
	if doc.BudgetImplUSD == nil || *doc.BudgetImplUSD != 1 || doc.BudgetReviewUSD == nil || *doc.BudgetReviewUSD != 0.5 { // AC-21
		t.Errorf("frames = %v / %v", doc.BudgetImplUSD, doc.BudgetReviewUSD)
	}
	if doc.EstimateAfterUSD == nil || *doc.EstimateAfterUSD != 11.5 { // AC-22（見込み 10 USD + 1.5）
		t.Errorf("estimate_after_usd = %v, want 11.5", doc.EstimateAfterUSD)
	}
	if doc.LimitUSD == nil || *doc.LimitUSD != 14 { // AC-23
		t.Errorf("limit_usd = %v, want 14", doc.LimitUSD)
	}
	if doc.ExceedsByUSD == nil || *doc.ExceedsByUSD != 0 || doc.LimitAfterUSD == nil || *doc.LimitAfterUSD != 14 || doc.NewChallengeReason != nil {
		t.Errorf("within the limit: exceeds=%v limitAfter=%v reason=%v", doc.ExceedsByUSD, doc.LimitAfterUSD, doc.NewChallengeReason)
	}
}

// AC-24: 足した後の見込みが上限を超えるときは、超える額と上げた後の上限を返す（断らない）。
func TestFollowupPromotePreview_OverLimitReturnsTheExceedingAmount_AC24(t *testing.T) {
	ws := addableFollowupWorkspace(t)
	doc := previewRun(t, ws, "F-1", "--budget-impl-usd", "30", "--budget-review-usd", "25")
	if doc.Destination != "task" || doc.EstimateAfterUSD == nil || *doc.EstimateAfterUSD != 65 || doc.LimitUSD == nil || *doc.LimitUSD != 14 {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.ExceedsByUSD == nil || *doc.ExceedsByUSD != 51 || doc.LimitAfterUSD == nil || *doc.LimitAfterUSD != 65 {
		t.Errorf("exceeds_by_usd = %v, limit_after_usd = %v, want 51 and 65", doc.ExceedsByUSD, doc.LimitAfterUSD)
	}
}

// 上げる先が新しい課題になるときは、種類と、タスクにしなかった理由を返し、タスクの値は null。
func TestFollowupPromotePreview_NewChallengeCarriesTheReason(t *testing.T) {
	ws := addableFollowupWorkspace(t)
	doc := previewRun(t, ws, "F-1", "--new-challenge")
	if doc.Destination != "challenge" || doc.NewChallengeReason == nil || *doc.NewChallengeReason == "" {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.TargetChallengeID != nil || doc.BudgetImplUSD != nil || doc.EstimateAfterUSD != nil || doc.LimitUSD != nil || doc.ExceedsByUSD != nil {
		t.Errorf("task-only values must be null: %+v", doc)
	}
}

// AC-25: --preview は、フォローアップ・課題・足す依頼・作業ログ・周の起動のどれも書き換えない。
func TestFollowupPromotePreview_WritesNothing_AC25(t *testing.T) {
	ws := addableFollowupWorkspace(t)
	snapshot := func() string {
		var b strings.Builder
		for _, q := range []string{`SELECT * FROM followup`, `SELECT id, version, status, updated_at FROM challenge`, `SELECT * FROM task_addition`, `SELECT COUNT(*) FROM activity`, `SELECT COUNT(*) FROM cycle`, `SELECT COUNT(*) FROM run`} {
			for _, row := range coretest.QueryRows(t, ws, q) {
				b.WriteString(strings.TrimSpace(strings.Join(strings.Fields(toString(row)), " ")) + "\n")
			}
		}
		return b.String()
	}
	before := snapshot()
	previewRun(t, ws, "F-1")
	previewRun(t, ws, "F-1", "--new-challenge")
	if after := snapshot(); after != before {
		t.Errorf("--preview changed the store:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func toString(row []any) string {
	var b bytes.Buffer
	_ = json.NewEncoder(&b).Encode(row)
	return b.String()
}

// AC-26: --preview は、端末でない標準入力でも、--relayed なしで通る。--preview でも、存在しないフォローアップ・上げられないものは断る。
func TestFollowupPromotePreview_NeedsNoConfirmationButRefusesWhatCannotBePromoted_AC26(t *testing.T) {
	ws := addableFollowupWorkspace(t)
	if code, _, stderr := fuRun(t, ws, "followup", "promote", "F-1", "--preview"); code != 0 {
		t.Errorf("exit=%d stderr=%s, want 0 without a confirmation", code, stderr)
	}
	if code, _, stderr := fuRun(t, ws, "followup", "promote", "F-9", "--preview"); code == 0 || readErrorCode(t, stderr) != "not_found" {
		t.Errorf("unknown followup: exit=%d stderr=%s, want not_found", code, stderr)
	}
}

// 要約（本人確認の前に見せる文）と --preview の文は同じ値を出す。上限を超えるときは、超える額と上げた後の上限も出す。
func TestFollowupPromotePreview_TextShowsTheSameValuesAsTheSummary(t *testing.T) {
	ws := addableFollowupWorkspace(t)
	var out, errb bytes.Buffer
	args := []string{"followup", "promote", "F-1", "--preview", "--budget-impl-usd", "30", "--budget-review-usd", "25", "--workspace", ws}
	if code := runWithLookPath(args, strings.NewReader(""), &out, &errb, defaultCommands(), lookPathFor(t)); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	for _, want := range []string{"C-1", "実装 30 USD・レビュー対応 25 USD", "足した後の見込み: 65 USD（上限 14 USD）", "上限を超える額:   51 USD（上げた後の上限 65 USD）"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, out.String())
		}
	}
}

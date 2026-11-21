package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// #658: 取り消して人の判断を待っている操作は、show のテキストに直前の試行の結果と理由（取り込みが続いた旨）が出る。
func TestChallengeDetailText_ShowsReasonOfCancelledOperation(t *testing.T) {
	res := core.ExecutionResultLimitReached
	detail := "既定ブランチが速く進み、取り込みが 3 回続いたため止めました"
	d := &core.ChallengeDetail{Operations: []core.IrreversibleOperation{{
		ID: "OP-1", Kind: core.OperationKindRelease, Summary: "merge PR", State: core.OperationStatePending,
		LastExecutionResult: &res, LastExecutionDetail: &detail,
	}}}
	got := challengeDetailText(d)
	if !strings.Contains(got, "直前の試行: limit_reached（"+detail+"）") {
		t.Errorf("show text does not carry the reason:\n%s", got)
	}

	// 取り消しでない結果は「直前の試行」には出さない（「最後の試行」に出る）。
	waiting := core.ExecutionResultWaitingChecks
	d.Operations[0].LastExecutionResult = &waiting
	if got := challengeDetailText(d); strings.Contains(got, "直前の試行") {
		t.Errorf("non-cancel result should not be shown:\n%s", got)
	}
}

// S3a: land_refused は取り消す結果なので「直前の試行」に委譲先の理由が出る。land_sent（待つ）は「最後の試行」に語が出る。
func TestChallengeDetailText_ShowsLastAttemptOfDelegatedRelease(t *testing.T) {
	refused, why := core.ExecutionResultLandRefused, "コンフリクトがあり入れられなかった"
	d := &core.ChallengeDetail{Operations: []core.IrreversibleOperation{{
		ID: "OP-1", Kind: core.OperationKindRelease, Summary: "反映", State: core.OperationStatePending,
		LastExecutionResult: &refused, LastExecutionDetail: &why,
	}}}
	if got := challengeDetailText(d); !strings.Contains(got, "直前の試行: land_refused（"+why+"）") {
		t.Errorf("land_refused reason missing:\n%s", got)
	}
	sent, sentDetail := core.ExecutionResultLandSent, "待っています\x1b[0m"
	d.Operations[0].State, d.Operations[0].LastExecutionResult, d.Operations[0].LastExecutionDetail = core.OperationStateApproved, &sent, &sentDetail
	got := challengeDetailText(d)
	if !strings.Contains(got, "詳細: 待っています") || strings.Contains(got, "\x1b") {
		t.Errorf("detail of a waiting result missing or not escaped:\n%q", got)
	}
	if !strings.Contains(got, "最後の試行: land_sent（委譲先へ「入れてよい」を返した）") {
		t.Errorf("land_sent word missing:\n%s", got)
	}
}

// S3a: status の文の出力は、反映の操作の行に最後の試行の結果の語を出す（承認済みの land_sent・未承認へ戻った land_refused）。
func TestOverviewText_ShowsLastAttemptWordOfReleaseRows(t *testing.T) {
	sent, refused := core.ExecutionResultLandSent, core.ExecutionResultLandRefused
	ov := &core.Overview{
		ApprovedOperations:   []core.IrreversibleOperation{{ID: "OP-1", ChallengeID: "C-1", Kind: core.OperationKindRelease, Summary: "反映", LastExecutionResult: &sent}},
		NeedsHumanOperations: []core.IrreversibleOperation{{ID: "OP-2", ChallengeID: "C-2", Kind: core.OperationKindRelease, Summary: "反映", LastExecutionResult: &refused}},
	}
	got := overviewText(ov)
	for _, want := range []string{"OP-1\tC-1\trelease\t反映\t最後の試行: land_sent（委譲先へ「入れてよい」を返した）", "OP-2\tC-2\trelease\t反映\t最後の試行: land_refused（委譲先が入れられなかった）"} {
		if !strings.Contains(got, want) {
			t.Errorf("status text lacks %q:\n%s", want, got)
		}
	}
}

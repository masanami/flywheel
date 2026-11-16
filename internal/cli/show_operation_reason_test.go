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

	// 承認済み（実行待ち）の操作・取り消しでない結果には出さない。
	waiting := core.ExecutionResultWaitingChecks
	d.Operations[0].LastExecutionResult = &waiting
	if got := challengeDetailText(d); strings.Contains(got, "直前の試行") {
		t.Errorf("non-cancel result should not be shown:\n%s", got)
	}
}

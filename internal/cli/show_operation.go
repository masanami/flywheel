package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// runShowOperation は `flywheel show <OP-ID>` の実装。core.GetOperation を呼んで表示するだけで、何も書かない。
func runShowOperation(a Args, id string) (any, error) {
	if a.Bools["plan"] {
		return nil, NewError(CodeUsageError, "--plan は課題の ID（C-<n>）だけで使えます。操作の ID（OP-<n>）には指定できません")
	}
	// 宣言は反映先のブランチに使う（読めなくても操作は出し、反映先は - になる）。
	decl, err := core.LoadConnectorsDeclaration(a.Store.Workspace())
	if err != nil {
		decl = nil
	}
	d, err := a.Store.GetOperation(context.Background(), id, decl)
	if errors.Is(err, core.ErrNotFound) {
		return nil, NewError(CodeNotFound, fmt.Sprintf("不可逆操作 %s は見つかりません", escapeControl(id)))
	}
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.FromOperationDetail(d), text: operationDetailText(d)}, nil
}

// operationKindLabels・operationStateLabels は `show <OP-ID>` のテキストの語。
var operationKindLabels = map[core.OperationKind]string{
	core.OperationKindRelease:      "本番への反映",
	core.OperationKindDelete:       "削除",
	core.OperationKindExternalSend: "外部への送信",
	core.OperationKindOther:        "その他",
}

var operationStateLabels = map[core.OperationState]string{
	core.OperationStatePending:  "承認待ち",
	core.OperationStateApproved: "承認済み",
	core.OperationStateRejected: "差し戻し",
	core.OperationStateExecuted: "実行済み",
	core.OperationStateClosed:   "閉じた",
}

// operationDetailText は --json 無しの `show <OP-ID>` の表示。
func operationDetailText(d *core.OperationDetail) string {
	op, t := d.Operation, d.Target
	orDash := func(s *string) string {
		if s == nil {
			return "-"
		}
		return escapeControl(*s)
	}
	pr := "-"
	if t.PullRequestNumber != nil && t.PullRequestURL != nil {
		pr = "#" + strconv.Itoa(*t.PullRequestNumber) + " " + escapeControl(*t.PullRequestURL)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "操作 ID:      %s\n", op.ID)
	fmt.Fprintf(&b, "種類:         %s (%s)\n", operationKindLabels[op.Kind], op.Kind)
	fmt.Fprintf(&b, "課題 ID:      %s\n", op.ChallengeID)
	fmt.Fprintf(&b, "課題タイトル: %s\n", escapeControl(d.ChallengeTitle))
	fmt.Fprintf(&b, "リポジトリ:   %s\n", orDash(t.Repo))
	fmt.Fprintf(&b, "反映先:       %s\n", orDash(t.BaseBranch))
	fmt.Fprintf(&b, "PR:           %s\n", pr)
	fmt.Fprintf(&b, "参照:         %s\n", orDash(op.Ref))
	fmt.Fprintf(&b, "要約:         %s\n", escapeControl(op.Summary))
	fmt.Fprintf(&b, "状態:         %s (%s)\n", operationStateLabels[op.State], op.State)
	if op.ClosedReason != nil {
		fmt.Fprintf(&b, "閉じた理由:   %s\n", *op.ClosedReason)
	}
	if op.SupersededByOperationID != nil {
		fmt.Fprintf(&b, "置き換え先:   %s\n", *op.SupersededByOperationID)
	}
	if op.LastExecutionResult != nil {
		fmt.Fprintf(&b, "最後の試行:   %s（%s）\n", *op.LastExecutionResult, view.ExecutionResultLabel(*op.LastExecutionResult))
		if op.LastExecutionDetail != nil {
			fmt.Fprintf(&b, "  詳細:       %s\n", escapeControl(*op.LastExecutionDetail))
		}
	}
	return b.String()
}

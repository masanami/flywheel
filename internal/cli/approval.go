package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは本人確認つきの操作（approve・reject・answer）の実コマンドを
// 持つ（Issue #12・#13）。core の2段階 API（① Prepare*＝読み取り、②
// Execute*＝書き込み）を、① の結果から人間向けの要約テキストを組み、
// core.Verify(core.ChannelCLI, tty_confirm Verifier, summary, 対象ID) で
// 確認を得てから ② を呼ぶ、という決まった形で使う。遷移の可否・承認の
// 成立条件・作業ログの記録はすべて core が持ち、ここには書かない（P2・P4）。
//
// approve・reject は対象 ID の形（"C-<n>" か "OP-<n>"）で、課題の承認
// （PrepareApproval／ExecuteApproval）と不可逆操作の単独の承認
// （PrepareOperationApproval／ExecuteOperationApproval）を振り分ける（#13）。

// isOperationID は id が不可逆操作の ID の表示形（"OP-" 始まり）かどうかを
// 判定する。実際の形式検査（正の整数か等）は core.parseOperationID が担う
// （非公開のため CLI からは呼べない）。ここでの判定は「課題向けの API へ
// 進むか、不可逆操作向けの API へ進むか」の振り分けだけに使い、"OP-" で
// 始まる不正な形（例: "OP-0"）は不可逆操作向けの Prepare* が ErrNotFound を
// 返すことで fail-closed に拒否される。
func isOperationID(id string) bool {
	return strings.HasPrefix(id, "OP-")
}

// runApprove は `flywheel approve <ID> [--hold-release]` の実装
// （T5・T13・T14。#13: 不可逆操作の ID には単独の承認として動く）。
func runApprove(a Args) (any, error) {
	id := a.Positional[0]
	holdRelease := a.Bools["hold-release"]

	if isOperationID(id) {
		return runApproveOperation(a, id, holdRelease)
	}
	return runApproveChallenge(a, id, holdRelease)
}

// runApproveOperation は `flywheel approve <OP-ID> [--hold-release]` の実装。
// --hold-release は不可逆操作の ID には指定できない（usage_error。
// docs/features/m1-core.md §承認・§受入基準「不可逆操作と D12」）。
func runApproveOperation(a Args, id string, holdRelease bool) (any, error) {
	// --hold-release の可否は ID の形だけで静的に決まるため、ストアへ触れる前に
	// 拒否する（self-review 指摘: Prepare の後に置くと、存在しない OP-ID では
	// not_found、承認済みの OP-ID では invalid_transition が先に返り、入力規則の
	// 違反が状態次第で別の終了コードになっていた）。
	if holdRelease {
		return nil, NewError(CodeUsageError, "不可逆操作の ID への --hold-release は指定できません")
	}

	preview, err := a.Store.PrepareOperationApproval(context.Background(), id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	summary := operationApprovalSummaryText(preview, nil)
	att, err := core.Verify(core.ChannelCLI, newTTYConfirmVerifier(a.Stdin), summary, id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	op, approval, err := a.Store.ExecuteOperationApproval(context.Background(), core.OperationApprovalRequest{
		OperationID:              id,
		ExpectedVersion:          preview.Version,
		ExpectedChallengeVersion: preview.ChallengeVersion,
		Decision:                 core.ApprovalDecisionApproved,
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.OperationApprovalResponse{Approval: view.FromApproval(*approval), Operation: view.FromOperation(*op)},
		text: op.ID + "\n",
	}, nil
}

// runApproveChallenge は `flywheel approve <C-ID> [--hold-release]` の実装
// （T5・T13・T14）。--hold-release は完了確認待ちの課題にだけ指定できる。
// 計画承認待ちの課題への指定は usage_error（fail-closed。core.Verify を
// 呼ぶ前＝端末を開く前に拒否する）。完了確認待ちなら core.ExecuteApproval に
// HoldRelease をそのまま渡す（T14。D12 の release の一括承認を core 側で
// 抑止する）。
func runApproveChallenge(a Args, id string, holdRelease bool) (any, error) {
	preview, err := a.Store.PrepareApproval(context.Background(), id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	if holdRelease {
		switch preview.Kind {
		case core.ApprovalKindPlan:
			return nil, NewError(CodeUsageError, "計画承認待ちの課題への --hold-release は指定できません")
		case core.ApprovalKindCompletion:
			// 完了確認待ち: T14 として下の通常経路（本人確認→Execute）へ進む。
		default:
			// PrepareApproval が返す Kind は現状 plan／completion の2値だけだが、
			// ApprovalKind 自体は release を含む3値の閉集合。ここで default を
			// 落とすと、未知の Kind に対して --hold-release が黙って無視され
			// 通常の approve が成立してしまう fail-open になる
			// （self-review 指摘）。到達しないはずだが fail-closed に拒否する。
			return nil, NewError(CodeInternalError, "未対応の承認の種類です: "+string(preview.Kind))
		}
	}

	summary := approvalSummaryText(preview, core.ApprovalDecisionApproved, holdRelease, nil)
	att, err := core.Verify(core.ChannelCLI, newTTYConfirmVerifier(a.Stdin), summary, id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	c, approval, err := a.Store.ExecuteApproval(context.Background(), core.ApprovalRequest{
		ChallengeID:     id,
		ExpectedVersion: preview.Version,
		Decision:        core.ApprovalDecisionApproved,
		HoldRelease:     holdRelease,
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.ChallengeApprovalResponse{Approval: view.FromApproval(*approval), Challenge: view.FromChallenge(*c)},
		text: challengeText(*c),
	}, nil
}

// runReject は `flywheel reject <ID> --reason <r>` の実装
// （T6・T15。#13: 不可逆操作の ID には単独の差し戻しとして動く）。
func runReject(a Args) (any, error) {
	id := a.Positional[0]
	reason := a.Values["reason"]

	if isOperationID(id) {
		return runRejectOperation(a, id, reason)
	}
	return runRejectChallenge(a, id, reason)
}

// runRejectOperation は `flywheel reject <OP-ID> --reason <r>` の実装。
func runRejectOperation(a Args, id string, reason string) (any, error) {
	preview, err := a.Store.PrepareOperationRejection(context.Background(), id, reason)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	summary := operationApprovalSummaryText(preview, &reason)
	att, err := core.Verify(core.ChannelCLI, newTTYConfirmVerifier(a.Stdin), summary, id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	op, approval, err := a.Store.ExecuteOperationApproval(context.Background(), core.OperationApprovalRequest{
		OperationID:              id,
		ExpectedVersion:          preview.Version,
		ExpectedChallengeVersion: preview.ChallengeVersion,
		Decision:                 core.ApprovalDecisionRejected,
		Reason:                   &reason,
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.OperationApprovalResponse{Approval: view.FromApproval(*approval), Operation: view.FromOperation(*op)},
		text: op.ID + "\n",
	}, nil
}

// runRejectChallenge は `flywheel reject <C-ID> --reason <r>` の実装（T6・T15）。
func runRejectChallenge(a Args, id string, reason string) (any, error) {
	preview, err := a.Store.PrepareRejection(context.Background(), id, reason)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	summary := approvalSummaryText(preview, core.ApprovalDecisionRejected, false, &reason)
	att, err := core.Verify(core.ChannelCLI, newTTYConfirmVerifier(a.Stdin), summary, id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	c, approval, err := a.Store.ExecuteApproval(context.Background(), core.ApprovalRequest{
		ChallengeID:     id,
		ExpectedVersion: preview.Version,
		Decision:        core.ApprovalDecisionRejected,
		Reason:          &reason,
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.ChallengeApprovalResponse{Approval: view.FromApproval(*approval), Challenge: view.FromChallenge(*c)},
		text: challengeText(*c),
	}, nil
}

// runAnswer は `flywheel answer <ID> --answer <a>` の実装（T12）。
func runAnswer(a Args) (any, error) {
	id := a.Positional[0]
	answer := a.Values["answer"]

	preview, err := a.Store.PrepareAnswer(context.Background(), id, answer)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	summary := answerSummaryText(preview)
	att, err := core.Verify(core.ChannelCLI, newTTYConfirmVerifier(a.Stdin), summary, id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	c, hold, err := a.Store.ExecuteAnswer(context.Background(), core.AnswerRequest{
		ChallengeID:     id,
		ExpectedVersion: preview.Version,
		Answer:          answer,
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.ChallengeHoldResponse{Challenge: view.FromChallenge(*c), Hold: view.FromHold(*hold)},
		text: challengeText(*c),
	}, nil
}

// approvalSummaryText は approve・reject が確認前に /dev/tty へ表示する要約
// （AC-39: 課題の ID・タイトル・承認の種類、計画の承認なら本文と版／
// AC-41: reject は対応する承認と同じ要約＋入力された理由）。
// reason が非 nil なら reject の要約として理由を末尾に加える。
// decision・holdRelease は、不可逆操作の一覧を「同時に承認される」「承認され
// ない」へ振り分けるために core.ApprovalPreview.ReleaseEffect へ渡す（D12 の
// 規則そのものは core が持ち、ここでは表示だけを行う）。
func approvalSummaryText(p *core.ApprovalPreview, decision core.ApprovalDecision, holdRelease bool, reason *string) string {
	var b strings.Builder
	// 種類ラベル・本文の両方を switch で明示的に分岐する（self-review 指摘:
	// 以前は if/else で「completion でなければ計画」としており、
	// runApprove の --hold-release 判定（同ファイル）は3値目の Kind を
	// default で fail-closed に拒否しているのに、要約の組み立てだけは
	// 未知の種類を黙って「計画」として表示してしまう非対称があった。
	// PrepareApproval が返す Kind は現状 plan／completion の2値だけだが、
	// ApprovalKind 自体は release を含む3値の閉集合。到達しないはずだが、
	// 未知の値を確信を持った誤ったラベルで見せないようにする）。
	var kindLabel string
	switch p.Kind {
	case core.ApprovalKindPlan:
		kindLabel = "計画"
	case core.ApprovalKindCompletion:
		kindLabel = "完了"
	default:
		kindLabel = "不明（" + string(p.Kind) + "）"
	}
	fmt.Fprintf(&b, "課題 ID:     %s\n", p.ChallengeID)
	fmt.Fprintf(&b, "タイトル:    %s\n", p.Title)
	fmt.Fprintf(&b, "承認の種類:  %s\n", kindLabel)
	if p.Kind == core.ApprovalKindPlan {
		fmt.Fprintf(&b, "計画 v%d:\n%s\n", p.PlanVersion, p.PlanBody)
	} else {
		fmt.Fprintf(&b, "完了条件:    %s\n", p.DoneCriteria)
	}
	if p.Kind == core.ApprovalKindCompletion {
		// D12: 完了の承認の要約は、同時に承認される release と、同時には
		// 承認されない不可逆操作の一覧を含む（#13）。どちらへ振り分けるかは
		// core が今回の操作（承認か差し戻しか・--hold-release か）から決める
		// ため、要約は常に実際の結果と一致する（self-review 指摘）。
		effect := p.ReleaseEffect(decision, holdRelease)
		fmt.Fprintf(&b, "同時に承認される本番反映 (release): %s\n", operationListSummary(effect.Approved))
		fmt.Fprintf(&b, "同時には承認されない不可逆操作:     %s\n", operationListSummary(effect.NotApproved))
	}
	if reason != nil {
		fmt.Fprintf(&b, "理由:        %s\n", *reason)
	}
	return b.String()
}

// operationListSummary は不可逆操作の一覧を、完了の承認の要約に埋め込む
// 1行の要約テキストにする。空なら「(無し)」。
func operationListSummary(ops []core.IrreversibleOperation) string {
	if len(ops) == 0 {
		return "(無し)"
	}
	var b strings.Builder
	for i, op := range ops {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s(%s) %s", op.ID, op.Kind, op.Summary)
	}
	return b.String()
}

// operationApprovalSummaryText は approve・reject <OP-ID> が確認前に /dev/tty
// へ表示する要約（不可逆操作の単独の承認・差し戻し: 操作の ID・種類・要約・
// 参照と、課題の ID・タイトル・状態。差し戻しでは理由も含む）。
func operationApprovalSummaryText(p *core.OperationApprovalPreview, reason *string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "操作 ID:      %s\n", p.OperationID)
	fmt.Fprintf(&b, "種類:         %s\n", string(p.Kind))
	fmt.Fprintf(&b, "要約:         %s\n", p.Summary)
	fmt.Fprintf(&b, "参照:         %s\n", textOrDash(nilableString(p.Ref)))
	fmt.Fprintf(&b, "課題 ID:      %s\n", p.ChallengeID)
	fmt.Fprintf(&b, "課題タイトル: %s\n", p.ChallengeTitle)
	label, _ := p.ChallengeStatus.Label()
	fmt.Fprintf(&b, "課題の状態:   %s (%s)\n", label, p.ChallengeStatus)
	if reason != nil {
		fmt.Fprintf(&b, "理由:         %s\n", *reason)
	}
	return b.String()
}

// answerSummaryText は answer が確認前に /dev/tty へ表示する要約
// （AC-42: 課題の ID・タイトルと、問い・入力された回答）。
func answerSummaryText(p *core.AnswerPreview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "課題 ID:  %s\n", p.ChallengeID)
	fmt.Fprintf(&b, "タイトル: %s\n", p.Title)
	fmt.Fprintf(&b, "問い:     %s\n", p.Question)
	fmt.Fprintf(&b, "回答:     %s\n", p.Answer)
	return b.String()
}

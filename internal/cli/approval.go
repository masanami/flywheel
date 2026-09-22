package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは本人確認つきの操作（approve・reject・answer）の実コマンドを
// 持つ（Issue #12）。core の2段階 API（① Prepare*＝読み取り、② Execute*＝
// 書き込み）を、① の結果から人間向けの要約テキストを組み、
// core.Verify(core.ChannelCLI, tty_confirm Verifier, summary, 対象ID) で
// 確認を得てから ② を呼ぶ、という決まった形で使う。遷移の可否・承認の
// 成立条件・作業ログの記録はすべて core が持ち、ここには書かない（P2・P4）。

// runApprove は `flywheel approve <ID> [--hold-release]` の実装（T5・T13）。
// --hold-release は #13（未承認の release を残す完了承認）が対象。本チケット
// （#12）では、計画承認待ちへの --hold-release は usage_error（CLI の入力
// 規則。core.OperationForApprove のコメントに従い CLI 側の責務とする）、
// 完了確認待ちへの --hold-release は「未実装（#13 で実装）」の internal_error
// にする（いずれも core.Verify を呼ぶ前＝端末を開く前に拒否する）。
func runApprove(a Args) (any, error) {
	id := a.Positional[0]
	holdRelease := a.Bools["hold-release"]

	preview, err := a.Store.PrepareApproval(context.Background(), id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	if holdRelease {
		switch preview.Kind {
		case core.ApprovalKindPlan:
			return nil, NewError(CodeUsageError, "計画承認待ちの課題への --hold-release は指定できません")
		case core.ApprovalKindCompletion:
			return nil, NewError(CodeInternalError, "--hold-release は未実装です（#13 で実装）")
		default:
			// PrepareApproval が返す Kind は現状 plan／completion の2値だけだが
			// （approvalKindForStatus）、ApprovalKind 自体は release を含む
			// 3値の閉集合（#13 で release が増えうる）。ここで default を
			// 落とすと、未知の Kind に対して --hold-release が黙って無視され
			// 通常の approve が成立してしまう fail-open になる
			// （self-review 指摘）。到達しないはずだが fail-closed に拒否する。
			return nil, NewError(CodeInternalError, "未対応の承認の種類です: "+string(preview.Kind))
		}
	}

	summary := approvalSummaryText(preview, nil)
	att, err := core.Verify(core.ChannelCLI, newTTYConfirmVerifier(a.Stdin), summary, id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	c, approval, err := a.Store.ExecuteApproval(context.Background(), core.ApprovalRequest{
		ChallengeID:     id,
		ExpectedVersion: preview.Version,
		Decision:        core.ApprovalDecisionApproved,
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: map[string]any{"challenge": challengeJSON(*c), "approval": approvalJSON(*approval)},
		text: challengeText(*c),
	}, nil
}

// runReject は `flywheel reject <ID> --reason <r>` の実装（T6・T15）。
func runReject(a Args) (any, error) {
	id := a.Positional[0]
	reason := a.Values["reason"]

	preview, err := a.Store.PrepareRejection(context.Background(), id, reason)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	summary := approvalSummaryText(preview, &reason)
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
		json: map[string]any{"challenge": challengeJSON(*c), "approval": approvalJSON(*approval)},
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
		json: map[string]any{"challenge": challengeJSON(*c), "hold": holdJSON(*hold)},
		text: challengeText(*c),
	}, nil
}

// approvalSummaryText は approve・reject が確認前に /dev/tty へ表示する要約
// （AC-39: 課題の ID・タイトル・承認の種類、計画の承認なら本文と版／
// AC-41: reject は対応する承認と同じ要約＋入力された理由）。
// reason が非 nil なら reject の要約として理由を末尾に加える。
func approvalSummaryText(p *core.ApprovalPreview, reason *string) string {
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
	if reason != nil {
		fmt.Fprintf(&b, "理由:        %s\n", *reason)
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

// approvalJSON は core.Approval を「成功時の JSON 出力の規約」の approval
// オブジェクトの形へ変換する（show の approvals の要素・approve/reject の
// 単発の成功出力が共有する）。
func approvalJSON(a core.Approval) map[string]any {
	return map[string]any{
		"kind":           string(a.Kind),
		"decision":       string(a.Decision),
		"operation_id":   nilableString(a.OperationID),
		"target_version": a.TargetVersion,
		"actor":          a.Actor,
		"channel":        a.Channel,
		"verification":   a.Verification,
		"reason":         nilableString(a.Reason),
		"decided_at":     FormatTimestamp(a.DecidedAt),
	}
}

// holdJSON は core.Hold を「成功時の JSON 出力の規約」の hold オブジェクトの
// 形へ変換する（show の holds の要素・answer の単発の成功出力が共有する）。
func holdJSON(h core.Hold) map[string]any {
	label, _ := h.FromStatus.Label()
	var answeredAt any
	if h.AnsweredAt != nil {
		answeredAt = FormatTimestamp(*h.AnsweredAt)
	}
	return map[string]any{
		"question":          h.Question,
		"from_status":       string(h.FromStatus),
		"from_status_label": label,
		"raised_at":         FormatTimestamp(h.RaisedAt),
		"answer":            nilableString(h.Answer),
		"answered_at":       answeredAt,
		"answered_by":       nilableString(h.AnsweredBy),
	}
}

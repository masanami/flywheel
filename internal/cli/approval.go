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
// core.Verify(core.ChannelCLI, verifierFor が選んだ Verifier, summary, 対象ID) で
// 確認を得てから ② を呼ぶ。Verifier は既定が tty_confirm（端末で要約を表示して
// 対象 ID を入力させる）、--relayed のときは relayed（要約を表示せず、会話の
// 手がかりを core へ渡すだけ。relayed.go）、という決まった形で使う。遷移の可否・承認の
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

	verifier, err := verifierFor(a)
	if err != nil {
		return nil, err
	}
	if isOperationID(id) {
		return runApproveOperation(a, verifier, id, holdRelease)
	}
	return runApproveChallenge(a, verifier, id, holdRelease)
}

// runApproveOperation は `flywheel approve <OP-ID> [--hold-release]` の実装。
// --hold-release は不可逆操作の ID には指定できない（usage_error。
// docs/features/m1-core.md §承認・§受入基準「不可逆操作と D12」）。
func runApproveOperation(a Args, verifier core.Verifier, id string, holdRelease bool) (any, error) {
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

	// release の head は ① で取得し、要約に示して ② へ渡す（② で取り直さない）。承認の対象にできない
	// release は、確認を求める前に upstream_unavailable で終わる。
	rd, err := operationReleaseDecision(context.Background(), a, preview)
	if err != nil {
		return nil, err
	}
	headSHA := ""
	if rd != nil {
		headSHA = rd.Head
	}

	summary := operationApprovalSummaryText(preview, nil) + operationHeadLines(rd)
	att, err := core.Verify(core.ChannelCLI, verifier, summary, id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	op, approval, err := a.Store.ExecuteOperationApproval(context.Background(), core.OperationApprovalRequest{
		OperationID:              id,
		ExpectedVersion:          preview.Version,
		ExpectedChallengeVersion: preview.ChallengeVersion,
		Decision:                 core.ApprovalDecisionApproved,
		HeadSHA:                  headSHA,
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
func runApproveChallenge(a Args, verifier core.Verifier, id string, holdRelease bool) (any, error) {
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

	// 完了の承認（--hold-release なし）は、同時に承認する release の head を ① で取得し、承認の対象に
	// できない release を外す。
	var rds []releaseDecision
	if preview.Kind == core.ApprovalKindCompletion && !holdRelease {
		rds, err = completionReleaseDecisions(context.Background(), a, preview)
		if err != nil {
			return nil, err
		}
	}

	summary := approvalSummaryText(preview, core.ApprovalDecisionApproved, holdRelease, nil)
	if rds != nil {
		summary = approvalSummaryTextWithReleases(preview, rds)
	}
	att, err := core.Verify(core.ChannelCLI, verifier, summary, id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	c, approval, err := a.Store.ExecuteApproval(context.Background(), core.ApprovalRequest{
		ChallengeID:        id,
		ExpectedVersion:    preview.Version,
		Decision:           core.ApprovalDecisionApproved,
		HoldRelease:        holdRelease,
		ReleaseHeads:       coreReleaseHeads(rds),
		ExcludedReleaseIDs: excludedIDs(rds),
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	approvedViews, excludedViews := releaseViews(rds)
	return textOutput{
		json: view.ChallengeApprovalResponse{Approval: view.FromApproval(*approval), Challenge: view.FromChallenge(*c), Releases: approvedViews, ExcludedReleases: excludedViews},
		text: challengeText(*c),
	}, nil
}

// runReject は `flywheel reject <ID> --reason <r>` の実装
// （T6・T15。#13: 不可逆操作の ID には単独の差し戻しとして動く）。
func runReject(a Args) (any, error) {
	id := a.Positional[0]
	reason := a.Values["reason"]

	verifier, err := verifierFor(a)
	if err != nil {
		return nil, err
	}
	if isOperationID(id) {
		if len(a.Multi["field"]) > 0 {
			return nil, NewError(CodeUsageError, "--field は課題の差し戻しにだけ指定できます")
		}
		return runRejectOperation(a, verifier, id, reason)
	}
	return runRejectChallenge(a, verifier, id, reason, a.Multi["field"])
}

// runRejectOperation は `flywheel reject <OP-ID> --reason <r>` の実装。
func runRejectOperation(a Args, verifier core.Verifier, id string, reason string) (any, error) {
	preview, err := a.Store.PrepareOperationRejection(context.Background(), id, reason)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	summary := operationApprovalSummaryText(preview, &reason)
	att, err := core.Verify(core.ChannelCLI, verifier, summary, id)
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
func runRejectChallenge(a Args, verifier core.Verifier, id string, reason string, fields []string) (any, error) {
	// 閉集合の外の値は、本人確認の前に何も書かず拒む（正規化の規則は core が持つ）。
	if _, ok := core.NormalizeRejectedFields(fields); !ok {
		return nil, NewError(CodeValidationFailed, "--field は issues・expected・non_goals・figure のいずれかを指定してください")
	}
	preview, err := a.Store.PrepareRejection(context.Background(), id, reason)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	summary := approvalSummaryText(preview, core.ApprovalDecisionRejected, false, &reason)
	att, err := core.Verify(core.ChannelCLI, verifier, summary, id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	c, approval, err := a.Store.ExecuteApproval(context.Background(), core.ApprovalRequest{
		ChallengeID:     id,
		ExpectedVersion: preview.Version,
		Decision:        core.ApprovalDecisionRejected,
		Reason:          &reason,
		RejectedFields:  fields,
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

	verifier, err := verifierFor(a)
	if err != nil {
		return nil, err
	}
	preview, err := a.Store.PrepareAnswer(context.Background(), id, answer)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	summary := answerSummaryText(preview)
	att, err := core.Verify(core.ChannelCLI, verifier, summary, id)
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
	return buildApprovalSummary(p, decision, holdRelease, reason, nil)
}

// approvalSummaryTextWithReleases は完了の承認（--hold-release なし）の要約。同時に承認される release を
// ① の判定（rds）の承認の対象だけにし、外した release は同時には承認されない側に示す。head と理由を添える。
func approvalSummaryTextWithReleases(p *core.ApprovalPreview, rds []releaseDecision) string {
	return buildApprovalSummary(p, core.ApprovalDecisionApproved, false, nil, rds)
}

func buildApprovalSummary(p *core.ApprovalPreview, decision core.ApprovalDecision, holdRelease bool, reason *string, rds []releaseDecision) string {
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
	fmt.Fprintf(&b, "タイトル:    %s\n", escapeControl(p.Title))
	fmt.Fprintf(&b, "承認の種類:  %s\n", kindLabel)
	if p.Kind == core.ApprovalKindPlan {
		// 本文は複数行の文書として読ませるが、各行を字下げして要約の見出し行と見分けられるようにする。
		fmt.Fprintf(&b, "計画 v%d:\n%s", p.PlanVersion, indentLines(escapeControlKeepLines(p.PlanBody), "    "))
		writePlanStageSummary(&b, p)
	} else {
		fmt.Fprintf(&b, "完了条件:    %s\n", escapeControl(p.DoneCriteria))
		writeCompletionStageSummary(&b, p)
	}
	if p.Kind == core.ApprovalKindCompletion {
		// D12: 完了の承認の要約は、同時に承認される release と、同時には
		// 承認されない不可逆操作の一覧を含む（#13）。どちらへ振り分けるかは
		// core が今回の操作（承認か差し戻しか・--hold-release か）から決める
		// ため、要約は常に実際の結果と一致する（self-review 指摘）。
		effect := p.ReleaseEffect(decision, holdRelease)
		if rds != nil {
			effect = core.ReleaseEffect{NotApproved: append([]core.IrreversibleOperation(nil), p.OtherPendingOperations...)}
			for _, d := range rds {
				if d.Reason == "" {
					effect.Approved = append(effect.Approved, d.Op)
				} else {
					effect.NotApproved = append(effect.NotApproved, d.Op)
				}
			}
		}
		fmt.Fprintf(&b, "同時に承認される本番反映 (release): %s\n", operationListSummary(effect.Approved))
		fmt.Fprintf(&b, "同時には承認されない不可逆操作:     %s\n", operationListSummary(effect.NotApproved))
		writeReleaseHeadLines(&b, rds)
	}
	if reason != nil {
		fmt.Fprintf(&b, "理由:        %s\n", *reason)
	}
	return b.String()
}

// writePlanStageSummary は計画の承認の要約へ、論点の各問いと回答・予想の各例・やらないことを
// 書く。段階の欄が NULL の見出しは出さない（すべて NULL なら何も足さない）。
func writePlanStageSummary(b *strings.Builder, p *core.ApprovalPreview) {
	if len(p.Issues) > 0 {
		b.WriteString("論点:\n")
		for i, is := range p.Issues {
			fmt.Fprintf(b, "  Q%d. %s\n", i+1, escapeControl(is.Question))
			for j, o := range is.Options {
				fmt.Fprintf(b, "    %d) %s\n", j+1, escapeControl(o.Label))
			}
			fmt.Fprintf(b, "    推奨: %d\n", is.Recommended)
		}
		if p.IssuesAnswer != nil {
			fmt.Fprintf(b, "  回答: %s\n", escapeControl(*p.IssuesAnswer))
		}
	}
	if len(p.Expected) > 0 {
		b.WriteString("完了したら見えるもの（予想）:\n")
		for i, e := range p.Expected {
			fmt.Fprintf(b, "  %d. %s\n     変更前: %s\n     変更後: %s\n", i+1, escapeControl(e.Scene), escapeControl(e.Before), escapeControl(e.After))
		}
	}
	if len(p.NonGoals) > 0 {
		b.WriteString("やらないこと:\n")
		for _, g := range p.NonGoals {
			fmt.Fprintf(b, "  - %s\n", escapeControl(g))
		}
	}
}

// writeCompletionStageSummary は完了の承認の要約へ、予想と実物の変更後の対を書く
// （対の組み立ては core の Pairs。足りない位置は印を出す）。
func writeCompletionStageSummary(b *strings.Builder, p *core.ApprovalPreview) {
	if len(p.Pairs) == 0 {
		return
	}
	b.WriteString("予想と実物:\n")
	for i, pr := range p.Pairs {
		exp, act := core.PairExpectedMissingMark, core.PairActualMissingMark
		if pr.Expected != nil {
			exp = escapeControl(pr.Expected.After)
		}
		if pr.Actual != nil {
			act = escapeControl(pr.Actual.After)
		}
		fmt.Fprintf(b, "  %d. 予想: %s\n     実物: %s\n", i+1, exp, act)
	}
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
		fmt.Fprintf(&b, "%s(%s) %s", op.ID, op.Kind, escapeControl(op.Summary))
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
	fmt.Fprintf(&b, "要約:         %s\n", escapeControl(p.Summary))
	ref := "-"
	if p.Ref != nil {
		ref = escapeControl(*p.Ref)
	}
	fmt.Fprintf(&b, "参照:         %s\n", ref)
	fmt.Fprintf(&b, "課題 ID:      %s\n", p.ChallengeID)
	fmt.Fprintf(&b, "課題タイトル: %s\n", escapeControl(p.ChallengeTitle))
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
	fmt.Fprintf(&b, "タイトル: %s\n", escapeControl(p.Title))
	fmt.Fprintf(&b, "問い:     %s\n", escapeControlKeepLines(p.Question))
	fmt.Fprintf(&b, "回答:     %s\n", p.Answer)
	return b.String()
}

// escapeControl は外部・LLM が書きうる値（段階の欄・課題のタイトル・完了条件・不可逆操作の要約と参照）を、承認の要約へ 1 行で埋め込めるように
// する。改行・復帰・タブ・ESC などの制御文字と Unicode の行・段落の区切り
// （U+2028・U+2029）、双方向制御文字・ゼロ幅文字を、見える形（\n・\r・\t・\x1b・\u2028 など）に置き換える。
// 値の中の改行で、人間が承認の根拠にする端末の要約に偽の行や見出しを作らせない。
func escapeControl(s string) string {
	return escapeControlRunes(s, false)
}

// escapeControlKeepLines は計画の本文（PlanBody）用。本文は複数行の文書として
// 読ませるので、改行とタブは残し、それ以外の制御文字（端末の画面を書き換える
// ESC など）だけを見える形にする（偽の見出し行は、呼び出し側の字下げで防ぐ）。
func escapeControlKeepLines(s string) string {
	return escapeControlRunes(s, true)
}

func escapeControlRunes(s string, keepLines bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case keepLines && (r == '\n' || r == '\t'):
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			fmt.Fprintf(&b, `\x%02x`, r)
		case r == 0x2028 || r == 0x2029 || isBidiOrZeroWidth(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isBidiOrZeroWidth は、表示の並びを入れ替える双方向制御文字（U+202A〜202E・U+2066〜2069、
// U+200E・U+200F・U+061C）とゼロ幅文字（U+200B〜200D・U+2060・U+FEFF）かを返す。
// 見えないまま承認の根拠の読みを変えさせないため、見える形にする。
func isBidiOrZeroWidth(r rune) bool {
	switch {
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	case r >= 0x200b && r <= 0x200f, r == 0x061c, r == 0x2060, r == 0xfeff:
		return true
	}
	return false
}

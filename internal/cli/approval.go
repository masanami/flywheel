package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは本人確認つきの操作（approve・reject・answer）の実コマンドを
// 持つ（Issue #12・#13）。core の2段階 API（① Prepare*＝読み取り、②
// Execute*＝書き込み）を、① の結果から人間向けの要約テキストを組み、
// core.Verify(channelFor(a), verifierFor が選んだ Verifier, summary, 対象ID) で
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
	reopen := a.Bools["reopen"]

	if reopen && (holdRelease || !isOperationID(id)) {
		return nil, NewError(CodeUsageError, "--reopen は不可逆操作の ID にだけ指定でき、--hold-release とは同時に指定できません")
	}
	verifier, err := verifierFor(a)
	if err != nil {
		return nil, err
	}
	if reopen {
		return runReopenOperation(a, verifier, id)
	}
	if isOperationID(id) {
		return runApproveOperation(a, verifier, id, holdRelease)
	}
	return runApproveChallenge(a, verifier, id, holdRelease)
}

// runReopenOperation は `flywheel approve <OP-ID> --reopen` の実装。検証の後に動いた head が取り込みだけとも中身の変更とも
// 確かめられず（人間待ち）、課題が完了の release について、人が承認せず検証中へ戻す方を選ぶ。人間待ちかどうかは core が
// 自分で比較して確かめ（AssessReleaseReopen）、そうでなければ戻さない。
func runReopenOperation(a Args, verifier core.Verifier, id string) (any, error) {
	// 検証中へ戻す書き込みは、core が欄の印の検査と発言の記録を持たない。欄からは戻さない（ターミナルで行う）。
	if a.Chat != nil {
		return nil, NewError(CodeVerificationRejected, id+" を検証中へ戻す操作は、対話の欄からは行えません。ターミナルで行ってください")
	}
	src := headSources(a)
	as, err := a.Store.AssessReleaseReopen(context.Background(), id, src)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	if as.Verdict != core.ReleaseReopenNeedsHuman {
		return nil, NewError(CodeValidationFailed, id+" は人間待ち（取り込みだけとも中身の変更とも確かめられない）ではないため、--reopen で戻せません")
	}
	summary := fmt.Sprintf("%s の課題を完了から検証中へ戻し、本番反映を置き換えます。\n理由: 検証の後に PR の head が動き、取り込みだけとも中身の変更とも確かめられませんでした（%s）\n",
		id, escapeControl(as.Detail))
	if _, err := core.Verify(channelFor(a), verifier, summary, id); err != nil {
		return nil, mapCoreErr(err)
	}
	res, err := a.Store.ReopenChallengeForRelease(context.Background(), core.ChannelCLI, core.ReopenForReleaseInput{OperationID: id, Reason: core.ReopenReasonUndeterminable, Sources: src, ContractSources: reopenContractSources(a)})
	if err != nil {
		return nil, mapCoreErr(err)
	}
	if !res.Reopened {
		return nil, NewError(CodeValidationFailed, id+" は課題を検証中へ戻せませんでした")
	}
	return textOutput{
		json: view.OperationReopenResponse{ChallengeID: res.ChallengeID, ClosedOperationID: res.ClosedOperationID, NewOperationID: res.NewOperationID, Reason: string(core.ReopenReasonUndeterminable)},
		text: fmt.Sprintf("%s\t課題 %s を検証中へ戻し、本番反映を %s に置き換えた\n", id, res.ChallengeID, res.NewOperationID),
	}, nil
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

	summary := operationApprovalSummaryText(preview, nil) + operationHeadLines(rd, a.Chat != nil)
	att, err := core.Verify(channelFor(a), verifier, summary, id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	op, approval, err := a.Store.ExecuteOperationApproval(context.Background(), core.OperationApprovalRequest{
		OperationID:              id,
		ExpectedVersion:          preview.Version,
		ExpectedChallengeVersion: preview.ChallengeVersion,
		Decision:                 core.ApprovalDecisionApproved,
		HeadSHA:                  headSHA,
		HeadSources:              headSources(a),
		AcceptUndeterminable:     rd != nil && rd.Undeterminable != nil,
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	resp := view.OperationApprovalResponse{Approval: view.FromApproval(*approval), Operation: view.FromOperation(*op)}
	if rd != nil && rd.MergeIn != nil {
		hc := view.FromReleaseHeadAssessment(rd.MergeIn)
		resp.MergeIn = &hc
	}
	if rd != nil && rd.Undeterminable != nil {
		hc := view.FromReleaseHeadAssessment(rd.Undeterminable)
		resp.Undeterminable = &hc
	}
	return textOutput{json: resp, text: op.ID + "\n"}, nil
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
	att, err := core.Verify(channelFor(a), verifier, summary, id)
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
		HeadSources:        headSources(a),
		Agent:              agentDeclForApproval(a),
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
	att, err := core.Verify(channelFor(a), verifier, summary, id)
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
	att, err := core.Verify(channelFor(a), verifier, summary, id)
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
		json: view.ChallengeApprovalResponse{
			Approval: view.FromApproval(*approval), Challenge: view.FromChallenge(*c),
			RunningReleases: approval.RunningReleases, WithdrawnReleases: approval.WithdrawnReleases,
		},
		text: challengeText(*c) + rejectReleaseNotes(approval.WithdrawnReleases, approval.RunningReleases, false),
	}, nil
}

// runAnswer は `flywheel answer <ID> --answer <a> [--replan]` の実装（T12）。--replan は回答に
// 「範囲が変わる」印を付け、委譲を再開せず計画の作り直しへ戻す（遷移・成立条件は core が持つ）。
func runAnswer(a Args) (any, error) {
	id := a.Positional[0]
	_, hasAnswer := a.Values["answer"]
	replan := a.Bools["replan"]
	option, hasOption := a.Values["option"]
	planItems := len(a.Multi["choice"]) + len(a.Multi["text"])
	if planItems > 0 {
		if hasAnswer || replan || hasOption {
			return nil, NewError(CodeUsageError, "--choice・--text は --answer・--replan・--option と同時に指定できません")
		}
		return runPlanAnswer(a, id)
	}
	if !hasAnswer && !hasOption {
		return nil, NewError(CodeUsageError, "--answer か --option（保留への回答）、または --choice・--text（計画の問いへの答え）のどれかを指定してください")
	}
	if hasOption && strings.TrimSpace(option) == "" {
		return nil, NewError(CodeUsageError, "--option には選択肢の識別を指定してください")
	}
	answer := a.Values["answer"]

	verifier, err := verifierFor(a)
	if err != nil {
		return nil, err
	}
	preview, err := a.Store.PrepareAnswerOption(context.Background(), id, answer, option, replan)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	if replan && !core.ReplanAllowedFrom(preview.FromStatus) {
		return nil, NewError(CodeValidationFailed, "--replan は、保留の直前の状態が着手中・検証中（承認済みの計画に基づく作業の途中）の課題にだけ付けられます")
	}
	summary := answerSummaryText(preview, replan)
	att, err := core.Verify(channelFor(a), verifier, summary, id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	// 計画を作り直す回答では、契約の実行を止めない。取りやめるタスクの実行と上げる依頼は、作り直した計画の承認の後に、状態の確認が取り消す。
	c, hold, err := a.Store.ExecuteAnswer(context.Background(), core.AnswerRequest{
		ChallengeID:     id,
		ExpectedVersion: preview.Version,
		Answer:          answer,
		Replan:          replan,
		Option:          option,
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.ChallengeHoldResponse{Challenge: view.FromChallenge(*c), Hold: view.FromHold(*hold)},
		text: challengeText(*c),
	}, nil
}

// parsePlanAnswerItems は --choice <問い>=<選択肢の番号>・--text <問い>=<文> を答えの一覧へ読む。問いは Q1 でも 1 でも受ける。
// `=` と値が無い・番号の形が不正・同じ問いの重複は usage_error。
func parsePlanAnswerItems(choices, texts []string) ([]core.PlanAnswerItem, error) {
	var items []core.PlanAnswerItem
	seen := map[int64]bool{}
	parse := func(flag, raw string, isChoice bool) error {
		q, v, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(v) == "" {
			return NewError(CodeUsageError, fmt.Sprintf("--%s は <問いの番号>=<値> の形で指定してください: %q", flag, raw))
		}
		n, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(q), "Q"), "q"), 10, 64)
		if err != nil || n < 1 {
			return NewError(CodeUsageError, fmt.Sprintf("--%s の問いの番号は Q1 か 1 の形で指定してください: %q", flag, raw))
		}
		if seen[n] {
			return NewError(CodeUsageError, fmt.Sprintf("問い Q%d への答えが 2 回指定されています", n))
		}
		seen[n] = true
		it := core.PlanAnswerItem{Question: n}
		if isChoice {
			c, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil || c < 1 {
				return NewError(CodeUsageError, fmt.Sprintf("--choice の選択肢の番号は 1 以上の整数で指定してください: %q", raw))
			}
			it.Choice = &c
		} else {
			it.Text = &v
		}
		items = append(items, it)
		return nil
	}
	for _, c := range choices {
		if err := parse("choice", c, true); err != nil {
			return nil, err
		}
	}
	for _, t := range texts {
		if err := parse("text", t, false); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// runPlanAnswer は `flywheel answer <ID> --choice … --text …` の実装（計画の承認待ちの問いへの答え。T19）。
// 形は runAnswer と同じ ① 要約 → core.Verify → ② 実行。分類済へ戻すか・記録だけかは core が決める。
func runPlanAnswer(a Args, id string) (any, error) {
	items, err := parsePlanAnswerItems(a.Multi["choice"], a.Multi["text"])
	if err != nil {
		return nil, err
	}
	verifier, err := verifierFor(a)
	if err != nil {
		return nil, err
	}
	preview, err := a.Store.PreparePlanAnswer(context.Background(), id, items)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	att, err := core.Verify(channelFor(a), verifier, planAnswerSummaryText(preview), id)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	c, _, err := a.Store.ExecutePlanAnswer(context.Background(), core.PlanAnswerRequest{
		ChallengeID: id, ExpectedVersion: preview.Version, Answers: items,
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.ChallengeResponse{Challenge: view.FromChallenge(*c)}, text: challengeText(*c)}, nil
}

// planAnswerSummaryText は計画の問いへの答えが確認前に /dev/tty へ表示する要約。
func planAnswerSummaryText(p *core.PlanAnswerPreview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "課題 ID:  %s\n", p.ChallengeID)
	fmt.Fprintf(&b, "タイトル: %s\n", escapeControl(p.Title))
	fmt.Fprintf(&b, "計画の版: %d\n", p.PlanVersion)
	for _, l := range p.Answers {
		fmt.Fprintf(&b, "問い Q%d: %s\n", l.Question, escapeControl(l.QuestionText))
		if l.Choice != nil {
			mark := "推奨と違う"
			if l.Recommended {
				mark = "推奨と同じ"
			}
			fmt.Fprintf(&b, "  答え:   %d) %s（%s）\n", *l.Choice, escapeControl(l.ChoiceLabel), mark)
		} else {
			fmt.Fprintf(&b, "  答え:   %s\n", escapeControl(*l.Text))
		}
	}
	if p.ReturnsToClassified {
		b.WriteString("結果: 課題を分類済へ戻し、計画を作り直す（新しい版の計画を承認し直す）\n")
	} else {
		b.WriteString("結果: 答えを記録するだけ（課題の状態は計画の承認待ちのまま）\n")
	}
	return b.String()
}

// runWithdraw は `flywheel withdraw <C-ID> --reason <理由>` の実装（課題の取り下げ。M45H28）。
// 形は runAnswer と同じ ① 要約 → core.Verify → ② 実行。遷移・不可逆操作の差し戻し・保留の
// クローズ・作業ログの規則は core が持つ。
func runWithdraw(a Args) (any, error) {
	id := a.Positional[0]
	reason := a.Values["reason"]
	if strings.TrimSpace(reason) == "" {
		return nil, NewError(CodeUsageError, "--reason には取り下げの理由を指定してください")
	}

	verifier, err := verifierFor(a)
	if err != nil {
		return nil, err
	}
	preview, err := a.Store.PrepareWithdraw(context.Background(), id, reason)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	att, err := core.Verify(channelFor(a), verifier, withdrawSummaryText(preview), id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	// 開いた契約の run があれば、先に止める（止めると課題の版が変わりうるので、版を取り直す）。
	if n, err := cancelOpenContractRuns(a, id); err != nil {
		return nil, err
	} else if n > 0 {
		if preview, err = a.Store.PrepareWithdraw(context.Background(), id, reason); err != nil {
			return nil, mapCoreErr(err)
		}
	}

	c, err := a.Store.ExecuteWithdraw(context.Background(), core.WithdrawRequest{
		ChallengeID:     id,
		ExpectedVersion: preview.Version,
		Reason:          reason,
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.ChallengeResponse{Challenge: view.FromChallenge(*c)}, text: challengeText(*c)}, nil
}

// withdrawSummaryText は withdraw が確認前に /dev/tty へ表示する要約（課題の ID・タイトル・現在の状態・理由と、
// 同時に差し戻される不可逆操作・回答なしで閉じる保留）。
func withdrawSummaryText(p *core.WithdrawPreview) string {
	var b strings.Builder
	statusLabel, _ := p.Status.Label()
	fmt.Fprintf(&b, "課題 ID:  %s\n", p.ChallengeID)
	fmt.Fprintf(&b, "タイトル: %s\n", escapeControl(p.Title))
	fmt.Fprintf(&b, "操作:     取り下げ（現在の状態: %s。取り下げた課題は元に戻せない）\n", statusLabel)
	fmt.Fprintf(&b, "理由:     %s\n", escapeControlKeepLines(p.Reason))
	fmt.Fprintf(&b, "同時に差し戻される不可逆操作: %s\n", operationListSummary(p.RejectedOperations))
	if p.ClosedHold != nil {
		fmt.Fprintf(&b, "回答なしで閉じる保留: %s\n", escapeControlKeepLines(*p.ClosedHold))
	}
	return b.String()
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
		writeDirectCompletionSummary(&b, p)
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
		writeConditionsAfterRelease(&b, p.ConditionsAfterRelease, view.OutlookFor(len(effect.Approved) > 0, p.HasApprovedRelease))
		if p.MergeabilityUnconfirmed {
			fmt.Fprintf(&b, "PR のマージ可否: %s\n", core.MergeabilityUnconfirmedNote)
		}
		if decision == core.ApprovalDecisionRejected {
			// 差し戻しは未実行の release を取り消す（規則は core。取り消す release は「承認されない」側から外す）。
			effect.NotApproved = withoutOperations(effect.NotApproved, p.ReleasesToWithdraw)
		}
		fmt.Fprintf(&b, "同時に承認される本番反映 (release): %s\n", operationListSummary(effect.Approved))
		fmt.Fprintf(&b, "同時には承認されない不可逆操作:     %s\n", operationListSummary(effect.NotApproved))
		writeReleaseHeadLines(&b, rds)
		if decision == core.ApprovalDecisionRejected {
			b.WriteString(rejectReleaseNotes(p.ReleasesToWithdraw, p.RunningReleases, true))
		}
	}
	if reason != nil {
		fmt.Fprintf(&b, "理由:        %s\n", *reason)
	}
	return b.String()
}

// writeDirectCompletionSummary は完了の承認の要約へ、親が直接行った完了の記録の成果物の参照と説明を、完了条件の
// 次に書く（完了の検証〈J5〉を通っていないので、承認の根拠はこれらと完了条件）。記録が無ければ何も足さない。
func writeDirectCompletionSummary(b *strings.Builder, p *core.ApprovalPreview) {
	dc := p.DirectCompletion
	if dc == nil {
		return
	}
	b.WriteString("親が直接行った（完了の検証は通っていません）。成果物:\n")
	for _, ref := range dc.Artifacts {
		fmt.Fprintf(b, "  - %s\n", escapeControl(ref))
	}
	if dc.Note != nil {
		fmt.Fprintf(b, "  説明: %s\n", escapeControl(*dc.Note))
	}
}

// writeConditionsAfterRelease は完了の承認の要約へ、検証が「本番反映で満たされる見込み」と区分した
// 達成条件を書く（#451）。無ければ何も書かない。この操作で本番反映が 1 つも承認されない
// （--hold-release・差し戻し・head の照合で外れた・release が無い）ときは、満たされないまま残ることを
// 見出しで示す。承認済みで未実行の release があれば、それが走れば満たされることを示す。
func writeConditionsAfterRelease(b *strings.Builder, conds []string, outlook view.ConditionsOutlook) {
	if len(conds) == 0 {
		return
	}
	b.WriteString(view.ConditionsAfterReleaseHeading(outlook) + "\n")
	for _, c := range conds {
		fmt.Fprintf(b, "  - %s\n", escapeControl(c))
	}
}

// writePlanStageSummary は計画の承認の要約へ、論点の各問いと回答・予想の各例・やらないことを
// 書く。段階の欄が NULL の見出しは出さない（すべて NULL なら何も足さない）。
func writePlanStageSummary(b *strings.Builder, p *core.ApprovalPreview) {
	writePlanTopics(b, p.PlanTopics)
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
	if len(p.PlanTasks) > 0 {
		fmt.Fprintf(b, "タスクのツリー（%d 件・統合先を使うか: %s）:\n", len(p.PlanTasks), usesIntegrationText(p.PlanUsesIntegration))
		for _, t := range p.PlanTasks {
			line := fmt.Sprintf("  %s %s", t.Identifier(), escapeControl(t.Title))
			if t.Source != "" {
				line += "（取り込み元の子: " + escapeControl(t.Source) + "）"
			}
			if len(t.DependsOn) > 0 {
				line += "（前提: " + strings.Join(t.DependsOn, "・") + "）"
			}
			line += taskFrameText(t)
			b.WriteString(line + "\n")
		}
		if p.PlanBudgetUSD != nil && taskFramesPresent(p.PlanTasks) {
			fmt.Fprintf(b, "課題の上限（タスクの枠の合計）: %s USD\n", strconv.FormatFloat(*p.PlanBudgetUSD, 'f', -1, 64))
		}
	}
}

// taskFramesPresent は、計画のタスクが全部枠を持つか。
func taskFramesPresent(tasks []core.PlanTaskView) bool {
	for _, t := range tasks {
		if t.BudgetImplUSD == nil || t.BudgetReviewUSD == nil {
			return false
		}
	}
	return len(tasks) > 0
}

// taskFrameText はタスクの枠（実装・レビュー対応）の表示。枠を持たないタスクは空。
func taskFrameText(t core.PlanTaskView) string {
	if t.BudgetImplUSD == nil || t.BudgetReviewUSD == nil {
		return ""
	}
	return "（枠: 実装 " + strconv.FormatFloat(*t.BudgetImplUSD, 'f', -1, 64) + " USD・レビュー対応 " + strconv.FormatFloat(*t.BudgetReviewUSD, 'f', -1, 64) + " USD）"
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
func answerSummaryText(p *core.AnswerPreview, replan bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "課題 ID:  %s\n", p.ChallengeID)
	fmt.Fprintf(&b, "タイトル: %s\n", escapeControl(p.Title))
	fmt.Fprintf(&b, "問い:     %s\n", escapeControlKeepLines(p.Question))
	if p.Option != "" {
		fmt.Fprintf(&b, "選択肢:   %s（%s）\n", escapeControl(p.OptionLabel), escapeControl(p.Option))
		if p.Answer != "" {
			fmt.Fprintf(&b, "添え書き: %s\n", p.Answer)
		}
	} else {
		fmt.Fprintf(&b, "回答:     %s\n", p.Answer)
	}
	if replan {
		b.WriteString("範囲が変わる: 委譲を再開せず、計画を作り直して承認待ちへ戻す（既存の PR・ブランチは引き継ぐ）\n")
	}
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

// cancelOpenContractRuns は課題の開いた契約の run（form: cli の接続ツールの run）を、宣言の commands.cancel で止める。
// 開いた run が無ければ宣言も claude も要らず、何もしない。止めた数を返す。
func cancelOpenContractRuns(a Args, id string) (int, error) {
	ctx := context.Background()
	open, err := a.Store.HasOpenContractRun(ctx, id)
	if err != nil {
		return 0, mapCoreErr(err)
	}
	if !open {
		return 0, nil
	}
	ws := a.Store.Workspace()
	decl, err := core.LoadAgentDeclaration(ws)
	if err != nil {
		return 0, mapCoreErr(err)
	}
	conn, err := core.LoadConnectorsDeclaration(ws)
	if err != nil {
		return 0, mapCoreErr(err)
	}
	n, err := a.Store.CancelOpenContractRuns(ctx, core.ContractCancelInput{
		ChallengeID: id, AgentDecl: decl, ConnDecl: conn, Client: invoker.NewLauncherWithLookPath(a.LookPath),
	})
	if err != nil {
		return n, mapCoreErr(err)
	}
	return n, nil
}

// rejectReleaseNotes は完了の差し戻しで取り消した本番反映と、実行中のため止めなかった本番反映を伝える行
// （取り消す規則・実行中の判定は core が持つ。ここは結果を表示するだけ）。
// preview が真なら確認前の要約の見通し（「取り消す」「止めない」）、偽なら結果（「取り消した」「止めていない」）を書く。
func rejectReleaseNotes(withdrawn, running []string, preview bool) string {
	var b strings.Builder
	if len(withdrawn) > 0 {
		verb := "取り消した"
		if preview {
			verb = "取り消す"
		}
		fmt.Fprintf(&b, "未実行の本番反映 %s を%s（マージされない。直して再び完了を承認すると新しい本番反映が立つ）\n", strings.Join(withdrawn, ", "), verb)
	}
	if len(running) > 0 {
		verb := "止めていない"
		if preview {
			verb = "止めない"
		}
		fmt.Fprintf(&b, "実行中のため本番反映 %s は%s（マージされうる）\n", strings.Join(running, ", "), verb)
	}
	return b.String()
}

// withoutOperations は ops から ids の ID の操作を除いた一覧を返す。
func withoutOperations(ops []core.IrreversibleOperation, ids []string) []core.IrreversibleOperation {
	if len(ids) == 0 {
		return ops
	}
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	out := make([]core.IrreversibleOperation, 0, len(ops))
	for _, op := range ops {
		if !drop[op.ID] {
			out = append(out, op)
		}
	}
	return out
}

// reopenContractSources は、課題を完了から戻す前にタスクの契約の実行を読む口（宣言と契約のコマンドの起動）。
// 宣言を読めなければ口を渡さない（core は、終わっていないタスクの実行があるのに口が無ければ戻さない）。
func reopenContractSources(a Args) core.ReopenContractSources {
	ws := a.Store.Workspace()
	decl, err := core.LoadAgentDeclaration(ws)
	if err != nil {
		return core.ReopenContractSources{}
	}
	conn, err := core.LoadConnectorsDeclaration(ws)
	if err != nil {
		return core.ReopenContractSources{}
	}
	return core.ReopenContractSources{Contract: invoker.NewLauncherWithLookPath(a.LookPath), Agent: decl, Connectors: conn}
}

// agentDeclForApproval は計画の承認で、枠のキーを持たない 1 個のタスクの枠をサイズごとの既定で決めるための宣言
// （読めなければ nil。そのタスクの枠は空のまま残り、読むときに見直し前の決め方の値になる）。
func agentDeclForApproval(a Args) *core.AgentDeclaration {
	decl, err := core.LoadAgentDeclaration(a.Store.Workspace())
	if err != nil {
		return nil
	}
	return decl
}

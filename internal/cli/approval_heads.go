package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは approve の ① で、承認の対象の release ごとに PR の head を取得し、承認の対象にできない
// release を理由つきで外す判定を持つ（外す理由の判定は CLI の責務。core は GitHub を読まない）。

// excludeReason は release を承認の対象から外す理由の名前（閉集合）。
type excludeReason string

const (
	reasonHeadUnavailable      excludeReason = "head_unavailable"
	reasonNotAPRURL            excludeReason = "not_a_pr_url"
	reasonRepoNotDeclared      excludeReason = "repo_not_declared"
	reasonVerifiedHeadMismatch excludeReason = "verified_head_mismatch"
	reasonMergedUnapproved     excludeReason = "merged_unapproved"
)

// description は要約に示す、理由の説明の文。
func (r excludeReason) description() string {
	switch r {
	case reasonHeadUnavailable:
		return "参照の PR の head を GitHub から取得できませんでした"
	case reasonNotAPRURL:
		return "参照が GitHub の PR の URL ではありません"
	case reasonRepoNotDeclared:
		return "参照の PR のリポジトリが connectors.json の repos[].remote にありません"
	case reasonVerifiedHeadMismatch:
		return "検証の後に push されたコミットがあります（取得した head が J5 の検証の head と異なり、既定ブランチの取り込みだけで CI が通っているとは確かめられませんでした）"
	case reasonMergedUnapproved:
		return "承認していない head で既にマージされ、承認を取り消した release です（承認し直せません。差し戻しは reject <OP-ID>）"
	}
	return string(r)
}

// releaseDecision は release 1 件の ① の判定。Reason が空なら承認の対象で、Head が承認の記録に残す head。
type releaseDecision struct {
	Op     core.IrreversibleOperation
	Head   string
	Reason excludeReason
	// NoVerifiedHead は承認の対象で、検証の head の記録が無い（比較していない）。
	NoVerifiedHead bool
	// MergeIn は、取得した head が検証の head と異なるが、既定ブランチの取り込みだけで CI が通っていると判定して
	// 承認の対象にしたときの評価（検証の head・今の head・後から入ったコミット・直した差分）。② へ渡し、承認の記録に残す。
	MergeIn *core.ReleaseHeadAssessment
	// Assessment は、検証の head と異なる head を評価した結果（外した理由が verified_head_mismatch のとき。評価できなければ nil）。
	Assessment *core.ReleaseHeadAssessment
	// Detail は外した理由の根拠（評価の結果。無ければ空）。
	Detail string
}

// resolveReleaseDecisions は ops（release）それぞれについて、外す理由か承認する head を決める。
// 判定の順は 直前の試行の結果が merged_unapproved か → PR の URL か → リポジトリが宣言にあるか → head の取得 → 検証の head との比較。
// verified は release の ID から最新の検証の head を返す（記録なしは ok=false）。
// mergedUnapproved は release の ID について、直前の試行の結果が merged_unapproved かを返す。
func resolveReleaseDecisions(ctx context.Context, ws string, ops []core.IrreversibleOperation, verified func(id string) (string, bool), mergedUnapproved func(id string) bool) ([]releaseDecision, error) {
	declared := map[string]bool{}
	decl, err := core.LoadConnectorsDeclaration(ws)
	switch {
	case err == nil:
		for _, r := range decl.Repos {
			declared[strings.ToLower(r.Remote)] = true
		}
	case errors.Is(err, core.ErrConfigNotFound):
		// 宣言が無ければ、どのリポジトリも宣言に無い。
	default:
		return nil, mapCoreErr(err)
	}

	out := make([]releaseDecision, len(ops))
	var fetchOps []core.IrreversibleOperation
	var fetchIdx []int
	for i, op := range ops {
		out[i].Op = op
		if mergedUnapproved(op.ID) {
			out[i].Reason = reasonMergedUnapproved
			continue
		}
		if op.Ref == nil {
			out[i].Reason = reasonNotAPRURL
			continue
		}
		repo, _, ok := core.ParseReleasePullRequestRef(*op.Ref)
		switch {
		case !ok:
			out[i].Reason = reasonNotAPRURL
		case !declared[strings.ToLower(repo)]:
			out[i].Reason = reasonRepoNotDeclared
		default:
			fetchOps = append(fetchOps, op)
			fetchIdx = append(fetchIdx, i)
		}
	}
	if len(fetchOps) > 0 {
		heads := core.ResolveReleaseHeads(ctx, fetchOps, newCheckSource())
		for j, h := range heads {
			i := fetchIdx[j]
			if h.HeadSHA == nil {
				out[i].Reason = reasonHeadUnavailable
				continue
			}
			out[i].Head = *h.HeadSHA
			v, ok := verified(out[i].Op.ID)
			if !ok {
				out[i].NoVerifiedHead = true
				continue
			}
			if v != out[i].Head {
				assessVerifiedHeadMismatch(ctx, &out[i], v)
			}
		}
	}
	return out, nil
}

// assessVerifiedHeadMismatch は、取得した head が検証の head（verified）と異なる release を評価する。
// 既定ブランチの取り込みだけで今の head の CI が通っていれば、評価した head を承認の対象にする（MergeIn）。
// そうでなければ（取り込み以外の変更・判定できない・CI が通っていない・取得に失敗）外す。
func assessVerifiedHeadMismatch(ctx context.Context, d *releaseDecision, verified string) {
	a, err := core.AssessReleaseHead(ctx, newCheckSource(), newCompareSource(), *d.Op.Ref, verified)
	switch {
	case err != nil:
		d.Reason = reasonVerifiedHeadMismatch
		d.Detail = "検証の head と今の head の違いを調べられませんでした: " + err.Error()
	case a.HeadSHA == verified:
		// 取得し直す間に head が検証の head へ戻った。通常の承認。
		d.Head = verified
	case a.MergeInApprovable():
		d.Head = a.HeadSHA
		d.MergeIn = a
	default:
		d.Reason = reasonVerifiedHeadMismatch
		d.Assessment = a
		d.Detail = describeAssessment(a)
	}
}

// describeAssessment は、検証の head と異なる head を承認の対象にできない根拠を 1 文で返す。
func describeAssessment(a *core.ReleaseHeadAssessment) string {
	switch a.Change.Kind {
	case core.HeadChangeContentChanged:
		return "取り込み以外のコミットが入っています（" + a.Change.Detail + "）"
	case core.HeadChangeMergeInOnly:
		return fmt.Sprintf("既定ブランチの取り込みだけですが、今の head の CI が通っていません（チェック %d 件）", a.ChecksCount)
	default:
		return "取り込みだけか判定できませんでした（" + a.Change.Detail + "）"
	}
}

func includedReleases(ds []releaseDecision) []releaseDecision {
	var out []releaseDecision
	for _, d := range ds {
		if d.Reason == "" {
			out = append(out, d)
		}
	}
	return out
}

func excludedReleases(ds []releaseDecision) []releaseDecision {
	var out []releaseDecision
	for _, d := range ds {
		if d.Reason != "" {
			out = append(out, d)
		}
	}
	return out
}

// coreReleaseHeads は承認する組を core.ApprovalRequest.ReleaseHeads の形にする。
func coreReleaseHeads(ds []releaseDecision) []core.ReleaseHead {
	var out []core.ReleaseHead
	for _, d := range includedReleases(ds) {
		h := d.Head
		rh := core.ReleaseHead{Operation: d.Op, HeadSHA: &h}
		if d.MergeIn != nil {
			v := d.MergeIn.VerifiedHead
			rh.MergeInVerifiedHead = &v
		}
		out = append(out, rh)
	}
	return out
}

func excludedIDs(ds []releaseDecision) []string {
	var out []string
	for _, d := range excludedReleases(ds) {
		out = append(out, d.Op.ID)
	}
	return out
}

// shortHead は head の先頭 12 文字。
func shortHead(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// writeReleaseHeadLines は完了の承認の要約へ、承認の対象の release ごとの head（短縮形・完全形）と、
// 外した release の理由（名前と説明の文）を書く。
func writeReleaseHeadLines(b *strings.Builder, ds []releaseDecision) {
	for _, d := range includedReleases(ds) {
		// 完全形は他の文字の無い単独の行に置く（コピー・ダブルクリックで SHA だけを取れるように）。
		fmt.Fprintf(b, "  %s の head（短縮形 %s。完全形は次の行）\n", d.Op.ID, shortHead(d.Head))
		fmt.Fprintf(b, "%s\n", d.Head)
		if d.NoVerifiedHead {
			b.WriteString("  ※検証の head の記録が無いため、比較していません\n")
		}
		if d.MergeIn != nil {
			writeHeadChangeLines(b, "  ", d.MergeIn)
		}
	}
	for _, d := range excludedReleases(ds) {
		fmt.Fprintf(b, "  %s は同時に承認されません（未承認のまま残ります）: %s（%s）\n", d.Op.ID, d.Reason, d.Reason.description())
		if d.Detail != "" {
			fmt.Fprintf(b, "    根拠: %s\n", escapeControl(d.Detail))
		}
	}
}

// completionReleaseDecisions は完了の承認（--hold-release なし）の ① の判定。承認の対象の release が
// 無ければ nil。
func completionReleaseDecisions(ctx context.Context, a Args, p *core.ApprovalPreview) ([]releaseDecision, error) {
	if len(p.PendingReleases) == 0 {
		return nil, nil
	}
	return resolveReleaseDecisions(ctx, a.Store.Workspace(), p.PendingReleases, func(id string) (string, bool) {
		v, ok := p.VerifiedHeadSHAs[id]
		return v, ok
	}, func(id string) bool {
		return p.ReapprovalForbidden[id]
	})
}

// operationReleaseDecision は release の単独の承認の ① の判定。外す理由があれば upstream_unavailable
// （終了コード 1。詳細に理由の名前）。release 以外では nil を返す。
func operationReleaseDecision(ctx context.Context, a Args, p *core.OperationApprovalPreview) (*releaseDecision, error) {
	if p.Kind != core.OperationKindRelease {
		return nil, nil
	}
	op := core.IrreversibleOperation{ID: p.OperationID, Kind: p.Kind, Summary: p.Summary, Ref: p.Ref}
	ds, err := resolveReleaseDecisions(ctx, a.Store.Workspace(), []core.IrreversibleOperation{op}, func(string) (string, bool) {
		if p.VerifiedHeadSHA == nil {
			return "", false
		}
		return *p.VerifiedHeadSHA, true
	}, func(string) bool {
		// 単独の承認の merged_unapproved は core が ① で拒否する（rejectReapproval）。
		return false
	})
	if err != nil {
		return nil, err
	}
	d := ds[0]
	if d.Reason != "" {
		// 課題が完了で、検証の後に取り込み以外の変更が入った（または履歴が書き換わった）release は、承認できない
		// だけでなく、課題を着手中へ戻して検証し直す道がある（#485）。戻したことを出力して終わる。
		if d.Reason == reasonVerifiedHeadMismatch && p.ChallengeStatus == core.StatusDone && d.Assessment != nil && d.Assessment.Change.RequiresReverification() {
			res, err := a.Store.ReopenChallengeForRelease(ctx, core.ChannelCLI, core.ReopenForReleaseInput{OperationID: p.OperationID, Reason: core.ReopenReasonContentChanged})
			if err != nil {
				return nil, mapCoreErr(err)
			}
			if res.Reopened {
				return nil, newReleaseNotApprovableError(fmt.Sprintf(
					"%s を承認の対象にできません: %s（%s）。課題 %s を完了から着手中へ戻し、本番反映を %s に置き換えました（%s は取り下げました）。"+
						"検証をやり直し、完了の承認で %s を承認してください",
					p.OperationID, d.Reason, d.Detail, res.ChallengeID, res.NewOperationID, res.ClosedOperationID, res.NewOperationID))
			}
		}
		msg := fmt.Sprintf("%s を承認の対象にできません: %s（%s）", p.OperationID, d.Reason, d.Reason.description())
		if d.Detail != "" {
			msg += "。根拠: " + d.Detail
		}
		return nil, newReleaseNotApprovableError(msg)
	}
	return &d, nil
}

func operationHeadLines(d *releaseDecision) string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	// 完全形は他の文字の無い単独の行に置く。
	fmt.Fprintf(&b, "head:         %s（短縮形。完全形は次の行）\n", shortHead(d.Head))
	fmt.Fprintf(&b, "%s\n", d.Head)
	if d.NoVerifiedHead {
		b.WriteString("              ※検証の head の記録が無いため、比較していません\n")
	}
	if d.MergeIn != nil {
		writeHeadChangeLines(&b, "              ", d.MergeIn)
	}
	return b.String()
}

// writeHeadChangeLines は、検証の後に動いた release の head の評価を要約へ書く: 検証の head・今の head・
// 判定・後から入ったコミットの一覧・取り込みのマージが直した差分。
func writeHeadChangeLines(b *strings.Builder, indent string, a *core.ReleaseHeadAssessment) {
	switch a.Change.Kind {
	case core.HeadChangeMergeInOnly:
		ci := "今の head の CI は通っています"
		if !a.ChecksPassed {
			ci = "今の head の CI は通っていません"
		}
		fmt.Fprintf(b, "%s※検証の後に、既定ブランチの取り込みだけが入っています（%s）。検証の head と異なる head を承認します\n", indent, ci)
	case core.HeadChangeContentChanged:
		fmt.Fprintf(b, "%s※検証の後に、取り込み以外の変更が入っています（%s）\n", indent, escapeControl(a.Change.Detail))
	default:
		fmt.Fprintf(b, "%s※検証の後に head が動きましたが、取り込みだけか判定できませんでした（%s）\n", indent, escapeControl(a.Change.Detail))
	}
	fmt.Fprintf(b, "%s検証の head: %s（短縮形。完全形は次の行）\n%s\n", indent, shortHead(a.VerifiedHead), a.VerifiedHead)
	fmt.Fprintf(b, "%s今の head:   %s\n", indent, shortHead(a.HeadSHA))
	if len(a.Change.Commits) > 0 {
		fmt.Fprintf(b, "%s検証の後に入ったコミット（%d 件）:\n", indent, len(a.Change.Commits))
		for _, c := range a.Change.Commits {
			fmt.Fprintf(b, "%s  - %s [%s] %s\n", indent, shortHead(c.SHA), commitRoleLabel(c.Role), escapeControl(c.Subject))
		}
	}
	for _, fix := range a.Change.ConflictFixes {
		if len(fix.Files) == 0 {
			fmt.Fprintf(b, "%s取り込みのマージ %s: 検証した版と既定ブランチ側の両方と異なるファイルはありません（衝突を直した差分なし）\n", indent, shortHead(fix.MergeCommit))
			continue
		}
		fmt.Fprintf(b, "%s取り込みのマージ %s が、検証した版と既定ブランチ側の両方と異なる形にしたファイル（衝突を直した差分。検証した版からの差分）:\n", indent, shortHead(fix.MergeCommit))
		for _, f := range fix.Files {
			fmt.Fprintf(b, "%s  ファイル: %s\n", indent, escapeControl(f.Filename))
			if f.PatchUnavailable {
				fmt.Fprintf(b, "%s    （上流が差分を返しませんでした。GitHub の比較で確かめてください）\n", indent)
				continue
			}
			b.WriteString(indentLines(escapeControlKeepLines(f.Patch), indent+"    "))
			if !strings.HasSuffix(f.Patch, "\n") {
				b.WriteString("\n")
			}
		}
	}
}

// commitRoleLabel は検証の後に入ったコミットの役割の表示名。
func commitRoleLabel(r core.CommitRole) string {
	switch r {
	case core.CommitRoleMergeIn:
		return "取り込みのマージ"
	case core.CommitRoleDefaultBranch:
		return "既定ブランチ側"
	case core.CommitRolePRChange:
		return "PR 側の変更"
	}
	return string(r)
}

func releaseViews(ds []releaseDecision) (approved []view.ApprovedRelease, excluded []view.ExcludedRelease) {
	for _, d := range includedReleases(ds) {
		ar := view.ApprovedRelease{OperationID: d.Op.ID, HeadSHA: d.Head}
		if d.MergeIn != nil {
			hc := view.FromReleaseHeadAssessment(d.MergeIn)
			ar.MergeIn = &hc
		}
		approved = append(approved, ar)
	}
	for _, d := range excludedReleases(ds) {
		excluded = append(excluded, view.ExcludedRelease{OperationID: d.Op.ID, Reason: string(d.Reason), Detail: d.Detail})
	}
	return approved, excluded
}

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
		return "検証の後に push されたコミットがあります（取得した head が J5 の検証の head と異なります）"
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
			if v, ok := verified(out[i].Op.ID); ok {
				if v != out[i].Head {
					out[i].Reason = reasonVerifiedHeadMismatch
				}
			} else {
				out[i].NoVerifiedHead = true
			}
		}
	}
	return out, nil
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
		out = append(out, core.ReleaseHead{Operation: d.Op, HeadSHA: &h})
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
		fmt.Fprintf(b, "  %s の head: %s（%s）", d.Op.ID, shortHead(d.Head), d.Head)
		if d.NoVerifiedHead {
			b.WriteString(" ※検証の head の記録が無いため、比較していません")
		}
		b.WriteString("\n")
	}
	for _, d := range excludedReleases(ds) {
		fmt.Fprintf(b, "  %s は同時に承認されません（未承認のまま残ります）: %s（%s）\n", d.Op.ID, d.Reason, d.Reason.description())
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
		return nil, newReleaseNotApprovableError(
			fmt.Sprintf("%s を承認の対象にできません: %s（%s）", p.OperationID, d.Reason, d.Reason.description()))
	}
	return &d, nil
}

func operationHeadLines(d *releaseDecision) string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "head:         %s（%s）\n", shortHead(d.Head), d.Head)
	if d.NoVerifiedHead {
		b.WriteString("              ※検証の head の記録が無いため、比較していません\n")
	}
	return b.String()
}

func releaseViews(ds []releaseDecision) (approved []view.ApprovedRelease, excluded []view.ExcludedRelease) {
	for _, d := range includedReleases(ds) {
		approved = append(approved, view.ApprovedRelease{OperationID: d.Op.ID, HeadSHA: d.Head})
	}
	for _, d := range excludedReleases(ds) {
		excluded = append(excluded, view.ExcludedRelease{OperationID: d.Op.ID, Reason: string(d.Reason)})
	}
	return approved, excluded
}

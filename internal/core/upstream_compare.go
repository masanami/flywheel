package core

// このファイルは、release の PR の head が検証の後に動いたとき、動いた中身が「既定ブランチを
// 取り込んだだけ」かを判定する規則（docs/features/m4.5-learning-and-self-improvement.md
// §検証の後に head が動いた release）を持つ。GitHub の compare の取得は IF（UpstreamCompareSource）の
// 実装〔internal/adapters/github〕が担い、取り込みかどうかの規則・CI が通っているかの判定は core が持つ。
// ストアに触れない。

import (
	"context"
	"fmt"
	"strings"
)

// UpstreamComparison.Status の値（GitHub の compare の status）。
const (
	UpstreamCompareAhead     = "ahead"
	UpstreamCompareBehind    = "behind"
	UpstreamCompareIdentical = "identical"
	UpstreamCompareDiverged  = "diverged"
)

// UpstreamCommit は compare が返すコミット 1 件。
type UpstreamCommit struct {
	SHA string
	// Parents は親のコミットの SHA（マージのコミットは 2 つ以上）。
	Parents []string
	// Message はコミットメッセージの全文。
	Message string
}

// UpstreamFile は compare が返す変更ファイル 1 件。
type UpstreamFile struct {
	Filename string
	// Patch はそのファイルの差分（unified diff の本体）。上流が返さないファイル（大きすぎる・バイナリ）は空。
	Patch string
}

// UpstreamComparison は 2 つのコミットの比較（base...head）の正規化済みの値。
type UpstreamComparison struct {
	// Status は UpstreamCompare* の値。
	Status string
	// TotalCommits は上流が数えた head までのコミットの総数。Commits の件数より多ければ打ち切られている。
	TotalCommits int
	Commits      []UpstreamCommit
	Files        []UpstreamFile
	// FilesTruncated は変更ファイルの一覧が 1 ページに収まらず、打ち切られているかもしれないこと。
	FilesTruncated bool
}

// UpstreamCompareSource は release の head の変化の判定が呼ぶ、2 つのコミットの比較の取得 IF。
// 実装は internal/adapters/github が `gh api` の GET だけで行い、取得と正規化だけを担う。
type UpstreamCompareSource interface {
	// CompareCommits は repo（"<owner>/<name>"）の base...head の比較を返す。base・head はコミットの
	// SHA かブランチ名。
	CompareCommits(ctx context.Context, repo, base, head string) (UpstreamComparison, error)
}

// HeadChangeKind は検証の head から今の head への変化の区分。
type HeadChangeKind string

const (
	// HeadChangeMergeInOnly は、後から入ったコミットが既定ブランチを取り込むマージと、既定ブランチ側の
	// コミットだけであること。
	HeadChangeMergeInOnly HeadChangeKind = "merge_in_only"
	// HeadChangeContentChanged は、PR 側に取り込み以外のコミットが入ったこと。
	HeadChangeContentChanged HeadChangeKind = "content_changed"
	// HeadChangeUndeterminable は、force-push・打ち切り・取得の失敗などで判定できないこと。
	HeadChangeUndeterminable HeadChangeKind = "undeterminable"
)

// CommitRole は、検証の後に入ったコミット 1 件の役割。
type CommitRole string

const (
	// CommitRoleMergeIn は、PR 側にあって既定ブランチを取り込むマージのコミット。
	CommitRoleMergeIn CommitRole = "merge_in"
	// CommitRoleDefaultBranch は、既定ブランチ側から取り込まれたコミット。
	CommitRoleDefaultBranch CommitRole = "default_branch"
	// CommitRolePRChange は、PR 側にあって取り込み以外のコミット（中身の変更）。
	CommitRolePRChange CommitRole = "pr_change"
)

// HeadChangeCommit は検証の後に入ったコミット 1 件。
type HeadChangeCommit struct {
	SHA string
	// Subject はコミットメッセージの 1 行目。
	Subject string
	Role    CommitRole
}

// ConflictFixFile は、取り込みのマージで両方の親と異なるファイル 1 件と、検証した版（PR 側の親）からの差分。
type ConflictFixFile struct {
	Filename string
	Patch    string
	// PatchUnavailable は上流が差分を返さなかったファイル（Patch は空）。
	PatchUnavailable bool
}

// ConflictFix は取り込みのマージ 1 件が、両方の親と異なる形にしたファイルの一覧。
type ConflictFix struct {
	MergeCommit string
	Files       []ConflictFixFile
}

// HeadChange は ClassifyHeadChange の結果。
type HeadChange struct {
	Kind HeadChangeKind
	// Detail は区分の根拠（undeterminable は判定できなかった理由、content_changed は取り込みでない
	// コミットの SHA）。merge_in_only は空。
	Detail string
	// Commits は検証の head の後に入ったコミット（上流が返した順）。
	Commits []HeadChangeCommit
	// ConflictFixes は merge_in_only のときだけ、取り込みのマージごとに直した差分を持つ。
	ConflictFixes []ConflictFix
	// HistoryRewritten は、今の head が検証の head の先にない（force-push・rebase 等で履歴が書き換わった）と、
	// 上流が確かに返したこと。取得の失敗・打ち切りでは立てない。
	HistoryRewritten bool
}

// RequiresReverification は、検証の head の中身をそのまま信頼できず、課題を着手中へ戻して検証し直す必要があると
// 確かに言えるかを返す。取り込み以外のコミットが入った（content_changed）か、履歴が書き換わった（force-push 等）ときに真。
// 取得の失敗・打ち切りで判定できないときは、一時的な失敗で課題を戻さないよう偽。
func (c HeadChange) RequiresReverification() bool {
	return c.Kind == HeadChangeContentChanged || (c.Kind == HeadChangeUndeterminable && c.HistoryRewritten)
}

func undeterminable(format string, args ...any) HeadChange {
	return HeadChange{Kind: HeadChangeUndeterminable, Detail: fmt.Sprintf(format, args...)}
}

// commitSubject は message の 1 行目。
func commitSubject(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	return strings.TrimSpace(line)
}

// ClassifyHeadChange は、検証の head（verifiedHead）から今の head（currentHead）までに入ったコミットが
// 「既定ブランチ（prBase）を取り込んだだけ」かを判定する。
//
// R＝verifiedHead...currentHead のコミット、P＝prBase...currentHead のコミット（既定ブランチに無い、PR 側だけのコミット）。
// verifiedHead...currentHead が ahead でない（force-push 等）・コミットが打ち切られている・取得の失敗は判定できない。
// R の各コミットは、P に無ければ既定ブランチから取り込まれた側。P にあれば、親が 2 つ以上でそのうち 1 つ以上が P に
// 無い（既定ブランチの履歴にある）ときだけ取り込みのマージで、それ以外は中身の変更。
// すべてが取り込みなら、マージごとに「両方の親と異なるファイル」と PR 側の親からの差分を取って返す。
// それが取れなければ判定できない（直した差分を要約に出せない取り込みは承認の対象にしない）。
func ClassifyHeadChange(ctx context.Context, src UpstreamCompareSource, repo, prBase, verifiedHead, currentHead string) HeadChange {
	if src == nil {
		return undeterminable("no compare source")
	}
	r, err := src.CompareCommits(ctx, repo, verifiedHead, currentHead)
	if err != nil {
		return undeterminable("compare %s...%s: %v", shortSHA(verifiedHead), shortSHA(currentHead), err)
	}
	if r.Status != UpstreamCompareAhead {
		out := undeterminable("the head is not ahead of the verified head (status %q)", r.Status)
		out.HistoryRewritten = true
		return out
	}
	if r.TotalCommits > len(r.Commits) {
		return undeterminable("the commit list is truncated (%d of %d)", len(r.Commits), r.TotalCommits)
	}
	if len(r.Commits) == 0 {
		return undeterminable("the comparison has no commits")
	}
	p, err := src.CompareCommits(ctx, repo, prBase, currentHead)
	if err != nil {
		return undeterminable("compare %s...%s: %v", prBase, shortSHA(currentHead), err)
	}
	if p.TotalCommits > len(p.Commits) {
		return undeterminable("the PR commit list is truncated (%d of %d)", len(p.Commits), p.TotalCommits)
	}
	inP := make(map[string]bool, len(p.Commits))
	for _, c := range p.Commits {
		inP[c.SHA] = true
	}
	if !inP[currentHead] {
		return undeterminable("the head %s is not a commit of the PR side of %s", shortSHA(currentHead), prBase)
	}

	out := HeadChange{Kind: HeadChangeMergeInOnly}
	var mergeIns []UpstreamCommit
	firstOffender := ""
	for _, c := range r.Commits {
		hc := HeadChangeCommit{SHA: c.SHA, Subject: commitSubject(c.Message)}
		switch {
		case !inP[c.SHA]:
			hc.Role = CommitRoleDefaultBranch
		case isDefaultBranchMerge(c, inP):
			hc.Role = CommitRoleMergeIn
			mergeIns = append(mergeIns, c)
		default:
			hc.Role = CommitRolePRChange
			if firstOffender == "" {
				firstOffender = c.SHA
			}
		}
		out.Commits = append(out.Commits, hc)
	}
	if firstOffender != "" {
		out.Kind = HeadChangeContentChanged
		out.Detail = "commit " + firstOffender + " is not a merge of the default branch"
		return out
	}
	for _, m := range mergeIns {
		fix, ok, why := conflictFix(ctx, src, repo, m, inP)
		if !ok {
			return undeterminable("%s", why)
		}
		out.ConflictFixes = append(out.ConflictFixes, fix)
	}
	return out
}

// isDefaultBranchMerge は、P のコミット c が、親が 2 つ以上で、そのうち 1 つ以上が P に無い
// （既定ブランチの履歴にある）取り込みのマージかを返す。
func isDefaultBranchMerge(c UpstreamCommit, inP map[string]bool) bool {
	if len(c.Parents) < 2 {
		return false
	}
	for _, par := range c.Parents {
		if !inP[par] {
			return true
		}
	}
	return false
}

// conflictFix は取り込みのマージ m の、両方の親と異なるファイルと、PR 側の親（P にある最初の親。無ければ最初の親）からの
// 差分を返す。ok=false は取得に失敗した・一覧が打ち切られているかもしれない。
func conflictFix(ctx context.Context, src UpstreamCompareSource, repo string, m UpstreamCommit, inP map[string]bool) (ConflictFix, bool, string) {
	prSide, defaultSide := m.Parents[0], ""
	for _, par := range m.Parents {
		if inP[par] {
			prSide = par
			break
		}
	}
	for _, par := range m.Parents {
		if !inP[par] && par != prSide {
			defaultSide = par
			break
		}
	}
	if defaultSide == "" {
		return ConflictFix{}, false, fmt.Sprintf("the merge %s has no default-branch parent", shortSHA(m.SHA))
	}
	fromPR, err := src.CompareCommits(ctx, repo, prSide, m.SHA)
	if err != nil {
		return ConflictFix{}, false, fmt.Sprintf("compare %s...%s: %v", shortSHA(prSide), shortSHA(m.SHA), err)
	}
	fromDefault, err := src.CompareCommits(ctx, repo, defaultSide, m.SHA)
	if err != nil {
		return ConflictFix{}, false, fmt.Sprintf("compare %s...%s: %v", shortSHA(defaultSide), shortSHA(m.SHA), err)
	}
	if fromPR.FilesTruncated || fromDefault.FilesTruncated {
		return ConflictFix{}, false, fmt.Sprintf("the changed files of the merge %s may be truncated", shortSHA(m.SHA))
	}
	differsFromDefault := make(map[string]bool, len(fromDefault.Files))
	for _, f := range fromDefault.Files {
		differsFromDefault[f.Filename] = true
	}
	fix := ConflictFix{MergeCommit: m.SHA}
	for _, f := range fromPR.Files {
		if !differsFromDefault[f.Filename] {
			continue
		}
		fix.Files = append(fix.Files, ConflictFixFile{Filename: f.Filename, Patch: f.Patch, PatchUnavailable: f.Patch == ""})
	}
	return fix, true, ""
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// ChecksAllPassed は PR の head のチェックが 1 件以上あり、すべて合格（classifyCheck）かを返す。
// 0 件・失敗・未完了は通っていないとする（実行の判定の順ではチェック 0 件を合格にするが、検証の後に動いた
// head を人の確認なしの検証で承認する道では、何も見ていない状態を通ったことにしない）。
func ChecksAllPassed(checks []UpstreamCheck) bool {
	if len(checks) == 0 {
		return false
	}
	for _, c := range checks {
		if classifyCheck(c) != checkPass {
			return false
		}
	}
	return true
}

// ReleaseHeadAssessment は、検証の head と今の head が異なる release の評価（AssessReleaseHead の結果）。
type ReleaseHeadAssessment struct {
	// HeadSHA は評価した今の head、VerifiedHead は比べた検証の head。
	HeadSHA      string
	VerifiedHead string
	// PRState は PR の状態（open・closed・merged）。
	PRState string
	// Change は検証の head から今の head までの変化。
	Change HeadChange
	// ChecksPassed は今の head のチェックが 1 件以上あり、すべて合格であること。
	ChecksPassed bool
	ChecksCount  int
}

// MergeInApprovable は、取り込みだけで CI が通っており、検証の head から承認し直せる評価かを返す。
func (a ReleaseHeadAssessment) MergeInApprovable() bool {
	return a.Change.Kind == HeadChangeMergeInOnly && a.ChecksPassed
}

// AssessReleaseHead は ref（PR の URL）の PR の今の head を読み、verifiedHead から動いた中身を評価する。
// PR が open でなければ判定できない扱いにする。ref が PR の URL でない・PR の取得に失敗したときはエラー。
// 今の head が verifiedHead と同じなら、Change は空（Kind が空）で返す。
func AssessReleaseHead(ctx context.Context, checks UpstreamCheckSource, compare UpstreamCompareSource, ref, verifiedHead string) (*ReleaseHeadAssessment, error) {
	repo, number, ok := parseReleasePullRequestRef(ref)
	if !ok {
		return nil, fmt.Errorf("core: %q is not a GitHub pull request URL", ref)
	}
	if checks == nil {
		return nil, fmt.Errorf("core: no check source")
	}
	pr, err := checks.GetPullRequestChecks(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	a := &ReleaseHeadAssessment{HeadSHA: pr.HeadSHA, VerifiedHead: verifiedHead, PRState: pr.State, ChecksCount: len(pr.Checks), ChecksPassed: ChecksAllPassed(pr.Checks)}
	if pr.HeadSHA == "" || pr.HeadSHA == verifiedHead {
		return a, nil
	}
	if pr.State != string(artifactStateOpen) {
		a.Change = undeterminable("the pull request is %s", pr.State)
		return a, nil
	}
	a.Change = ClassifyHeadChange(ctx, compare, repo, pr.Base, verifiedHead, pr.HeadSHA)
	return a, nil
}

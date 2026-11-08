package core

// このファイルは、課題の前の委譲が残した PR（前の成果物）を、次の委譲の入力（子の標準入力と
// J3 の入力）へ渡すための取り出しと状態の確認を持つ。完了確認の差し戻し・J5 の not_met・保留への
// 回答のいずれの後でも、次の委譲は子の文脈を持たない新しいセッションか、前の文脈を持つ再開に
// なりうる。前のブランチと PR が渡らないと、子が新しいブランチと PR を作り直すおそれがある。
//
// 取り出しの正は run_artifact の PR（委譲の後の照合が gh で確かめた値）で、無ければ最新の
// 成功した run の報告の pr_urls。PR の状態は委譲の前に GetPullRequest で確かめ、core が状態を
// 決めて構造化した値（PriorArtifact）として渡す。続け方の文面は invoker が持つ。

import (
	"context"
	"database/sql"
	"strings"
)

// PriorArtifactState は前の成果物の PR の状態。
type PriorArtifactState string

const (
	// PriorArtifactOpen は PR が開いている（同じ PR に push で続ける）。
	PriorArtifactOpen PriorArtifactState = "open"
	// PriorArtifactClosed は PR が閉じている（マージされていない）。
	PriorArtifactClosed PriorArtifactState = "closed"
	// PriorArtifactMerged は PR がマージ済み。
	PriorArtifactMerged PriorArtifactState = "merged"
	// PriorArtifactUnknown は PR の状態を確かめられなかった。
	PriorArtifactUnknown PriorArtifactState = "unknown"
)

// PriorArtifact は次の委譲に渡す、前の委譲の PR。前の PR が無い課題では nil。
//
// Branch・PRURL・HeadSHA は、続け先として渡してよい値だけを入れる: State が open か unknown のときだけ
// 入り（HeadSHA は確かめられたときだけ）、closed・merged では空にする。
type PriorArtifact struct {
	State   PriorArtifactState
	Branch  string
	PRURL   string
	HeadSHA string
}

// priorPR は前の委譲の PR の候補（状態を確かめる前）。
type priorPR struct {
	URL    string
	Branch string
}

// newestPRInRepo は urls のうち repo（"<owner>/<name>"）の PR で、番号が最大の（＝最後に作られた）
// URL を返す。無ければ ""。
func newestPRInRepo(urls []string, repo string) string {
	best, bestN := "", 0
	for _, u := range urls {
		r, n, ok := parsePullRequestURL(u)
		if !ok || !strings.EqualFold(r, repo) {
			continue
		}
		if n > bestN {
			best, bestN = u, n
		}
	}
	return best
}

// loadPriorPR は課題の委譲の run（id 昇順。全部の計画の版）から、最新の「前の PR」を取り出す。
// repo は委譲先のリポジトリ。正は run_artifact の PR（照合が確かめた値）で、委譲先のリポジトリの PR を
// 持つ最新の run のうち、番号が最大のもの。無ければ、最新の成功した run の報告の pr_urls のうち
// 委譲先のリポジトリのもの。ブランチ名は同じ run の branch の成果物、無ければ loadRunBranch（既定
// ブランチは除く）。どこにも PR が無ければ nil。
func loadPriorPR(ctx context.Context, tx *sql.Tx, runs []runRow, declared []HumanQuestionKind, repo ConnectorRepo) (*priorPR, error) {
	branchOf := func(r runRow, arts []runArtifactRow) (string, error) {
		for i := len(arts) - 1; i >= 0; i-- {
			if a := arts[i]; a.Kind == artifactKindBranch && plausibleBranchName(a.Ref) && a.Ref != repo.DefaultBranch {
				return a.Ref, nil
			}
		}
		return loadRunBranch(ctx, tx, r, declared, repo.DefaultBranch)
	}
	for i := len(runs) - 1; i >= 0; i-- {
		arts, err := listRunArtifacts(ctx, tx, runIntID(runs[i]))
		if err != nil {
			return nil, err
		}
		var urls []string
		for _, a := range arts {
			if a.Kind == artifactKindPR {
				urls = append(urls, a.Ref)
			}
		}
		if u := newestPRInRepo(urls, repo.Remote); u != "" {
			b, err := branchOf(runs[i], arts)
			if err != nil {
				return nil, err
			}
			return &priorPR{URL: u, Branch: b}, nil
		}
	}
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		if r.Result != RunResultSucceeded || r.Output == "" {
			continue
		}
		rep, ok := validateDelegationReport([]byte(r.Output), declared)
		if !ok {
			continue
		}
		if u := newestPRInRepo(rep.PRURLs, repo.Remote); u != "" {
			arts, err := listRunArtifacts(ctx, tx, runIntID(r))
			if err != nil {
				return nil, err
			}
			b, err := branchOf(r, arts)
			if err != nil {
				return nil, err
			}
			return &priorPR{URL: u, Branch: b}, nil
		}
	}
	return nil, nil
}

// resolvePriorArtifact は候補 p の PR の状態を src で確かめ、次の委譲に渡す前の成果物を決める。
// 呼ぶのはトランザクションの外。確かめられなかった（src が無い・URL が分解できない・取得の失敗・
// 状態が未知）ときは状態不明として、候補の値をそのまま渡す（委譲は止めない）。閉じた・マージ済みの
// PR は続け先として渡さないので、状態だけにする。
func resolvePriorArtifact(ctx context.Context, src UpstreamBranchSource, p priorPR, defaultBranch string) *PriorArtifact {
	unknown := &PriorArtifact{State: PriorArtifactUnknown, Branch: p.Branch, PRURL: p.URL}
	if src == nil {
		return unknown
	}
	repo, n, ok := parsePullRequestURL(p.URL)
	if !ok {
		return unknown
	}
	pr, err := src.GetPullRequest(ctx, repo, n)
	if err != nil {
		return unknown
	}
	switch pr.State {
	case string(PriorArtifactClosed):
		return &PriorArtifact{State: PriorArtifactClosed}
	case string(PriorArtifactMerged):
		return &PriorArtifact{State: PriorArtifactMerged}
	case string(PriorArtifactOpen):
		out := &PriorArtifact{State: PriorArtifactOpen, Branch: p.Branch, PRURL: p.URL, HeadSHA: strings.TrimSpace(pr.HeadSHA)}
		if pr.URL != "" {
			out.PRURL = pr.URL
		}
		if ref := strings.TrimSpace(pr.HeadRef); plausibleBranchName(ref) && ref != defaultBranch {
			out.Branch = ref
		}
		return out
	}
	return unknown
}

// formatPriorArtifactSection は J3 の入力の区画「前の成果物」の本文（状態と、続け先として渡す値）。
// 前の PR が無ければ ""。閉じた・マージ済みの PR は URL・ブランチを書かない。
func formatPriorArtifactSection(p *PriorArtifact) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	switch p.State {
	case PriorArtifactOpen:
		b.WriteString("前の委譲が作った PR の状態: 開いている (open)")
	case PriorArtifactClosed:
		b.WriteString("前の委譲が作った PR の状態: 閉じている (closed)")
	case PriorArtifactMerged:
		b.WriteString("前の委譲が作った PR の状態: マージ済み (merged)")
	default:
		b.WriteString("前の委譲が作った PR の状態: 確かめられなかった (unknown)")
	}
	if p.State == PriorArtifactClosed || p.State == PriorArtifactMerged {
		return b.String()
	}
	if p.Branch != "" {
		b.WriteString("\nブランチ: " + p.Branch)
	}
	if p.PRURL != "" {
		b.WriteString("\nPR: " + p.PRURL)
	}
	if p.HeadSHA != "" {
		b.WriteString("\nhead: " + p.HeadSHA)
	}
	return b.String()
}

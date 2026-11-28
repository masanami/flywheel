package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// --- 取り込めるものに無い指定の理由 ---

// ingestRejectReason は、取り込めるものに無い指定を断る理由を 1 つ返す。理由は、課題になっている
// （子の記録を含む）・閉じている・方針の外・見つからない（上流の開いている Issue の一覧にまだ無いを含む）の
// どれか。文は理由の語で始める。key は `<owner>/<repo>#<番号>` の指定（番号だけの指定は ""）。
//
// 呼ぶのは、指定の範囲の取り込み元と一覧をすべて読めたと確かめた後だけ。開いている Issue の一覧に無いものは、
// 閉じているのか無いのかが一覧からは分からないので、1 件の取得で確かめる。返す error は呼び出し側の中断だけ。
func ingestRejectReason(ctx context.Context, scan *IngestScan, in IngestSelectedInput, key string, number int) (string, error) {
	type target struct{ repo, key string }
	var targets []target // 指定が指しうる、宣言のリポジトリの Issue（宣言の順）
	for _, src := range in.Sources {
		if key == "" && in.SourceID != nil && src.ID != *in.SourceID {
			continue
		}
		for _, repo := range src.Repos {
			k := fmt.Sprintf("%s#%d", repo, number)
			if key == "" || strings.EqualFold(k, key) {
				targets = append(targets, target{repo: repo, key: k})
			}
		}
	}

	// 課題になっているかは、上流に依らず記録で決まる（宣言から外したリポジトリの指定も見る）。
	recorded := []string{key}
	if key == "" {
		recorded = recorded[:0]
		for _, t := range targets {
			recorded = append(recorded, t.key)
		}
	}
	for _, k := range recorded {
		if scan.taken[strings.ToLower(k)] {
			return fmt.Sprintf("already a challenge (%s is recorded as a challenge or as a child of one)", k), nil
		}
	}
	if key != "" && len(targets) == 0 {
		return "out of policy (the repository is not declared in any source)", nil
	}
	// 一覧に在って課題でも子でもないのに取り込めるものに無いのは、方針に合わないから。
	for _, t := range targets {
		if scan.listed[strings.ToLower(t.key)] {
			return fmt.Sprintf("out of policy (%s does not match the assignee_policy of its source)", t.key), nil
		}
	}

	var unlisted, closed, unreadable string
	for _, t := range targets {
		issue, err := in.Upstream.GetIssue(ctx, t.repo, number)
		if cerr := ctx.Err(); cerr != nil {
			return "", cerr
		}
		switch {
		case errors.Is(err, ErrUpstreamIssueNotFound), errors.Is(err, ErrUpstreamIssueTransferred):
		case err != nil:
			unreadable = fmt.Sprintf("not found in the upstream list of open issues (the state of %s could not be read: %v)", t.key, err)
		case issue.State == "open":
			unlisted = fmt.Sprintf("not found: %s is not in the upstream list of open issues yet (the issue itself is open; "+
				"the list may lag right after the issue is created, so retry shortly)", t.key)
		default:
			closed = fmt.Sprintf("closed (%s is not open)", t.key)
		}
	}
	for _, reason := range []string{unlisted, closed, unreadable} {
		if reason != "" {
			return reason, nil
		}
	}
	return "not found (there is no such issue in the declared repositories)", nil
}

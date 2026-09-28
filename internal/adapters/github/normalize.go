package github

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// issuePayload は gh api の Issue 1 件分の応答（一覧の要素・1 件取得の本文の
// どちらも同じ形）のうち、正規化に必要な項目だけを取り出す。
type issuePayload struct {
	Number        int          `json:"number"`
	Title         string       `json:"title"`
	Body          *string      `json:"body"`
	User          issueUser    `json:"user"`
	Assignees     []issueUser  `json:"assignees"`
	Labels        []issueLabel `json:"labels"`
	State         string       `json:"state"`
	HTMLURL       string       `json:"html_url"`
	RepositoryURL string       `json:"repository_url"`
	// Comments・UpdatedAt は観測値（docs/features/m2-github-issue-ingest.md
	// §上流の更新の観測と既読）。一覧・1件取得のどちらの応答にも含まれるため、
	// 観測のための追加の API 呼び出しは要らない（QH9）。
	Comments  int    `json:"comments"`
	UpdatedAt string `json:"updated_at"`
}

type issueUser struct {
	Login string `json:"login"`
}

// issueLabel は REST の labels 応答が文字列（"bug"）とオブジェクト
// （{"name":"bug"}）のどちらの形でも来うることに対応する（GitHub REST API の
// Issue のスキーマは labels の要素に文字列とオブジェクトの両方を許す）。
type issueLabel struct {
	Name string
}

func (l *issueLabel) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		l.Name = name
		return nil
	}
	var obj struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("adapters/github: unmarshal label %q: %w", data, err)
	}
	l.Name = obj.Name
	return nil
}

// isPullRequestElement は raw が一覧の Issue 要素のうち Pull Request（応答が
// pull_request キーを持つ）かどうかを判定する（core は Pull Request を
// 意識しない。§機能全体の設計）。
func isPullRequestElement(raw json.RawMessage) (bool, error) {
	var probe struct {
		PullRequest json.RawMessage `json:"pull_request"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false, fmt.Errorf("adapters/github: probe pull_request key: %w", err)
	}
	return probe.PullRequest != nil, nil
}

// normalizeIssue は gh api の応答 1 件分（json.RawMessage）を
// core.UpstreamIssue へ正規化する。Repo・ExternalKey は repository_url の
// 表記（API 応答の表記）を採用する（internal/core/upstream.go の契約。
// 宣言側の表記のゆれをそのまま持ち込まない）。
func normalizeIssue(raw json.RawMessage) (core.UpstreamIssue, error) {
	var payload issuePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: decode issue: %w", err)
	}

	repo, err := repoFromRepositoryURL(payload.RepositoryURL)
	if err != nil {
		return core.UpstreamIssue{}, err
	}

	if payload.State != "open" && payload.State != "closed" {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: unexpected issue state %q for %s#%d", payload.State, repo, payload.Number)
	}

	body := ""
	if payload.Body != nil {
		body = *payload.Body
	}

	assignees := make([]string, 0, len(payload.Assignees))
	for _, a := range payload.Assignees {
		assignees = append(assignees, a.Login)
	}

	labels := make([]string, 0, len(payload.Labels))
	for _, l := range payload.Labels {
		labels = append(labels, l.Name)
	}

	return core.UpstreamIssue{
		ExternalKey: fmt.Sprintf("%s#%d", repo, payload.Number),
		Repo:        repo,
		Number:      payload.Number,
		Title:       payload.Title,
		Body:        body,
		Reporter:    payload.User.Login,
		Assignees:   assignees,
		Labels:      labels,
		State:       payload.State,
		URL:         payload.HTMLURL,
		Comments:    payload.Comments,
		UpdatedAt:   payload.UpdatedAt,
	}, nil
}

// commentPayload は gh api の Issue コメント 1 件分の応答のうち、正規化に
// 必要な項目だけを取り出す（#80）。
type commentPayload struct {
	Body      *string   `json:"body"`
	User      issueUser `json:"user"`
	CreatedAt string    `json:"created_at"`
	HTMLURL   string    `json:"html_url"`
}

// normalizeComment は gh api の応答 1 件分（json.RawMessage）を
// core.UpstreamComment へ正規化する。CreatedAt は加工・パースしない
// （UpstreamIssue.UpdatedAt と同じ方針）。
func normalizeComment(raw json.RawMessage) (core.UpstreamComment, error) {
	var payload commentPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return core.UpstreamComment{}, fmt.Errorf("adapters/github: decode comment: %w", err)
	}

	body := ""
	if payload.Body != nil {
		body = *payload.Body
	}

	return core.UpstreamComment{
		Author:    payload.User.Login,
		Body:      body,
		CreatedAt: payload.CreatedAt,
		URL:       payload.HTMLURL,
	}, nil
}

// reposMarker は REST API の応答の repository_url に含まれる区切り
// （"/repos/<owner>/<name>"）。
const reposMarker = "/repos/"

// repoFromRepositoryURL は repository_url（例:
// "https://api.github.com/repos/masanami/flywheel"）から
// "<owner>/<name>" を取り出す。取れなければエラーを返す
// （internal/core/upstream.go「取れなければ失敗」）。
func repoFromRepositoryURL(u string) (string, error) {
	idx := strings.Index(u, reposMarker)
	if idx == -1 {
		return "", fmt.Errorf("adapters/github: cannot find %q in repository_url %q", reposMarker, u)
	}
	repo := strings.TrimSuffix(u[idx+len(reposMarker):], "/")
	if repo == "" || strings.Count(repo, "/") != 1 {
		return "", fmt.Errorf("adapters/github: unexpected repository_url %q (parsed repo %q)", u, repo)
	}
	return repo, nil
}

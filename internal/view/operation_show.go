package view

import "github.com/masanami/flywheel/internal/core"

// OperationShowChallenge は `show <OP-ID>` の、操作が属する課題。
type OperationShowChallenge struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// OperationTarget は不可逆操作の対象（core.OperationTarget）。分からない値は null。
type OperationTarget struct {
	BaseBranch *string `json:"base_branch"`
	PRNumber   *int    `json:"pr_number"`
	PRURL      *string `json:"pr_url"`
	Repo       *string `json:"repo"`
}

// OperationShowResponse は `show <OP-ID>` の `{"challenge": …, "operation": …, "target": …}`。
type OperationShowResponse struct {
	Challenge OperationShowChallenge `json:"challenge"`
	Operation Operation              `json:"operation"`
	Target    OperationTarget        `json:"target"`
}

// FromOperationDetail は core.OperationDetail を OperationShowResponse へ変換する。
func FromOperationDetail(d *core.OperationDetail) OperationShowResponse {
	return OperationShowResponse{
		Challenge: OperationShowChallenge{ID: d.Operation.ChallengeID, Title: d.ChallengeTitle},
		Operation: FromOperation(d.Operation),
		Target: OperationTarget{
			BaseBranch: d.Target.BaseBranch,
			PRNumber:   d.Target.PullRequestNumber,
			PRURL:      d.Target.PullRequestURL,
			Repo:       d.Target.Repo,
		},
	}
}

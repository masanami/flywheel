// このファイルは不可逆操作 1 件を読む公開 API（`show <OP-ID>`）を持つ。読み取りだけで、何も書かない。

package core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// OperationDetail は不可逆操作 1 件と、その課題の題名、反映の対象。
type OperationDetail struct {
	// Operation は課題の詳細（ChallengeDetail.Operations）の要素と同じ値。
	Operation      IrreversibleOperation
	ChallengeTitle string
	Target         OperationTarget
}

// OperationTarget は不可逆操作の対象。分からない値は nil。
type OperationTarget struct {
	// Repo（<owner>/<repo>）・PullRequestNumber・PullRequestURL は、参照が GitHub の PR の URL のときだけ値を持つ。
	Repo              *string
	PullRequestNumber *int
	PullRequestURL    *string
	// BaseBranch は反映先のブランチ。参照が PR の URL の release だけが値を持つ。反映の実行（judgeReleaseUpstream）が
	// 反映先とするのと同じ、宣言のそのリポジトリの既定ブランチ。PR の base ではなく、上流は読まない。
	BaseBranch *string
}

// GetOperation は不可逆操作 id（"OP-<n>"）を返す。形式の不正な ID と存在しない ID は ErrNotFound。
// connectors は反映先のブランチを引く宣言で、nil でよい（反映先は nil になる）。
func (s *Store) GetOperation(ctx context.Context, id string, connectors *ConnectorsDeclaration) (*OperationDetail, error) {
	oid, ok := parseOperationID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var out OperationDetail
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		row, err := loadOperationRow(ctx, tx, oid)
		if err != nil {
			return err
		}
		challenge, err := loadChallenge(ctx, tx, row.ChallengeID)
		if err != nil {
			return err
		}
		out.ChallengeTitle = challenge.Title
		ops, err := loadOperations(ctx, tx, row.ChallengeID)
		if err != nil {
			return err
		}
		for _, op := range ops {
			if op.ID == formatOperationID(oid) {
				out.Operation = op
				out.Target = operationTarget(op, connectors)
				return nil
			}
		}
		return fmt.Errorf("core: operation %s is not in the operations of challenge %s", id, challenge.ID)
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &out, nil
}

// operationTarget は op の参照と宣言から対象を組み立てる。
func operationTarget(op IrreversibleOperation, connectors *ConnectorsDeclaration) OperationTarget {
	var out OperationTarget
	if op.Ref == nil {
		return out
	}
	repo, number, ok := parseReleasePullRequestRef(*op.Ref)
	if !ok {
		return out
	}
	url := *op.Ref
	out.Repo, out.PullRequestNumber, out.PullRequestURL = &repo, &number, &url
	if op.Kind != OperationKindRelease || connectors == nil {
		return out
	}
	for _, r := range connectors.Repos {
		if strings.EqualFold(r.Remote, repo) {
			base := r.DefaultBranch
			out.BaseBranch = &base
			break
		}
	}
	return out
}

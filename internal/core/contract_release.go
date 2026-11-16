package core

// このファイルは `form: cli` の接続ツールの run が成果物つきで検証中へ進むとき（レビュー待ちの取り込み・succeeded）に、
// 成果物の PR を指す release（承認待ちの不可逆操作）を作り、前の PR を指す未実行の release を置き換える。
//
//   - 作る: base が宣言の既定ブランチで、open の PR ごとに 1 件。plugin 形態の照合と同じ createOperation（skipDuplicate）で作るので、
//     同じ PR の release を重ねて作らない。宛先が既定ブランチでない PR・宣言にないリポジトリの PR には作らない。
//   - 置き換える: 今回の PR の集合が空でないとき、同じ課題の承認待ち・承認済み（実行の試行が終わっていない）で、ref が今回の集合に
//     無い release を closed（理由 superseded）にして、承認の対象から外す。新しい状態は足さない。
//   - PR の取得に失敗したときは何も作らず、エラーを返す（検証中へ進めず、次の周で読み直す）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// contractReleaseSource は release の作成と置き換えに要る、PR の取得の口と宣言。Reconcile が nil なら release は扱わない。
type contractReleaseSource struct {
	Reconcile UpstreamBranchSource
	Decl      *ConnectorsDeclaration
	// J4 は、親が答える待ち（decider が parent・kind が answer）を J4 へ回せるとき真（偽ならオーナーの保留へ写す）。
	J4 bool
}

// defaultBranchPRs は arts の pr のうち、宣言のリポジトリで base が既定ブランチの open の PR を返す（重複は 1 件）。
// 見つからない（404）・取得した PR が求めたものと違う PR は数えない。それ以外の取得の失敗はエラー。
func (rs *contractReleaseSource) defaultBranchPRs(ctx context.Context, arts []ContractArtifact) ([]UpstreamPullRequest, error) {
	var out []UpstreamPullRequest
	seen := map[string]bool{}
	for _, a := range arts {
		if a.Kind != "pr" {
			continue
		}
		repoName, number, ok := parseReleasePullRequestRef(strings.TrimSpace(a.Ref))
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s#%d", strings.ToLower(repoName), number)
		if seen[key] {
			continue
		}
		seen[key] = true
		var decl *ConnectorRepo
		if rs.Decl != nil {
			for i := range rs.Decl.Repos {
				if strings.EqualFold(rs.Decl.Repos[i].Remote, repoName) {
					decl = &rs.Decl.Repos[i]
					break
				}
			}
		}
		if decl == nil {
			continue
		}
		pr, err := rs.Reconcile.GetPullRequest(ctx, decl.Remote, number)
		if err != nil {
			if errors.Is(err, ErrUpstreamPullRequestNotFound) {
				continue
			}
			return nil, fmt.Errorf("looking up the pull request %s#%d failed: %w", decl.Remote, number, err)
		}
		if gotRepo, gotNumber, ok := parseReleasePullRequestRef(pr.URL); !ok || gotNumber != number || !strings.EqualFold(gotRepo, decl.Remote) {
			continue
		}
		if pr.State == string(artifactStateOpen) && pr.Base == decl.DefaultBranch {
			out = append(out, pr)
		}
	}
	return out, nil
}

// createReleaseForPR は PR 1 件の release を、同じ課題に同じ PR の release が無ければ登録する（plugin 形態の照合と共通）。
// 同じ ref の release が既にある（閉じたものを含む）・課題が完了しているときは何もしない。
func (s *Store) createReleaseForPR(ctx context.Context, challengeID string, runInt int64, pr UpstreamPullRequest) error {
	ref := pr.URL
	summary := pr.Title
	if strings.TrimSpace(summary) == "" {
		summary = pr.URL // 要約は空にできないので、タイトルの無い PR は URL を要約にする
	}
	_, err := s.createOperation(ctx, ChannelInvoker, &runInt, true, OperationInput{
		ChallengeID: challengeID, Kind: string(OperationKindRelease), Summary: summary, Ref: &ref,
	})
	if err != nil && !errors.Is(err, errOperationDuplicate) && !errors.Is(err, ErrTerminalState) {
		return err
	}
	return nil
}

// registerContractReleases は、課題が着手中なら rep の成果物の PR の release を作り、前の PR の release を置き換える。
// 検証中へ進める遷移の前に呼ぶ（作れなかったときに、release の無い検証中を作らない）。
func (s *Store) registerContractReleases(ctx context.Context, rs *contractReleaseSource, rep *ContractRunReport) error {
	if rs == nil || rs.Reconcile == nil {
		return nil
	}
	runInt, ok := parseRunID(rep.RunID)
	cid, cok := parseChallengeID(rep.ChallengeID)
	if !ok || !cok {
		return nil
	}
	if inProgress, err := s.challengeInProgress(ctx, cid); err != nil || !inProgress {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	prs, err := rs.defaultBranchPRs(ctx, rep.Artifacts)
	if err != nil {
		return err
	}
	for _, pr := range prs {
		again, err := s.onlySupersededReleaseFor(ctx, cid, pr.URL)
		if err != nil {
			return err
		}
		if again {
			// 前の PR へ戻った。置き換えで閉じた release しか無いので、承認できる release を作り直す。
			ref := pr.URL
			summary := pr.Title
			if strings.TrimSpace(summary) == "" {
				summary = pr.URL
			}
			if _, err := s.createOperation(ctx, ChannelInvoker, &runInt, false, OperationInput{
				ChallengeID: rep.ChallengeID, Kind: string(OperationKindRelease), Summary: summary, Ref: &ref}); err != nil && !errors.Is(err, ErrTerminalState) {
				return err
			}
			continue
		}
		if err := s.createReleaseForPR(ctx, rep.ChallengeID, runInt, pr); err != nil {
			return err
		}
	}
	if len(prs) == 0 {
		return nil
	}
	keep := map[string]bool{}
	for _, pr := range prs {
		if repo, number, ok := parseReleasePullRequestRef(pr.URL); ok {
			keep[prKey(repo, number)] = true
		}
	}
	return s.supersedeStaleReleases(ctx, cid, runInt, keep)
}

func prKey(repo string, number int) string {
	return fmt.Sprintf("%s#%d", strings.ToLower(repo), number)
}

// onlySupersededReleaseFor は、課題 cid の ref の release がすべて superseded で閉じたものだけか（1 件以上ある）。
// 差し戻しで前の PR へ戻ったとき、承認できる release を作り直すための判定。
func (s *Store) onlySupersededReleaseFor(ctx context.Context, cid int64, ref string) (bool, error) {
	var total, superseded int
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT COUNT(*), COALESCE(SUM(state = ? AND closed_reason = ?), 0) FROM operation WHERE challenge_id = ? AND kind = ? AND ref = ?`,
			string(OperationStateClosed), string(ClosedReasonSuperseded), cid, string(OperationKindRelease), ref).Scan(&total, &superseded)
	})
	return total > 0 && total == superseded, classifyReadWriteErr(err)
}

// supersedeStaleReleases は課題 cid の承認待ち・承認済みの release のうち、実行の試行が終わっていなくて、ref が keep（小文字の URL）に
// 無いものを closed（superseded）にする。ref の無い release は触らない。
func (s *Store) supersedeStaleReleases(ctx context.Context, cid, runInt int64, keep map[string]bool) error {
	var stale []int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, ref FROM operation WHERE challenge_id = ? AND kind = ? AND state IN (?, ?) AND ref IS NOT NULL ORDER BY id ASC`,
			cid, string(OperationKindRelease), string(OperationStatePending), string(OperationStateApproved))
		if err != nil {
			return err
		}
		type cand struct {
			id  int64
			ref string
		}
		var cands []cand
		for rows.Next() {
			var c cand
			if err := rows.Scan(&c.id, &c.ref); err != nil {
				_ = rows.Close()
				return err
			}
			cands = append(cands, c)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, c := range cands {
			repo, number, ok := parseReleasePullRequestRef(c.ref)
			if !ok || keep[prKey(repo, number)] {
				continue // PR の URL ではない ref の release は触らない
			}
			last, err := latestOperationExecution(ctx, tx, c.id)
			if err != nil {
				return err
			}
			if last != nil && last.open() {
				continue
			}
			stale = append(stale, c.id)
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil || len(stale) == 0 {
		return err
	}
	actor, err := s.resolveActor()
	if err != nil {
		return err
	}
	for _, id := range stale {
		err := s.mutateAsRun(ctx, actor, ChannelInvoker, VerificationNone, &runInt, func(tx *sql.Tx, rec *activityRecorder) error {
			row, err := loadOperationRow(ctx, tx, id)
			if err != nil {
				return err
			}
			if row.State != OperationStatePending && row.State != OperationStateApproved {
				return nil
			}
			if last, err := latestOperationExecution(ctx, tx, id); err != nil {
				return err
			} else if last != nil && last.open() {
				return nil
			}
			chal, err := loadChallenge(ctx, tx, row.ChallengeID)
			if err != nil {
				return err
			}
			nowStr := formatTimestamp(rec.at)
			res, err := tx.ExecContext(ctx,
				`UPDATE operation SET state = ?, version = ?, closed_reason = ?, closed_at = ? WHERE id = ? AND version = ? AND state = ?`,
				string(OperationStateClosed), row.Version+1, string(ClosedReasonSuperseded), nowStr, id, row.Version, string(row.State))
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n != 1 {
				return fmt.Errorf("core: supersede release %s: expected to update 1 row, updated %d", formatOperationID(id), n)
			}
			if err := rec.record("operation", id, ActivityActionOpClose,
				map[string]any{"state": string(row.State)},
				map[string]any{"state": string(OperationStateClosed), "version": row.Version + 1, "closed_reason": string(ClosedReasonSuperseded), "trigger": "contract_review"}); err != nil {
				return err
			}
			return bumpChallengeVersionForOperation(ctx, tx, rec, row.ChallengeID, chal.Version, id, ActivityActionOpClose, nowStr, Attestation{})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

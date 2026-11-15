package core

// このファイルは、release の PR の head が最新の検証の head と異なるとき、承認と着手中へ戻す判定を
// core が自分で比較し直して確かめる規則を持つ。CLI が申告した「取り込みだけ」「検証し直しが必要」は
// 使わない（申告は表示と、core が確かめる入力の組み立てにだけ使う）。比較の取得は IF（UpstreamCheckSource・
// UpstreamCompareSource）経由で、ストアの書き込みのトランザクションの外で行う。

import (
	"context"
	"database/sql"
	"errors"
)

// ReleaseHeadSources は、core が release の head を自分で評価するための取得の口。どちらかが nil なら評価できず、
// 検証の head と異なる head は承認も着手中へ戻すこともできない（fail-closed）。
type ReleaseHeadSources struct {
	Checks  UpstreamCheckSource
	Compare UpstreamCompareSource
}

// assessHeadsForApproval は heads（release の ID → 承認しようとする head）のうち、最新の検証の head があり、
// head がそれと異なる release について、core 自身が評価した結果を返す。評価できない（取得の失敗・口が無い）
// release は結果に入れない（② が承認しない）。ストアは読み取りだけで、比較の取得はトランザクションの外。
func (s *Store) assessHeadsForApproval(ctx context.Context, heads map[int64]string, src ReleaseHeadSources) (map[int64]*ReleaseHeadAssessment, error) {
	type target struct {
		id       int64
		ref      string
		verified string
		head     string
	}
	var targets []target
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		for id, head := range heads {
			row, err := loadOperationRow(ctx, tx, id)
			if errors.Is(err, ErrNotFound) {
				continue // 組の照合は ② の ErrConflict に任せる
			}
			if err != nil {
				return err
			}
			if !row.Ref.Valid {
				continue
			}
			ref := row.Ref.String
			verified, err := latestVerifiedHead(ctx, tx, &ref)
			if err != nil {
				return err
			}
			if verified == nil || *verified == head {
				continue
			}
			targets = append(targets, target{id: id, ref: ref, verified: *verified, head: head})
		}
		return nil
	}); err != nil {
		return nil, classifyReadWriteErr(err)
	}
	out := make(map[int64]*ReleaseHeadAssessment, len(targets))
	if src.Checks == nil || src.Compare == nil {
		return out, nil
	}
	for _, t := range targets {
		a, err := AssessReleaseHead(ctx, src.Checks, src.Compare, t.ref, t.verified)
		if err != nil {
			continue
		}
		out[t.id] = a
	}
	return out, nil
}

// releaseHeadVerdict は承認する head（head）を、最新の検証の head（verified。nil は記録なし）と core 自身の評価
// （a。評価できなければ nil）に照らして判定する。
//   - 記録なし・head が検証の head と一致: 承認できる。
//   - 異なる: a が同じ検証の head・同じ head についての評価で、取り込みだけで CI が通っているときだけ承認できる
//     （recordMergeIn。承認の記録に検証の head を残す）。acceptUndeterminable が真なら、人間待ち（取り込みだけとも
//     中身の変更とも確かめられない）の評価も承認できる（undeterminableDetail に根拠を返し、承認の作業ログに残す）。
func releaseHeadVerdict(verified *string, head string, a *ReleaseHeadAssessment, acceptUndeterminable bool) (ok, recordMergeIn bool, undeterminableDetail string) {
	if verified == nil || *verified == head {
		return true, false, ""
	}
	if a == nil || a.VerifiedHead != *verified || a.HeadSHA != head {
		return false, false, ""
	}
	if a.MergeInApprovable() {
		return true, true, ""
	}
	if acceptUndeterminable && a.AcceptableByHuman() {
		return true, false, a.Change.Detail
	}
	return false, false, ""
}

// ReleaseReopenVerdict は、完了の課題の release を着手中へ戻すかの判定。
type ReleaseReopenVerdict string

const (
	// ReleaseReopenNone は、戻さない（課題が完了でない・検証の head の記録なし・head が検証の head と同じ・
	// 取り込みだけ・比較の取得の失敗や口が無いなど、検証し直しが要ると確かめられない）。
	ReleaseReopenNone ReleaseReopenVerdict = "none"
	// ReleaseReopenRequired は、取り込み以外のコミットが入った・履歴が書き換わったと確かに言え、戻して検証し直す。
	ReleaseReopenRequired ReleaseReopenVerdict = "reopen"
	// ReleaseReopenNeedsHuman は、取り込みだけとも中身の変更とも確かめられず（比較の打ち切り・head 無し等）、人が
	// 承認するか着手中へ戻すかを選ぶ。
	ReleaseReopenNeedsHuman ReleaseReopenVerdict = "needs_human"
)

// ReleaseReopenAssessment は AssessReleaseReopen の結果。
type ReleaseReopenAssessment struct {
	Verdict ReleaseReopenVerdict
	// Eligible は、release が承認待ちか承認済みで、課題が完了であること（戻す対象になりうる）。偽なら Verdict は none。
	Eligible bool
	// Detail は人間待ちの理由（判定できなかった理由）。needs_human のときだけ。
	Detail string
	// Assessment は評価した結果（評価していなければ nil）。
	Assessment *ReleaseHeadAssessment
}

// AssessReleaseReopen は opID の release を着手中へ戻すべきかを、core が自分で比較して判定する（戻す判定の唯一の場所。
// CLI・本番反映の片付け・ReopenChallengeForRelease が使う）。戻す対象でない・評価できないときは none（エラーにしない）。
// opID が存在しなければ ErrNotFound。
func (s *Store) AssessReleaseReopen(ctx context.Context, opID string, src ReleaseHeadSources) (*ReleaseReopenAssessment, error) {
	return s.assessReleaseReopen(ctx, opID, src, nil)
}

// assessReleaseReopen の pr は取得済みの PR（nil なら src.Checks で取得する）。
func (s *Store) assessReleaseReopen(ctx context.Context, opID string, src ReleaseHeadSources, pr *UpstreamPullRequestChecks) (*ReleaseReopenAssessment, error) {
	id, ok := parseOperationID(opID)
	if !ok {
		return nil, ErrNotFound
	}
	res := &ReleaseReopenAssessment{Verdict: ReleaseReopenNone}
	var ref, verified string
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		row, err := loadOperationRow(ctx, tx, id)
		if err != nil {
			return err
		}
		if row.Kind != string(OperationKindRelease) || (row.State != OperationStateApproved && row.State != OperationStatePending) {
			return nil
		}
		chal, err := loadChallenge(ctx, tx, row.ChallengeID)
		if err != nil {
			return err
		}
		if chal.Status != StatusDone {
			return nil
		}
		res.Eligible = true
		if !row.Ref.Valid {
			return nil
		}
		r := row.Ref.String
		v, err := latestVerifiedHead(ctx, tx, &r)
		if err != nil {
			return err
		}
		if v != nil {
			ref, verified = r, *v
		}
		return nil
	}); err != nil {
		return nil, classifyReadWriteErr(err)
	}
	if !res.Eligible || verified == "" || src.Compare == nil {
		return res, nil
	}
	repo, number, ok := parseReleasePullRequestRef(ref)
	if !ok {
		return res, nil
	}
	if pr == nil {
		if src.Checks == nil {
			return res, nil
		}
		got, err := src.Checks.GetPullRequestChecks(ctx, repo, number)
		if err != nil {
			return res, nil
		}
		pr = &got
	}
	if pr.HeadSHA == verified {
		return res, nil
	}
	a := assessReleaseHeadOfPR(ctx, src.Compare, repo, *pr, verified)
	res.Assessment = a
	switch {
	case a.Change.RequiresReverification():
		res.Verdict = ReleaseReopenRequired
	case a.Change.WaitsForHuman():
		res.Verdict = ReleaseReopenNeedsHuman
		res.Detail = a.Change.Detail
	}
	return res, nil
}

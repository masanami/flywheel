package core

// このファイルは、検査を通った区切りを分ける判断点の出力を、区切りを分ける記録と課題へ写す
// （docs/features/request-to-plan-s3.md）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// sliceSplitSingleByJudgment は、作業ログ slice_split_single の細目 by のうち、区切りを分ける判断点が分けないと判定したことの値。
const sliceSplitSingleByJudgment = "judgment"

// errSliceSplitOutputStale は、写す直前に読み直した課題か記録が、出力を写せる形でなくなっていたことを表す内部の sentinel。
var errSliceSplitOutputStale = errors.New("core: the slice split output is stale")

// sliceSplitAcceptsOutput は、実行 runID の出力を課題 current へ写してよいか: 課題が分類済で、版が実行を起動したときの版と同じで、
// 区切りを分ける記録が pending。写しの書き込みの中で呼ぶ。
func sliceSplitAcceptsOutput(ctx context.Context, tx *sql.Tx, cid, runID int64, current *Challenge) (bool, error) {
	if current.Status != StatusClassified {
		return false, nil
	}
	var runVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT challenge_version FROM run WHERE id = ?`, runID).Scan(&runVersion); err != nil {
		return false, err
	}
	if runVersion != int64(current.Version) {
		return false, nil
	}
	state, _, found, err := sliceSplitState(ctx, tx, cid)
	return found && state == SliceSplitStatePending, err
}

// applyJ10Output は実行 runDisplay の出力 out を課題 id へ写し、写した後の課題の状態を返す。写さなかった（出力を捨てた・
// 証跡を書けなかった）ときは nil。捨てたときは何も書かない。
func (s *Store) applyJ10Output(ctx context.Context, runDisplay, id string, out *j10Output) (*string, error) {
	runID, okRun := parseRunID(runDisplay)
	cid, okChallenge := parseChallengeID(id)
	if !okRun || !okChallenge {
		return nil, ErrValidation
	}
	if out.Verdict == J10VerdictSingle {
		return s.applyJ10Single(ctx, runID, cid)
	}
	return s.applyJ10Split(ctx, runDisplay, runID, cid, id, out)
}

// applyJ10Single は記録を single にする。課題は分類済のままで、版も変えない。
func (s *Store) applyJ10Single(ctx context.Context, runID, cid int64) (*string, error) {
	actor, err := s.resolveActor()
	if err != nil {
		return nil, err
	}
	var status *string
	err = s.mutateAsRun(ctx, actor, ChannelInvoker, VerificationNone, &runID, func(tx *sql.Tx, rec *activityRecorder) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		ok, err := sliceSplitAcceptsOutput(ctx, tx, cid, runID, current)
		if err != nil || !ok {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE slice_split SET state = ?, updated_at = ? WHERE challenge_id = ?`,
			string(SliceSplitStateSingle), formatTimestamp(rec.at), cid); err != nil {
			return err
		}
		v := string(current.Status)
		status = &v
		return rec.record("challenge", cid, ActivityActionSliceSplitSingle, nil, map[string]any{"by": sliceSplitSingleByJudgment})
	})
	if err != nil {
		return nil, err
	}
	return status, nil
}

// sliceSplitProposal は slice_split.proposal に入れる案。
type sliceSplitProposal struct {
	Reason   string        `json:"reason"`
	Slices   []Slice       `json:"slices"`
	Evidence []j10Evidence `json:"evidence"`
}

// proposal は検査を通った split の出力を案にする。
func (out *j10Output) proposal() sliceSplitProposal {
	p := sliceSplitProposal{Reason: out.Reason, Slices: make([]Slice, len(out.Slices)), Evidence: out.Evidence}
	for i, sl := range out.Slices {
		p.Slices[i] = Slice{Title: *sl.Title, Headline: *sl.Headline, Context: *sl.Context, Summary: *sl.Summary,
			Scope: *sl.Scope, Repo: *sl.Repo, DependsOn: sl.DependsOn}
	}
	return p
}

// applyJ10Split は証跡のファイルを書き、分けるかの保留で課題を人間対応待ちにして、記録を awaiting_owner にする。
// ファイルは、写してよいことを確かめた書き込みの中で、ほかの行より先に書く。書けなければ書き込みを取り消し、作業ログだけを残す。
func (s *Store) applyJ10Split(ctx context.Context, runDisplay string, runID, cid int64, id string, out *j10Output) (*string, error) {
	p := out.proposal()
	proposal, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal the slice split proposal: %w", ErrStoreError, err)
	}
	evidencePath := sliceSplitEvidencePath(cid)
	var evidenceFailure error
	c, err := s.transitionAsInvoker(ctx, runDisplay, id, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		ok, err := sliceSplitAcceptsOutput(ctx, tc.tx, cid, runID, tc.current)
		if err != nil {
			return err
		}
		if !ok {
			return errSliceSplitOutputStale
		}
		source, err := loadSourceBindingByChallengeID(ctx, tc.tx, cid)
		if err != nil {
			return err
		}
		url := j10NoValue
		if source != nil {
			url = source.URL
		}
		content := sliceSplitEvidenceContent(tc.current.Title, url, tc.nowStr, p)
		if err := s.writeRequirementEvidence(evidencePath, content); err != nil {
			evidenceFailure = err
			return err
		}
		holdID, err := insertSliceSplitHold(ctx, tc, p, evidencePath)
		if err != nil {
			return err
		}
		_, err = tc.tx.ExecContext(ctx,
			`UPDATE slice_split SET state = ?, proposal = ?, evidence_path = ?, hold_id = ?, updated_at = ? WHERE challenge_id = ?`,
			string(SliceSplitStateAwaitingOwner), string(proposal), evidencePath, holdID, tc.nowStr, cid)
		return err
	})
	switch {
	case evidenceFailure != nil:
		return nil, s.recordSliceSplitEvidenceFailure(ctx, runID, cid, evidencePath, evidenceFailure)
	case errors.Is(err, errSliceSplitOutputStale), errors.Is(err, ErrInvalidTransition), errors.Is(err, ErrTerminalState):
		return nil, nil
	case err != nil:
		return nil, err
	}
	v := string(c.Status)
	return &v, nil
}

// recordSliceSplitEvidenceFailure は証跡を書けなかったことを作業ログに残す。
func (s *Store) recordSliceSplitEvidenceFailure(ctx context.Context, runID, cid int64, evidencePath string, failed error) error {
	actor, err := s.resolveActor()
	if err != nil {
		return err
	}
	return s.mutateAsRun(ctx, actor, ChannelInvoker, VerificationNone, &runID, func(_ *sql.Tx, rec *activityRecorder) error {
		return rec.record("challenge", cid, ActivityActionSliceSplitEvidenceFailed, nil,
			map[string]any{"path": evidencePath, "reason": failed.Error()})
	})
}

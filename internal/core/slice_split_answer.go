package core

// このファイルは、分けるかの保留への回答（split・single）の中身と、保留を開いたままの取り下げで区切りを分ける記録を直す処理を持つ。
// どの保留が分けるかの保留かは、区切りを分ける記録の hold_id で見分ける。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// sliceSplitSingleByOwner は、作業ログ slice_split_single の細目 by のうち、オーナーが分けないと答えたことの値。
const sliceSplitSingleByOwner = "owner"

// sliceSplitAnswer は、分けるかの保留に答えるときの、区切りを分ける記録（答える前の値）。
type sliceSplitAnswer struct {
	proposal     sliceSplitProposal
	evidencePath string
}

// loadSliceSplitAnswer は、保留 holdID が課題 cid の分けるかの保留なら、区切りを分ける記録を返す。そうでなければ nil。
func loadSliceSplitAnswer(ctx context.Context, tx *sql.Tx, cid, holdID int64) (*sliceSplitAnswer, error) {
	var proposal, evidence string
	err := tx.QueryRowContext(ctx, `SELECT proposal, evidence_path FROM slice_split WHERE challenge_id = ? AND hold_id = ? AND state = ?`,
		cid, holdID, string(SliceSplitStateAwaitingOwner)).Scan(&proposal, &evidence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &sliceSplitAnswer{evidencePath: evidence}
	if err := json.Unmarshal([]byte(proposal), &out.proposal); err != nil {
		return nil, fmt.Errorf("slice_split of %s: proposal: %w", formatChallengeID(cid), err)
	}
	return out, nil
}

// applySliceSplitSingle は single の回答で、記録を single にして hold_id を空にし、作業ログ slice_split_single を残す。
// 課題を分類済へ戻すのは、回答の遷移（保留の戻り先）。
func applySliceSplitSingle(ctx context.Context, tc *transitionCtx, att Attestation) error {
	if _, err := tc.tx.ExecContext(ctx, `UPDATE slice_split SET state = ?, hold_id = NULL, updated_at = ? WHERE challenge_id = ?`,
		string(SliceSplitStateSingle), tc.nowStr, tc.id); err != nil {
		return err
	}
	after := map[string]any{"by": sliceSplitSingleByOwner}
	att.addRelayTo(after)
	return tc.rec.record("challenge", tc.id, ActivityActionSliceSplitSingle, nil, after)
}

// applySliceSplit は split の回答を、回答の遷移（answer。p）と同じ書き込みの中で写す: 要求の記録・区切りごとの課題・課題の依存を作り、
// 元の課題を取り下げ、区切りを分ける記録を split にする。途中の誤りは、呼び出し元の書き込みごと（回答も）取り消す。
// 返すのは取り下げた元の課題。
//
// 取り下げは、回答の本人確認だけで受ける（p.coveredBy）。前提の検査・不可逆操作の差し戻しは、取り下げ（ExecuteWithdraw）と同じ。
// 終わっていない委譲の実行は、課題の状態によらず ErrConflict で断る（回答の遷移の後の課題は着手中ではないため、ここで確かめる）。
func (s *Store) applySliceSplit(ctx context.Context, tx *sql.Tx, rec *activityRecorder, p runTransitionParams, answered *Challenge, split *sliceSplitAnswer) (*Challenge, error) {
	cid, ok := parseChallengeID(answered.ID)
	if !ok {
		return nil, ErrNotFound
	}
	open, err := hasOpenDelegationRun(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	if open {
		return nil, errWithdrawOpenDelegation(answered.ID)
	}
	original, err := loadChallenge(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	nowStr := formatTimestamp(rec.at)

	requirementID, err := insertSliceSplitRequirement(ctx, tx, rec, p.att, original, cid, split.evidencePath, nowStr)
	if err != nil {
		return nil, err
	}
	created := make([]int64, len(split.proposal.Slices))
	for i, sl := range split.proposal.Slices {
		if created[i], err = insertSliceChallenge(ctx, tx, rec, p.att, original, requirementID, sl, nowStr); err != nil {
			return nil, err
		}
	}
	for i, sl := range split.proposal.Slices {
		if err := insertSliceDependencies(ctx, tx, rec, created, i, sl.DependsOn, nowStr); err != nil {
			return nil, err
		}
	}

	proposal := split.proposal
	proposal.Slices = append([]Slice(nil), proposal.Slices...)
	challengeIDs := make([]string, len(created))
	for i, id := range created {
		challengeIDs[i] = formatChallengeID(id)
		proposal.Slices[i].ChallengeID = challengeIDs[i]
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal the slice split proposal: %w", ErrStoreError, err)
	}
	// 記録を split にするのは取り下げより先（取り下げは awaiting_owner の記録を、保留を開いたままのものとして扱う）。
	if _, err := tx.ExecContext(ctx,
		`UPDATE slice_split SET state = ?, proposal = ?, requirement_id = ?, hold_id = NULL, updated_at = ? WHERE challenge_id = ?`,
		string(SliceSplitStateSplit), string(encoded), requirementID, nowStr, cid); err != nil {
		return nil, err
	}

	reason := fmt.Sprintf("区切りの課題に分けた（要求 %s）", formatRequirementID(requirementID))
	version := answered.Version
	withdraw := p
	withdraw.op = OpWithdraw
	withdraw.coveredBy = actionAnswer
	withdraw.expectedVersion = &version
	withdraw.holdCheck = nil
	withdraw.resolvePreceding = nil
	withdraw.replan, withdraw.replanResolved = false, nil
	withdrawn, err := s.transitionInTx(ctx, tx, rec, withdraw, cid, func(ctx context.Context, tc *transitionCtx) error {
		_, err := applyWithdrawal(ctx, tc, reason, p.att)
		return err
	})
	if err != nil {
		return nil, err
	}

	after := map[string]any{"state": string(SliceSplitStateSplit), "requirement_id": formatRequirementID(requirementID), "challenge_ids": challengeIDs}
	p.att.addRelayTo(after)
	if err := rec.record("challenge", cid, ActivityActionSliceSplitDone, nil, after); err != nil {
		return nil, err
	}
	return withdrawn, nil
}

// insertSliceSplitRequirement は、元の Issue を出どころにする要求の記録を作る。外部キーと URL は元の課題の取り込み元の対応の値。
func insertSliceSplitRequirement(ctx context.Context, tx *sql.Tx, rec *activityRecorder, att Attestation, original *Challenge, cid int64, evidencePath, nowStr string) (int64, error) {
	source, err := loadSourceBindingByChallengeID(ctx, tx, cid)
	if err != nil {
		return 0, err
	}
	if source == nil {
		return 0, fmt.Errorf("core: split %s: the challenge has no source binding", original.ID)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO requirement (origin, title, external_key, url, origin_challenge_id, evidence_path, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(RequirementOriginIssue), original.Title, source.ExternalKey, source.URL, cid, evidencePath, nowStr)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	after := map[string]any{
		"title": original.Title, "origin": string(RequirementOriginIssue), "evidence_path": evidencePath,
		"external_key": source.ExternalKey, "url": source.URL, "origin_challenge_id": original.ID,
	}
	att.addRelayTo(after)
	return id, rec.record("requirement", id, ActivityActionRequirementCreate, nil, after)
}

// insertSliceChallenge は区切り 1 つの課題を、分類済で作る（分類の判断点を通さない）。優先度・緊急度・起票者は元の課題の値。
// 取り込み元の対応は作らない。
func insertSliceChallenge(ctx context.Context, tx *sql.Tx, rec *activityRecorder, att Attestation, original *Challenge, requirementID int64, sl Slice, nowStr string) (int64, error) {
	id, _, err := insertChallengeRow(ctx, tx, rec.at, sl.Title, sl.Summary, sl.Scope, original.Urgency, original.Reporter, ChallengeOriginSplit, nil, nil)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE challenge SET status = ?, priority = ?, headline = ?, context = ?, requirement_id = ?, updated_at = ? WHERE id = ?`,
		string(StatusClassified), nullablePriority(original.Priority), sl.Headline, sl.Context, requirementID, nowStr, id); err != nil {
		return 0, err
	}
	after := createdChallengeAfter(sl.Title, sl.Summary, sl.Scope, original.Urgency, original.Reporter)
	after["status"] = string(StatusClassified)
	after["priority"] = nullablePriority(original.Priority)
	after["headline"], after["context"] = sl.Headline, sl.Context
	after["origin"] = string(ChallengeOriginSplit)
	after["requirement_id"] = formatRequirementID(requirementID)
	att.addRelayTo(after)
	return id, rec.record("challenge", id, "create", nil, after)
}

// insertSliceDependencies は区切り i（0 始まり）の depends_on（1 始まりの区切りの番号）を、作った課題どうしの依存にする。
func insertSliceDependencies(ctx context.Context, tx *sql.Tx, rec *activityRecorder, created []int64, i int, dependsOn []int, nowStr string) error {
	if len(dependsOn) == 0 {
		return nil
	}
	targets := make([]int64, 0, len(dependsOn))
	for _, n := range dependsOn {
		if n < 1 || n > len(created) || n == i+1 {
			return fmt.Errorf("core: split: slice %d depends on an invalid slice number %d", i+1, n)
		}
		targets = append(targets, created[n-1])
	}
	cid := created[i]
	changed, err := planDependencyChange(ctx, tx, cid, targets, true)
	if err != nil {
		return err
	}
	ids, err := writeDependencyEdges(ctx, tx, cid, changed, true, nowStr)
	if err != nil {
		return err
	}
	// 依存の追加（AddDependencies）と同じく、版を 1 つ上げる。
	var version int
	if err := tx.QueryRowContext(ctx, `UPDATE challenge SET version = version + 1, updated_at = ? WHERE id = ? RETURNING version`, nowStr, cid).Scan(&version); err != nil {
		return err
	}
	return rec.record("challenge", cid, ActivityActionDepAdd, nil, map[string]any{"depends_on": ids, "version": version})
}

// releaseSliceSplitHold は、分けるかの保留を開いたまま取り下げる課題の記録の hold_id を空にし（状態は awaiting_owner のまま）、
// 取り下げの書き込みが成立した後に消す証跡の場所を返す。そういう記録が無ければ空。
func releaseSliceSplitHold(ctx context.Context, tc *transitionCtx) (string, error) {
	var evidence sql.NullString
	err := tc.tx.QueryRowContext(ctx,
		`UPDATE slice_split SET hold_id = NULL, updated_at = ? WHERE challenge_id = ? AND state = ? RETURNING evidence_path`,
		tc.nowStr, tc.id, string(SliceSplitStateAwaitingOwner)).Scan(&evidence)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return evidence.String, err
}

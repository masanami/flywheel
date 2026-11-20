package core

// このファイルは、課題が着手中のときに読んだ成果物つきのレビュー待ちを、検証中へ進める前に確かめる順序を持つ:
//
//  1. options に land が無い → 保留（land_unavailable）。
//  2. 中身の識別がちょうど 1 つでない・反映先宛ての成果物が 2 つ以上 → 保留（content_ref_invalid）。
//  3. 宣言の required_facts を満たさない名前がある → 同じ契約の run へ respond を返す。2 回続いたら保留（facts_unmet）。
//  4. どれにも当たらない → 成果物を取り込み、反映の操作を作って検証中へ進める（submitContractReview）。
//
// 1〜3 に当たったときは、成果物の取り込みも反映の操作の登録も行わない。
//
// 統合先を使う課題（challenge.uses_integration）のタスクの実行は、2 の「反映先宛ての成果物が 2 つ以上」の確かめを、
// 次の確かめに置き換える（receiveIntegrationReview）。3 の後に行い、取り込むときは課題を進めず、タスクだけを検証中にする。
//
//  a. 反映先宛ての成果物が 1 つでもある → 保留（integration_ref_invalid）。
//  b. 統合先の識別（role が integration の branch）と統合先の中身の識別（同じく commit）がちょうど 1 つずつ無い → 保留（同上）。
//  c. 統合先の識別が課題に記録した統合先（challenge.integration_ref）と違う → 保留（同上）。記録が無ければ記録する。
//  d. どれにも当たらない → 成果物を取り込み、タスクだけを検証中にする。反映の操作は作らない。
//
// 取りやめのタスクの実行のレビュー待ちは、どの課題でも受け取らない。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// contractOptionLand は、レビュー待ちの options に委譲先が「入れてよい」の答えを受けられることを示す選択肢。
const contractOptionLand = "land"

// contractRoleIntegration は、成果物の要素の role の値のうち、統合先を使う実行の統合ブランチとその先頭のコミットを指す印。
const contractRoleIntegration = "integration"

// factsUnmetLimit は、required_facts を満たさないレビュー待ちが続いたとき、respond を返さず保留にする回数。
const factsUnmetLimit = 2

// unmetFact は required_facts を満たしていない名前 1 つ。
type unmetFact struct {
	Name     string
	Got      string
	HasValue bool
	Accepted []string
}

// unmetRequiredFacts は required と facts を照らし、満たしていない名前を名前の辞書順で返す
// （facts に名前が無い・値が受け入れる値のどれでもない）。facts が nil でも名前ごとに「無い」として扱う。
func unmetRequiredFacts(required map[string][]string, facts map[string]string) []unmetFact {
	var out []unmetFact
	for name, accepted := range required {
		v, ok := facts[name]
		met := false
		if ok {
			for _, a := range accepted {
				if a == v {
					met = true
					break
				}
			}
		}
		if !met {
			out = append(out, unmetFact{Name: name, Got: v, HasValue: ok, Accepted: accepted})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (u unmetFact) describe() string {
	got := "無い"
	if u.HasValue {
		got = u.Got
	}
	return fmt.Sprintf("%s（返ってきた値: %s、受け入れる値: %s）", u.Name, got, strings.Join(u.Accepted, "、"))
}

func describeUnmetFacts(us []unmetFact) string {
	parts := make([]string, 0, len(us))
	for _, u := range us {
		parts = append(parts, "- "+u.describe())
	}
	return strings.Join(parts, "\n")
}

// factsUnmetNote は respond の添え書き。満たしていない名前ごとに、返ってきた値（無ければ「無い」）と受け入れる値を並べる。
func factsUnmetNote(us []unmetFact) string {
	return "委譲先が値で返したことが、宣言の受け入れる値を満たしていない。満たしていない名前:\n" + describeUnmetFacts(us)
}

func hasLandOption(opts []string) bool {
	for _, o := range opts {
		if o == contractOptionLand {
			return true
		}
	}
	return false
}

// reviewReachesFactsCheck は、レビュー待ちが facts の確認の前の 2 つ（land・中身の識別）を通るか。
func reviewReachesFactsCheck(st *ContractStatus) bool {
	ra := st.RequestedAction
	if st.State != ContractStateWaiting || ra == nil || contractWaitDestination(ra, st.Artifacts) != contractWaitReview {
		return false
	}
	if !hasLandOption(ra.Options) {
		return false
	}
	_, n := reviewContentRefs(st.Artifacts)
	return n == 1
}

// countUnmetFacts は状態の記録と同じ書き込みで、満たさなかった回数（contract_run.facts_unmet_count）を更新する。
// 数えるのは、状態の確認（c.CountFacts）で、宣言に required_facts があり、課題が着手中で、レビュー待ちが land・中身の識別の
// 確認を通ったとき。満たさなければ 1 増やし、満たせば 0 に戻す。
func countUnmetFacts(ctx context.Context, tx *sql.Tx, c contractCall, st *ContractStatus, rowID int64, report *ContractRunReport) error {
	if len(c.FactsRequired) == 0 || report == nil || !reviewReachesFactsCheck(st) {
		return nil
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM challenge WHERE id = ?`, c.ChallengeIDInt).Scan(&status); err != nil {
		return err
	}
	if Status(status) != StatusInProgress {
		return nil
	}
	unmet := len(unmetRequiredFacts(c.FactsRequired, st.Facts)) > 0
	if !c.CountFacts && unmet {
		return nil // 増やすのは状態の確認だけ（resume・start の応答は数えない）。満たす読みの 0 への戻しはどのコマンドでも行う
	}
	var prev int
	if err := tx.QueryRowContext(ctx, `SELECT facts_unmet_count FROM contract_run WHERE id = ?`, rowID).Scan(&prev); err != nil {
		return err
	}
	next := 0
	if unmet {
		// 回数が上限に達しているのに課題が着手中なら、その保留には回答が付いている（上限に達すると同じ読みで保留にする）ので、数え直す。
		next = prev + 1
		if prev >= factsUnmetLimit {
			next = 1
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET facts_unmet_count = ? WHERE id = ?`, next, rowID); err != nil {
		return err
	}
	report.FactsCounted, report.FactsUnmetCount, report.FactsPrevCount = c.CountFacts, next, prev
	return nil
}

// restoreFactsCount は、facts の確認より前の確認（反映先宛ての成果物の数）で保留にしたとき、この読みで増やした回数を元に戻す
// （facts の確認に至らなかった読みを、満たさなかった回数に数えない）。
func (s *Store) restoreFactsCount(ctx context.Context, rep *ContractRunReport) error {
	if !rep.FactsCounted {
		return nil
	}
	cid, ok := parseChallengeID(rep.ChallengeID)
	if !ok {
		return nil
	}
	return classifyReadWriteErr(s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE contract_run SET facts_unmet_count = ? WHERE challenge_id = ? AND external_run_id = ?`,
			rep.FactsPrevCount, cid, rep.ExternalRunID)
		return err
	}))
}

func landUnavailableQuestion(runID string) string {
	return fmt.Sprintf("接続ツールの run（%s）のレビュー待ちの選択肢に %s が無い。委譲先が「入れてよい」の答えを受けられないので、検証中へ進めなかった。", runID, contractOptionLand) +
		"flywheel は自分では反映しない。委譲先を「入れてよい」の答えを受けられる版に上げてから回答するか、課題を取り下げる。" +
		"回答しても、回答の文は委譲先へ返さない。次に読んだレビュー待ちにも選択肢 land が無ければ、もう一度保留にする。"
}

func contentRefInvalidQuestion(runID string, n int) string {
	return fmt.Sprintf("接続ツールの run（%s）のレビュー待ちの中身の識別（種類が commit で role の無い成果物の ref）が %d 個見つかった。ちょうど 1 つが要るので、検証中へ進めなかった。", runID, n) +
		"委譲先の出力を直してから回答するか、課題を取り下げる。回答しても、回答の文は委譲先へ返さない。次に読んだレビュー待ちも同じなら、もう一度保留にする。"
}

func tooManyLandablePRsQuestion(runID string, n int) string {
	return fmt.Sprintf("接続ツールの run（%s）のレビュー待ちに、反映先（既定ブランチ）宛ての成果物（PR）が %d 個ある。1 つの実行が反映先へ入れる成果物は 1 つに限るので、検証中へ進めなかった。", runID, n) +
		"委譲先の出力を直してから回答するか、課題を取り下げる。回答しても、回答の文は委譲先へ返さない。次に読んだレビュー待ちも同じなら、もう一度保留にする。"
}

func integrationLandablePRQuestion(runID string, n int) string {
	return fmt.Sprintf("接続ツールの run（%s）のレビュー待ちに、反映先（既定ブランチ）宛ての成果物（PR）が %d 個ある。統合先を使うタスクの成果は反映先ではなく統合先へ入れるものなので、検証へ進めなかった。", runID, n) +
		"委譲先の出力を直してから回答するか、課題を取り下げる。回答しても、回答の文は委譲先へ返さない。次に読んだレビュー待ちも同じなら、もう一度保留にする。"
}

func integrationIdentityCountQuestion(runID string, branches, commits int) string {
	return fmt.Sprintf("接続ツールの run（%s）のレビュー待ちに、統合先の識別（役割が統合先の、種類が branch の成果物の ref）が %d 個、統合先の中身の識別（役割が統合先の、種類が commit の成果物の ref）が %d 個見つかった。", runID, branches, commits) +
		"どちらもちょうど 1 つが要るので、検証へ進めなかった。委譲先の出力を直してから回答するか、課題を取り下げる。回答しても、回答の文は委譲先へ返さない。次に読んだレビュー待ちも同じなら、もう一度保留にする。"
}

func integrationRefMismatchQuestion(runID, got, recorded string) string {
	return fmt.Sprintf("接続ツールの run（%s）のレビュー待ちの統合先の識別（%s）が、この課題に記録した統合先（%s）と違う。1 つの課題の統合先は 1 つなので、検証へ進めなかった。", runID, got, recorded) +
		"委譲先の出力を直してから回答するか、課題を取り下げる。回答しても、回答の文は委譲先へ返さない。次に読んだレビュー待ちも同じなら、もう一度保留にする。"
}

func integrationSucceededQuestion(runID string, merged, unreadable bool) string {
	if merged {
		return fmt.Sprintf("接続ツールの run（%s）が、レビュー待ちを経ずに終わった（成功）。成果物は、検証を経ないまま統合先へ入っている。検証へ進めなかった。", runID) +
			"入った中身を確かめてから回答するか、課題を取り下げる。"
	}
	note := ""
	if unreadable {
		note = "上流の成果物を読めなかったので、入っていないものとして扱った。"
	}
	return fmt.Sprintf("接続ツールの run（%s）が、レビュー待ちを経ずに終わった（成功）。成果物は統合先へ入っていない。%s検証へ進めなかった。", runID, note) +
		"回答すると、このタスクを着手中へ戻して委譲し直す。課題を取り下げることもできる。"
}

func factsUnmetQuestion(runID string, us []unmetFact) string {
	return fmt.Sprintf("接続ツールの run（%s）のレビュー待ちが、宣言の required_facts を満たさないまま %d 回続いたので、もう差し戻さず保留にした。満たしていない名前:\n", runID, factsUnmetLimit) +
		describeUnmetFacts(us) + "\n回答すると、回答の文を添えて、もう一度委譲先へ差し戻す。"
}

// holdContractReview は課題が着手中なら、レビュー待ちを受け取れない保留を理由 reason で立てる（契約の run が結び付くタスクに結び付ける）。
// 保留への回答は委譲先へ返さないので、契約の run には結び付けない（wait_hold_id を使わない）。着手中でなければ何もしない（偽）。
func (s *Store) holdContractReview(ctx context.Context, rep *ContractRunReport, question string, reason holdReason) (bool, error) {
	return s.holdContractReviewWith(ctx, rep, question, reason, nil)
}

// holdContractReviewWith は holdContractReview に、保留を立てる同じトランザクションで行う書き込み extra（nil なら無し）を足したもの。
func (s *Store) holdContractReviewWith(ctx context.Context, rep *ContractRunReport, question string, reason holdReason, extra func(ctx context.Context, tx *sql.Tx) error) (bool, error) {
	_, err := s.transitionAsInvoker(context.WithoutCancel(ctx), rep.RunID, rep.ChallengeID, OpHold,
		func(ctx context.Context, tc *transitionCtx) error {
			if tc.current.Status != StatusInProgress {
				return errReconcileNotEligible
			}
			taskID, err := contractRunTaskID(ctx, tc.tx, tc.id, rep.ExternalRunID)
			if err != nil {
				return err
			}
			tc.taskID = taskID
			if err := s.insertHoldWithOccurrence(ctx, tc, question, reason, ""); err != nil {
				return err
			}
			if extra != nil {
				return extra(ctx, tc.tx)
			}
			return nil
		})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errReconcileNotEligible) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// reviewIntakeScope は、レビュー待ちを受け取る課題とタスクの読み（課題が統合先を使うか・契約の run が結び付くタスクとその状態）。
type reviewIntakeScope struct {
	Integration bool
	// Reflect は、契約の実行が反映の依頼（上げる依頼）であること。
	Reflect    bool
	TaskID     *int64
	TaskStatus TaskStatus
}

func (s *Store) loadReviewIntakeScope(ctx context.Context, cid int64, externalRunID string) (reviewIntakeScope, error) {
	var sc reviewIntakeScope
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		integ, err := challengeUsesIntegration(ctx, tx, cid)
		if err != nil {
			return err
		}
		sc.Integration = integ != nil && *integ
		var kind sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM contract_run WHERE challenge_id = ? AND external_run_id = ?`, cid, externalRunID).Scan(&kind); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		sc.Reflect = kind.String == contractRunKindReflect
		sc.TaskID, err = contractRunTaskID(ctx, tx, cid, externalRunID)
		if err != nil || sc.TaskID == nil {
			return err
		}
		var st string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ?`, *sc.TaskID).Scan(&st); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		sc.TaskStatus = TaskStatus(st)
		return nil
	})
	return sc, classifyReadWriteErr(err)
}

// receiveContractReview は、課題が着手中のレビュー待ちを、上の順で確かめる。保留にしたら rep.Held を立てる。
// respond を返すと決めたら rep.PendingRespond に答えを置く（返すのは状態の確認の呼び出し元）。
func (s *Store) receiveContractReview(ctx context.Context, rep *ContractRunReport, connector *Connector, rs *contractReleaseSource) error {
	cid, ok := parseChallengeID(rep.ChallengeID)
	if !ok {
		return nil
	}
	if inProgress, err := s.challengeInProgress(ctx, cid); err != nil || !inProgress {
		return err
	}
	scope, err := s.loadReviewIntakeScope(ctx, cid, rep.ExternalRunID)
	if err != nil {
		return err
	}
	if scope.TaskID != nil && scope.TaskStatus == TaskStatusCancelled {
		return nil // 取りやめのタスクの実行のレビュー待ちは受け取らない（タスクは取りやめのまま）
	}
	hold := func(q string, r holdReason) error {
		held, err := s.holdContractReview(ctx, rep, q, r)
		rep.Held = held
		return err
	}
	ra := rep.RequestedAction
	if !hasLandOption(ra.Options) {
		return hold(landUnavailableQuestion(rep.ExternalRunID), holdReasonLandUnavailable)
	}
	if _, n := reviewContentRefs(rep.Artifacts); n != 1 {
		return hold(contentRefInvalidQuestion(rep.ExternalRunID, n), holdReasonContentRefInvalid)
	}
	// 上げる依頼（反映先へ上げる反映の依頼）のレビュー待ちは、反映先宛ての成果物（PR）がちょうど 1 つでなければ受け取らない。
	var reflectPR *UpstreamPullRequest
	if scope.Reflect {
		if reflectPR, err = s.checkReflectPRs(ctx, rep, rs, hold); err != nil || reflectPR == nil {
			return err
		}
	} else if !scope.Integration && rs != nil && rs.Reconcile != nil {
		prs, err := rs.defaultBranchPRs(context.WithoutCancel(ctx), rep.Artifacts)
		if err != nil {
			if rerr := s.restoreFactsCount(ctx, rep); rerr != nil {
				return rerr
			}
			return err
		}
		if len(prs) >= 2 {
			if err := s.restoreFactsCount(ctx, rep); err != nil {
				return err
			}
			return hold(tooManyLandablePRsQuestion(rep.ExternalRunID, len(prs)), holdReasonContentRefInvalid)
		}
	}
	// 宣言の required_facts は、タスクの実行のレビュー待ちに掛ける。上げる依頼（反映の依頼）のレビュー待ちには掛けない
	// （委譲先の上げる依頼のレビュー待ちは facts を返さない。S3a の反映の段も required_facts を掛けない）。
	if connector != nil && len(connector.RequiredFacts) > 0 && !scope.Reflect {
		if unmet := unmetRequiredFacts(connector.RequiredFacts, rep.Facts); len(unmet) > 0 {
			if !rep.FactsCounted {
				return nil // 状態の確認の読みではない（resume・start の応答）。次の状態の確認で数える
			}
			if rep.FactsUnmetCount >= factsUnmetLimit {
				return hold(factsUnmetQuestion(rep.ExternalRunID, unmet), holdReasonFactsUnmet)
			}
			note := factsUnmetNote(unmet)
			owner, err := s.undeliveredFactsUnmetAnswer(ctx, cid, scope.TaskID, rep.RunID)
			if err != nil {
				return err
			}
			if owner != "" {
				note += "\n\nオーナーの回答: " + owner
			}
			rep.PendingRespond = &contractAnswer{Choice: contractChoiceRespond, Note: note}
			return nil
		}
	}
	if scope.Reflect {
		return s.submitReflectReview(ctx, rep, rs, *reflectPR)
	}
	if scope.Integration {
		return s.receiveIntegrationReview(ctx, rep, rs, cid, scope, hold)
	}
	return s.submitContractReview(ctx, rep, rs)
}

// integrationArtifactRefs は arts のうち、役割が統合先（role が integration）の branch・commit の ref を、それぞれ並べて返す。
func integrationArtifactRefs(arts []ContractArtifact) (branches, commits []string) {
	for _, a := range arts {
		if a.Role != contractRoleIntegration {
			continue
		}
		switch a.Kind {
		case "branch":
			branches = append(branches, a.Ref)
		case "commit":
			commits = append(commits, a.Ref)
		}
	}
	return branches, commits
}

// receiveIntegrationReview は、統合先を使う課題のタスクの実行のレビュー待ち（S3a の確かめを通ったもの）を、上の a〜d で確かめ、
// 通ったら成果物を取り込んでタスクだけを検証中にする（課題は着手中のまま。反映の操作は作らない）。
func (s *Store) receiveIntegrationReview(ctx context.Context, rep *ContractRunReport, rs *contractReleaseSource, cid int64, scope reviewIntakeScope, hold func(string, holdReason) error) error {
	if rs != nil && rs.Reconcile != nil {
		prs, err := rs.defaultBranchPRs(context.WithoutCancel(ctx), rep.Artifacts)
		if err != nil {
			return err
		}
		if len(prs) >= 1 {
			return hold(integrationLandablePRQuestion(rep.ExternalRunID, len(prs)), holdReasonIntegrationRefInvalid)
		}
	}
	branches, commits := integrationArtifactRefs(rep.Artifacts)
	if len(branches) != 1 || len(commits) != 1 {
		return hold(integrationIdentityCountQuestion(rep.ExternalRunID, len(branches), len(commits)), holdReasonIntegrationRefInvalid)
	}
	recorded, err := s.challengeIntegrationRef(ctx, cid)
	if err != nil {
		return err
	}
	if recorded != "" && recorded != branches[0] {
		return hold(integrationRefMismatchQuestion(rep.ExternalRunID, branches[0], recorded), holdReasonIntegrationRefInvalid)
	}
	return s.importIntegrationReview(ctx, rep, cid, scope, branches[0])
}

func (s *Store) challengeIntegrationRef(ctx context.Context, cid int64) (string, error) {
	var ref sql.NullString
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT integration_ref FROM challenge WHERE id = ?`, cid).Scan(&ref)
	})
	return ref.String, classifyReadWriteErr(err)
}

// importIntegrationReview は課題が着手中なら、統合先の識別が無ければ課題へ記録し、成果物を rep の run へ取り込み、タスクを
// 検証中にする（同じトランザクション。課題の状態は変えない）。タスクが既に検証中で、直前に取り込んだ成果物と同じ中身の読みは、
// 成果物を取り込み直さない（中身が変わったときだけ、新しい成果物を取り込む）。
func (s *Store) importIntegrationReview(ctx context.Context, rep *ContractRunReport, cid int64, scope reviewIntakeScope, integrationRef string) error {
	runInt, ok := parseRunID(rep.RunID)
	if !ok {
		return nil
	}
	actor, err := s.resolveActor()
	if err != nil {
		return err
	}
	err = s.mutateAsRun(context.WithoutCancel(ctx), actor, ChannelInvoker, VerificationNone, &runInt, func(tx *sql.Tx, rec *activityRecorder) error {
		ch, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if ch == nil || ch.Status != StatusInProgress {
			return nil
		}
		res, err := tx.ExecContext(ctx, `UPDATE challenge SET integration_ref = ? WHERE id = ? AND integration_ref IS NULL`, integrationRef, cid)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			// 統合先の識別を課題に記録した（初めて）。作業ログに残す（変えた値と、原因の run）。
			if err := rec.record("challenge", cid, ActivityActionIntegrationRef, map[string]any{"integration_ref": nil},
				map[string]any{"integration_ref": integrationRef}); err != nil {
				return err
			}
		}
		verifying := false
		if scope.TaskID != nil {
			var st string
			if err := tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ?`, *scope.TaskID).Scan(&st); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if TaskStatus(st) == TaskStatusCancelled {
				return nil
			}
			verifying = TaskStatus(st) == TaskStatusVerifying
		}
		if verifying {
			same, err := sameArtifactsAsPreviousRead(ctx, tx, cid, scope.TaskID, runInt, rep.Artifacts)
			if err != nil || same {
				return err
			}
		}
		prs, err := importContractArtifacts(ctx, tx, runInt, rep.Artifacts, s.currentTime())
		rep.PRs = append(rep.PRs, prs...)
		if err != nil {
			return err
		}
		if scope.TaskID != nil && !verifying {
			if _, err := setTaskStatus(ctx, tx, rec, *scope.TaskID, TaskStatusVerifying); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

// sameArtifactsAsPreviousRead は、課題・タスクの直前に成果物を取り込んだ契約の読み（runInt より前）の成果物が、arts の取り込む種類の
// 成果物（種類・ref・役割）と同じか。
func sameArtifactsAsPreviousRead(ctx context.Context, tx *sql.Tx, cid int64, taskID *int64, runInt int64, arts []ContractArtifact) (bool, error) {
	var prev sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(r.id) FROM run r WHERE r.challenge_id = ? AND r.kind = 'contract' AND r.id < ? AND COALESCE(r.task_id, 0) = COALESCE(?, 0)
		 AND EXISTS (SELECT 1 FROM run_artifact a WHERE a.run_id = r.id)`, cid, runInt, taskID).Scan(&prev); err != nil {
		return false, err
	}
	if !prev.Valid {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind, ref, COALESCE(role, '') FROM run_artifact WHERE run_id = ?`, prev.Int64)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	have := map[string]int{}
	for rows.Next() {
		var k, r, role string
		if err := rows.Scan(&k, &r, &role); err != nil {
			return false, err
		}
		have[k+"\x00"+r+"\x00"+role]++
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, a := range arts {
		switch a.Kind {
		case "pr", "branch", "commit":
			k := a.Kind + "\x00" + a.Ref + "\x00" + a.Role
			if have[k] == 0 {
				return false, nil
			}
			have[k]--
		}
	}
	for _, n := range have {
		if n != 0 {
			return false, nil
		}
	}
	return true, nil
}

// undeliveredFactsUnmetAnswer は、理由 facts_unmet の保留への最後の回答のうち、まだ委譲先へ返していないもの
// （回答より後に始まった契約の run が、いま読んだ run〔currentRunID〕だけ）を返す。無ければ空文字列。
// 保留・契約の run とも、タスクの単位（taskID が同じもの。タスクに結び付かなければ、タスクに結び付かないもの）で絞る。
func (s *Store) undeliveredFactsUnmetAnswer(ctx context.Context, cid int64, taskID *int64, currentRunID string) (string, error) {
	cur, _ := parseRunID(currentRunID)
	var answer string
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var a, at sql.NullString
		qerr := tx.QueryRowContext(ctx,
			`SELECT answer, answered_at FROM hold WHERE challenge_id = ? AND COALESCE(task_id, 0) = COALESCE(?, 0) AND answered_at IS NOT NULL AND reason = ? ORDER BY id DESC LIMIT 1`,
			cid, taskID, string(holdReasonFactsUnmet)).Scan(&a, &at)
		if errors.Is(qerr, sql.ErrNoRows) {
			return nil
		}
		if qerr != nil {
			return qerr
		}
		var later int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM run WHERE challenge_id = ? AND COALESCE(task_id, 0) = COALESCE(?, 0) AND kind = 'contract' AND operation_id IS NULL AND id <> ? AND started_at >= ?`, cid, taskID, cur, at.String).Scan(&later); err != nil {
			return err
		}
		if later == 0 {
			answer = a.String
		}
		return nil
	})
	return answer, classifyReadWriteErr(err)
}

// respondToReview は、required_facts を満たさないレビュー待ちを読んだ同じ状態の確認の中で、同じ契約の run へ respond を返す。
func (s *Store) respondToReview(ctx context.Context, in ContractPollInput, row *contractRunRow, connector *Connector, binding *sourceBinding, planVersion, cycleID *int64, rep *ContractRunReport) (*ContractRunReport, error) {
	command, err := fillRowCommandArgs(connector, row, connector.Commands.Resume, rep.ChallengeID, binding, *rep.PendingRespond)
	if err != nil {
		return nil, err
	}
	latest := *row
	latest.State, latest.Summary, latest.CostMicros = rep.State, rep.Summary, usdToMicros(rep.CostTotalUSD)
	if ra := rep.RequestedAction; ra != nil {
		latest.RequestedKind, latest.RequestedDecider, latest.RequestedText, latest.RequestedResolvableBy = ra.Kind, ra.Decider, ra.Text, ra.ResolvableBy
	}
	rrep, err := s.runExistingContractCommand(ctx, in.Client, in.AgentDecl, existingContractWorkDir(row, in.WorkDir), &latest, "resume", command, planVersion, cycleID, "")
	if err != nil {
		return nil, err
	}
	if rrep.Result != RunResultSucceeded {
		held, err := s.holdReclaimedContractStart(ctx, rrep.RunID, rrep.ChallengeID, contractResumeFailedQuestion(row.ExternalRunID, rrep.Result, rrep.Error))
		rrep.Held = held
		return rrep, err
	}
	// 返した直後に同じ求めの waiting が返っても、その周では確かめ直さない（次の状態の確認で数える）。
	if rrep.State == ContractStateWaiting && sameRequestedAction(&latest, rrep.RequestedAction) {
		return rrep, nil
	}
	if err := s.afterContractRead(ctx, rrep, connector, in.releaseSource()); err != nil {
		return nil, err
	}
	return rrep, nil
}

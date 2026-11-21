package core

// このファイルは `form: cli` の接続ツールの run が成果物つきの「レビュー待ち」になったときの流れを持つ
// （docs/features/m3-invoker-delegation.md「後半の流れ」の review の節）:
//
//   - waiting かつ requested_action.kind = review かつ成果物（pr・branch・commit）あり: run の終わりを待たず、成果物を
//     その読みの run へ取り込んで課題を検証中へ進める（submitContractReview）。契約の run は waiting のまま開いておく。
//   - 完了の検証（J5）が not_met: 同じ契約の run へ resume（選択肢 respond・添え書きに差し戻しの理由）で返す。
//
// 接続ツール固有の名前（ゲート名など）は読まない。見分けるのは契約の汎用の値だけである。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// contractRequestedKindReview は、成果物のレビューを待つ requested_action.kind。
const contractRequestedKindReview = "review"

// contractRequestedKindObserve は、見守るだけの待ち（保留にせず読み直す）の requested_action.kind。
const contractRequestedKindObserve = "observe"

// contractRequestedKindAnswer は回答を求める待ち。contractDeciderParent は親が答える相手（J4）。
const (
	contractRequestedKindAnswer  = "answer"
	contractRequestedKindApprove = "approve"
	contractDeciderParent        = "parent"
)

// contractRequestedKindValues は requested_action.kind の閉集合（入力の検査と ContractRequestedKindValues の正本）。
var contractRequestedKindValues = []string{contractRequestedKindAnswer, contractRequestedKindApprove, contractRequestedKindObserve, contractRequestedKindReview}

func validContractRequestedKind(k string) bool {
	for _, v := range contractRequestedKindValues {
		if k == v {
			return true
		}
	}
	return false
}

// hasReviewableArtifact は artifacts に、検証中へ進めるときに取り込む種類（pr・branch・commit）が 1 件でもあるか。
func hasReviewableArtifact(arts []ContractArtifact) bool {
	for _, a := range arts {
		switch a.Kind {
		case "pr", "branch", "commit":
			return true
		}
	}
	return false
}

// submitContractReview は課題が着手中なら、レビュー待ちの成果物を rep の run へ取り込んで検証中へ進める。
// 取り込みと遷移は同じトランザクションで行う。着手中でなければ（保留・検証中など）何もしない。
func (s *Store) submitContractReview(ctx context.Context, rep *ContractRunReport, rs *contractReleaseSource) error {
	if err := s.registerContractReleases(ctx, rs, rep); err != nil {
		return err
	}
	runInt, ok := parseRunID(rep.RunID)
	if !ok {
		return nil
	}
	_, err := s.transitionAsInvoker(context.WithoutCancel(ctx), rep.RunID, rep.ChallengeID, OpSubmit,
		func(ctx context.Context, tc *transitionCtx) error {
			if tc.current.Status != StatusInProgress {
				return errReconcileNotEligible
			}
			prs, err := importContractArtifacts(ctx, tc.tx, runInt, rep.Artifacts, s.currentTime())
			rep.PRs = append(rep.PRs, prs...)
			if err != nil {
				return err
			}
			return verifyContractTask(ctx, tc, rep.ExternalRunID)
		})
	if err != nil && !errors.Is(err, ErrInvalidTransition) && !errors.Is(err, ErrTerminalState) && !errors.Is(err, errReconcileNotEligible) {
		return err
	}
	return nil
}

// contractReworkLimitQuestion は、レビュー待ちの契約の run で差し戻しの上限に達したときの保留の問い。通常の委譲の問い
// （reworkLimitQuestion。回答の後に委譲を起動し直す）とは違い、契約の run は終わらず、回答の後に同じ run へ resume
// （選択肢 respond、添え書きに差し戻しの指摘とオーナーの回答）を返して直させる。
func contractReworkLimitQuestion(hit *reworkLimitHit) string {
	return fmt.Sprintf("差し戻しの上限に達したため、接続ツールの run へ差し戻しを返さず人間対応待ちにした（上限の種類: 差し戻し、回数: %d、上限: %d）。\n"+
		"直近の検証: %s（達成条件を満たさないと判定された）。接続ツールの run は終わらず開いたまま。"+
		"回答すると、次の周から数え直して、同じ run へ差し戻しの指摘とあなたの回答を返す（新しい run は起動しない）。",
		hit.Count, hit.Limit, hit.Last.ID)
}

// reviewReply は、レビュー待ちの契約の run に対して、この周で flywheel がすることの決定。
// Answer が非 nil なら resume で返す。HoldQuestion が非空なら返さず課題を保留にする（原因の run は HoldRunID）。
// Skip が真なら何も呼ばない（検証中で、成果物を取り込んだ読みの run を新しい読みで押し流さないため）。
type reviewReply struct {
	Answer       *contractAnswer
	HoldQuestion string
	HoldRunID    string
	Skip         bool
}

// pendingReviewReply は、レビュー待ち（waiting・review）の契約の run 1 件について、flywheel が返す答えを決める。
//   - 課題が着手中で、最後の J5 が not_met で、その後に契約のコマンドを呼んでいない: respond と差し戻しの理由を返す。
//     差し戻しの上限（agent.ReworkLimit。J5 の not_met の数。保留への回答の後から数え直す）に達していれば、返さず保留にする。
//   - 課題が着手中で、T15（人が完了の承認を差し戻した）の記録が最後の契約のコマンドより新しい: respond と差し戻しの理由（approval.reason）を返す。
//   - 課題が検証中: 何も呼ばない（Skip）。J5 は「直前の契約の run」の成果物を読むので、新しい読みで押し流さない。
//
// 上のどれでもなければ nil（通常の状態の確認へ進む）。
func (s *Store) pendingReviewReply(ctx context.Context, row *contractRunRow, planVersion *int64, agent *AgentDeclaration) (*reviewReply, error) {
	var out *reviewReply
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, row.ChallengeID)
		if err != nil || ch == nil {
			return err
		}
		var noWaitHold bool
		if err := tx.QueryRowContext(ctx, `SELECT wait_hold_id IS NULL FROM contract_run WHERE id = ?`, row.ID).Scan(&noWaitHold); err != nil {
			return err
		}
		switch ch.Status {
		case StatusVerifying, StatusAwaitingCompletionApproval:
			out = &reviewReply{Skip: true}
			return nil
		case StatusAwaitingHuman:
			// J5 の問いなどで止まっている間も、成果物を取り込んだ読みの run を押し流さない（この待ちを写した保留の答えは別の経路で返す）。
			if noWaitHold {
				out = &reviewReply{Skip: true}
			}
			return nil
		case StatusInProgress:
		default:
			return nil
		}
		if planVersion == nil {
			return nil
		}
		h, err := loadLaunchHistory(ctx, tx, row.ChallengeID)
		if err != nil {
			return err
		}
		if row.TaskID != nil {
			h = h.forTask(formatTaskID(*row.TaskID))
		}
		if _, hit := h.decideRework(int(*planVersion), agent.ReworkLimit); hit != nil {
			out = &reviewReply{HoldQuestion: contractReworkLimitQuestion(hit), HoldRunID: hit.Last.ID}
			return nil
		}
		var lastContract int64
		contractQ, contractArgs := `SELECT COALESCE(MAX(id), 0) FROM run WHERE challenge_id = ? AND kind = 'contract' AND operation_id IS NULL`, []any{row.ChallengeID}
		if row.TaskID != nil {
			// 差し戻しの指摘を渡したかは、このタスクの契約の run だけで比べる（別のタスクの run を挟んでも指摘は渡す）。
			contractQ += ` AND task_id = ?`
			contractArgs = append(contractArgs, *row.TaskID)
		}
		if err := tx.QueryRowContext(ctx, contractQ, contractArgs...).Scan(&lastContract); err != nil {
			return err
		}
		var note string
		if n := len(h.Verifications); n > 0 {
			latest := h.Verifications[n-1]
			if runIntID(latest) > lastContract && latest.Result == RunResultSucceeded {
				if v, ok := validateJ5Output([]byte(latest.Output)); ok && v.Verdict == J5VerdictNotMet && v.Feedback != nil {
					note = *v.Feedback
					// 差し戻しの上限の保留に答えた後は、オーナーの回答も添える（保留の間に状態を読まないので、指摘は未配達のまま残る）。
					if n := len(h.Holds); n > 0 && h.Holds[n-1].Answer != nil && holdIsAfterRun(h.Holds[n-1], latest) {
						note += "\n\nオーナーの回答: " + *h.Holds[n-1].Answer
					}
				}
			}
		}
		if note == "" {
			// 人が完了の承認を差し戻した（T15）。最後の契約のコマンドより新しい差し戻しがあれば、すべての理由を古い順にまとめて返す。
			reason, err := loadCompletionRejectionAfterContract(ctx, tx, row.ChallengeID)
			if err != nil || reason == "" {
				return err
			}
			note = reason
		}
		out = &reviewReply{Answer: &contractAnswer{Choice: contractChoiceRespond, Note: note}}
		return nil
	})
	return out, classifyReadWriteErr(err)
}

// loadCompletionRejectionAfterContract は、課題の完了確認での差し戻し（T15。理由が空でないもの）のうち、その課題の最後の
// 契約のコマンドの開始より新しいものを、すべて古い順に添え書きへまとめて返す（複数たまっていても 1 回の respond で返す）。
// 契約のコマンドを呼んだ後は返さないので、二重に配達しない。該当が無ければ空文字列を返す。
// 計画の承認での差し戻しは含めない。
func loadCompletionRejectionAfterContract(ctx context.Context, tx *sql.Tx, challengeID int64) (string, error) {
	rs, err := loadCompletionRejections(ctx, tx, challengeID)
	if err != nil || len(rs) == 0 {
		return "", err
	}
	var lastRun sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT MAX(started_at) FROM run WHERE challenge_id = ? AND kind = 'contract' AND operation_id IS NULL`, challengeID).Scan(&lastRun); err != nil {
		return "", err
	}
	var undelivered []CompletionRejection
	for _, r := range rs {
		// runStartedOnOrAfter と同じ比べ方: 契約の run の開始が差し戻し以降なら配達済み。
		if lastRun.Valid && lastRun.String >= formatTimestamp(r.DecidedAt) {
			continue
		}
		undelivered = append(undelivered, r)
	}
	return formatCompletionRejectionsSection(undelivered), nil
}

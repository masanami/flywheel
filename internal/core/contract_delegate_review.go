package core

// このファイルは `form: cli` の接続ツールの run が成果物つきの「レビュー待ち」になったときの流れを持つ
// （docs/features/m3-invoker-delegation.md「後半の流れ」の review の節）:
//
//   - waiting かつ requested_action.kind = review かつ成果物（pr・branch・commit）あり: run の終わりを待たず、成果物を
//     その読みの run へ取り込んで課題を検証中へ進める（submitContractReview）。契約の run は waiting のまま開いておく。
//   - 完了の検証（J5）が not_met: 同じ契約の run へ resume（選択肢 respond・添え書きに差し戻しの理由）で返す。
//   - 人の完了の差し戻し（T15）の後: 最後の契約のコマンドより新しい差し戻しの理由を、同じく resume（respond）で 1 回だけ返す。
//   - 本番反映（flywheel のマージ）の後: resume（選択肢 ready）で返し、接続ツールの run を終わらせる。
//
// 接続ツール固有の名前（ゲート名など）は読まない。見分けるのは契約の汎用の値だけである。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// contractRequestedKindReview は、成果物のレビューを待つ requested_action.kind。
const contractRequestedKindReview = "review"

// contractRequestedKindObserve は、見守るだけの待ち（保留にせず読み直す）の requested_action.kind。
const contractRequestedKindObserve = "observe"

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
func (s *Store) submitContractReview(ctx context.Context, rep *ContractRunReport) error {
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
			return err
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
//   - 本番反映が済んでいる（その課題の release が executed）: ready を返して接続ツールの run を終わらせる。
//   - 課題が着手中で、最後の J5 が not_met で、その後に契約のコマンドを呼んでいない: respond と差し戻しの理由を返す。
//     差し戻しの上限（agent.ReworkLimit。J5 の not_met の数。保留への回答の後から数え直す）に達していれば、返さず保留にする。
//   - 課題が着手中で、最後の J5 が未配達の not_met でなく、最後の契約のコマンドより新しい人の完了の差し戻し（T15）がある:
//     その理由を respond で返す（J5 の経路より前、差し戻しの上限の判定より前に評価する）。
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
		var executed int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operation WHERE challenge_id = ? AND kind = 'release' AND state = 'executed'`,
			row.ChallengeID).Scan(&executed); err != nil {
			return err
		}
		// ready は課題が完了（本番反映の後）のときだけ返す。検証中などに別の release が executed でも、新しい run へ早すぎる ready を返さない。
		if executed > 0 && ch.Status == StatusDone {
			out = &reviewReply{Answer: &contractAnswer{Choice: contractChoiceReady}}
			return nil
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
		var lastContract int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM run WHERE challenge_id = ? AND kind = 'contract'`, row.ChallengeID).Scan(&lastContract); err != nil {
			return err
		}
		// 人が完了の承認を差し戻した（T15）後に契約のコマンドを呼んでいなければ、その理由を respond で返す。
		// 最後の J5 が未配達の not_met のとき（差し戻しの上限の判定を含む）は、従来の経路が先に扱う。
		if _, _, undelivered := latestJ5NotMetUndelivered(h, lastContract); !undelivered {
			note, err := pendingCompletionRejectionNote(ctx, tx, row.ChallengeID, lastContract)
			if err != nil {
				return err
			}
			if note != "" {
				out = &reviewReply{Answer: &contractAnswer{Choice: contractChoiceRespond, Note: note}}
				return nil
			}
		}
		if _, hit := h.decideRework(int(*planVersion), agent.ReworkLimit); hit != nil {
			out = &reviewReply{HoldQuestion: contractReworkLimitQuestion(hit), HoldRunID: hit.Last.ID}
			return nil
		}
		latest, v, ok := latestJ5NotMetUndelivered(h, lastContract)
		if !ok {
			return nil
		}
		note := *v.Feedback
		// 差し戻しの上限の保留に答えた後は、オーナーの回答も添える（保留の間に状態を読まないので、指摘は未配達のまま残る）。
		if n := len(h.Holds); n > 0 && h.Holds[n-1].Answer != nil && holdIsAfterRun(h.Holds[n-1], latest) {
			note += "\n\nオーナーの回答: " + *h.Holds[n-1].Answer
		}
		out = &reviewReply{Answer: &contractAnswer{Choice: contractChoiceRespond, Note: note}}
		return nil
	})
	return out, classifyReadWriteErr(err)
}

// latestJ5NotMetUndelivered は、最後の J5 が契約のコマンド（lastContract）より後に出た not_met で、まだ差し戻しを返していないとき、
// その run と検査済みの出力を返す（ok が真）。
func latestJ5NotMetUndelivered(h *launchHistory, lastContract int64) (runRow, *j5ValidatedOutput, bool) {
	if len(h.Verifications) == 0 {
		return runRow{}, nil, false
	}
	latest := h.Verifications[len(h.Verifications)-1]
	if runIntID(latest) <= lastContract || latest.Result != RunResultSucceeded {
		return runRow{}, nil, false
	}
	v, ok := validateJ5Output([]byte(latest.Output))
	if !ok || v.Verdict != J5VerdictNotMet || v.Feedback == nil {
		return runRow{}, nil, false
	}
	return latest, v, true
}

// pendingCompletionRejectionNote は、最後の契約のコマンドより新しい人の完了の差し戻し（approval の reason。--relayed の記録も同じ）を
// 古い順に並べた添え書きを返す。無ければ空文字列。契約のコマンドを呼べば最後のコマンドが新しくなるので、同じ理由は 1 回しか返さない。
func pendingCompletionRejectionNote(ctx context.Context, tx *sql.Tx, challengeID, lastContract int64) (string, error) {
	rejections, err := loadCompletionRejections(ctx, tx, challengeID)
	if err != nil || len(rejections) == 0 {
		return "", err
	}
	var since time.Time
	if lastContract > 0 {
		r, err := loadRunByID(ctx, tx, lastContract)
		if err != nil {
			return "", err
		}
		if r != nil {
			since = r.StartedAt
		}
	}
	var pending []CompletionRejection
	for _, r := range rejections {
		if r.DecidedAt.After(since) {
			pending = append(pending, r)
		}
	}
	if len(pending) == 0 {
		return "", nil
	}
	return formatCompletionRejectionsSection(pending), nil
}

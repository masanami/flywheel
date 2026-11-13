package core

// このファイルは #84（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §判断点の共通の規則）が持つ、判断点に依らない共通のクエリ・入力の組み立て
// 補助を持つ。J1（本チケット。judgment_j1.go）と、後続の J2（#85）が共有する
// 想定で新規ファイルに切り出す。判断点固有の写像・検査・CLI の結線は
// judgment_j1.go（と #85 の judgment_j2.go）に置く。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// priorityRank は優先度の並び順（P0→P1→P2→未設定）を表す整数を返す
// （§一括の操作（サイクル）「各段の対象は、優先度 P0・P1・P2・未設定の順、
// 同じ優先度では ID の昇順で処理する」。この規則は個別の `--auto` 操作にも
// 適用する【仮定】: 仕様は「各段の対象は」と書いており `cycle` の段と
// `--auto` の個別の操作が同じ core の処理を呼ぶ〈§一括の操作「各段は、個別の
// 操作…と同じ core の処理を呼ぶ」〉ため、対象の選び方も揃えるのが自然と
// 判断した）。
func priorityRank(p *Priority) int {
	if p == nil {
		return 3
	}
	switch *p {
	case PriorityP0:
		return 0
	case PriorityP1:
		return 1
	case PriorityP2:
		return 2
	default:
		return 3
	}
}

// loadChallengesByStatusSorted は status の課題を、優先度→ID の昇順
// （priorityRank。同じ優先度では SQL の `ORDER BY id ASC` の並びを
// sort.SliceStable が保つ）で返す。
func loadChallengesByStatusSorted(ctx context.Context, tx *sql.Tx, status Status) ([]Challenge, error) {
	rows, err := tx.QueryContext(ctx, challengeSelectColumns+` WHERE status = ? ORDER BY id ASC`, string(status))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Challenge
	for rows.Next() {
		c, err := scanChallengeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stableSortChallengesByPriority(out)
	return out, nil
}

// stableSortChallengesByPriority は out を priorityRank で安定ソートする
// （呼び出し前の並び順〈ID 昇順〉を同順位の中で保つ。design-reviewer 指摘・
// round1 CONFIRMED: 以前はコメントで sort.SliceStable を使うとしながら
// 手書きの挿入ソートを実装しており、コメントと実装が食い違っていた）。
func stableSortChallengesByPriority(out []Challenge) {
	sort.SliceStable(out, func(i, j int) bool {
		return priorityRank(out[i].Priority) < priorityRank(out[j].Priority)
	})
}

// challengeHasActiveRun は challengeID（内部整数 ID）の課題が終了していない
// run を持つかを返す（§判断点の共通の規則「判断の呼び出しの対象にするのは…
// 終了していない run を持たない課題に限る」）。
func challengeHasActiveRun(ctx context.Context, tx *sql.Tx, challengeID int64) (bool, error) {
	row, err := loadActiveRunByChallengeID(ctx, tx, challengeID)
	if err != nil {
		return false, err
	}
	if row != nil {
		return true, nil
	}
	// 周をまたいで続く契約の run（form: cli）も、課題の実行中として数える。
	var n int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND ended_at IS NULL`, challengeID).Scan(&n)
	if err != nil || n > 0 {
		return n > 0, err
	}
	// 保留の間に契約の run が成功し、まだ検証中へ進めていない課題（二つ目の委譲を起動させない）も同じ。
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run k JOIN challenge c ON c.id = k.challenge_id
		WHERE k.challenge_id = ? AND k.held_success = 1 AND c.status = 'in_progress'`, challengeID).Scan(&n); err != nil || n > 0 {
		return n > 0, err
	}
	// 契約の start の最中に回収され、まだ保留に写していない課題（外部の run が残っているかもしれない）も、実行中として数える。
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run r JOIN challenge c ON c.id = r.challenge_id
		WHERE r.challenge_id = ? AND `+reclaimedContractStartWhere, challengeID).Scan(&n); err != nil || n > 0 {
		return n > 0, err
	}
	// 契約の run が failed・cancelled で終わったのに保留を作れていない課題も同じ。
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run k JOIN challenge c ON c.id = k.challenge_id
		WHERE k.challenge_id = ? AND `+unheldContractStopWhere, challengeID).Scan(&n)
	return n > 0, err
}

// challengeAutoExclusion は challengeID が §判断点の共通の規則「周の中で自動で選ぶ対象からは、
// 上流の状態が closed・missing の課題と、ポリシーの状態が out_of_policy の課題を除く」に当たるかを
// 判定し、当たれば除外の理由（not_started の 1 要素）を返す。当たらなければ nil（ID を指定した
// 個別の操作では呼ばない＝この除外を適用しない。§判断点の共通の規則「課題の ID を指定した個別の
// 操作は、上の除外を適用しない」）。対応（source_binding）が無い課題（`create` で作った課題）は
// 除外しない。
//
// 上流の状態が closed でも、その課題自身の本番反映の操作で取り込み元が閉じた（closedByOwnRelease
// が真）なら、上流の状態では除外しない（#440。委譲の PR は `Closes #<取り込み元>` を書くので、
// 完了の検証より前に本番反映を実行すると取り込み元が閉じる。ここで外すと課題が検証中のまま止まる）。
// ポリシーの状態による除外はそのまま当てる。
//
// 理由は食い違いの種類ごとに分ける（upstream_closed・upstream_missing・out_of_policy）。複数に
// 当たるときは上流の状態を先に見る（食い違いの表示〈discrepancyKinds〉の並びと同じ）。Detail は
// 取り込み元の URL。
func challengeAutoExclusion(ctx context.Context, tx *sql.Tx, challengeID int64) (*NotStarted, error) {
	sb, err := loadSourceBindingByChallengeID(ctx, tx, challengeID)
	if err != nil {
		return nil, err
	}
	if sb == nil {
		return nil, nil
	}
	ns := func(r NotStartedReason) *NotStarted {
		return &NotStarted{ChallengeID: formatChallengeID(challengeID), Reason: r, Detail: sb.URL}
	}
	switch sb.UpstreamState {
	case upstreamStateClosed:
		own, err := closedByOwnRelease(ctx, tx, challengeID)
		if err != nil {
			return nil, err
		}
		if !own {
			return ns(NotStartedUpstreamClosed), nil
		}
	case upstreamStateMissing:
		return ns(NotStartedUpstreamMissing), nil
	}
	if sb.PolicyState == policyStateOutOfPolicy {
		return ns(NotStartedOutOfPolicy), nil
	}
	return nil, nil
}

// closedByOwnRelease は、challengeID の課題の取り込み元が「その課題自身の本番反映（委譲の PR の
// マージ）で閉じた」とストアの記録だけで見なせるかを返す（#440。GitHub への問い合わせはしない）。
// 条件は次の 2 つ。
//
//  1. その課題の release の不可逆操作のうち、実行済み（state=executed。実行の結果が merged・
//     already_merged のときだけこの状態になる）で、参照（ref）が PR の URL として読め、マージの
//     コミット（merge_commit）と実行の日時（executed_at）が記録されているものが 1 つ以上ある。
//  2. そのうち最も新しい実行より後に、取り込みが取り込み元の reopen（upstream_state_change で
//     upstream_state が open になった作業ログ）を記録していない。本番反映の後に reopen された
//     取り込み元が再び閉じたのは、本番反映によるものではないため（外部の理由で閉じたものと同じに扱う）。
//
// flywheel を通さず GitHub で直接マージされた課題（release が未実行）は、委譲の run の成果物の
// PR と一致しても真にしない（外部の理由で閉じたものと同じに扱う）。
func closedByOwnRelease(ctx context.Context, tx *sql.Tx, challengeID int64) (bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT ref, merge_commit, executed_at FROM operation WHERE challenge_id = ? AND kind = ? AND state = ? ORDER BY id`,
		challengeID, string(OperationKindRelease), string(OperationStateExecuted))
	if err != nil {
		return false, err
	}
	latest := ""
	for rows.Next() {
		var ref, mergeCommit, executedAt sql.NullString
		if err := rows.Scan(&ref, &mergeCommit, &executedAt); err != nil {
			_ = rows.Close()
			return false, err
		}
		if !ref.Valid || !mergeCommit.Valid || strings.TrimSpace(mergeCommit.String) == "" || !executedAt.Valid {
			continue
		}
		if _, _, ok := parseReleasePullRequestRef(ref.String); !ok {
			continue
		}
		if executedAt.String > latest {
			latest = executedAt.String
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	_ = rows.Close()
	if latest == "" {
		return false, nil
	}
	reopened, err := upstreamReopenedAfter(ctx, tx, challengeID, latest)
	if err != nil {
		return false, err
	}
	return !reopened, nil
}

// upstreamReopenedAfter は、challengeID の課題に、日時 at（formatTimestamp の形）より後の取り込み元の
// reopen（action=upstream_state_change で after の upstream_state が open）の作業ログがあるかを返す。
// 日時の形は固定の桁なので文字列の比較で前後を決める。
func upstreamReopenedAfter(ctx context.Context, tx *sql.Tx, challengeID int64, at string) (bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT after FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = 'upstream_state_change' AND at > ?`,
		challengeID, at)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var after sql.NullString
		if err := rows.Scan(&after); err != nil {
			return false, err
		}
		if !after.Valid {
			continue
		}
		var v struct {
			UpstreamState string `json:"upstream_state"`
		}
		if err := json.Unmarshal([]byte(after.String), &v); err != nil {
			return false, err
		}
		if v.UpstreamState == string(upstreamStateOpen) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// latestJudgmentRun は latestSucceededJudgmentOutput が返す、直近で成功した
// 判断の呼び出し 1 件の要点。
type latestJudgmentRun struct {
	RunID            string
	ChallengeVersion int64
	Output           string
}

// latestSucceededJudgmentOutput は challengeID の、判断点 j の run のうち
// 結果が succeeded だった直近 1 件（run.id 降順の先頭）の ID・起動時点の
// 課題の版・構造化出力（JSON 文字列）を返す（無ければ nil）。判定結果を
// 課題自身の列に持たせず run の記録から導くために使う（J1 の not_mine 除外・
// `status` の triage 表示。judgment_j1.go 参照）。
func latestSucceededJudgmentOutput(ctx context.Context, tx *sql.Tx, challengeID int64, j JudgmentPoint) (*latestJudgmentRun, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT id, challenge_version, output FROM run
		 WHERE challenge_id = ? AND judgment = ? AND kind = ? AND result = ?
		 ORDER BY id DESC LIMIT 1`,
		challengeID, string(j), string(runKindJudgment), string(runResultSucceeded),
	)
	var id, version int64
	var output sql.NullString
	err := row.Scan(&id, &version, &output)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !output.Valid {
		return nil, nil
	}
	return &latestJudgmentRun{RunID: formatRunID(id), ChallengeVersion: version, Output: output.String}, nil
}

// formatSourceBindingSection は課題の取り込み元の対応（source_binding）を
// 判断点の入力向けのテキストへ整形する（§判断点の共通の規則・§J1「J1 の
// 入力は…取り込み元の対応の記録（あれば）…である」）。対応が無い課題
// （sb が nil。`create` で作った課題）では空文字列を返す（呼び出し側はこの
// 場合データの区画を作らない）。
func formatSourceBindingSection(sb *sourceBinding) string {
	if sb == nil {
		return ""
	}
	return "URL: " + sb.URL + "\n" +
		"外部キー: " + sb.ExternalKey + "\n" +
		"上流の状態: " + string(sb.UpstreamState) + "\n" +
		"ポリシーの状態: " + string(sb.PolicyState)
}

// formatHoldsSection は保留の記録（問いと回答）を古い順のテキストへ整形する
// （§判断点の共通の規則「判断点の入力に、その課題に保留の記録があれば、
// すべての保留の問いと回答を古い順に含める」）。holds は既に古い順
// （loadHolds が id 昇順で返す）。空なら空文字列を返す（呼び出し側はこの
// 場合データの区画を作らない＝空の区画を渡さない）。
func formatHoldsSection(holds []Hold) string {
	if len(holds) == 0 {
		return ""
	}
	var b strings.Builder
	for i, h := range holds {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("問い: ")
		b.WriteString(h.Question)
		b.WriteString("\n回答: ")
		switch {
		case h.Answer != nil:
			b.WriteString(*h.Answer)
		case h.ClosedAt != nil:
			b.WriteString("(回答なしで閉じた)")
		default:
			b.WriteString("(未回答)")
		}
	}
	return b.String()
}

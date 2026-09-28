package core

// このファイルは #84（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §判断点の共通の規則）が持つ、判断点に依らない共通のクエリ・入力の組み立て
// 補助を持つ。J1（本チケット。judgment_j1.go）と、後続の J2（#85）が共有する
// 想定で新規ファイルに切り出す。判断点固有の写像・検査・CLI の結線は
// judgment_j1.go（と #85 の judgment_j2.go）に置く。

import (
	"context"
	"database/sql"
	"errors"
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
// （呼び出し前の並び順〈ID 昇順〉を同順位の中で保つ）。
func stableSortChallengesByPriority(out []Challenge) {
	// sort.SliceStable は N が小さい（周内の対象数）ことを踏まえ、依存を
	// 増やさない単純な挿入ソートで十分。標準ライブラリの sort を素直に使う。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && priorityRank(out[j].Priority) < priorityRank(out[j-1].Priority); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
}

// challengeHasActiveRun は challengeID（内部整数 ID）の課題が終了していない
// run を持つかを返す（§判断点の共通の規則「判断の呼び出しの対象にするのは…
// 終了していない run を持たない課題に限る」）。
func challengeHasActiveRun(ctx context.Context, tx *sql.Tx, challengeID int64) (bool, error) {
	row, err := loadActiveRunByChallengeID(ctx, tx, challengeID)
	if err != nil {
		return false, err
	}
	return row != nil, nil
}

// challengeAutoExcludedByPolicy は challengeID が §判断点の共通の規則「周の
// 中で自動で選ぶ対象からは、上流の状態が closed・missing の課題と、ポリシーの
// 状態が out_of_policy の課題を除く」に当たるかを判定する（ID を指定した
// 個別の操作では呼ばない＝この除外を適用しない。§判断点の共通の規則「課題の
// ID を指定した個別の操作は、上の除外を適用しない」）。対応（source_binding）
// が無い課題（`create` で作った課題）は除外しない。
func challengeAutoExcludedByPolicy(ctx context.Context, tx *sql.Tx, challengeID int64) (bool, error) {
	sb, err := loadSourceBindingByChallengeID(ctx, tx, challengeID)
	if err != nil {
		return false, err
	}
	if sb == nil {
		return false, nil
	}
	if sb.UpstreamState == upstreamStateClosed || sb.UpstreamState == upstreamStateMissing {
		return true, nil
	}
	if sb.PolicyState == policyStateOutOfPolicy {
		return true, nil
	}
	return false, nil
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
		if h.Answer != nil {
			b.WriteString(*h.Answer)
		} else {
			b.WriteString("(未回答)")
		}
	}
	return b.String()
}

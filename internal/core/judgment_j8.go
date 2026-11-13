package core

// このファイルは J8（改修の提案）の出力を、1 つのトランザクションでストアへ写す。
//
// 写像の流れ:
//
//	① 出力の形を検査する（validateJ8Output。落ちれば run を invalid_output にして何も書かない）
//	② トランザクションの中で、根拠の発生が入力の明細に載せた発生で、かつまだ未処理かを確かめ直す
//	   （並行に走った別の J8 が先に処理していれば出力全体を不正にする）
//	③ 提案ごとに、同じ改修の鍵の終端でない改修課題があればそこへ紐づけ、無ければ未分類の改修課題を起票し、
//	   根拠の発生に課題の ID（C-<n>）を付ける。作業ログ（action=improvement）を残す
//	④ 原因 performance の未処理の発生（入力を組み立てた時点のもの）に「待つ」（wait）を付ける
//
// どこかが失敗すれば課題も印も作業ログも残らない。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// errJ8Invalid は出力がストアの事実（入力の明細・発生の処理の状態）に合わないことを表す。
var errJ8Invalid = errors.New("core: J8 output does not match the store")

// j8PromptGuardSentence は層 prompt の改修課題の完了条件に、J8 の出力によらず core が足す文。
const j8PromptGuardSentence = "指示文の分量の上限（CheckPromptSizes）と構造の不変条件（禁止語の検査）のテストを通すこと。"

// j8Mapped は写像の結果。
type j8Mapped struct {
	// Filed は新しく起票した改修課題の ID。
	Filed []string
	// Linked は紐づけた既存の改修課題の ID（重複の抑え）。
	Linked []string
	// Waited は「待つ」を付けた発生の ID。
	Waited []string
}

// j8DoneCriteria は改修課題の完了条件。層 prompt にだけ分量と禁止語の検査の文を足す。
func j8DoneCriteria(p J8Proposal) string {
	if p.Layer != OccurrenceLayerPrompt {
		return p.DoneCriteria
	}
	return strings.TrimRight(p.DoneCriteria, "\n") + "\n" + j8PromptGuardSentence
}

// j8Description は改修課題の説明（層・対象・原因・指標・期待する変化・種類・根拠の発生の ID・成果物の経路）。
func j8Description(p J8Proposal, route string) string {
	return fmt.Sprintf("層: %s\n対象: %s\n原因: %s\n改善したい指標: %s\n期待する変化: %s\n種類: %s\n根拠の発生: %s\n%s",
		p.Layer, p.Target, p.Cause, p.Metric, p.Expected, p.Action, strings.Join(p.OccurrenceIDs, ", "), route)
}

// findOpenImprovement は同じ改修の鍵（層の一致と、対象の前後の空白を除いた完全一致）の、終端でない
// （完了でも取り下げでもない）改修課題のうち最も古いものの ID を返す。無ければ 0。
func findOpenImprovement(ctx context.Context, tx *sql.Tx, layer OccurrenceLayer, target string) (int64, error) {
	terminal := terminalStatusArgs()
	args := append([]any{string(ChallengeOriginImprovement), string(layer)}, terminal...)
	rows, err := tx.QueryContext(ctx,
		`SELECT id, improvement_target FROM challenge
		 WHERE origin = ? AND improvement_layer = ? AND status NOT IN (`+placeholders(len(terminal))+`) ORDER BY id ASC`,
		args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var t sql.NullString
		if err := rows.Scan(&id, &t); err != nil {
			return 0, err
		}
		if strings.TrimSpace(nullStrValue(t)) == target {
			return id, nil
		}
	}
	return 0, rows.Err()
}

// applyJ8 は検査を通った提案を 1 つのトランザクションで写す。allowed は J8 の入力の明細に載せた発生の ID、
// waitIDs は入力を組み立てた時点の「原因 performance の未処理の発生」。出力がストアの事実に合わなければ
// invalid=true（何も書かない）。originURL はワークスペースの origin（読めなければ空）。
func (s *Store) applyJ8(ctx context.Context, runIDDisplay string, props []J8Proposal, allowed map[int64]bool,
	waitIDs []int64, decl *ConnectorsDeclaration, originURL string) (m j8Mapped, invalid bool, err error) {
	runInt, ok := parseRunID(runIDDisplay)
	if !ok {
		return m, false, ErrValidation
	}
	actor, err := resolveActor()
	if err != nil {
		return m, false, err
	}
	err = s.mutateAsRun(ctx, actor, ChannelInvoker, VerificationNone, &runInt, func(tx *sql.Tx, rec *activityRecorder) error {
		m = j8Mapped{}
		// 根拠の発生の ID は、入力の明細に載せた発生の中にある。
		evidence := make([][]int64, len(props))
		for i, p := range props {
			for _, raw := range p.OccurrenceIDs {
				id, ok := parseOccurrenceID(raw)
				if !ok || !allowed[id] {
					return errJ8Invalid
				}
				evidence[i] = append(evidence[i], id)
			}
		}
		for i, p := range props {
			cid, err := findOpenImprovement(ctx, tx, p.Layer, p.Target)
			if err != nil {
				return err
			}
			if cid == 0 {
				if s.insertChallenge != nil {
					if err := s.insertChallenge(tx, ImprovementKey{Layer: string(p.Layer), Target: p.Target}); err != nil {
						return err
					}
				}
				doneCriteria := j8DoneCriteria(p)
				description := j8Description(p, improvementRoute(p.Layer, decl, originURL))
				key := ImprovementKey{Layer: string(p.Layer), Target: p.Target}
				cid, _, err = insertChallengeRow(ctx, tx, rec.at, p.Title, description, doneCriteria, nil, rec.actor,
					ChallengeOriginImprovement, nil, &key)
				if err != nil {
					return err
				}
				after := createdChallengeAfter(p.Title, description, doneCriteria, nil, rec.actor)
				after["origin"] = string(ChallengeOriginImprovement)
				after["improvement_layer"] = string(p.Layer)
				after["improvement_target"] = p.Target
				after["occurrence_ids"] = p.OccurrenceIDs
				if err := rec.record("challenge", cid, ActivityActionImprovement, nil, after); err != nil {
					return err
				}
				m.Filed = append(m.Filed, formatChallengeID(cid))
			} else {
				m.Linked = append(m.Linked, formatChallengeID(cid))
			}
			for _, oid := range evidence[i] {
				err := markOccurrenceHandled(ctx, tx, oid, formatChallengeID(cid),
					`outcome = ? AND cause IS NOT NULL AND cause NOT IN (?, ?)`,
					string(OccurrenceOutcomeBad), string(OccurrenceCausePerformance), string(OccurrenceCauseNone))
				if errors.Is(err, errOccurrenceNotPending) {
					return errJ8Invalid
				}
				if err != nil {
					return err
				}
			}
		}
		for _, oid := range waitIDs {
			err := markOccurrenceHandled(ctx, tx, oid, occurrenceHandledWait, `cause = ?`, string(OccurrenceCausePerformance))
			if errors.Is(err, errOccurrenceNotPending) {
				return errJ8Invalid
			}
			if err != nil {
				return err
			}
			m.Waited = append(m.Waited, formatOccurrenceID(oid))
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errJ8Invalid) {
			return j8Mapped{}, true, nil
		}
		return j8Mapped{}, false, err
	}
	return m, false, nil
}

// terminalStatusArgs は遷移表（IsTerminal）が終端とする状態の集合を、SQL の引数の並びで返す。
// 終端の状態を SQL に直書きしない（遷移表に状態を足したときに、ここが黙って古くならないようにする）。
func terminalStatusArgs() []any {
	var out []any
	for _, e := range StatusVocabulary {
		if IsTerminal(Table, StatusVocabulary, e.Code) {
			out = append(out, string(e.Code))
		}
	}
	return out
}

// placeholders は n 個の "?" をカンマで並べた文字列を返す。
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

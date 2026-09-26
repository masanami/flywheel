package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// --- 取り込み（ingest）の公開 API と結果の型 ---
//
// docs/features/m2-github-issue-ingest.md §機能全体の設計「アーキテクチャ決定」:
// core の取り込みは、①ストアを読む（書き込みロックなし）→②取得する（書き込み
// ロックなし）→③Issue ごとに書き込みトランザクション（BEGIN IMMEDIATE）の中で
// 最新の状態を読み直して反映する、の順に進む。
//
// この骨格は #56 が作った（対応の無い、ポリシーに合う Issue から課題を作る
// ＝ingest_create）。#57 が対応のある Issue の冪等な更新（reconcileBoundIssue。
// fingerprint の 3 分岐・完了した課題のスキップ・ingest_update）を、#58 が次を
// 足した:
//   - open の一覧に現れた対応: reconcileBoundIssue の 1 つの書き込みトランザク
//     ションの中で reopen（upstream_state_change）とポリシーの再判定
//     （policy_state_change。self_assignees を解決できた回だけ＝AC-33）も判定する
//   - open の一覧に現れなかった対応: リポジトリのループの後で
//     checkUnseenOpenBindings → confirmOpenListAbsence が GetIssue で 1 件ずつ
//     確かめる（§上流の close の検出）
//   - どちらの経路も、判定した結果を反映の計画（ingestReflection）にして
//     applyIngestReflection へ渡す。版の加算と作業ログの記録はここ 1 か所に
//     まとめ、1 回の反映で契機が複数起きても版は 1 だけ増やす（§作業ログと版）。
//     #65 系の観測値（upstream_observation_change）も、この計画の契機として足す
//
// --source の指定（宣言のうちどの取り込み元を処理するか。省略時は全件・
// 空文字列との区別）は #63（core.SelectSources）の責務。Ingest は「選ばれた
// 取り込み元」の一覧を受け取るだけにする（親要件チケット #56 の完了条件にある
// 仮定）。

// IngestOutcome は ingest の JSON の要素の `result` の値
// （docs/features/m2-github-issue-ingest.md §IF / API `ingest` の JSON 出力）。
// #56 が IngestOutcomeCreated・IngestOutcomeFailed を、#57 が
// IngestOutcomeUpdated・IngestOutcomeUnchanged・IngestOutcomeSkippedDone・
// IngestOutcomeFingerprintUnknownVersion を生成する（reconcileBoundIssue）。
// #58 は新しい IngestOutcome を足さない（close の確かめ・ポリシーの再判定は
// result とは別の反映で、result は人間記入欄と fingerprint についての結果の
// ままである。§IF / API「`result` は人間記入欄と fingerprint についての結果
// であり、`unchanged` は上流の状態・ポリシーの状態が変わった場合も含む」）。
type IngestOutcome string

// IngestOutcome の値（仕様の列挙の順）。
const (
	IngestOutcomeCreated                   IngestOutcome = "created"
	IngestOutcomeUpdated                   IngestOutcome = "updated"
	IngestOutcomeUnchanged                 IngestOutcome = "unchanged"
	IngestOutcomeSkippedDone               IngestOutcome = "skipped_done"
	IngestOutcomeFingerprintUnknownVersion IngestOutcome = "fingerprint_unknown_version"
	IngestOutcomeFailed                    IngestOutcome = "failed"
)

// ingestOutcomeValues は IngestOutcome の閉集合（仕様の列挙の順）。
var ingestOutcomeValues = []IngestOutcome{
	IngestOutcomeCreated,
	IngestOutcomeUpdated,
	IngestOutcomeUnchanged,
	IngestOutcomeSkippedDone,
	IngestOutcomeFingerprintUnknownVersion,
	IngestOutcomeFailed,
}

// IngestOutcomeValues は IngestOutcome の閉集合の写しを返す（CLI のテストが
// 仕様の列挙と双方向に照合するため）。
func IngestOutcomeValues() []IngestOutcome {
	return append([]IngestOutcome(nil), ingestOutcomeValues...)
}

// IngestInput は Store.Ingest の入力。
type IngestInput struct {
	// Sources は処理する取り込み元（宣言の順。§実行の起点「宣言のすべての
	// 取り込み元を宣言の順に処理する」）。LoadSourcesDeclaration で検証した
	// 宣言から --source で選んだもの（#63 の SelectSources の戻り値）を渡す
	// こと。Ingest は検証済みであることを前提にし、渡されたものをそのまま
	// 処理する（--source の省略と空文字列の区別も呼び出し側の責務）。
	Sources []SourceEntry
	// Upstream は上流（GitHub）の取得の実装（internal/adapters/github の #55、
	// またはテストの偽の実装）。
	Upstream UpstreamIssueSource
}

// IngestResult は Store.Ingest の出力（§IF / API `ingest` の JSON をそのまま
// 組み立てられる形）。
type IngestResult struct {
	Sources []SourceIngestResult
}

// SourceIngestResult は IngestResult の取り込み元 1 件分。
type SourceIngestResult struct {
	ID string
	// SelfAssigneesResolved は self_assignees の解決に成功したか
	// （§取り込みの対象「self_assignees を省略した取り込み元は…」）。
	// 明示された取り込み元は常に true。
	SelfAssigneesResolved bool
	Repos                 []RepoIngestResult
}

// RepoIngestResult は SourceIngestResult のリポジトリ 1 件分。
type RepoIngestResult struct {
	Repo string
	// Error は一覧の取得が失敗したときだけ非 nil（部分成功。§機能要件「取得」
	// 「一覧の取得に失敗したリポジトリは、そのリポジトリについて何も変更せず、
	// 結果に失敗として示す。他のリポジトリの処理は続ける」）。
	Error    *string
	Items    []IngestItemResult
	Excluded int
}

// IngestItemResult は RepoIngestResult の 1 要素（課題を作った・対応のある
// Issue だけを並べる。ポリシーに合わない新しい Issue は Excluded の件数だけに
// 数える）。
type IngestItemResult struct {
	ExternalKey   string
	ChallengeID   string
	Result        IngestOutcome
	UpstreamState string
	PolicyState   string
	// Error は Result が IngestOutcomeFailed のときの失敗の要約。
	Error *string
}

// Ingest は in.Sources を宣言の順に処理する 1 回の取り込みの実行本体
// （パッケージコメントのアーキテクチャに従う）。リポジトリ単位の失敗（一覧の
// 取得）・Issue 単位の失敗（1 件の反映）はいずれも結果に示し、他の処理は続ける
// （部分成功）。実行全体に効く失敗は結果に積まず error を返す: actor を解決
// できない（ErrActorUnavailable。取得の前に確かめ、何も変更しない）・ストアを
// 読めない（①）・ctx の取り消し（それまでに反映した Issue は残る）。
func (s *Store) Ingest(ctx context.Context, ch Channel, in IngestInput) (*IngestResult, error) {
	// 作業ログの actor（OS のログインユーザー名）は M1 の mutate と同じ規則で
	// 解決する。解決できない環境では上流へ接続せずに終える。
	actor, err := resolveActor()
	if err != nil {
		return nil, err
	}

	// ①ストアを読む（書き込みロックなし）: 対応のある外部キーごとの上流の状態と
	// 課題が完了しているか。ここで読んだ値は②の後の判定の目安にだけ使い、実際の
	// 反映は③（reconcileBoundIssue／confirmOpenListAbsence）で読み直す。
	bound, err := s.loadBoundExternalKeys(ctx)
	if err != nil {
		return nil, err
	}

	result := &IngestResult{Sources: make([]SourceIngestResult, 0, len(in.Sources))}

	for _, src := range in.Sources {
		sr := SourceIngestResult{ID: src.ID, Repos: make([]RepoIngestResult, 0, len(src.Repos))}

		// ②取得する（の一部）: self_assignees の解決。呼ぶのは
		// Upstream.CurrentLogin だけで、ストアの書き込みロックは保持しない。
		selfAssignees, resolved := resolveIngestSelfAssignees(ctx, src, in.Upstream)
		sr.SelfAssigneesResolved = resolved

		for _, repo := range src.Repos {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			rr := RepoIngestResult{Repo: repo, Items: make([]IngestItemResult, 0)}

			// ②取得する: ストアの書き込みロックを保持しない。
			issues, err := in.Upstream.ListOpenIssues(ctx, repo)
			if err != nil {
				msg := err.Error()
				rr.Error = &msg
				sr.Repos = append(sr.Repos, rr)
				continue // 部分成功: このリポジトリは何も変更せず、他は続ける
			}

			for _, issue := range issues {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				_, isBound := bound[issue.ExternalKey]
				item, excluded, err := s.ingestOneIssue(ctx, actor, ch, src, selfAssignees, resolved, isBound, issue)
				switch {
				case err != nil:
					msg := err.Error()
					rr.Items = append(rr.Items, IngestItemResult{
						ExternalKey: issue.ExternalKey,
						Result:      IngestOutcomeFailed,
						Error:       &msg,
					})
				case excluded:
					rr.Excluded++
				case item != nil:
					rr.Items = append(rr.Items, *item)
				}
			}
			// #58: 一覧の取得に成功したこのリポジトリで、①で読んだ対応のうち
			// open の一覧に現れなかったもの（upstream_state が open・未完了）を
			// GetIssue で 1 件ずつ確かめる（§上流の close の検出）。対応を
			// リポジトリで選ぶときは、宣言の表記（repo）と external_key の表記
			// （API の正規の表記）の違いを考えて大文字小文字を無視する。一覧の
			// 取得に失敗したリポジトリ（rr.Error != nil）はここへ来ない
			// （AC「一覧の取得に失敗したリポジトリの課題は…変わらない」）。
			closeItems, err := s.checkUnseenOpenBindings(ctx, actor, ch, repo, bound, issues, in.Upstream)
			if err != nil {
				return nil, err
			}
			rr.Items = append(rr.Items, closeItems...)
			sr.Repos = append(sr.Repos, rr)
		}
		result.Sources = append(result.Sources, sr)
	}
	return result, nil
}

// ingestOneIssue は 1 件の Issue を判定する:
//   - 対応が既にあれば、reconcileBoundIssue で冪等な更新を行う
//   - 対応が無く、ポリシーに合わなければ excluded=true
//   - 対応が無く、ポリシーに合えば課題と対応を作り、item を返す
//
// 対応の有無（isBound）は①で読んだ値。実際の作成・更新は
// createChallengeFromIssue／reconcileBoundIssue が 1 つの書き込みトランザクション
// の中で読み直してから行う（③）。
//
// self_assignees の解決に失敗した取り込み元でも、対応のある Issue の冪等な更新は
// 行う【仮定】。AC-33 が除くのはポリシーの再判定（#58）だけで、fingerprint に
// よる人間記入欄の置き換えには例外が無いため。
func (s *Store) ingestOneIssue(ctx context.Context, actor string, ch Channel, src SourceEntry, selfAssignees map[string]bool, resolved bool, isBound bool, issue UpstreamIssue) (item *IngestItemResult, excluded bool, err error) {
	if isBound {
		item, err := s.reconcileBoundIssue(ctx, actor, ch, src, selfAssignees, resolved, issue)
		return item, false, err
	}

	if !issueMatchesAssigneePolicy(src.Policy(), selfAssignees, issue.Assignees) {
		return nil, true, nil
	}

	created, err := s.createChallengeFromIssue(ctx, actor, ch, src, issue)
	if err != nil {
		if errors.Is(err, errSourceBindingExternalKeyTaken) {
			// ①の後に並行した別の取り込みがこの外部キーの対応を先に作った
			// （AC-60）。このトランザクションはロールバックされ課題も作られない。
			// 書き込みトランザクションは BEGIN IMMEDIATE で直列化されるので、
			// 先に作った側は既にコミット済みで、reconcileBoundIssue の③の読み直し
			// で必ず見える。「対応が既にある」分岐と同じ冪等な更新へ回す。
			item, err := s.reconcileBoundIssue(ctx, actor, ch, src, selfAssignees, resolved, issue)
			return item, false, err
		}
		return nil, false, err
	}
	return created, false, nil
}

// reconcileBoundIssue は対応が既にある Issue を、1 つの書き込みトランザクション
// の中で最新の状態に読み直してから（③）冪等に反映する（§冪等な作成と更新・
// §ポリシーに合わなくなった課題・§上流の close の検出「reopen」・§作業ログと版）。
//
// result の判定の順（仕様から導いたもの。人間記入欄と fingerprint についての
// 結果だけを表す）:
//  1. 課題が完了している: skipped_done（AC-58）。完了と版未知が重なるときは、
//     終端状態の課題へ書き込みを試みない側に倒して完了を先に見る【仮定】
//  2. 記録された fingerprint の版が 2 でない: fingerprint_unknown_version
//     （AC-57。fail-closed。fail-closed が凍結するのは人間記入欄と fingerprint
//     だけなので、下記の reopen・ポリシーの再判定はこの場合も行う）
//  3. 本文の fingerprint が記録と一致: unchanged（AC-49。上流の状態・ポリシーの
//     状態が変わっていてもこの値のまま＝§IF / API「`unchanged` は上流の状態・
//     ポリシーの状態が変わった場合も含む」）
//  4. 一致しない: updated。タイトル・説明・緊急度・fingerprint を上流の値に
//     置き換える。起票者・完了条件・優先度・状態・計画、承認・保留・不可逆操作は
//     変えない（AC-50〜55・AC-88）
//
// 完了していない課題（1・skipped_done 以外）については、result とは独立に
// 次の 2 つの契機も判定する（#58）:
//   - reopen: 上流の状態が closed／missing なら open に戻す（AC-71）
//   - ポリシーの再判定: selfAssignees の解決に成功した回（resolved）だけ行う
//     （AC-33 の既存の安全側を引き継ぐ）。ポリシーに合えば in_policy、合わなければ
//     out_of_policy にする（AC-76〜79）
//
// 起きた契機（人間記入欄の更新・上流の状態の変化・ポリシーの状態の変化）は
// applyIngestReflection が 1 つの UPDATE・1 回の版の増分・契機ごとの作業ログの
// エントリへまとめる（§作業ログと版「1 回の反映で複数の契機が同時に起きても
// 版は 1 だけ増やす」）。
func (s *Store) reconcileBoundIssue(ctx context.Context, actor string, ch Channel, src SourceEntry, selfAssignees map[string]bool, resolved bool, issue UpstreamIssue) (*IngestItemResult, error) {
	var item *IngestItemResult
	err := s.mutateAs(ctx, actor, ch, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		// ③最新の状態を読み直す。
		sb, err := loadSourceBindingByExternalKey(ctx, tx, issue.ExternalKey)
		if err != nil {
			return err
		}
		if sb == nil {
			// M2 は対応を削除する操作を持たないので、①の後に対応が消えることは
			// 無い想定。防御的にこの Issue だけを失敗させる。
			return fmt.Errorf("core: ingest: no source_binding for external_key %q", issue.ExternalKey)
		}
		cid, ok := parseChallengeID(sb.ChallengeID)
		if !ok {
			return fmt.Errorf("core: ingest: malformed challenge id %q", sb.ChallengeID)
		}
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}

		newFP := Fingerprint(issue.Body)
		var outcome IngestOutcome
		switch {
		case IsTerminal(Table, StatusVocabulary, current.Status):
			outcome = IngestOutcomeSkippedDone
		case !hasKnownFingerprintVersion(sb.Fingerprint):
			outcome = IngestOutcomeFingerprintUnknownVersion
		case newFP == sb.Fingerprint:
			outcome = IngestOutcomeUnchanged
		default:
			outcome = IngestOutcomeUpdated
		}

		newUpstreamState := sb.UpstreamState
		newPolicyState := sb.PolicyState
		if outcome != IngestOutcomeSkippedDone {
			if sb.UpstreamState == upstreamStateClosed || sb.UpstreamState == upstreamStateMissing {
				newUpstreamState = upstreamStateOpen
			}
			if resolved {
				if issueMatchesAssigneePolicy(src.Policy(), selfAssignees, issue.Assignees) {
					newPolicyState = policyStateInPolicy
				} else {
					newPolicyState = policyStateOutOfPolicy
				}
			}
		}

		plan := ingestReflection{outcome: outcome, upstream: newUpstreamState, policy: newPolicyState}
		if outcome == IngestOutcomeUpdated {
			plan.human = &humanFieldChange{
				title:       issue.Title,
				description: issue.Body,
				urgency:     computeIngestUrgency(issue.Labels, src.UrgencyLabels),
				fingerprint: newFP,
			}
		}
		item, err = applyIngestReflection(ctx, tx, rec, cid, current, sb, plan)
		return err
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

// ingestReflection は対応 1 件への反映の計画（判定を済ませた値）。判定は
// reconcileBoundIssue・confirmOpenListAbsence が行い、applyIngestReflection は
// 計画を書き込みへ写すだけにする（反映の段で上流の応答を読み直さない）。
type ingestReflection struct {
	// outcome は item の result（人間記入欄と fingerprint についての結果）。
	outcome IngestOutcome
	// human は人間記入欄と fingerprint の置き換え。nil なら変えない。
	human *humanFieldChange
	// upstream・policy は反映後の上流の状態・ポリシーの状態（変えないなら
	// 現在の値を入れる）。
	upstream upstreamState
	policy   policyState
}

// humanFieldChange は人間記入欄（タイトル・説明・緊急度）と fingerprint の
// 置き換え後の値。
type humanFieldChange struct {
	title       string
	description string
	urgency     *Urgency
	fingerprint string
}

// applyIngestReflection は plan を反映する（呼び出し元は reconcileBoundIssue と
// confirmOpenListAbsence）。人間記入欄・上流の状態・ポリシーの状態のうち実際に
// 変わるものをまとめ、変化が 1 つも無ければ何も書き込まない（§作業ログと版
// 「値が変わらなければ記録しない」）。変化があれば:
//   - challenge の UPDATE を 1 回（版を 1 だけ増やす）
//   - source_binding の更新を applySourceBindingUpdate で 1 回
//   - 変わった契機ごとに作業ログのエントリを 1 つ（順は ingest_update →
//     upstream_state_change → policy_state_change。§作業ログと版）。各エントリの
//     after.version はどれも増やした後の同じ版
func applyIngestReflection(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid int64, current *Challenge, sb *sourceBinding, plan ingestReflection) (*IngestItemResult, error) {
	humanChanged := plan.human != nil
	upstreamChanged := plan.upstream != sb.UpstreamState
	policyChanged := plan.policy != sb.PolicyState

	if !humanChanged && !upstreamChanged && !policyChanged {
		return &IngestItemResult{
			ExternalKey:   sb.ExternalKey,
			ChallengeID:   sb.ChallengeID,
			Result:        plan.outcome,
			UpstreamState: string(sb.UpstreamState),
			PolicyState:   string(sb.PolicyState),
		}, nil
	}

	newTitle, newDescription, newUrgency := current.Title, current.Description, current.Urgency
	humanBefore := map[string]any{}
	humanAfter := map[string]any{}
	if humanChanged {
		h := plan.human
		humanBefore["fingerprint"] = sb.Fingerprint
		humanAfter["fingerprint"] = h.fingerprint
		if h.title != current.Title {
			humanBefore["title"] = current.Title
			humanAfter["title"] = h.title
			newTitle = h.title
		}
		if h.description != current.Description {
			humanBefore["description"] = current.Description
			humanAfter["description"] = h.description
			newDescription = h.description
		}
		if !urgencyEqual(current.Urgency, h.urgency) {
			humanBefore["urgency"] = nullableUrgency(current.Urgency)
			humanAfter["urgency"] = nullableUrgency(h.urgency)
			newUrgency = h.urgency
		}
	}

	newVersion := current.Version + 1
	res, err := tx.ExecContext(ctx,
		`UPDATE challenge SET title = ?, description = ?, urgency = ?, version = ?, updated_at = ? WHERE id = ? AND version = ?`,
		newTitle, newDescription, nullableUrgency(newUrgency), newVersion, formatTimestamp(rec.at), cid, current.Version,
	)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, fmt.Errorf("core: ingest: update challenge %s: expected to update 1 row, updated %d", formatChallengeID(cid), affected)
	}

	sbUpdate := updateSourceBindingInput{}
	if humanChanged {
		fp := plan.human.fingerprint
		sbUpdate.Fingerprint = &fp
	}
	if upstreamChanged {
		state := plan.upstream
		sbUpdate.UpstreamState = &state
	}
	if policyChanged {
		policy := plan.policy
		sbUpdate.PolicyState = &policy
	}
	if _, _, err := applySourceBindingUpdate(ctx, tx, rec.at, cid, sbUpdate); err != nil {
		return nil, err
	}

	if humanChanged {
		humanAfter["version"] = newVersion
		if err := rec.record("challenge", cid, "ingest_update", humanBefore, humanAfter); err != nil {
			return nil, err
		}
	}
	if upstreamChanged {
		before := map[string]any{"upstream_state": string(sb.UpstreamState)}
		after := map[string]any{"upstream_state": string(plan.upstream), "version": newVersion}
		if err := rec.record("challenge", cid, "upstream_state_change", before, after); err != nil {
			return nil, err
		}
	}
	if policyChanged {
		before := map[string]any{"policy_state": string(sb.PolicyState)}
		after := map[string]any{"policy_state": string(plan.policy), "version": newVersion}
		if err := rec.record("challenge", cid, "policy_state_change", before, after); err != nil {
			return nil, err
		}
	}

	return &IngestItemResult{
		ExternalKey:   sb.ExternalKey,
		ChallengeID:   sb.ChallengeID,
		Result:        plan.outcome,
		UpstreamState: string(plan.upstream),
		PolicyState:   string(plan.policy),
	}, nil
}

// createChallengeFromIssue は 1 つの書き込みトランザクションの中で、対応が
// まだ無いことを読み直してから（③）、状態 未分類 の課題と source_binding を
// 作り、ingest_create を作業ログへ記録する（§冪等な作成と更新・§作業ログと版）。
func (s *Store) createChallengeFromIssue(ctx context.Context, actor string, ch Channel, src SourceEntry, issue UpstreamIssue) (*IngestItemResult, error) {
	urgency := computeIngestUrgency(issue.Labels, src.UrgencyLabels)
	fp := Fingerprint(issue.Body)

	var item *IngestItemResult
	err := s.mutateAs(ctx, actor, ch, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		// ③最新の状態を読み直す: ingestOneIssue の事前チェックからこのトランザ
		// クションを開くまでの間に、別のプロセスが同じ外部キーの対応を作って
		// いないかを確認する。
		existing, err := loadSourceBindingByExternalKey(ctx, tx, issue.ExternalKey)
		if err != nil {
			return err
		}
		if existing != nil {
			return errSourceBindingExternalKeyTaken
		}

		id, _, err := insertChallengeRow(ctx, tx, rec.at, issue.Title, issue.Body, "", urgency, issue.Reporter)
		if err != nil {
			return err
		}

		sb, err := insertSourceBinding(ctx, tx, rec.at, id, createSourceBindingInput{
			SourceID:      src.ID,
			ExternalKey:   issue.ExternalKey,
			URL:           issue.URL,
			Fingerprint:   fp,
			UpstreamState: upstreamStateOpen,
			PolicyState:   policyStateInPolicy,
		})
		if err != nil {
			return err
		}

		// after は課題の人間記入欄と external_key（M1 の create と同じく version
		// は載せない。§作業ログと版）。
		after := createdChallengeAfter(issue.Title, issue.Body, "", urgency, issue.Reporter)
		after["external_key"] = sb.ExternalKey
		if err := rec.record("challenge", id, "ingest_create", nil, after); err != nil {
			return err
		}

		item = &IngestItemResult{
			ExternalKey:   sb.ExternalKey,
			ChallengeID:   formatChallengeID(id),
			Result:        IngestOutcomeCreated,
			UpstreamState: string(sb.UpstreamState),
			PolicyState:   string(sb.PolicyState),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

// resolveIngestSelfAssignees は src の self_assignees を正規化した集合にする。
//   - 省略（nil）なら upstream.CurrentLogin で解決する。解決できなければ空集合と
//     resolved=false を返す（§取り込みの対象「解決に失敗したときは…」）
//   - 明示した空配列 `[]` は省略として扱わず、空集合（resolved=true）にする。
//     仕様が gh api user で補うのは「省略した」取り込み元だけで、`[]` を省略と
//     同じにすると対象を広げる向き（取り込みの安全側と逆）になるため（#53 が
//     #56 に委ねた判断）
//   - 正規化して空文字列になる要素（"" や "@"）は集合に入れない（どの login にも
//     一致しない。宣言の検証で拒否するかは #56 の範囲外とする）
func resolveIngestSelfAssignees(ctx context.Context, src SourceEntry, upstream UpstreamIssueSource) (map[string]bool, bool) {
	if src.SelfAssignees != nil {
		set := make(map[string]bool, len(src.SelfAssignees))
		for _, a := range src.SelfAssignees {
			if n := NormalizeLogin(a); n != "" {
				set[n] = true
			}
		}
		return set, true
	}
	login, err := upstream.CurrentLogin(ctx)
	if err != nil || NormalizeLogin(login) == "" {
		return map[string]bool{}, false
	}
	return map[string]bool{NormalizeLogin(login): true}, true
}

// issueMatchesAssigneePolicy は §取り込みの対象 の規則を判定する:
//   - self-only: assignee が 1 人以上いて、全員が selfAssignees に含まれる
//   - exclude-others: assignee がいない、または全員が selfAssignees に含まれる
//
// selfAssignees が解決できていない（空集合・resolved=false）ときは、
// self-only は「1 人以上」を満たせず常に false、exclude-others は「assignee が
// いない」のケースだけが true になる（現行と同じ安全側。§取り込みの対象）。
func issueMatchesAssigneePolicy(policy AssigneePolicy, selfAssignees map[string]bool, assignees []string) bool {
	allInSelf := func() bool {
		for _, a := range assignees {
			if !selfAssignees[NormalizeLogin(a)] {
				return false
			}
		}
		return true
	}
	switch policy {
	case AssigneePolicySelfOnly:
		return len(assignees) > 0 && allInSelf()
	case AssigneePolicyExcludeOthers:
		return allInSelf()
	default:
		return false
	}
}

// computeIngestUrgency は issue のラベルを urgencyLabels（宣言の完全一致の
// 対応表）と突き合わせ、緊急度を決める。一致するラベルが無い、または異なる
// 緊急度へ写るラベルが複数あれば nil（未設定。§冪等な作成と更新・QP11）。
func computeIngestUrgency(labels []string, urgencyLabels map[string]string) *Urgency {
	matched := map[Urgency]bool{}
	for _, l := range labels {
		if v, ok := urgencyLabels[l]; ok {
			matched[Urgency(v)] = true
		}
	}
	if len(matched) != 1 {
		return nil
	}
	for u := range matched {
		result := u
		return &result
	}
	return nil
}

// boundExternalKeySnapshot は①で読む対応 1 件分の目安（実際の反映は③で
// 読み直す）。
type boundExternalKeySnapshot struct {
	UpstreamState upstreamState
	// Terminal は課題が完了しているか（IsTerminal(Table, StatusVocabulary, status)）。
	Terminal bool
}

// loadBoundExternalKeys は対応のある外部キーごとの上流の状態と、課題が完了して
// いるかを読む（①。読み取り専用のトランザクションで、書き込みロックを取らない）。
// 照合は external_key だけで行う（§クリティカル設計決定 1。取り込み元の id では
// 絞らない）。
func (s *Store) loadBoundExternalKeys(ctx context.Context) (map[string]boundExternalKeySnapshot, error) {
	keys := map[string]boundExternalKeySnapshot{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT sb.external_key, sb.upstream_state, c.status
			FROM source_binding sb
			JOIN challenge c ON c.id = sb.challenge_id
		`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var key, upstream, status string
			if err := rows.Scan(&key, &upstream, &status); err != nil {
				return err
			}
			keys[key] = boundExternalKeySnapshot{
				UpstreamState: upstreamState(upstream),
				Terminal:      IsTerminal(Table, StatusVocabulary, Status(status)),
			}
		}
		return rows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return keys, nil
}

// splitExternalKey は "<owner>/<name>#<番号>" を repo（"<owner>/<name>"）と
// Issue 番号に分ける。形が壊れていれば ok=false（防御的。M2 が書く external_key は
// 常にこの形なので実運用では起きない想定）。
func splitExternalKey(externalKey string) (repo string, number int, ok bool) {
	i := strings.LastIndexByte(externalKey, '#')
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(externalKey[i+1:])
	if err != nil {
		return "", 0, false
	}
	return externalKey[:i], n, true
}

// checkUnseenOpenBindings は①で読んだ対応のうち、repo の open の一覧
// （seenIssues）に現れなかったもの（upstream_state が open・未完了）を、
// Issue 番号の昇順で 1 件ずつ確かめる（§上流の close の検出）。宣言から外した
// リポジトリ・取り込み元の対応は repo が一致しないので候補にならない。
//
// repo の一致は external_key の repo 部分と大文字小文字を無視して比べる
// （宣言の表記と API の正規の表記の違いを吸収する。upstream.go の
// ExternalKey の表記の契約を参照）。一覧に「現れたか」の判定は、一覧・1件の
// 取得のどちらも同じ adapter が同じ repo に対して返す正規の表記に揃う前提で、
// external_key の完全一致（大文字小文字を区別）で行う【仮定】。
func (s *Store) checkUnseenOpenBindings(ctx context.Context, actor string, ch Channel, repo string, bound map[string]boundExternalKeySnapshot, seenIssues []UpstreamIssue, upstream UpstreamIssueSource) ([]IngestItemResult, error) {
	seen := make(map[string]bool, len(seenIssues))
	for _, issue := range seenIssues {
		seen[issue.ExternalKey] = true
	}

	type candidate struct {
		key    string
		repo   string // external_key の repo 部分（API の正規の表記。GetIssue へそのまま渡す）
		number int
	}
	var candidates []candidate
	for key, snap := range bound {
		if snap.Terminal || snap.UpstreamState != upstreamStateOpen || seen[key] {
			continue
		}
		keyRepo, number, ok := splitExternalKey(key)
		if !ok || !strings.EqualFold(keyRepo, repo) {
			continue
		}
		candidates = append(candidates, candidate{key: key, repo: keyRepo, number: number})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].number < candidates[j].number })

	items := make([]IngestItemResult, 0, len(candidates))
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// GetIssue には external_key に記録された表記（c.repo）をそのまま渡す
		// （宣言側の表記 repo を使わない。ExternalKey の正規表記の契約と同じ
		// 理由。TestIngest_CloseDetection_RepoMatchIsCaseInsensitive が検証する）。
		item, err := s.confirmOpenListAbsence(ctx, actor, ch, c.key, c.repo, c.number, upstream)
		switch {
		case err != nil:
			msg := err.Error()
			items = append(items, IngestItemResult{ExternalKey: c.key, Result: IngestOutcomeFailed, Error: &msg})
		case item != nil:
			items = append(items, *item)
		}
	}
	return items, nil
}

// confirmOpenListAbsence は 1 件の対応について、open の一覧に現れなかった
// 理由を GetIssue で確かめる（§上流の close の検出）。GetIssue はストアの
// 書き込みロックの外で呼び（②と同じ順序）、応答を 1 つの書き込みトランザク
// ションの中で読み直してから反映する（③）。
//
//   - GetIssue が 404/410（errors.Is(err, ErrUpstreamIssueNotFound)）:
//     upstream_state を missing にする
//   - GetIssue がそれ以外で失敗: upstream_state を変えず、結果を failed にする
//   - GetIssue が成功し、応答が closed: upstream_state を closed にする
//   - GetIssue が成功し、応答が closed でない（open）: 変えない
//
// この経路では人間記入欄・fingerprint・policy_state は変えない（close の確かめは
// 上流の状態だけを見る）。読み直した時点で対応が消えている・課題が完了している・
// upstream_state が既に open でなくなっている（並行した別の反映に先を越された）
// 場合は、冪等に何もしない（item は nil のまま）。
func (s *Store) confirmOpenListAbsence(ctx context.Context, actor string, ch Channel, externalKey, repo string, number int, upstream UpstreamIssueSource) (*IngestItemResult, error) {
	issue, getErr := upstream.GetIssue(ctx, repo, number)

	var item *IngestItemResult
	err := s.mutateAs(ctx, actor, ch, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		sb, err := loadSourceBindingByExternalKey(ctx, tx, externalKey)
		if err != nil {
			return err
		}
		if sb == nil {
			return nil // 冪等: 対応が消えていれば何もしない
		}
		cid, ok := parseChallengeID(sb.ChallengeID)
		if !ok {
			return fmt.Errorf("core: ingest: malformed challenge id %q", sb.ChallengeID)
		}
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if IsTerminal(Table, StatusVocabulary, current.Status) || sb.UpstreamState != upstreamStateOpen {
			return nil // 冪等: 読み直した時点で完了している・open でなくなっている
		}

		var newState upstreamState
		switch {
		case errors.Is(getErr, ErrUpstreamIssueNotFound):
			newState = upstreamStateMissing
		case getErr != nil:
			msg := getErr.Error()
			item = &IngestItemResult{
				ExternalKey:   sb.ExternalKey,
				ChallengeID:   sb.ChallengeID,
				Result:        IngestOutcomeFailed,
				UpstreamState: string(sb.UpstreamState),
				PolicyState:   string(sb.PolicyState),
				Error:         &msg,
			}
			return nil
		case issue.State == "closed":
			newState = upstreamStateClosed
		default:
			newState = upstreamStateOpen
		}

		// 版の増分・source_binding の更新・upstream_state_change の記録は
		// reconcileBoundIssue と同じ applyIngestReflection に任せる。この経路は
		// 人間記入欄・fingerprint・policy_state を変えないので human は nil・policy は
		// 現状のまま、result は unchanged とする【仮定】（変化が無ければ何も書き込ま
		// ない）。
		item, err = applyIngestReflection(ctx, tx, rec, cid, current, sb, ingestReflection{
			outcome:  IngestOutcomeUnchanged,
			upstream: newState,
			policy:   sb.PolicyState,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// --- 取り込み（ingest）の公開 API と結果の型 ---
//
// docs/features/m2-github-issue-ingest.md §機能全体の設計「アーキテクチャ決定」:
// core の取り込みは、①ストアを読む（書き込みロックなし）→②取得する（書き込み
// ロックなし）→③Issue ごとに書き込みトランザクション（BEGIN IMMEDIATE）の中で
// 最新の状態を読み直して反映する、の順に進む。
//
// この骨格は #56 が作った。#56 が実装したのは「対応の無い、ポリシーに合う
// Issue から課題を作る」（ingest_create）だけで、#57（本チケット）が
// ingestOneIssue の「対応が既にある」分岐と、並行した取り込みに先を越された
// 分岐に、冪等な更新（reconcileBoundIssue。fingerprint の 3 分岐・完了した課題の
// スキップ・ingest_update）を足した。次は後続チケットがこの同じ骨格に足す:
//   - #58: open の一覧に現れた Issue についてのポリシーの状態の再判定
//     （policy_state_change。resolved のときだけ＝AC-33）と reopen
//     （upstream_state_change）は、reconcileBoundIssue の 1 つの書き込み
//     トランザクションの中に足す（1 Issue の反映は 1 トランザクション・版は
//     1 回だけ増やす＝§作業ログと版）。selfAssignees・resolved はそのとき
//     reconcileBoundIssue へ通す。open の一覧に現れなかった対応の close の
//     確かめは、Ingest のリポジトリのループの分岐点（下記）に足す
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

	// ①ストアを読む（書き込みロックなし）: 対応のある外部キーの集合。ここで
	// 読んだ値は②の後の判定の目安にだけ使い、実際の反映は③で読み直す。
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
				item, excluded, err := s.ingestOneIssue(ctx, actor, ch, src, selfAssignees, bound[issue.ExternalKey], issue)
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
			// #58 の分岐点: 一覧の取得に成功したこのリポジトリで、①で読んだ
			// 対応のうち open の一覧に現れなかったもの（upstream_state が open・
			// 未完了）を GetIssue で 1 件ずつ確かめる。対応をリポジトリで選ぶ
			// ときは、宣言の表記（repo）と external_key の表記（API の正規の
			// 表記）の違いを考えて大文字小文字を無視する。#56 では何もしない。
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
func (s *Store) ingestOneIssue(ctx context.Context, actor string, ch Channel, src SourceEntry, selfAssignees map[string]bool, isBound bool, issue UpstreamIssue) (item *IngestItemResult, excluded bool, err error) {
	if isBound {
		item, err := s.reconcileBoundIssue(ctx, actor, ch, src, issue)
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
			item, err := s.reconcileBoundIssue(ctx, actor, ch, src, issue)
			return item, false, err
		}
		return nil, false, err
	}
	return created, false, nil
}

// reconcileBoundIssue は対応が既にある Issue を、1 つの書き込みトランザクション
// の中で最新の状態に読み直してから（③）冪等に反映する（§冪等な作成と更新・
// §作業ログと版）。result の判定の順（仕様から導いたもの）:
//  1. 課題が完了している: skipped_done（AC-58）。完了と版未知が重なるときは、
//     終端状態の課題へ書き込みを試みない側に倒して完了を先に見る【仮定】
//  2. 記録された fingerprint の版が 2 でない: fingerprint_unknown_version
//     （AC-57。fail-closed）
//  3. 本文の fingerprint が記録と一致: unchanged（AC-49）
//  4. 一致しない: updated。タイトル・説明・緊急度・fingerprint を上流の値に
//     置き換え、ingest_update を記録し、課題の版を 1 増やす。起票者・完了条件・
//     優先度・状態・計画、承認・保留・不可逆操作は変えない（AC-50〜55・AC-88）
//
// 判定（result を決める）と反映（書き込み）を分けてあり、反映は result が
// updated のときだけ行う。#58（ポリシーの再判定・reopen）や fingerprint 以外の
// source_binding の属性の反映は、result が unchanged でも起こりうるので、判定の
// 後・反映の段に契機として足し、複数の契機が起きても版は 1 回だけ増やす
// （§作業ログと版）。upstream_state・policy_state は今は読み直した値を結果に
// 載せるだけで変えない。
func (s *Store) reconcileBoundIssue(ctx context.Context, actor string, ch Channel, src SourceEntry, issue UpstreamIssue) (*IngestItemResult, error) {
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

		if outcome == IngestOutcomeUpdated {
			if err := applyIngestUpdate(ctx, tx, rec, cid, current, sb.Fingerprint, newFP, issue, computeIngestUrgency(issue.Labels, src.UrgencyLabels)); err != nil {
				return err
			}
		}

		item = &IngestItemResult{
			ExternalKey:   sb.ExternalKey,
			ChallengeID:   sb.ChallengeID,
			Result:        outcome,
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

// applyIngestUpdate は課題の人間記入欄（タイトル・説明・緊急度）と対応の
// fingerprint を上流の値に置き換え、ingest_update を記録し、課題の版を 1 増やす。
// before/after には変わった項目と fingerprint だけを載せ、after にだけ version を
// 載せる（M1 の H14・H16）。
func applyIngestUpdate(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid int64, current *Challenge, oldFP, newFP string, issue UpstreamIssue, urgency *Urgency) error {
	before := map[string]any{"fingerprint": oldFP}
	after := map[string]any{"fingerprint": newFP}
	newTitle := current.Title
	if issue.Title != current.Title {
		before["title"] = current.Title
		after["title"] = issue.Title
		newTitle = issue.Title
	}
	newDescription := current.Description
	if issue.Body != current.Description {
		before["description"] = current.Description
		after["description"] = issue.Body
		newDescription = issue.Body
	}
	newUrgency := current.Urgency
	if !urgencyEqual(current.Urgency, urgency) {
		before["urgency"] = nullableUrgency(current.Urgency)
		after["urgency"] = nullableUrgency(urgency)
		newUrgency = urgency
	}

	newVersion := current.Version + 1
	res, err := tx.ExecContext(ctx,
		`UPDATE challenge SET title = ?, description = ?, urgency = ?, version = ?, updated_at = ? WHERE id = ? AND version = ?`,
		newTitle, newDescription, nullableUrgency(newUrgency), newVersion, formatTimestamp(rec.at), cid, current.Version,
	)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("core: ingest: update challenge %s: expected to update 1 row, updated %d", formatChallengeID(cid), affected)
	}

	fp := newFP
	if _, _, err := applySourceBindingUpdate(ctx, tx, rec.at, cid, updateSourceBindingInput{Fingerprint: &fp}); err != nil {
		return err
	}

	after["version"] = newVersion
	return rec.record("challenge", cid, "ingest_update", before, after)
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

// loadBoundExternalKeys は対応のある外部キーの集合を読む（①。読み取り専用の
// トランザクションで、書き込みロックを取らない）。照合は external_key だけで
// 行う（§クリティカル設計決定 1。取り込み元の id では絞らない）。
func (s *Store) loadBoundExternalKeys(ctx context.Context) (map[string]bool, error) {
	keys := map[string]bool{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT external_key FROM source_binding`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			keys[k] = true
		}
		return rows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return keys, nil
}

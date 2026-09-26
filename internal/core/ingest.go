package core

import (
	"context"
	"database/sql"
	"errors"
)

// --- 取り込み（ingest）の公開 API と結果の型 ---
//
// docs/features/m2-github-issue-ingest.md §機能全体の設計「アーキテクチャ決定」:
// core の取り込みは、①ストアを読む（書き込みロックなし）→②取得する（書き込み
// ロックなし）→③Issue ごとに書き込みトランザクション（BEGIN IMMEDIATE）の中で
// 最新の状態を読み直して反映する、の順に進む。
//
// この骨格は #56（本チケット）が作る。#56 が実装するのは「対応の無い、
// ポリシーに合う Issue から課題を作る」（ingest_create）だけで、次は後続
// チケットがこの同じ骨格に足す:
//   - #57: 冪等な更新（fingerprint の 3 分岐・ingest_update）。ingestOneIssue の
//     「対応が既にある」分岐と、並行した取り込みに先を越された分岐に足す
//   - #58: ポリシーの状態の再判定（policy_state_change）は ingestOneIssue の
//     「対応が既にある」分岐（resolved のときだけ）に、上流の close の確かめ・
//     食い違い（discrepancies）は Ingest のリポジトリのループの「一覧に現れ
//     なかった対応」の分岐点に足す
//
// --source の指定（宣言のうちどの取り込み元を処理するか。省略時は全件・
// 空文字列との区別）は #63（core.SelectSources）の責務。Ingest は「選ばれた
// 取り込み元」の一覧を受け取るだけにする（親要件チケット #56 の完了条件にある
// 仮定）。

// IngestOutcome は ingest の JSON の要素の `result` の値
// （docs/features/m2-github-issue-ingest.md §IF / API `ingest` の JSON 出力）。
// 本チケット（#56）が生成するのは IngestOutcomeCreated・IngestOutcomeFailed
// だけ。残りは #57（冪等な更新の 3 分岐）・#58（close の確かめ）が生成する。
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
				item, excluded, err := s.ingestOneIssue(ctx, actor, ch, src, selfAssignees, resolved, bound[issue.ExternalKey], issue)
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
				// item == nil && !excluded && err == nil: 対応が既にある Issue
				// （#57/#58 の分岐点。#56 では何もしない）、または並行した別の
				// ingest が先に対応を作った（下記 ingestOneIssue のコメント）。
				// どちらも今回の結果には何も足さない。
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
//   - 対応が既にあれば、何もしない（#57/#58 の分岐点。§取り込みの対象「対応の
//     ある課題のポリシーの再判定をしない」とも整合する）
//   - 対応が無く、ポリシーに合わなければ excluded=true
//   - 対応が無く、ポリシーに合えば課題と対応を作り、item を返す
//
// 対応の有無（isBound）は①で読んだ値。実際の作成は createChallengeFromIssue
// が 1 つの書き込みトランザクションの中で読み直してから行う（③）。
func (s *Store) ingestOneIssue(ctx context.Context, actor string, ch Channel, src SourceEntry, selfAssignees map[string]bool, resolved, isBound bool, issue UpstreamIssue) (item *IngestItemResult, excluded bool, err error) {
	if isBound {
		if !resolved {
			// self_assignees を解決できなかった取り込み元では、対応のある課題の
			// ポリシーの再判定をしない（AC-33。§ポリシーに合わなくなった課題 の
			// 規則より優先する）。#58 がポリシーの状態の判定を足すのは、この
			// 分岐の外（resolved のとき）だけにする。
			return nil, false, nil
		}
		// #57（冪等な更新）・#58（close の確かめ・ポリシーの状態）の分岐点。
		// #56 では何もしない。
		return nil, false, nil
	}

	if !issueMatchesAssigneePolicy(src.Policy(), selfAssignees, issue.Assignees) {
		return nil, true, nil
	}

	created, err := s.createChallengeFromIssue(ctx, actor, ch, src, issue)
	if err != nil {
		if errors.Is(err, errSourceBindingExternalKeyTaken) {
			// ①の後に並行した別の取り込みがこの外部キーの対応を先に作った。
			// このトランザクションはロールバックされ課題も作られない（重複を
			// 作らない）。#57 の分岐点: ここは「対応が既にある」分岐と同じ扱い
			// （③で読み直した対応に対する冪等な更新）へ回す。#56 では対応の
			// ある Issue に何もしないので、結果にも何も足さない。
			return nil, false, nil
		}
		return nil, false, err
	}
	return created, false, nil
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

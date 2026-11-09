package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// このファイルは bind・unbind（取り込み元の Issue の無い課題を、あとから GitHub Issue に
// 紐づける・外す。docs/features/m2-github-issue-ingest.md §課題と Issue の紐づけ）を持つ。
//
// どちらも承認を要さず、本人確認も求めない（`dep add`・`edit` と同じ VerificationNone）。課題の
// 状態・origin は見ない・変えない。bind の上流の取得（GetIssue）は書き込みトランザクションの外で
// 行い、トランザクションの中で課題の対応の有無と Issue の対応先を読み直す（取り込みの②③と同じ）。

// BindInput は Store.BindChallenge の入力。
type BindInput struct {
	// ChallengeID は紐づける課題の ID（"C-<n>"）。
	ChallengeID string
	// ExternalKey は紐づける Issue の "<owner>/<name>#<番号>"（利用者が書いた表記。
	// repo の大文字小文字は宣言との照合で区別しない）。
	ExternalKey string
	// Sources は宣言（sources.json。LoadSourcesDeclaration の戻り値）の取り込み元のすべて。
	// Issue のリポジトリがどれの repos にも含まれなければ ErrRepoNotDeclared。
	Sources []SourceEntry
	// Upstream は上流（GitHub）の取得の実装（本番は internal/adapters/github、テストは偽の実装）。
	Upstream UpstreamIssueSource
}

// parseBindExternalKey は "<owner>/<name>#<番号>" を repo と番号に分ける。repo は owner と name の
// 2 要素（空白を含まない）、番号は先頭に 0・符号の無い正の整数だけを受け付ける。
func parseBindExternalKey(key string) (repo string, number int, ok bool) {
	repo, number, ok = parseExternalKey(key)
	if !ok || strconv.Itoa(number) != key[strings.LastIndex(key, "#")+1:] {
		return "", 0, false
	}
	owner, name, found := strings.Cut(repo, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") || strings.ContainsAny(repo, " \t\r\n") {
		return "", 0, false
	}
	return repo, number, true
}

// findDeclaredSource は repo（大文字小文字を区別しない）を repos に含む最初の取り込み元と、
// 宣言の表記の repo を返す。
func findDeclaredSource(sources []SourceEntry, repo string) (src SourceEntry, declaredRepo string, ok bool) {
	for _, s := range sources {
		for _, r := range s.Repos {
			if strings.EqualFold(r, repo) {
				return s, r, true
			}
		}
	}
	return SourceEntry{}, "", false
}

// bumpChallengeVersion は current の課題の版を 1 上げる（楽観ロック。updated_at は at）。
func bumpChallengeVersion(ctx context.Context, tx *sql.Tx, at string, cid int64, current *Challenge) (newVersion int, err error) {
	newVersion = current.Version + 1
	res, err := tx.ExecContext(ctx,
		`UPDATE challenge SET version = ?, updated_at = ? WHERE id = ? AND version = ?`,
		newVersion, at, cid, current.Version)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return 0, fmt.Errorf("core: bump version of %s: expected to update 1 row, updated %d", formatChallengeID(cid), affected)
	}
	return newVersion, nil
}

// BindChallenge は in.ChallengeID の課題を、上流の Issue in.ExternalKey に紐づける。次の 4 条件の
// どれか 1 つでも満たさなければ、理由付きのエラーで拒み、何も記録しない:
//  1. 課題に対応が無い（あれば ErrAlreadyBound）
//  2. Issue のリポジトリが宣言のいずれかの取り込み元の repos に含まれる（大文字小文字を区別しない。
//     含まれなければ ErrRepoNotDeclared）
//  3. Issue が open（上流から取得する。open でなければ ErrIssueNotOpen。取得で 404・410 なら
//     ErrUpstreamIssueNotFound・移管なら ErrUpstreamIssueTransferred〔どちらも ErrValidation も
//     真〕、それ以外の失敗は ErrUpstreamFetchFailed）
//  4. その Issue がほかの課題に対応していない（あれば ErrIssueBoundElsewhere）。external_key は
//     上流の応答の正規の表記で照合する
//
// 課題 ID の形式が不正・課題が無ければ ErrNotFound、ExternalKey の形が不正なら ErrValidation。
// 課題の状態・origin は制約しない・変えない。
//
// 通れば、対応の作成・作業ログ（action=bind。before に紐づける前の見出しと説明、after に
// external_key・source_id・url・版）・課題の版の +1 を 1 つの書き込みトランザクションで記録する。
// 課題の見出し・説明はこの時点では置き換えず、次の取り込みに任せる。そのため対応の fingerprint は
// boundFingerprint にする（次の取り込みが必ず updated になる）。対応の他の値は取り込みの作成と同じ
// （上流の状態 open・ポリシーの状態 in_policy・観測値は取得した Issue の値・読んだ時点のコメント数 0）で、
// ポリシーの状態は次の取り込みが self_assignees を解決できた回に再判定される。
func (s *Store) BindChallenge(ctx context.Context, ch Channel, in BindInput) (*Challenge, error) {
	cid, ok := parseChallengeID(in.ChallengeID)
	if !ok {
		return nil, ErrNotFound
	}
	repo, number, ok := parseBindExternalKey(in.ExternalKey)
	if !ok {
		return nil, fmt.Errorf("%w: external key %q must be <owner>/<name>#<number>", ErrValidation, in.ExternalKey)
	}

	// ①を先に確かめる（対応のある課題のために上流へ接続しない）。課題の有無も同じ読み取りで確かめる。
	if err := s.checkBindable(ctx, cid); err != nil {
		return nil, err
	}

	// ②宣言の取り込み元（上流へ接続しない）。
	src, declaredRepo, ok := findDeclaredSource(in.Sources, repo)
	if !ok {
		return nil, fmt.Errorf("%w: %s is not in the repos of any declared source", ErrRepoNotDeclared, repo)
	}

	// ③上流から取得する（書き込みトランザクションの外）。
	issue, err := in.Upstream.GetIssue(ctx, declaredRepo, number)
	switch {
	case errors.Is(err, ErrUpstreamIssueNotFound):
		return nil, fmt.Errorf("%w: %s#%d: %w", ErrValidation, declaredRepo, number, err)
	case errors.Is(err, ErrUpstreamIssueTransferred):
		return nil, fmt.Errorf("%w: %s#%d: %w", ErrValidation, declaredRepo, number, err)
	case err != nil:
		return nil, fmt.Errorf("%w: %s#%d: %w", ErrUpstreamFetchFailed, declaredRepo, number, err)
	}
	issueRepo, _, keyOK := splitExternalKey(issue.ExternalKey)
	if !keyOK {
		return nil, fmt.Errorf("%w: %s#%d: the response has no usable external key (%q)", ErrUpstreamFetchFailed, declaredRepo, number, issue.ExternalKey)
	}
	if !strings.EqualFold(issueRepo, declaredRepo) {
		return nil, fmt.Errorf("%w: %s#%d: the response is for %s: %w", ErrValidation, declaredRepo, number, issue.ExternalKey, ErrUpstreamIssueTransferred)
	}
	if issue.State != "open" {
		return nil, fmt.Errorf("%w: %s is %s", ErrIssueNotOpen, issue.ExternalKey, issue.State)
	}

	var result *Challenge
	err = s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		// 取得の間に変わりうる①④を、書き込みトランザクションの中で読み直す。
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if existing, err := loadSourceBindingByChallengeID(ctx, tx, cid); err != nil {
			return err
		} else if existing != nil {
			return fmt.Errorf("%w: %s is bound to %s", ErrAlreadyBound, current.ID, existing.ExternalKey)
		}
		if holder, err := loadSourceBindingByExternalKey(ctx, tx, issue.ExternalKey); err != nil {
			return err
		} else if holder != nil {
			return fmt.Errorf("%w: %s is bound to %s", ErrIssueBoundElsewhere, issue.ExternalKey, holder.ChallengeID)
		}

		sb, err := insertSourceBinding(ctx, tx, rec.at, cid, createSourceBindingInput{
			SourceID:              src.ID,
			ExternalKey:           issue.ExternalKey,
			URL:                   issue.URL,
			Fingerprint:           boundFingerprint,
			UpstreamState:         upstreamStateOpen,
			PolicyState:           policyStateInPolicy,
			CommentsCount:         issue.Comments,
			UpstreamUpdatedAt:     issue.UpdatedAt,
			ReadCommentsCount:     0,
			ReadUpstreamUpdatedAt: issue.UpdatedAt,
		})
		switch {
		case errors.Is(err, errSourceBindingExists):
			return fmt.Errorf("%w: %s already has a source binding", ErrAlreadyBound, current.ID)
		case errors.Is(err, errSourceBindingExternalKeyTaken):
			return fmt.Errorf("%w: %s", ErrIssueBoundElsewhere, issue.ExternalKey)
		case err != nil:
			return err
		}

		nowStr := formatTimestamp(rec.at)
		now, err := parseTimestamp(nowStr)
		if err != nil {
			return err
		}
		newVersion, err := bumpChallengeVersion(ctx, tx, nowStr, cid, current)
		if err != nil {
			return err
		}
		before := map[string]any{"title": current.Title, "description": current.Description}
		after := map[string]any{"external_key": sb.ExternalKey, "source_id": sb.SourceID, "url": sb.URL, "version": newVersion}
		if err := rec.record("challenge", cid, "bind", before, after); err != nil {
			return err
		}

		updated := *current
		updated.Version = newVersion
		updated.UpdatedAt = now
		result = &updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// checkBindable は cid の課題があり、対応が無いことを読み取りで確かめる（bind の①）。
func (s *Store) checkBindable(ctx context.Context, cid int64) error {
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		existing, err := loadSourceBindingByChallengeID(ctx, tx, cid)
		if err != nil {
			return err
		}
		if existing != nil {
			return fmt.Errorf("%w: %s is bound to %s", ErrAlreadyBound, current.ID, existing.ExternalKey)
		}
		return nil
	})
	return classifyReadWriteErr(err)
}

// UnbindChallenge は id の課題と上流の Issue の対応を外す。対応の無い課題は ErrNotBound で拒む
// （何も書かない）。課題 ID の形式が不正・課題が無ければ ErrNotFound。課題の状態は制約しない。
//
// 対応の削除・作業ログ（action=unbind。before に外した対応の external_key・source_id・url・
// fingerprint・上流の状態・ポリシーの状態、after に版）・課題の版の +1 を 1 つの書き込み
// トランザクションで記録する。課題の見出し・説明・親子・依存は変えない。外した後の取り込みは、その
// Issue を「対応の無い Issue」として扱い、宣言のポリシーに合えば新しい課題を作る（元の課題とは別）。
func (s *Store) UnbindChallenge(ctx context.Context, ch Channel, id string) (*Challenge, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var result *Challenge
	err := s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		sb, err := loadSourceBindingByChallengeID(ctx, tx, cid)
		if err != nil {
			return err
		}
		if sb == nil {
			return fmt.Errorf("%w: %s", ErrNotBound, current.ID)
		}
		if err := deleteSourceBinding(ctx, tx, cid); err != nil {
			return err
		}

		nowStr := formatTimestamp(rec.at)
		now, err := parseTimestamp(nowStr)
		if err != nil {
			return err
		}
		newVersion, err := bumpChallengeVersion(ctx, tx, nowStr, cid, current)
		if err != nil {
			return err
		}
		before := map[string]any{
			"external_key":   sb.ExternalKey,
			"source_id":      sb.SourceID,
			"url":            sb.URL,
			"fingerprint":    sb.Fingerprint,
			"upstream_state": string(sb.UpstreamState),
			"policy_state":   string(sb.PolicyState),
		}
		if err := rec.record("challenge", cid, "unbind", before, map[string]any{"version": newVersion}); err != nil {
			return err
		}

		updated := *current
		updated.Version = newVersion
		updated.UpdatedAt = now
		result = &updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

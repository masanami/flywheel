// このファイルは source_binding（課題と上流の GitHub Issue の対応）の読み書きを
// 持つ。マイグレーション 0002・データモデルは親要件チケット #51
// （docs/features/m2-github-issue-ingest.md）§クリティカル設計決定 1 の採用案に
// 従う: 課題 1 つに対応は高々 1 つ（challenge_id が主キー）、照合は external_key
// だけで行う（一意性も external_key 単独。source_id は由来の記録）、
// last_synced_at は持たない。
//
// ここに置くのは #52 の完了条件の「内部 API」（課題 ID での取得・外部キーでの
// 取得・作成・更新）で、識別子はすべて非公開にしている。#56 は読み書きの
// API と内部の型（sourceBinding・upstreamState・policyState）を非公開のまま
// 残し、show のための読み取り専用の公開の形 SourceBinding だけを足した（書き
// 込みは core の取り込み〔Ingest〕だけが行う）。作成・更新は tx を受け取る関数として置き、作業ログを書かない:
// 対応の変化は QP6 に従って、呼び出し側（#56〜#58）が mutate の同じトランザク
// ションの中で課題の作成・更新・作業ログ・版の +1 と合わせて扱う（QH1 は
// source_binding を作業ログ・版の対象外にする案 D を退けている）。

package core

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

var (
	// errSourceBindingExternalKeyTaken は、external_key が既にストアの別の対応で
	// 使われていることを表す（external_key の一意制約。AC-104）。
	errSourceBindingExternalKeyTaken = errors.New("core: external_key already has a source_binding")

	// errSourceBindingExists は、課題に既に対応があることを表す（課題 1 つに
	// 対応は高々 1 つ。challenge_id が主キー）。
	errSourceBindingExists = errors.New("core: challenge already has a source_binding")
)

// upstreamState は対応する上流（GitHub Issue）の状態（source_binding.upstream_state）。
// 0001 の status 等と同じく、スキーマに CHECK 制約は付けず core 層
// （validateSourceBindingStates）で閉集合を検証する。
type upstreamState string

const (
	upstreamStateOpen    upstreamState = "open"
	upstreamStateClosed  upstreamState = "closed"
	upstreamStateMissing upstreamState = "missing"
)

func (v upstreamState) valid() bool {
	switch v {
	case upstreamStateOpen, upstreamStateClosed, upstreamStateMissing:
		return true
	}
	return false
}

// policyState は対応が取り込み元のポリシーに合っているかの状態
// （source_binding.policy_state）。
type policyState string

const (
	policyStateInPolicy    policyState = "in_policy"
	policyStateOutOfPolicy policyState = "out_of_policy"
)

func (v policyState) valid() bool {
	switch v {
	case policyStateInPolicy, policyStateOutOfPolicy:
		return true
	}
	return false
}

// sourceBinding は課題と上流の GitHub Issue の対応（§データモデル source_binding）。
type sourceBinding struct {
	ChallengeID   string // "C-<n>"
	SourceID      string
	ExternalKey   string // "<owner>/<name>#<番号>"
	URL           string
	Fingerprint   string // "<版>:<値>"
	UpstreamState upstreamState
	PolicyState   policyState
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const sourceBindingSelectColumns = `SELECT challenge_id, source_id, external_key, url, fingerprint, upstream_state, policy_state, created_at, updated_at FROM source_binding`

// loadSourceBindingWhere は cond（"challenge_id = ?" 等）に合う対応を tx から
// 読む。対応が無ければ (nil, nil)（課題と対応は 1 対 0/1 の任意の関係なので、
// 「無い」をエラーにしない）。
func loadSourceBindingWhere(ctx context.Context, tx *sql.Tx, cond string, arg any) (*sourceBinding, error) {
	var (
		challengeID                                               int64
		sourceID, externalKey, url, fingerprint, upstream, policy string
		createdAtStr, updatedAtStr                                string
	)
	err := tx.QueryRowContext(ctx, sourceBindingSelectColumns+" WHERE "+cond, arg).
		Scan(&challengeID, &sourceID, &externalKey, &url, &fingerprint, &upstream, &policy, &createdAtStr, &updatedAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	createdAt, err := parseTimestamp(createdAtStr)
	if err != nil {
		return nil, err
	}
	updatedAt, err := parseTimestamp(updatedAtStr)
	if err != nil {
		return nil, err
	}
	return &sourceBinding{
		ChallengeID:   formatChallengeID(challengeID),
		SourceID:      sourceID,
		ExternalKey:   externalKey,
		URL:           url,
		Fingerprint:   fingerprint,
		UpstreamState: upstreamState(upstream),
		PolicyState:   policyState(policy),
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
	}, nil
}

// loadSourceBindingByChallengeID は challengeID（内部整数 ID）の課題の対応を
// tx から読む。対応が無ければ (nil, nil)。
func loadSourceBindingByChallengeID(ctx context.Context, tx *sql.Tx, challengeID int64) (*sourceBinding, error) {
	return loadSourceBindingWhere(ctx, tx, "challenge_id = ?", challengeID)
}

// loadSourceBindingByExternalKey は external_key で対応を tx から読む（照合は
// external_key だけで行う。§クリティカル設計決定 1）。external_key は加工せず
// バイト列の一致で照合する（表記の正規化は呼び出し側の責務）。対応が無ければ
// (nil, nil)。
func loadSourceBindingByExternalKey(ctx context.Context, tx *sql.Tx, externalKey string) (*sourceBinding, error) {
	return loadSourceBindingWhere(ctx, tx, "external_key = ?", externalKey)
}

// createSourceBindingInput は insertSourceBinding の入力。
type createSourceBindingInput struct {
	SourceID      string
	ExternalKey   string
	URL           string
	Fingerprint   string
	UpstreamState upstreamState
	PolicyState   policyState
}

// insertSourceBinding は challengeID の課題に新しい対応を作る。呼び出し側は
// 課題が同じトランザクションの中に存在することを確かめておく（存在しなければ
// 外部キー違反の生のエラーが返る）。
//
//   - 必須の文字列が空・状態が閉集合の外なら ErrValidation
//   - 課題に既に対応があれば errSourceBindingExists
//   - external_key が既に別の対応で使われていれば errSourceBindingExternalKeyTaken。
//     重複はストアの一意制約が拒否し（AC-104）、ここではその違反を翻訳するだけで
//     事前の読み取りでは判定しない
//
// created_at・updated_at には at を使う。作業ログは書かない（ファイル冒頭の注記）。
func insertSourceBinding(ctx context.Context, tx *sql.Tx, at time.Time, challengeID int64, in createSourceBindingInput) (*sourceBinding, error) {
	if in.SourceID == "" || in.ExternalKey == "" || in.URL == "" || in.Fingerprint == "" ||
		!in.UpstreamState.valid() || !in.PolicyState.valid() {
		return nil, ErrValidation
	}
	existing, err := loadSourceBindingByChallengeID(ctx, tx, challengeID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, errSourceBindingExists
	}

	nowStr := formatTimestamp(at)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO source_binding (challenge_id, source_id, external_key, url, fingerprint, upstream_state, policy_state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		challengeID, in.SourceID, in.ExternalKey, in.URL, in.Fingerprint, string(in.UpstreamState), string(in.PolicyState), nowStr, nowStr,
	)
	if store.IsUniqueViolation(err) {
		return nil, errSourceBindingExternalKeyTaken
	}
	if err != nil {
		return nil, err
	}
	return loadSourceBindingByChallengeID(ctx, tx, challengeID)
}

// updateSourceBindingInput は applySourceBindingUpdate の入力。nil のフィールドは
// 「指定なし（変更しない）」を表す（EditInput と同じ規則）。source_id・
// external_key は対応の同一性なので更新の対象にしない。
type updateSourceBindingInput struct {
	URL           *string
	Fingerprint   *string
	UpstreamState *upstreamState
	PolicyState   *policyState
}

// applySourceBindingUpdate は challengeID の対応を部分更新し、変更前と変更後を
// 返す（呼び出し側が作業ログの before／after と版を増やすかを決めるため）。
// 対応が無ければ (nil, nil, nil)。指定された値が空・閉集合の外なら
// ErrValidation。値が変わらなければ書き込まず、updated_at も変えずに
// before と同じ値を after として返す（EditChallenge と同じ「値が変わらなければ
// 書かない」規則）。作業ログは書かない（ファイル冒頭の注記）。
func applySourceBindingUpdate(ctx context.Context, tx *sql.Tx, at time.Time, challengeID int64, in updateSourceBindingInput) (before, after *sourceBinding, err error) {
	if (in.URL != nil && *in.URL == "") || (in.Fingerprint != nil && *in.Fingerprint == "") ||
		(in.UpstreamState != nil && !in.UpstreamState.valid()) || (in.PolicyState != nil && !in.PolicyState.valid()) {
		return nil, nil, ErrValidation
	}
	before, err = loadSourceBindingByChallengeID(ctx, tx, challengeID)
	if err != nil || before == nil {
		return nil, nil, err
	}

	next := *before
	if in.URL != nil {
		next.URL = *in.URL
	}
	if in.Fingerprint != nil {
		next.Fingerprint = *in.Fingerprint
	}
	if in.UpstreamState != nil {
		next.UpstreamState = *in.UpstreamState
	}
	if in.PolicyState != nil {
		next.PolicyState = *in.PolicyState
	}
	if next == *before {
		return before, before, nil
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE source_binding SET url = ?, fingerprint = ?, upstream_state = ?, policy_state = ?, updated_at = ? WHERE challenge_id = ?`,
		next.URL, next.Fingerprint, string(next.UpstreamState), string(next.PolicyState), formatTimestamp(at), challengeID,
	)
	if err != nil {
		return nil, nil, err
	}
	after, err = loadSourceBindingByChallengeID(ctx, tx, challengeID)
	if err != nil {
		return nil, nil, err
	}
	return before, after, nil
}

// getSourceBindingByChallengeID は id の課題の対応を返す（対応が無ければ
// nil, nil）。id の形式が不正・課題が存在しなければ ErrNotFound（「課題が無い」と
// 「対応が無い」を区別する）。
func (s *Store) getSourceBindingByChallengeID(ctx context.Context, id string) (*sourceBinding, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var result *sourceBinding
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		if _, err := loadChallenge(ctx, tx, cid); err != nil {
			return err
		}
		sb, err := loadSourceBindingByChallengeID(ctx, tx, cid)
		result = sb
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return result, nil
}

// getSourceBindingByExternalKey は externalKey の対応を返す（対応が無ければ
// nil, nil）。
func (s *Store) getSourceBindingByExternalKey(ctx context.Context, externalKey string) (*sourceBinding, error) {
	var result *sourceBinding
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		sb, err := loadSourceBindingByExternalKey(ctx, tx, externalKey)
		result = sb
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return result, nil
}

// SourceBinding は課題の source_binding の公開の形で、GetChallenge が
// internal/cli の `show` のために返す（#56。docs/features/m2-github-issue-ingest.md
// §IF / API「`show` の拡張」）。対応の無い課題（`create` で作った課題・スキーマ版 1
// から上げたストアの既存の課題）は GetChallenge が nil を返す（AC-48・AC-103）。
type SourceBinding struct {
	SourceID      string
	ExternalKey   string
	URL           string
	Fingerprint   string
	UpstreamState string // "open" | "closed" | "missing"
	PolicyState   string // "in_policy" | "out_of_policy"
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// toPublic は内部の sourceBinding 行を公開の SourceBinding へ変換する。
// b が nil なら nil を返す（対応が無いことをそのまま伝える）。
func (b *sourceBinding) toPublic() *SourceBinding {
	if b == nil {
		return nil
	}
	return &SourceBinding{
		SourceID:      b.SourceID,
		ExternalKey:   b.ExternalKey,
		URL:           b.URL,
		Fingerprint:   b.Fingerprint,
		UpstreamState: string(b.UpstreamState),
		PolicyState:   string(b.PolicyState),
		CreatedAt:     b.CreatedAt,
		UpdatedAt:     b.UpdatedAt,
	}
}

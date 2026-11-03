package core

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
)

// このファイルは取り込みで、上流の sub-issue の関係を課題の親子（challenge_parent）へ写す
// （docs/features/ui-relation-graph.md §取り込み）。
//
// 写すのは、その回の open の一覧に見えた Issue（一覧の取得に成功したリポジトリのもの）だけ。
// 子の課題の親は「親の外部キーの対応のある課題（無ければ親なし）」に合わせる。一覧の取得に失敗した
// リポジトリ・その回に見えなかった Issue の課題は変えない。親が後のリポジトリで処理される順でも
// 結果が同じになるよう、全リポジトリの処理の後でまとめて合わせる。比較は大文字小文字を区別しない
// 外部キーで行う。輪になる親は付けず、取り込みは続ける。変わらなければ何も書かない。

// observedParents は見えた Issue の外部キー（小文字化）から、その親の外部キー（小文字化。親なしは ""）への写し。
type observedParents map[string]string

func (o observedParents) observe(issue UpstreamIssue) {
	o[strings.ToLower(issue.ExternalKey)] = strings.ToLower(issue.ParentExternalKey)
}

// syncIngestParents は observed を課題の親子へ写す。1 つの書き込みトランザクションで行う。
func (s *Store) syncIngestParents(ctx context.Context, actor string, ch Channel, observed observedParents) error {
	if len(observed) == 0 {
		return nil
	}
	return s.mutateAs(ctx, actor, ch, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		byKey, err := loadChallengeIDsByLowerExternalKey(ctx, tx)
		if err != nil {
			return err
		}
		type change struct {
			child  int64
			parent int64 // 0 は親なし
		}
		var changes []change
		for key, parentKey := range observed {
			child, ok := byKey[key]
			if !ok {
				continue
			}
			var parent int64
			if parentKey != "" {
				parent = byKey[parentKey]
			}
			changes = append(changes, change{child: child, parent: parent})
		}
		sort.Slice(changes, func(i, j int) bool { return changes[i].child < changes[j].child })
		// 親を外す変更を先にすべて行い、付ける変更はそのあと行う。付ける変更が（途中の状態では）輪になるときは
		// 後回しにして、進まなくなるまで繰り返す（親と子が入れ替わる変更が、課題の ID の順に左右されないように）。
		var sets []change
		for _, c := range changes {
			if c.parent != 0 && c.parent != c.child {
				sets = append(sets, c)
				continue
			}
			if c.parent == 0 {
				if _, err := clearParentInTx(ctx, tx, rec, c.child); err != nil {
					return err
				}
			}
		}
		for len(sets) > 0 {
			var deferred []change
			for _, c := range sets {
				if _, err := setParentInTx(ctx, tx, rec, c.child, c.parent); err != nil {
					if !errors.Is(err, ErrParentCycle) {
						return err
					}
					deferred = append(deferred, c)
				}
			}
			if len(deferred) == len(sets) {
				break // どれも付けられない（本当に輪になる親子は付けない）
			}
			sets = deferred
		}
		return nil
	})
}

// loadChallengeIDsByLowerExternalKey は対応のある外部キー（小文字化）から課題の内部 ID への写しを読む。
func loadChallengeIDsByLowerExternalKey(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT external_key, challenge_id FROM source_binding`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var key string
		var id int64
		if err := rows.Scan(&key, &id); err != nil {
			return nil, err
		}
		out[strings.ToLower(key)] = id
	}
	return out, rows.Err()
}

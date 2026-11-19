package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
)

// このファイルは課題の親子と依存（前提）の記録を読む口を持つ。関係グラフ（graph.go）と課題のツリー（tree.go）が使う。
// どれも呼び出し側の読み取りのトランザクションの中で使い、何も書かない。

// fillGraphRelations は親子と依存の辺を読んで g に入れる（1 つの読み取りの中で）。
func fillGraphRelations(ctx context.Context, tx *sql.Tx, g *ChallengeGraph) error {
	parentIDs, err := loadParentMap(ctx, tx)
	if err != nil {
		return err
	}
	parents := make(map[string]string, len(parentIDs))
	for c, p := range parentIDs {
		parents[formatChallengeID(c)] = formatChallengeID(p)
	}
	for i := range g.Nodes {
		if p, ok := parents[g.Nodes[i].ID]; ok {
			p := p
			g.Nodes[i].ParentID = &p
		}
	}

	eval, err := evaluateDependencies(ctx, tx)
	if err != nil {
		return err
	}
	via, err := loadDependencyRegistrations(ctx, tx)
	if err != nil {
		return err
	}
	type rawEdge struct {
		from, to  int64 // from = 先行（depends_on_id）、to = 後続（challenge_id）
		createdAt string
	}
	var raw []rawEdge
	erows, err := tx.QueryContext(ctx, `SELECT depends_on_id, challenge_id, created_at FROM challenge_dependency`)
	if err != nil {
		return err
	}
	for erows.Next() {
		var e rawEdge
		if err := erows.Scan(&e.from, &e.to, &e.createdAt); err != nil {
			_ = erows.Close()
			return err
		}
		raw = append(raw, e)
	}
	if err := erows.Close(); err != nil {
		return err
	}
	if err := erows.Err(); err != nil {
		return err
	}
	sort.Slice(raw, func(i, j int) bool {
		if raw[i].from != raw[j].from {
			return raw[i].from < raw[j].from
		}
		return raw[i].to < raw[j].to
	})
	for _, e := range raw {
		satisfied := false
		for _, d := range eval.Dependencies[e.to] {
			if d.DependsOn == e.from {
				satisfied = d.Met
			}
		}
		registered, ok := via[registrationKey{challenge: e.to, dependsOn: e.from, at: e.createdAt}]
		if !ok {
			registered = GraphRegisteredViaUnknown
		}
		g.Edges = append(g.Edges, GraphEdge{
			From: formatChallengeID(e.from), To: formatChallengeID(e.to), Kind: GraphEdgePrerequisite, Directed: true,
			Satisfied: satisfied, RegisteredVia: registered, RegisteredAt: e.createdAt,
		})
	}
	return nil
}

// registrationKey は依存を足した作業ログ（dep_add）と辺を結ぶ鍵。辺の created_at と作業ログの at は
// 同じ書き込みで同じ時刻文字列が入るので、後から同じ辺を外して足し直した場合も最後の登録に当たる。
type registrationKey struct {
	challenge int64
	dependsOn int64
	at        string
}

// loadDependencyRegistrations は作業ログの dep_add から、依存の登録の経路を引く。
// 経路 invoker は plan（計画の判断）、cli は manual（端末のコマンド）。それ以外は結ばない（unknown）。
func loadDependencyRegistrations(ctx context.Context, tx *sql.Tx) (map[registrationKey]GraphRegisteredVia, error) {
	rows, err := tx.QueryContext(ctx, `SELECT entity_id, at, channel, after FROM activity
		WHERE entity = 'challenge' AND action = ? ORDER BY id ASC`, ActivityActionDepAdd)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[registrationKey]GraphRegisteredVia{}
	for rows.Next() {
		var id int64
		var at, channel string
		var after sql.NullString
		if err := rows.Scan(&id, &at, &channel, &after); err != nil {
			return nil, err
		}
		var via GraphRegisteredVia
		switch Channel(channel) {
		case ChannelInvoker:
			via = GraphRegisteredViaPlan
		case ChannelCLI:
			via = GraphRegisteredViaManual
		default:
			continue
		}
		if !after.Valid {
			continue
		}
		var payload struct {
			DependsOn []string `json:"depends_on"`
		}
		if err := json.Unmarshal([]byte(after.String), &payload); err != nil {
			continue
		}
		for _, d := range payload.DependsOn {
			if n, ok := parseChallengeID(d); ok {
				out[registrationKey{challenge: id, dependsOn: n, at: at}] = via
			}
		}
	}
	return out, rows.Err()
}

// loadParentMap は課題の親子（子の内部 ID → 親の内部 ID）を読む。親の無い課題は含めない。
func loadParentMap(ctx context.Context, tx *sql.Tx) (map[int64]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT challenge_id, parent_id FROM challenge_parent`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]int64{}
	for rows.Next() {
		var c, p int64
		if err := rows.Scan(&c, &p); err != nil {
			return nil, err
		}
		out[c] = p
	}
	return out, rows.Err()
}

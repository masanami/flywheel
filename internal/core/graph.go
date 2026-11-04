package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"time"
)

// このファイルは課題の関係グラフ（docs/features/ui-relation-graph.md §関係グラフの API）を、
// core の記録（課題・依存・親子・run・作業ログ・カードの導出）だけから決定的に作る公開 API
// （ChallengeGraph）を持つ。LLM も上流（GitHub）も呼ばず、ストアへ書かない。

// GraphCategory はノードの区分（「誰が動くべきか」。閉集合。導く順は 人間 → 外部 → 完了 → 取り下げ → システム）。
type GraphCategory string

// GraphCategory の値。
const (
	// GraphCategoryHuman は人間の操作待ち（カードの next_human_actions が空でない）。
	GraphCategoryHuman GraphCategory = "human"
	// GraphCategoryExternal は外部待ち（自動では解けない、相手と理由を持つ待ち）。
	// いまの core にはこれを表す記録が無いので、いまは出ない。
	GraphCategoryExternal GraphCategory = "external"
	// GraphCategoryDone は状態が完了。
	GraphCategoryDone GraphCategory = "done"
	// GraphCategoryWithdrawn は状態が取り下げ（完了と同じく終わった課題で、進める主体がいない）。
	GraphCategoryWithdrawn GraphCategory = "withdrawn"
	// GraphCategorySystem は上のいずれでもない（システムが進めている。CI の待ちを含む）。
	GraphCategorySystem GraphCategory = "system"
)

// GraphCategoryValues は GraphCategory の閉集合の写しを返す。
func GraphCategoryValues() []GraphCategory {
	return []GraphCategory{GraphCategoryHuman, GraphCategoryExternal, GraphCategoryDone, GraphCategoryWithdrawn, GraphCategorySystem}
}

// GraphEdgeKind は依存の種類（閉集合）。
type GraphEdgeKind string

// GraphEdgeKind の値。
const (
	// GraphEdgePrerequisite は終わらないと始められない（先行の成果を使う）。
	GraphEdgePrerequisite GraphEdgeKind = "prerequisite"
	// GraphEdgeConflict は同時に走らせると衝突する（同じものを触るので順に走らせる）。
	// challenge_dependency は先行関係だけを記録するので、いまは出ない。
	GraphEdgeConflict GraphEdgeKind = "conflict"
)

// GraphEdgeKindValues は GraphEdgeKind の閉集合の写しを返す。
func GraphEdgeKindValues() []GraphEdgeKind {
	return []GraphEdgeKind{GraphEdgePrerequisite, GraphEdgeConflict}
}

// GraphRegisteredVia は依存を登録した経路（閉集合）。
type GraphRegisteredVia string

// GraphRegisteredVia の値。
const (
	// GraphRegisteredViaPlan は計画の判断（経路 invoker）。
	GraphRegisteredViaPlan GraphRegisteredVia = "plan"
	// GraphRegisteredViaManual は端末のコマンド（経路 cli）。
	GraphRegisteredViaManual GraphRegisteredVia = "manual"
	// GraphRegisteredViaUnknown は作業ログから決まらない。
	GraphRegisteredViaUnknown GraphRegisteredVia = "unknown"
)

// GraphRegisteredViaValues は GraphRegisteredVia の閉集合の写しを返す。
func GraphRegisteredViaValues() []GraphRegisteredVia {
	return []GraphRegisteredVia{GraphRegisteredViaPlan, GraphRegisteredViaManual, GraphRegisteredViaUnknown}
}

// GraphNode は課題 1 件のノード。
type GraphNode struct {
	ID       string
	Title    string
	Headline *string
	Context  *string
	Category GraphCategory
	// State は課題の状態（カードの state）。
	State Status
	// Priority は課題の優先度（カード／一覧と同じ値。付いていなければ nil）。
	Priority *Priority
	// NextHumanActions は人間の操作待ちの中身（カードの next_human_actions と同じ値・並び。無ければ空で nil ではない）。
	NextHumanActions []CardAction
	// Running は課題の run が動いている（カードの印 running か unresponsive）。
	Running bool
	// Stale は動いている run の記録が途絶えている（カードの印 unresponsive）。
	Stale bool
	// ParentID は親の課題の ID。無ければ nil。
	ParentID *string
}

// GraphEdge は依存 1 本。From が先行（依存先）、To が後続（依存元）。
type GraphEdge struct {
	From string
	To   string
	Kind GraphEdgeKind
	// Satisfied は依存が満たされているか（依存の評価と同じ規則）。
	Satisfied     bool
	RegisteredVia GraphRegisteredVia
	// RegisteredAt は challenge_dependency.created_at（記録された文字列そのまま）。
	RegisteredAt string
}

// ChallengeGraph は関係グラフ。ノードは課題の ID の数の昇順、辺は (From, To) の数の昇順。
type ChallengeGraph struct {
	Nodes []GraphNode
	Edges []GraphEdge
}

// ChallengeGraph は now の時点の関係グラフを core の記録だけから作る。カードの導出（区分・running・
// stale）はボードの課題の一覧と同じ規則を使い、上流の応答を要する印（waiting_ci）は区分に効かないので
// 取得しない。
func (s *Store) ChallengeGraph(ctx context.Context, now time.Time) (*ChallengeGraph, error) {
	cs, err := s.ListChallenges(ctx, ListOptions{})
	if err != nil {
		return nil, err
	}
	decl, derr := LoadAgentDeclaration(s.Workspace())
	if derr != nil {
		decl = nil
	}
	ov, err := s.GetOverviewFor(ctx, decl)
	if err != nil {
		return nil, err
	}
	g := &ChallengeGraph{Nodes: make([]GraphNode, 0, len(cs)), Edges: []GraphEdge{}}
	for _, c := range cs {
		runs, err := s.LoadCardRuns(ctx, c)
		if err != nil {
			return nil, err
		}
		g.Nodes = append(g.Nodes, graphNode(c, runs.Derive(c, ov, now)))
	}
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return fillGraphRelations(ctx, tx, g)
	}); err != nil {
		return nil, classifyReadWriteErr(err)
	}
	return g, nil
}

func graphNode(c Challenge, card *Card) GraphNode {
	n := GraphNode{
		ID: c.ID, Title: c.Title, Headline: c.Headline, Context: c.Context, Category: graphCategory(c, card), Priority: c.Priority,
		State: card.State, NextHumanActions: append([]CardAction{}, card.NextHumanActions...),
	}
	for _, m := range card.Modifiers {
		switch m {
		case CardModifierRunning:
			n.Running = true
		case CardModifierUnresponsive:
			n.Running = true
			n.Stale = true
		}
	}
	return n
}

func graphCategory(c Challenge, card *Card) GraphCategory {
	switch {
	case len(card.NextHumanActions) > 0:
		return GraphCategoryHuman
	case c.Status == StatusDone:
		return GraphCategoryDone
	case c.Status == StatusWithdrawn:
		return GraphCategoryWithdrawn
	default:
		return GraphCategorySystem
	}
}

// fillGraphRelations は親子と依存の辺を読んで g に入れる（1 つの読み取りの中で）。
func fillGraphRelations(ctx context.Context, tx *sql.Tx, g *ChallengeGraph) error {
	parents := map[string]string{}
	rows, err := tx.QueryContext(ctx, `SELECT challenge_id, parent_id FROM challenge_parent`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c, p int64
		if err := rows.Scan(&c, &p); err != nil {
			_ = rows.Close()
			return err
		}
		parents[formatChallengeID(c)] = formatChallengeID(p)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
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
			From: formatChallengeID(e.from), To: formatChallengeID(e.to), Kind: GraphEdgePrerequisite,
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

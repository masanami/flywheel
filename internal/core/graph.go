package core

import (
	"context"
	"database/sql"
	"time"
)

// このファイルは課題の関係グラフ（docs/features/ui-relation-graph.md §関係グラフの API）を、
// core の記録（課題・依存・親子・run・作業ログ・カードの導出）だけから決定的に作る公開 API
// （ChallengeGraph）を持つ。LLM も上流（GitHub）も呼ばず、ストアへ書かない。

// GraphCategory はノードの区分（「誰が動くべきか」。閉集合。導く順は 人間 → 外部 → 親のエージェント → 完了 → 取り下げ → システム）。
type GraphCategory string

// GraphCategory の値。
const (
	// GraphCategoryHuman は人間の操作待ち（カードの next_human_actions が空でない）。
	GraphCategoryHuman GraphCategory = "human"
	// GraphCategoryExternal は外部待ち（自動では解けない、相手と理由を持つ待ち）。
	// いまの core にはこれを表す記録が無いので、いまは出ない。
	GraphCategoryExternal GraphCategory = "external"
	// GraphCategoryParentAgent は親のエージェントが動く番（状態は人間対応待ちのまま、いま開いている保留の種類が
	// 親が直接行うで、人間の操作待ちが無い課題）。人間の操作待ちが先に当たる。
	GraphCategoryParentAgent GraphCategory = "parent_agent"
	// GraphCategoryDone は状態が完了。
	GraphCategoryDone GraphCategory = "done"
	// GraphCategoryWithdrawn は状態が取り下げ（完了と同じく終わった課題で、進める主体がいない）。
	GraphCategoryWithdrawn GraphCategory = "withdrawn"
	// GraphCategorySystem は上のいずれでもない（システムが進めている。CI の待ちを含む）。
	GraphCategorySystem GraphCategory = "system"
)

// GraphCategoryValues は GraphCategory の閉集合の写しを返す。
func GraphCategoryValues() []GraphCategory {
	return []GraphCategory{GraphCategoryHuman, GraphCategoryExternal, GraphCategoryParentAgent, GraphCategoryDone, GraphCategoryWithdrawn, GraphCategorySystem}
}

// GraphEdgeKind は依存の種類（閉集合）。
type GraphEdgeKind string

// GraphEdgeKind の値。
const (
	// GraphEdgePrerequisite は終わらないと始められない（先行の成果を使う）。
	GraphEdgePrerequisite GraphEdgeKind = "prerequisite"
)

// GraphEdgeKindValues は GraphEdgeKind の閉集合の写しを返す。
func GraphEdgeKindValues() []GraphEdgeKind {
	return []GraphEdgeKind{GraphEdgePrerequisite}
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

// GraphEdge は依存 1 本。前提（Kind=prerequisite）は From が先行（依存先）、To が後続（依存元）。
type GraphEdge struct {
	From string
	To   string
	Kind GraphEdgeKind
	// Directed は辺の向きがあるか。前提は常に true。
	Directed bool
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
	inParentAgent := map[string]bool{}
	for _, c := range ov.ParentAgentChallenges {
		inParentAgent[c.ID] = true
	}
	for _, c := range cs {
		runs, err := s.LoadCardRuns(ctx, c)
		if err != nil {
			return nil, err
		}
		g.Nodes = append(g.Nodes, graphNode(c, runs.Derive(c, ov, now), inParentAgent[c.ID]))
	}
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return fillGraphRelations(ctx, tx, g)
	}); err != nil {
		return nil, classifyReadWriteErr(err)
	}
	return g, nil
}

func graphNode(c Challenge, card *Card, parentAgent bool) GraphNode {
	n := GraphNode{
		ID: c.ID, Title: c.Title, Headline: c.Headline, Context: c.Context, Category: graphCategory(c, card, parentAgent), Priority: c.Priority,
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

func graphCategory(c Challenge, card *Card, parentAgent bool) GraphCategory {
	switch {
	case len(card.NextHumanActions) > 0:
		return GraphCategoryHuman
	case parentAgent:
		return GraphCategoryParentAgent
	case c.Status == StatusDone:
		return GraphCategoryDone
	case c.Status == StatusWithdrawn:
		return GraphCategoryWithdrawn
	default:
		return GraphCategorySystem
	}
}

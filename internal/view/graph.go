package view

import "github.com/masanami/flywheel/internal/core"

// このファイルは課題の関係グラフ（docs/features/ui-relation-graph.md §関係グラフの API。
// `GET /api/v1/workspaces/{name}/graph`）の応答の型を持つ。

// GraphNode は graph の nodes[] の要素（課題 1 件）。category・running・stale はボードのカードの導出と同じ規則。
type GraphNode struct {
	Category string  `json:"category"`
	Context  *string `json:"context"`
	Headline *string `json:"headline"`
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`
	Running  bool    `json:"running"`
	Stale    bool    `json:"stale"`
	Title    string  `json:"title"`
}

// GraphEdge は graph の edges[] の要素（依存 1 本）。from が先行（依存先）、to が後続（依存元）。
type GraphEdge struct {
	From          string `json:"from"`
	Kind          string `json:"kind"`
	RegisteredAt  string `json:"registered_at"`
	RegisteredVia string `json:"registered_via"`
	Satisfied     bool   `json:"satisfied"`
	To            string `json:"to"`
}

// GraphResponse は `GET …/graph` の本文。
type GraphResponse struct {
	Edges []GraphEdge `json:"edges"`
	Nodes []GraphNode `json:"nodes"`
}

// FromChallengeGraph は core の関係グラフを応答の型へ変換する（空でも `[]`）。
func FromChallengeGraph(g *core.ChallengeGraph) GraphResponse {
	out := GraphResponse{Edges: make([]GraphEdge, 0, len(g.Edges)), Nodes: make([]GraphNode, 0, len(g.Nodes))}
	for _, n := range g.Nodes {
		out.Nodes = append(out.Nodes, GraphNode{
			Category: string(n.Category), Context: n.Context, Headline: n.Headline, ID: n.ID,
			ParentID: n.ParentID, Running: n.Running, Stale: n.Stale, Title: n.Title,
		})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, GraphEdge{
			From: e.From, Kind: string(e.Kind), RegisteredAt: e.RegisteredAt, RegisteredVia: string(e.RegisteredVia),
			Satisfied: e.Satisfied, To: e.To,
		})
	}
	return out
}

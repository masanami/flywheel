package core

// このファイルは委譲の計画が使う候補 1 件の型と、起動の順（優先度・ID の昇順）の並べ替えを持つ。

import "sort"

// planCandidate は委譲の候補 1 件。Rank は優先度の順位（P0=0 … 未設定=3）、Seq は課題の内部整数 ID
// （昇順の比較用）。
type planCandidate struct {
	ID   string
	Rank int
	Seq  int64
}

func (c planCandidate) before(o planCandidate) bool {
	if c.Rank != o.Rank {
		return c.Rank < o.Rank
	}
	return c.Seq < o.Seq
}

func sortCandidates(cs []planCandidate) {
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].before(cs[j]) })
}

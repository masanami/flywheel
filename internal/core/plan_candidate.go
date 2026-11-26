package core

// このファイルは委譲の計画が使う候補 1 件の型と、起動の順（優先度・ID の昇順）の並べ替えを持つ。

import "sort"

// planCandidate は委譲の候補 1 件。Rank は優先度の順位（P0=0 … 未設定=3）、Seq は課題の内部整数 ID
// （昇順の比較用）。
type planCandidate struct {
	ID   string
	Rank int
	Seq  int64
	// TaskSeq は起動するタスクの内部整数 ID（0 はタスクを決めない候補）。同じ課題の候補は、タスクの ID の昇順に並ぶ。
	TaskSeq int64
	// SpecDraft は、下書きをリポジトリへ入れるタスクの、候補を作るときに読み直した下書きの全文（ほかのタスクは空）。
	SpecDraft string
}

func (c planCandidate) before(o planCandidate) bool {
	if c.Rank != o.Rank {
		return c.Rank < o.Rank
	}
	if c.Seq != o.Seq {
		return c.Seq < o.Seq
	}
	return c.TaskSeq < o.TaskSeq
}

func sortCandidates(cs []planCandidate) {
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].before(cs[j]) })
}

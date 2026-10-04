package core

// このファイルは直列化グループの作り方（親要件チケット #98 §実行スロット「同じリポジトリの
// 並列と直列化グループ」・M3P26・M3P27・M3P31・M3P32）の純粋な規則を持つ。ストアにも
// 予測の口にも触れず、予測の結果（または予測が得られなかった理由）から、リポジトリ 1 つぶんの
// グループと起動の順を決める。

import "sort"

// SerialGroupReason は serial_groups[].reasons の閉集合。
type SerialGroupReason string

// SerialGroupReason の値。定義順が reasons の並び順。
const (
	SerialReasonSharedFiles         SerialGroupReason = "shared_files"
	SerialReasonDependency          SerialGroupReason = "dependency"
	SerialReasonUnknownPair         SerialGroupReason = "unknown_pair"
	SerialReasonNotPredictable      SerialGroupReason = "not_predictable"
	SerialReasonPredictionFailed    SerialGroupReason = "prediction_failed"
	SerialReasonPredictionBudget    SerialGroupReason = "prediction_budget"
	SerialReasonNoPredictionDeclare SerialGroupReason = "no_prediction_declared"
	SerialReasonRunningRun          SerialGroupReason = "running_run"
)

var serialGroupReasonOrder = []SerialGroupReason{
	SerialReasonSharedFiles, SerialReasonDependency, SerialReasonUnknownPair, SerialReasonNotPredictable,
	SerialReasonPredictionFailed, SerialReasonPredictionBudget, SerialReasonNoPredictionDeclare, SerialReasonRunningRun,
}

// SerialGroupReasonValues は reasons の閉集合を定義順に返す（CLI の照合用の写し）。
func SerialGroupReasonValues() []SerialGroupReason {
	return append([]SerialGroupReason(nil), serialGroupReasonOrder...)
}

// SerialGroup は serial_groups の 1 要素。Challenges は実行中の課題を先頭に、続けて委譲の候補を
// 起動の順に並べる。Reasons は定義順に重複なく並べる。PredictionHeadSHA は予測の口を呼んで
// head_sha を得たときだけ非 nil。
type SerialGroup struct {
	Repo              string
	Challenges        []string
	Reasons           []SerialGroupReason
	PredictionHeadSHA *string
}

// planCandidate は委譲の候補 1 件。Issue は予測の口へ渡せる Issue 番号（取り込み元の対応が無ければ 0）。
// Rank は優先度の順位（P0=0 … 未設定=3）、Seq は課題の内部整数 ID（昇順の比較用）。
type planCandidate struct {
	ID    string
	Issue int
	Rank  int
	Seq   int64
}

func (c planCandidate) before(o planCandidate) bool {
	if c.Rank != o.Rank {
		return c.Rank < o.Rank
	}
	return c.Seq < o.Seq
}

// planRunning はその周より前から終了していない委譲の run を持つ課題（実行中の課題）。
type planRunning struct {
	ID    string
	Issue int
	Seq   int64
}

// plannedGroup は 1 つの直列化グループ。Candidates は起動の順。
type plannedGroup struct {
	Repo       string
	Running    []planRunning
	Candidates []planCandidate
	Reasons    map[SerialGroupReason]bool
	HeadSHA    string
}

func sortedReasons(set map[SerialGroupReason]bool) []SerialGroupReason {
	out := []SerialGroupReason{}
	for _, r := range serialGroupReasonOrder {
		if set[r] {
			out = append(out, r)
		}
	}
	return out
}

func (g *plannedGroup) public() SerialGroup {
	sg := SerialGroup{Repo: g.Repo, Reasons: sortedReasons(g.Reasons)}
	for _, r := range g.Running {
		sg.Challenges = append(sg.Challenges, r.ID)
	}
	for _, c := range g.Candidates {
		sg.Challenges = append(sg.Challenges, c.ID)
	}
	if g.HeadSHA != "" {
		h := g.HeadSHA
		sg.PredictionHeadSHA = &h
	}
	return sg
}

// repoPrediction はリポジトリ 1 つの予測の結果。FailClosed が非空なら、そのリポジトリの委譲の候補は
// すべて 1 つのグループに入る（その理由）。Output が非 nil なら組の規則でグループを作る。
// どちらも空（予測の口を呼ぶ必要が無かった）なら、候補は 1 件ずつ別のグループになる。
type repoPrediction struct {
	FailClosed []SerialGroupReason
	Output     *ConflictPredictionOutput
	// HeadSHA は予測の口を呼んで得た head_sha（得られなければ空）。
	HeadSHA string
}

func sortCandidates(cs []planCandidate) {
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].before(cs[j]) })
}

// buildRepoGroups は repo の直列化グループを作る。cands は優先度・ID の昇順でなくてもよい。
// 候補の無い成分（実行中の課題だけ）は返さない。返すグループは先頭の候補の順に並ぶ。
func buildRepoGroups(repo string, running []planRunning, cands []planCandidate, pred repoPrediction) []plannedGroup {
	if len(cands) == 0 {
		return nil
	}
	cands = append([]planCandidate(nil), cands...)
	sortCandidates(cands)

	if len(pred.FailClosed) > 0 {
		g := plannedGroup{Repo: repo, Running: running, Candidates: cands, Reasons: map[SerialGroupReason]bool{}, HeadSHA: pred.HeadSHA}
		for _, r := range pred.FailClosed {
			g.Reasons[r] = true
		}
		if len(running) > 0 {
			g.Reasons[SerialReasonRunningRun] = true
		}
		return []plannedGroup{g}
	}
	if pred.Output == nil {
		out := make([]plannedGroup, 0, len(cands))
		for _, c := range cands {
			out = append(out, plannedGroup{Repo: repo, Candidates: []planCandidate{c}, Reasons: map[SerialGroupReason]bool{}, HeadSHA: pred.HeadSHA})
		}
		return out
	}

	// 連結成分（union-find）。ノードは Issue 番号。
	parent := map[int]int{}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) { parent[find(a)] = find(b) }
	for _, r := range running {
		if r.Issue > 0 {
			parent[r.Issue] = r.Issue
		}
	}
	for _, c := range cands {
		if c.Issue > 0 {
			parent[c.Issue] = c.Issue
		}
	}
	type edge struct {
		a, b    int
		reasons []SerialGroupReason
		first   int // dependency.first（0 は無し）
	}
	var edges []edge
	for _, p := range pred.Output.Pairs {
		a, b := p.Issues[0], p.Issues[1]
		if _, ok := parent[a]; !ok {
			continue
		}
		if _, ok := parent[b]; !ok {
			continue
		}
		var reasons []SerialGroupReason
		first := 0
		switch p.Status {
		case predictPairUnknown:
			reasons = append(reasons, SerialReasonUnknownPair)
		default:
			for _, f := range p.SharedFiles {
				if !f.MergeFriendly && !f.Ignored {
					reasons = append(reasons, SerialReasonSharedFiles)
					break
				}
			}
			if p.DependencyFirst != nil {
				reasons = append(reasons, SerialReasonDependency)
				first = *p.DependencyFirst
			}
		}
		if len(reasons) == 0 {
			continue
		}
		union(a, b)
		edges = append(edges, edge{a: a, b: b, reasons: reasons, first: first})
	}

	byRoot := map[int]*plannedGroup{}
	var order []int
	for _, c := range cands {
		root := c.Issue
		if root > 0 {
			root = find(c.Issue)
		}
		key := root
		if c.Issue == 0 {
			key = -int(c.Seq) // Issue 番号の無い候補は予測の対象外（通常はここに来ない）
		}
		g, ok := byRoot[key]
		if !ok {
			g = &plannedGroup{Repo: repo, Reasons: map[SerialGroupReason]bool{}, HeadSHA: pred.HeadSHA}
			byRoot[key] = g
			order = append(order, key)
		}
		g.Candidates = append(g.Candidates, c)
	}
	for _, r := range running {
		if r.Issue <= 0 {
			continue
		}
		if g, ok := byRoot[find(r.Issue)]; ok {
			g.Running = append(g.Running, r)
			g.Reasons[SerialReasonRunningRun] = true
		}
	}
	for _, e := range edges {
		if g, ok := byRoot[find(e.a)]; ok {
			for _, r := range e.reasons {
				g.Reasons[r] = true
			}
		}
	}
	// 辺（先に入れる側 → 後）をグループごとに集めて順を決める。
	deps := map[int][][2]int{}
	for _, e := range edges {
		if e.first == 0 || (e.first != e.a && e.first != e.b) {
			continue
		}
		other := e.a
		if e.first == e.a {
			other = e.b
		}
		root := find(e.a)
		deps[root] = append(deps[root], [2]int{e.first, other})
	}
	out := make([]plannedGroup, 0, len(order))
	for _, key := range order {
		g := byRoot[key]
		if key > 0 {
			g.Candidates = orderByDependency(g.Candidates, deps[key])
		}
		out = append(out, *g)
	}
	return out
}

// orderByDependency は cands（優先度・ID の昇順）を、辺 [先, 後] を守って並べ直す。辺で順が
// 決まらないものは優先度・ID の昇順。辺が循環していたら辺をすべて無視する（cands の順のまま）。
func orderByDependency(cands []planCandidate, edges [][2]int) []planCandidate {
	if len(edges) == 0 {
		return cands
	}
	byIssue := map[int]int{} // Issue → cands の添字
	for i, c := range cands {
		byIssue[c.Issue] = i
	}
	indeg := make([]int, len(cands))
	succ := make([][]int, len(cands))
	for _, e := range edges {
		f, okf := byIssue[e[0]]
		t, okt := byIssue[e[1]]
		if !okf || !okt || f == t {
			continue // 実行中の課題との辺は順に使わない
		}
		succ[f] = append(succ[f], t)
		indeg[t]++
	}
	done := make([]bool, len(cands))
	out := make([]planCandidate, 0, len(cands))
	for len(out) < len(cands) {
		pick := -1
		for i := range cands { // cands は優先度・ID の昇順なので、最初に見つかる入次数 0 が最小
			if !done[i] && indeg[i] == 0 {
				pick = i
				break
			}
		}
		if pick < 0 {
			return cands // 循環: 辺をすべて無視する
		}
		done[pick] = true
		out = append(out, cands[pick])
		for _, t := range succ[pick] {
			indeg[t]--
		}
	}
	return out
}

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
	// 以下の 5 つは判断点（J9）が絡む理由。merge_candidate は互いに前提にし合う組（1 つの課題にまとめる候補。
	// 自動ではまとめない）、serial_judgment_* は判断の結果（強い・判断できない・呼び出しの失敗・費用の上限）。
	SerialReasonMergeCandidate     SerialGroupReason = "merge_candidate"
	SerialReasonJudgedStrong       SerialGroupReason = "serial_judgment_strong"
	SerialReasonJudgedUndetermined SerialGroupReason = "serial_judgment_undetermined"
	SerialReasonJudgmentFailed     SerialGroupReason = "serial_judgment_failed"
	SerialReasonJudgmentBudget     SerialGroupReason = "serial_judgment_budget"
	SerialReasonRunningRun         SerialGroupReason = "running_run"
)

var serialGroupReasonOrder = []SerialGroupReason{
	SerialReasonSharedFiles, SerialReasonDependency, SerialReasonUnknownPair, SerialReasonNotPredictable,
	SerialReasonPredictionFailed, SerialReasonPredictionBudget, SerialReasonNoPredictionDeclare,
	SerialReasonMergeCandidate, SerialReasonJudgedStrong, SerialReasonJudgedUndetermined,
	SerialReasonJudgmentFailed, SerialReasonJudgmentBudget, SerialReasonRunningRun,
}

// SerialGroupReasonValues は reasons の閉集合を定義順に返す（CLI の照合用の写し）。
func SerialGroupReasonValues() []SerialGroupReason {
	return append([]SerialGroupReason(nil), serialGroupReasonOrder...)
}

// SerialGroup は serial_groups の 1 要素。Challenges は実行中の課題を先頭に、続けて委譲の候補を
// 起動の順に並べる。Reasons は定義順に重複なく並べる。PredictionHeadSHA は予測の口を呼んで
// head_sha を得たときだけ非 nil。Judgments は、このグループの課題を含む組のうち判断点（J9）に判断させた
// 組の記録（判断させなければ空）。
type SerialGroup struct {
	Repo              string
	Challenges        []string
	Reasons           []SerialGroupReason
	PredictionHeadSHA *string
	Judgments         []SerialJudgment
}

// planCandidate は委譲の候補 1 件。Issue は予測の口へ渡せる Issue 番号（取り込み元の対応が無ければ 0）。
// Rank は優先度の順位（P0=0 … 未設定=3）、Seq は課題の内部整数 ID（昇順の比較用）。
type planCandidate struct {
	ID    string
	Issue int
	Rank  int
	Seq   int64
	// Plan は承認済みの計画の本文（J9 に渡す。無ければ空）。
	Plan string
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
	Judgments  []SerialJudgment
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
	sg.Judgments = append(sg.Judgments, g.Judgments...)
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
	// RunID は予測の口を呼んだ run の内部整数 ID（Output が非 nil のときだけ。衝突の記録に使う）。
	RunID int64

	// 以下は repos[].serial_judgment が true のリポジトリだけが持つ（判断点 J9）。
	// SerialJudgment が true なら、互いに前提にし合う組（dependency.stated が両方向で first が無い）を直列にする。
	// Verdicts は判断させた組の結果（候補どうしの組。鍵は課題の内部整数 ID の昇順）。weak だけが並列にする。
	// UndeclaredJudged は予測の宣言が無いリポジトリを候補どうしの組の判断で作ること（Free は判断した先頭の候補の数。
	// それより後ろの候補は全員と直列）。
	SerialJudgment   bool
	Verdicts         map[pairKey]pairJudgment
	UndeclaredJudged bool
	Free             int
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
	if pred.UndeclaredJudged {
		return buildUndeclaredJudgedGroups(repo, cands, pred)
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
	candByIssue := map[int]planCandidate{}
	for _, c := range cands {
		if c.Issue > 0 {
			candByIssue[c.Issue] = c
		}
	}
	var edges []predictedEdge
	for _, e := range predictedEdges(pred.Output.Pairs, func(n int) bool { _, ok := parent[n]; return ok }, pred.SerialJudgment) {
		ca, aok := candByIssue[e.a]
		cb, bok := candByIssue[e.b]
		if aok && bok {
			if v, ok := pred.Verdicts[newPairKey(ca, cb)]; ok {
				// 先後の依存も互いの前提も無い組が weak なら並列にする。それ以外は直列のまま理由を足す。
				if v.Outcome == PairOutcomeWeak && e.first == 0 && !e.mutual {
					continue
				}
				if r, serial := v.Outcome.serialReason(); serial {
					e.reasons = append(append([]SerialGroupReason(nil), e.reasons...), r)
				}
			}
		}
		edges = append(edges, e)
	}
	for _, e := range edges {
		union(e.a, e.b)
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
	attachJudgments(out, cands, pred.Verdicts)
	return out
}

// attachJudgments は判断させた組の記録を、その組の課題を含むグループに付ける。
func attachJudgments(groups []plannedGroup, cands []planCandidate, verdicts map[pairKey]pairJudgment) {
	if len(verdicts) == 0 {
		return
	}
	idOf := map[int64]string{}
	for _, c := range cands {
		idOf[c.Seq] = c.ID
	}
	keys := make([]pairKey, 0, len(verdicts))
	for k := range verdicts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		v := verdicts[k]
		rec := SerialJudgment{Challenges: [2]string{idOf[k[0]], idOf[k[1]]}, Outcome: v.Outcome, Reason: v.Reason}
		for gi := range groups {
			for _, c := range groups[gi].Candidates {
				if c.Seq == k[0] || c.Seq == k[1] {
					groups[gi].Judgments = append(groups[gi].Judgments, rec)
					break
				}
			}
		}
	}
}

// buildUndeclaredJudgedGroups は予測の宣言が無く、判断点で組を判断したリポジトリの直列化グループを作る。
// 動いている課題が無い前提（あれば判断せず、全員が 1 つのグループに入る）。判断した先頭 Free 件の候補どうしの
// 組は、weak なら辺を張らず、それ以外は辺を張る。判断しなかった組（Free より後ろの候補を含む組）は辺を張る。
func buildUndeclaredJudgedGroups(repo string, cands []planCandidate, pred repoPrediction) []plannedGroup {
	n := len(cands)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	type edge struct {
		i, j    int
		reasons []SerialGroupReason
	}
	var edges []edge
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			reasons := []SerialGroupReason{SerialReasonNoPredictionDeclare}
			if i < pred.Free && j < pred.Free {
				if v, ok := pred.Verdicts[newPairKey(cands[i], cands[j])]; ok {
					if v.Outcome == PairOutcomeWeak {
						continue
					}
					if r, serial := v.Outcome.serialReason(); serial {
						reasons = append(reasons, r)
					}
				}
			}
			edges = append(edges, edge{i, j, reasons})
			parent[find(i)] = find(j)
		}
	}
	byRoot := map[int]*plannedGroup{}
	var order []int
	for i, c := range cands {
		root := find(i)
		g, ok := byRoot[root]
		if !ok {
			g = &plannedGroup{Repo: repo, Reasons: map[SerialGroupReason]bool{}, HeadSHA: pred.HeadSHA}
			byRoot[root] = g
			order = append(order, root)
		}
		g.Candidates = append(g.Candidates, c)
	}
	for _, e := range edges {
		g := byRoot[find(e.i)]
		for _, r := range e.reasons {
			g.Reasons[r] = true
		}
	}
	out := make([]plannedGroup, 0, len(order))
	for _, root := range order {
		out = append(out, *byRoot[root])
	}
	attachJudgments(out, cands, pred.Verdicts)
	return out
}

// predictedEdge は予測の組のうち、同時に委譲しない（順に走らせる）理由を持つもの。first は
// dependency.first（0 は無し）。
type predictedEdge struct {
	a, b    int
	reasons []SerialGroupReason
	first   int
	// mutual は互いに相手を前提に挙げた組（dependency.stated が両方向で first が無い）。mutualEnabled のときだけ立つ。
	mutual bool
}

// predictedEdges は予測の出力の組から、直列化の理由（shared_files・dependency・unknown_pair）を持つ組を
// 返す。known が false を返す Issue（候補にも実行中にも無い）を含む組は飛ばす。直列化グループの作り方
// （buildRepoGroups）と衝突の記録（recordConflicts）が同じ組を使う。
func predictedEdges(pairs []PredictedPair, known func(issue int) bool, mutualEnabled bool) []predictedEdge {
	var edges []predictedEdge
	for _, p := range pairs {
		a, b := p.Issues[0], p.Issues[1]
		if !known(a) || !known(b) {
			continue
		}
		var reasons []SerialGroupReason
		first := 0
		mutual := false
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
			} else if mutualEnabled && statesBoth(p.DependencyStated, a, b) {
				reasons = append(reasons, SerialReasonMergeCandidate)
				mutual = true
			}
		}
		if len(reasons) == 0 {
			continue
		}
		edges = append(edges, predictedEdge{a: a, b: b, reasons: reasons, first: first, mutual: mutual})
	}
	return edges
}

// statesBoth は申告 stated が a→b と b→a の両方を含むか（互いに相手を前提に挙げたか）。
func statesBoth(stated []PredictedDependEdge, a, b int) bool {
	ab, ba := false, false
	for _, e := range stated {
		if e.Issue == a && e.DependsOn == b {
			ab = true
		}
		if e.Issue == b && e.DependsOn == a {
			ba = true
		}
	}
	return ab && ba
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

package core

// このファイルは 2 つの課題の計画を同時に進めてよいかの判断（判断点 J9。J7 は無い）を持つ:
// 出力の形と検査、入力の区画の組み立て、組ごとの呼び出しと費用の上限（1 組あたり・1 周あたり）、
// 予測の宣言が無いリポジトリの直列化グループの作り方。強い・判断できない・判断点の失敗・予算切れ・
// 出力の不正は直列、弱いときだけ並列にする。先後の依存がある組と互いに前提にし合う組は、判断の
// 結果に関わらず直列のまま（この組を判断に回すのは互いに前提にし合う組と両方が触る対象がある組だけ）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// J9Verdict は J9 の判定の閉集合。
type J9Verdict string

// J9Verdict の 3 値。
const (
	J9VerdictStrong    J9Verdict = "strong"
	J9VerdictWeak      J9Verdict = "weak"
	J9VerdictUncertain J9Verdict = "uncertain"
)

// J9VerdictValues は J9Verdict の全値。
var J9VerdictValues = []J9Verdict{J9VerdictStrong, J9VerdictWeak, J9VerdictUncertain}

// PairOutcome は組の判断の結果（serial_groups[].judgments[].outcome の閉集合）。strong・weak・uncertain は
// J9 の判定そのもので、failed は呼び出しの失敗・出力の不正、budget は費用の上限（1 組あたり・1 周あたり・
// 周の上限・枠超過）で呼ばなかった・打ち切られたこと。weak 以外はすべて直列。
type PairOutcome string

// PairOutcome の値。
const (
	PairOutcomeStrong    PairOutcome = "strong"
	PairOutcomeWeak      PairOutcome = "weak"
	PairOutcomeUncertain PairOutcome = "uncertain"
	PairOutcomeFailed    PairOutcome = "failed"
	PairOutcomeBudget    PairOutcome = "budget"
)

// PairOutcomeValues は PairOutcome の全値。
var PairOutcomeValues = []PairOutcome{PairOutcomeStrong, PairOutcomeWeak, PairOutcomeUncertain, PairOutcomeFailed, PairOutcomeBudget}

// serialReason は結果が直列にする理由を返す（weak は並列なので ok=false）。
func (o PairOutcome) serialReason() (SerialGroupReason, bool) {
	switch o {
	case PairOutcomeStrong:
		return SerialReasonJudgedStrong, true
	case PairOutcomeUncertain:
		return SerialReasonJudgedUndetermined, true
	case PairOutcomeFailed:
		return SerialReasonJudgmentFailed, true
	case PairOutcomeBudget:
		return SerialReasonJudgmentBudget, true
	}
	return "", false
}

// SerialJudgment は serial_groups[].judgments の要素（1 組の判断の記録）。
type SerialJudgment struct {
	Challenges [2]string
	Outcome    PairOutcome
	Reason     string
}

// pairJudgment は組の判断の結果（Reason は判断点の理由、または失敗・打ち切りの理由）。
type pairJudgment struct {
	Outcome PairOutcome
	Reason  string
}

// pairKey は 2 つの候補の組の鍵（課題の内部整数 ID の昇順）。
type pairKey [2]int64

func newPairKey(a, b planCandidate) pairKey {
	if b.Seq < a.Seq {
		a, b = b, a
	}
	return pairKey{a.Seq, b.Seq}
}

// j9MaxReasonChars は記録する判断の理由の文字数の上限。
const j9MaxReasonChars = 1000

type j9RawOutput struct {
	Verdict *string `json:"verdict"`
	Reason  *string `json:"reason"`
}

// j9OutputSchema は J9 の `--json-schema` に渡すスキーマ。
func j9OutputSchema() []byte {
	schema := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"verdict", "reason"},
		"properties": map[string]any{
			"verdict": map[string]any{"type": "string", "enum": j6StringEnum(J9VerdictValues)},
			"reason":  map[string]any{"type": "string", "minLength": 1},
		},
	}
	b, _ := json.Marshal(schema)
	return b
}

// validateJ9Output は J9 の出力を検査する。項目の欠落・閉集合の外の値・理由が空白だけなら ok=false。
func validateJ9Output(data []byte) (J9Verdict, string, bool) {
	var raw j9RawOutput
	if err := json.Unmarshal(data, &raw); err != nil || raw.Verdict == nil || raw.Reason == nil {
		return "", "", false
	}
	v := J9Verdict(*raw.Verdict)
	valid := false
	for _, x := range J9VerdictValues {
		if v == x {
			valid = true
		}
	}
	reason := strings.TrimSpace(*raw.Reason)
	if !valid || reason == "" {
		return "", "", false
	}
	reason, _ = truncateRunes(reason, j9MaxReasonChars)
	return v, reason, true
}

// judgedPair は判断に回す組（a が起動の順で先）。posA・posB は全リポジトリの起動の順での位置、
// mutual は互いに前提にし合う組、out は組の予測の出力（予測の宣言が無ければ nil）。
type judgedPair struct {
	a, b       planCandidate
	posA, posB int
	mutual     bool
	out        *ConflictPredictionOutput
}

// serialJudgmentSpentMicros は周 cycleID で J9 に使った費用（終了した run は費用、終了していない run は
// 渡した上限額）の合計を USD の 100 万分の 1 単位で返す。
func (s *Store) serialJudgmentSpentMicros(ctx context.Context, cycleID string) (int64, error) {
	cyid, ok := parseCycleID(cycleID)
	if !ok {
		return 0, ErrValidation
	}
	var v int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(COALESCE(cost_usd, max_budget_usd)), 0) FROM run WHERE cycle_id = ? AND kind = 'judgment' AND judgment = ?`,
			cyid, string(judgmentJ9)).Scan(&v)
	})
	return v, classifyReadWriteErr(err)
}

// judgePairs は pairs を 1 組ずつ J9 に判断させる。pairs の順に呼び、1 組あたり
// judgment_budget_usd.J9、1 周あたり serial_judgment_cycle_budget_usd を超える分は呼ばずに budget にする
// （残りの組も同じ）。呼び出しの失敗・出力の不正は failed。ストアの書き込みの失敗と ctx の終了だけがエラー。
func (s *Store) judgePairs(ctx context.Context, in DelegateInput, pairs []judgedPair, predictionText func(judgedPair) string) (map[pairKey]pairJudgment, error) {
	out := map[pairKey]pairJudgment{}
	if len(pairs) == 0 {
		return out, nil
	}
	perPair := in.AgentDecl.JudgmentBudgetFor(JudgmentJ9)
	capMicros := usdToMicros(in.AgentDecl.SerialJudgmentCycleBudgetUSD)
	pairMicros := usdToMicros(perPair)
	cyc := in.Cycle
	if cyc == nil {
		cyc = NewJudgmentCycle(in.CycleID)
	}
	for _, p := range pairs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := newPairKey(p.a, p.b)
		spent, err := s.serialJudgmentSpentMicros(ctx, in.CycleID)
		if err != nil {
			return nil, err
		}
		if spent+pairMicros > capMicros {
			out[key] = pairJudgment{Outcome: PairOutcomeBudget, Reason: "1 周あたりの判断の費用の上限に達した"}
			continue
		}
		pj, err := s.judgeOnePair(ctx, in, cyc, p, perPair, predictionText(p))
		if err != nil {
			return nil, err
		}
		out[key] = pj
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) judgeOnePair(ctx context.Context, in DelegateInput, cyc *JudgmentCycle, p judgedPair, perPair float64, prediction string) (pairJudgment, error) {
	run, ns, err := cyc.RunJudgment(ctx, s, RunJudgmentInput{
		Judgment:     JudgmentJ9,
		MaxBudgetUSD: perPair,
		TimeoutSec:   in.AgentDecl.TimeoutSec.Judgment,
		Sections:     prepareJ9(p, prediction),
		OutputSchema: j9OutputSchema(),
		Invoker:      in.Judgment,
	})
	switch {
	case errors.Is(err, ErrStoreError) || ctx.Err() != nil:
		return pairJudgment{}, err
	case err != nil:
		return pairJudgment{Outcome: PairOutcomeFailed, Reason: "判断を呼べなかった: " + err.Error()}, nil
	case ns != nil:
		return pairJudgment{Outcome: PairOutcomeBudget, Reason: "周の上限または枠超過で判断を呼ばなかった（" + string(ns.Reason) + "）"}, nil
	}
	switch run.Result {
	case RunResultSucceeded:
	case RunResultBudgetExhausted:
		return pairJudgment{Outcome: PairOutcomeBudget, Reason: "1 組あたりの費用の上限で打ち切られた"}, nil
	default:
		if run.RateLimited {
			return pairJudgment{Outcome: PairOutcomeBudget, Reason: "枠超過で判断できなかった"}, nil
		}
		return pairJudgment{Outcome: PairOutcomeFailed, Reason: "判断の呼び出しが " + string(run.Result) + " で終わった"}, nil
	}
	verdict, reason, ok := validateJ9Output(run.StructuredOutput)
	if !ok {
		if err := s.invalidateRunOutput(ctx, run.RunID); err != nil {
			return pairJudgment{}, err
		}
		return pairJudgment{Outcome: PairOutcomeFailed, Reason: "判断の出力が不正だった"}, nil
	}
	return pairJudgment{Outcome: PairOutcome(verdict), Reason: reason}, nil
}

// prepareJ9 は J9 の入力の区画（2 つの計画と予測の結果）を作る。
func prepareJ9(p judgedPair, prediction string) []JudgmentDataSection {
	return []JudgmentDataSection{
		{Label: "対象 A の計画", Content: planText(p.a)},
		{Label: "対象 B の計画", Content: planText(p.b)},
		{Label: "両方が触る対象の予測", Content: prediction},
	}
}

func planText(c planCandidate) string {
	body := strings.TrimSpace(c.Plan)
	if body == "" {
		body = "（計画の本文が無い）"
	}
	return fmt.Sprintf("課題 %s\n%s", c.ID, body)
}

// pairPredictionText は判断に渡す、組の予測の結果の文（予測が無ければその旨）。
func pairPredictionText(out *ConflictPredictionOutput, a, b planCandidate) string {
	if out == nil {
		return "予測は無い。2 つの計画だけで判断する。"
	}
	for _, pr := range out.Pairs {
		if (pr.Issues[0] != a.Issue || pr.Issues[1] != b.Issue) && (pr.Issues[0] != b.Issue || pr.Issues[1] != a.Issue) {
			continue
		}
		var sb strings.Builder
		if len(pr.SharedFiles) == 0 {
			sb.WriteString("両方が触る対象: 無し\n")
		} else {
			sb.WriteString("両方が触る対象:\n")
			for _, f := range pr.SharedFiles {
				extra := ""
				if f.MergeFriendly {
					extra += "（機械的に合わせやすい種類）"
				}
				if f.Ignored {
					extra += "（追跡の対象外）"
				}
				fmt.Fprintf(&sb, "- %s%s\n", f.Path, extra)
			}
		}
		if pr.DependencyFirst != nil {
			fmt.Fprintf(&sb, "先に入れるべき側: 課題の Issue 番号 %d\n", *pr.DependencyFirst)
		}
		for _, e := range pr.DependencyStated {
			fmt.Fprintf(&sb, "前提の申告: Issue 番号 %d は %d を前提にしている\n", e.Issue, e.DependsOn)
		}
		return strings.TrimRight(sb.String(), "\n")
	}
	return "この組の予測は得られなかった。2 つの計画だけで判断する。"
}

// judgeRepo は判断の対象のリポジトリ 1 つ（予測までを終えたもの）。cands は起動の順。
type judgeRepo struct {
	repo    ConnectorRepo
	running []planRunning
	cands   []planCandidate
	pred    *repoPrediction
}

// judgesPairs はこのリポジトリが組の判断（J9）を使うか。
func (r *judgeRepo) judgesPairs() bool {
	return r.pred.UndeclaredJudged || (r.pred.SerialJudgment && r.pred.Output != nil)
}

// slotRoom はこのリポジトリのスロットの数から、動いている課題の数を引いた数（スロットの宣言が無ければ上限なし）。
func (r *judgeRepo) slotRoom() int {
	slots := r.repo.Slots.Count
	if r.repo.Slots.Provider == string(slotProviderClone) {
		slots = len(r.repo.Slots.Paths)
	}
	if slots <= 0 {
		return math.MaxInt32
	}
	if slots -= len(r.running); slots < 0 {
		slots = 0
	}
	return slots
}

// globalDelegationFree は全体の同時委譲数の上限（max_parallel_runs）から、全リポジトリで動いている委譲の数を
// 引いた数（上限の宣言が無ければ上限なし）。
func (s *Store) globalDelegationFree(ctx context.Context, in DelegateInput) (int, error) {
	k := in.AgentDecl.MaxParallelRuns
	if k <= 0 {
		return math.MaxInt32, nil
	}
	var active int
	err := s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		active, err = activeDelegationCount(ctx, tx)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return 0, err
	}
	if k -= active; k < 0 {
		k = 0
	}
	return k, nil
}

// predFor は head（このリポジトリの起動しうる先頭の候補）での直列化グループを作るための予測の結果を返す。
func (r *judgeRepo) predFor(head []planCandidate, verdicts map[pairKey]pairJudgment) repoPrediction {
	p := *r.pred
	if !r.judgesPairs() {
		return p
	}
	seqs := map[int64]bool{}
	for _, c := range r.cands {
		seqs[c.Seq] = true
	}
	p.Verdicts = map[pairKey]pairJudgment{}
	for k, v := range verdicts {
		if seqs[k[0]] && seqs[k[1]] {
			p.Verdicts[k] = v
		}
	}
	p.Free = len(head)
	return p
}

// startCount は head から同時に起動しうる数（動いている課題を含まない直列化グループの数。動いている課題を含む
// グループの候補は、判断するリポジトリでだけ候補の数だけ枠を使うものとして数える）。
func (r *judgeRepo) startCount(head []planCandidate, verdicts map[pairKey]pairJudgment) int {
	if len(head) == 0 {
		return 0
	}
	n := 0
	for _, g := range buildRepoGroups(r.repo.Name, r.running, head, r.predFor(head, verdicts)) {
		if len(g.Running) == 0 {
			n++
		} else if r.judgesPairs() {
			// 動いている課題と同じグループの候補は起動しないが、判断するリポジトリでは枠が空いたと数えると
			// 同じグループへ候補が際限なく入り、起動しえない組まで判断するので、候補の数だけ枠を使うものとして数える
			// （判断しないリポジトリは組を判断しないので、起動しない候補は枠を使わない）。
			n += len(g.Candidates)
		}
	}
	return n
}

// newPairs は head の候補どうしの組のうち、まだ判断していない判断に回す組を返す。
func (r *judgeRepo) newPairs(head []planCandidate, pos map[int64]int, done map[pairKey]pairJudgment) []judgedPair {
	if len(head) < 2 {
		return nil
	}
	mk := func(i, j int, mutual bool, out *ConflictPredictionOutput) judgedPair {
		return judgedPair{a: head[i], b: head[j], posA: pos[head[i].Seq], posB: pos[head[j].Seq], mutual: mutual, out: out}
	}
	var pairs []judgedPair
	if r.pred.UndeclaredJudged {
		for i := 0; i < len(head); i++ {
			for j := i + 1; j < len(head); j++ {
				if _, ok := done[newPairKey(head[i], head[j])]; !ok {
					pairs = append(pairs, mk(i, j, false, nil))
				}
			}
		}
		return pairs
	}
	idx := map[int]int{}
	for i, c := range head {
		if c.Issue > 0 {
			idx[c.Issue] = i
		}
	}
	seen := map[pairKey]bool{}
	for _, e := range predictedEdges(r.pred.Output.Pairs, func(n int) bool { _, ok := idx[n]; return ok }, true) {
		if e.first != 0 || (!hasReason(e.reasons, SerialReasonSharedFiles) && !hasReason(e.reasons, SerialReasonMergeCandidate)) {
			continue
		}
		i, j := idx[e.a], idx[e.b]
		if i > j {
			i, j = j, i
		}
		key := newPairKey(head[i], head[j])
		if _, ok := done[key]; ok || seen[key] {
			continue
		}
		seen[key] = true
		pairs = append(pairs, mk(i, j, e.mutual, r.pred.Output))
	}
	return pairs
}

// judgeAcrossRepos は全リポジトリの候補を起動の順に 1 列に並べ、全体の空き（max_parallel_runs から動いている
// 委譲の数を引いた数）と各リポジトリのスロットの空きの範囲で、上から起動しうる候補を取り、その候補どうしの
// 組を判断させる。判断の結果（直列）で空いた枠は列の次の候補へ回し、新しく入った候補との組を判断する
// （費用の上限の残りの分だけ）。判断の結果は各リポジトリの予測の結果（Verdicts・Free）へ入れる。
func (s *Store) judgeAcrossRepos(ctx context.Context, in DelegateInput, repos []*judgeRepo) error {
	judging := false
	for _, r := range repos {
		if r.judgesPairs() {
			judging = true
		}
	}
	if !judging {
		return nil
	}
	free, err := s.globalDelegationFree(ctx, in)
	if err != nil {
		return err
	}
	type entry struct {
		c planCandidate
		r *judgeRepo
	}
	var order []entry
	for _, r := range repos {
		for _, c := range r.cands {
			order = append(order, entry{c, r})
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].c.before(order[j].c) })
	pos := map[int64]int{}
	for i, e := range order {
		pos[e.c.Seq] = i
	}
	taken := make([]bool, len(order))
	heads := map[*judgeRepo][]planCandidate{}
	verdicts := map[pairKey]pairJudgment{}
	mutualPairs := map[pairKey]judgedPair{}
	for {
		room := free
		starts := map[*judgeRepo]int{}
		for _, r := range repos {
			starts[r] = r.startCount(heads[r], verdicts)
			room -= starts[r]
		}
		added := map[*judgeRepo]int{}
		grew := false
		for i, e := range order {
			if room <= 0 {
				break
			}
			if taken[i] || e.r.slotRoom()-starts[e.r]-added[e.r] <= 0 {
				continue
			}
			taken[i] = true
			heads[e.r] = append(heads[e.r], e.c)
			added[e.r]++
			room--
			grew = true
		}
		if !grew {
			break
		}
		var pairs []judgedPair
		for _, r := range repos {
			if !r.judgesPairs() {
				continue
			}
			for _, p := range r.newPairs(heads[r], pos, verdicts) {
				if p.mutual {
					// 互いに前提にし合う組は判断の結果に関わらず直列でグループを変えないので、ループを抜けた後に
					// 残りの予算でまとめて判断する（ほかの組の予算を奪わない）。
					mutualPairs[newPairKey(p.a, p.b)] = p
					continue
				}
				pairs = append(pairs, p)
			}
		}
		sortJudgedPairs(pairs)
		got, err := s.judgePairs(ctx, in, pairs, func(p judgedPair) string { return pairPredictionText(p.out, p.a, p.b) })
		if err != nil {
			return err
		}
		for k, v := range got {
			verdicts[k] = v
		}
	}
	var last []judgedPair
	for _, p := range mutualPairs {
		last = append(last, p)
	}
	sortJudgedPairs(last)
	got, err := s.judgePairs(ctx, in, last, func(p judgedPair) string { return pairPredictionText(p.out, p.a, p.b) })
	if err != nil {
		return err
	}
	for k, v := range got {
		verdicts[k] = v
	}
	for _, r := range repos {
		if r.judgesPairs() {
			*r.pred = r.predFor(heads[r], verdicts)
		}
	}
	return nil
}

// sortJudgedPairs は判断の順に並べる: 互いに前提にし合う組を最後に回し、それぞれの中は、列の上位どうしの組
// （後ろの候補の位置、続けて前の候補の位置が小さい組）から。
func sortJudgedPairs(ps []judgedPair) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].mutual != ps[j].mutual {
			return !ps[i].mutual
		}
		if ps[i].posB != ps[j].posB {
			return ps[i].posB < ps[j].posB
		}
		return ps[i].posA < ps[j].posA
	})
}

func hasReason(rs []SerialGroupReason, r SerialGroupReason) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

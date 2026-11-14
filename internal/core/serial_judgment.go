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

// judgedPair は判断に回す組（a が起動の順で先）。
type judgedPair struct {
	a, b       planCandidate
	idxA, idxB int
}

// serialJudgmentCapacity は repo でこの周に同時に起動しうる候補の数（全体の同時委譲数とリポジトリの
// スロットの数の小さい方から、動いている課題の数を引く）。判断する組はこの数までの候補の組だけ。
func serialJudgmentCapacity(in DelegateInput, repo ConnectorRepo, running int) int {
	k := in.AgentDecl.MaxParallelRuns
	slots := repo.Slots.Count
	if repo.Slots.Provider == string(slotProviderClone) {
		slots = len(repo.Slots.Paths)
	}
	if slots > 0 && (k <= 0 || slots < k) {
		k = slots
	}
	if k -= running; k < 0 {
		k = 0
	}
	return k
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

// capacityCandidates は、起動の順に並べた候補のうち同時に起動しうる先頭の free 件を返す。
func capacityCandidates(sorted []planCandidate, free int) []planCandidate {
	if free > len(sorted) {
		free = len(sorted)
	}
	if free < 0 {
		free = 0
	}
	return sorted[:free]
}

// judgeDeclaredPairs は予測の宣言があるリポジトリで、判断に回す組（候補どうしで、先後の依存が無く、
// 両方が触る対象がある組、または互いに前提にし合う組。起動しうる数までの候補の組だけ）を判断させる。
func (s *Store) judgeDeclaredPairs(ctx context.Context, in DelegateInput, repo ConnectorRepo, running []planRunning, cands []planCandidate, out *ConflictPredictionOutput) (map[pairKey]pairJudgment, error) {
	sorted := append([]planCandidate(nil), cands...)
	sortCandidates(sorted)
	head := capacityCandidates(sorted, serialJudgmentCapacity(in, repo, len(running)))
	if len(head) < 2 {
		return nil, nil
	}
	idx := map[int]int{}
	for i, c := range head {
		if c.Issue > 0 {
			idx[c.Issue] = i
		}
	}
	seen := map[pairKey]bool{}
	var pairs []judgedPair
	for _, e := range predictedEdges(out.Pairs, func(n int) bool { _, ok := idx[n]; return ok }, true) {
		if e.first != 0 || (!hasReason(e.reasons, SerialReasonSharedFiles) && !hasReason(e.reasons, SerialReasonMergeCandidate)) {
			continue
		}
		i, j := idx[e.a], idx[e.b]
		if i > j {
			i, j = j, i
		}
		key := newPairKey(head[i], head[j])
		if seen[key] {
			continue
		}
		seen[key] = true
		pairs = append(pairs, judgedPair{a: head[i], b: head[j], idxA: i, idxB: j})
	}
	sortJudgedPairs(pairs)
	return s.judgePairs(ctx, in, pairs, func(p judgedPair) string { return pairPredictionText(out, p.a, p.b) })
}

// judgeUndeclaredPairs は予測の宣言が無いリポジトリで、起動しうる数までの候補の全部の組を判断させる。
func (s *Store) judgeUndeclaredPairs(ctx context.Context, in DelegateInput, repo ConnectorRepo, cands []planCandidate) (map[pairKey]pairJudgment, int, error) {
	sorted := append([]planCandidate(nil), cands...)
	sortCandidates(sorted)
	free := serialJudgmentCapacity(in, repo, 0)
	head := capacityCandidates(sorted, free)
	var pairs []judgedPair
	for i := 0; i < len(head); i++ {
		for j := i + 1; j < len(head); j++ {
			pairs = append(pairs, judgedPair{a: head[i], b: head[j], idxA: i, idxB: j})
		}
	}
	v, err := s.judgePairs(ctx, in, pairs, func(p judgedPair) string { return pairPredictionText(nil, p.a, p.b) })
	return v, len(head), err
}

func sortJudgedPairs(ps []judgedPair) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].idxA != ps[j].idxA {
			return ps[i].idxA < ps[j].idxA
		}
		return ps[i].idxB < ps[j].idxB
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

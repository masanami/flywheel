package core

// このファイルは委譲の段の実行計画（親要件チケット #98 §実行スロット「同じリポジトリの並列と
// 直列化グループ」・M3P25〜M3P34・M3P43・M3H8）を持つ: 委譲の候補と実行中の課題の収集、
// 衝突の予測の口の呼び出し（リポジトリごとに 1 回）、直列化グループの決定（serial_group.go の
// 純粋な規則）、グループごとの並行な実行、同時の起動の上限とスロットの枠待ち。
// 個別の `flywheel run`（ID の指定の有無を問わない）も Store.RunDelegation を通るので、
// 同じ処理に乗る。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// NotStartedSerialized は、実行中の課題と同じ直列化グループに入ったため起動しなかったことを
// 表す（S2。M3P31）。
const NotStartedSerialized NotStartedReason = "serialized"

// ErrSerialized は、ID を指定した `run` の課題が、終了していない委譲の run を持つ課題と同じ
// 直列化グループに入ったことを表す（M3P43。CLI の `serialized`）。
var ErrSerialized = errors.New("core: the challenge is in the same serial group as a running delegation")

// 予測の口へ渡す Issue の件数の範囲（口の受け付ける件数。仮定）。
const (
	minPredictionIssues = 2
	maxPredictionIssues = 20
)

// delegationWaitInterval は枠待ちの再評価の間隔（テストが短くする）。
var delegationWaitInterval = slotWaitInterval

// delegationPlan は委譲の段の実行計画。
type delegationPlan struct {
	groups []plannedGroup
}

func (p *delegationPlan) serialGroups() []SerialGroup {
	out := make([]SerialGroup, 0, len(p.groups))
	for i := range p.groups {
		out = append(out, p.groups[i].public())
	}
	return out
}

// groupOf は課題 id を候補に持つグループを返す。
func (p *delegationPlan) groupOf(id string) *plannedGroup {
	for i := range p.groups {
		for _, c := range p.groups[i].Candidates {
			if c.ID == id {
				return &p.groups[i]
			}
		}
	}
	return nil
}

// planOne は planDelegation が候補 1 件について読む事実。
type planOne struct {
	cand planCandidate
	repo string
}

// loadPlanCandidate は課題 cid を委譲の候補として計画に載せるための事実を読む。承認済みの計画が
// 今の宣言と合わない（委譲できない）課題は ok=false。
func (s *Store) loadPlanCandidate(ctx context.Context, in DelegateInput, cid int64) (planOne, bool, error) {
	var ch *Challenge
	var plan approvedPlan
	var planOK bool
	var binding *sourceBinding
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		if ch, err = loadChallenge(ctx, tx, cid); err != nil {
			return err
		}
		if plan, planOK, err = loadApprovedPlan(ctx, tx, cid); err != nil || !planOK {
			return err
		}
		binding, err = loadSourceBindingByChallengeID(ctx, tx, cid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return planOne{}, false, err
	}
	if ch == nil || !planOK {
		return planOne{}, false, nil
	}
	validated, ok := validateJ2Output([]byte(plan.Spec), j2ValidationContext{Agent: in.AgentDecl, Conn: in.ConnDecl, HasSource: binding != nil})
	if !ok || validated.Verdict != J2VerdictPlan {
		return planOne{}, false, nil
	}
	repo, connector := in.ConnDecl.findConnectorRepo(validated.Repo)
	if repo == nil || connector == nil {
		return planOne{}, false, nil
	}
	c := planCandidate{ID: ch.ID, Rank: priorityRank(ch.Priority), Seq: cid, Issue: sourceIssueNumber(binding, repo.Remote)}
	return planOne{cand: c, repo: repo.Name}, true, nil
}

// sourceIssueNumber は取り込み元の対応から予測の口へ渡せる Issue 番号を返す（渡せなければ 0）。
// 取り込み元の Issue が対象リポジトリ（remote）の Issue でなければ、番号は別の Issue を指すので
// 渡せない（0）。
func sourceIssueNumber(sb *sourceBinding, remote string) int {
	if sb == nil {
		return 0
	}
	srcRepo, n, ok := parseExternalKey(sb.ExternalKey)
	if !ok || n <= 0 || !strings.EqualFold(srcRepo, remote) {
		return 0
	}
	return n
}

// loadRunningDelegations は、その周より前から終了していない委譲の run を持つ課題を、リポジトリごとに
// 読む（スロットのリポジトリ。スロットが無ければ承認済みの計画のリポジトリ）。
func (s *Store) loadRunningDelegations(ctx context.Context, in DelegateInput) (map[string][]planRunning, error) {
	cycleInt, _ := parseCycleID(in.CycleID)
	type raw struct {
		cid  int64
		repo string
	}
	var raws []raw
	var bindings = map[int64]*sourceBinding{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT r.challenge_id, COALESCE(sl.repo, '') FROM run r LEFT JOIN slot sl ON sl.id = r.slot_id
			 WHERE r.kind = 'delegate' AND r.result IS NULL AND (r.cycle_id IS NULL OR r.cycle_id <> ?)
			 ORDER BY r.challenge_id ASC`, cycleInt)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x raw
			if err := rows.Scan(&x.cid, &x.repo); err != nil {
				_ = rows.Close()
				return err
			}
			raws = append(raws, x)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for i := range raws {
			b, err := loadSourceBindingByChallengeID(ctx, tx, raws[i].cid)
			if err != nil {
				return err
			}
			bindings[raws[i].cid] = b
			if raws[i].repo != "" {
				continue
			}
			if p, ok, err := loadApprovedPlan(ctx, tx, raws[i].cid); err != nil {
				return err
			} else if ok {
				if v, ok := validateJ2Output([]byte(p.Spec), j2ValidationContext{Agent: in.AgentDecl, Conn: in.ConnDecl, HasSource: b != nil}); ok {
					raws[i].repo = v.Repo
				}
			}
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	remoteOf := map[string]string{}
	for _, r := range in.ConnDecl.Repos {
		remoteOf[r.Name] = r.Remote
	}
	out := map[string][]planRunning{}
	seen := map[int64]bool{}
	for _, x := range raws {
		if x.repo == "" || seen[x.cid] {
			continue
		}
		seen[x.cid] = true
		out[x.repo] = append(out[x.repo], planRunning{ID: formatChallengeID(x.cid), Issue: sourceIssueNumber(bindings[x.cid], remoteOf[x.repo]), Seq: x.cid})
	}
	return out, nil
}

// predictionWorkDir は予測の口の作業ディレクトリを返す。worktree は元のクローン、clone は
// 状態が idle の作業用クローンのうち paths の順で最初のもの。idle が無ければ ok=false。
func (s *Store) predictionWorkDir(ctx context.Context, repo ConnectorRepo) (string, bool, error) {
	if slotProvider(repo.Slots.Provider) == slotProviderWorktree {
		return filepath.Join(s.workspace, repo.Slots.Base), true, nil
	}
	if err := s.EnsureSlots(ctx, repo); err != nil {
		return "", false, err
	}
	var rows []slotRow
	err := s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		rows, err = loadSlotRows(ctx, tx)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return "", false, err
	}
	for _, rel := range repo.Slots.Paths {
		p := filepath.Join(s.workspace, rel)
		for _, r := range rows {
			if r.Repo == repo.Name && r.Provider == slotProviderClone && r.Path == p && r.State == slotStateIdle {
				return p, true, nil
			}
		}
	}
	return "", false, nil
}

// predictRepo は repo の予測の結果（または予測が得られなかった理由）を決める。予測の口は、
// 渡す Issue が 2〜20 件で予測できる候補が 1 件以上あるときだけ、1 回呼ぶ。
func (s *Store) predictRepo(ctx context.Context, in DelegateInput, repo ConnectorRepo, connector *Connector, running []planRunning, cands []planCandidate) (repoPrediction, error) {
	var reasons []SerialGroupReason
	add := func(r SerialGroupReason) {
		for _, x := range reasons {
			if x == r {
				return
			}
		}
		reasons = append(reasons, r)
	}
	var send []int
	rs := append([]planRunning(nil), running...)
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Seq < rs[j].Seq })
	for _, r := range rs {
		if r.Issue == 0 {
			add(SerialReasonNotPredictable)
		} else {
			send = append(send, r.Issue)
		}
	}
	sorted := append([]planCandidate(nil), cands...)
	sortCandidates(sorted)
	predictable := 0
	for _, c := range sorted {
		if c.Issue == 0 {
			add(SerialReasonNotPredictable)
		} else {
			send = append(send, c.Issue)
			predictable++
		}
	}
	if len(send) > maxPredictionIssues {
		add(SerialReasonNotPredictable)
	}
	if len(reasons) > 0 {
		return repoPrediction{FailClosed: reasons}, nil
	}
	if len(send) < minPredictionIssues || predictable < 1 {
		return repoPrediction{}, nil
	}
	if connector.ConflictPrediction == nil {
		return repoPrediction{FailClosed: []SerialGroupReason{SerialReasonNoPredictionDeclare}}, nil
	}
	if in.Predictor == nil {
		return repoPrediction{FailClosed: []SerialGroupReason{SerialReasonPredictionFailed}}, nil
	}
	workDir, ok, err := s.predictionWorkDir(ctx, repo)
	if err != nil {
		return repoPrediction{}, err
	}
	if !ok {
		return repoPrediction{FailClosed: []SerialGroupReason{SerialReasonPredictionFailed}}, nil
	}
	res, err := s.runPrediction(ctx, predictRunInput{
		CycleID: in.CycleID, Repo: repo.Name, Declaration: *connector.ConflictPrediction, WorkDir: workDir, Issues: send,
		MaxBudgetUSD: float64(len(send)) * in.AgentDecl.ConflictPredictionBudgetUSD,
		TimeoutSec:   in.AgentDecl.TimeoutSec.Judgment, Predictor: in.Predictor,
	})
	switch {
	case errors.Is(err, ErrBudgetExceeded):
		return repoPrediction{FailClosed: []SerialGroupReason{SerialReasonPredictionBudget}}, nil
	case err != nil:
		return repoPrediction{}, err
	}
	if res.Output == nil {
		return repoPrediction{FailClosed: []SerialGroupReason{SerialReasonPredictionFailed}}, nil
	}
	status := map[int]string{}
	for _, i := range res.Output.Issues {
		status[i.Issue] = i.Status
	}
	for _, n := range send {
		switch status[n] {
		case predictIssuePredicted:
		case predictIssueBudgetExhausted:
			add(SerialReasonPredictionBudget)
		default: // failed、または出力に無い
			add(SerialReasonPredictionFailed)
		}
	}
	if len(reasons) > 0 {
		sort.SliceStable(reasons, func(i, j int) bool { return reasonIndex(reasons[i]) < reasonIndex(reasons[j]) })
		return repoPrediction{FailClosed: reasons, HeadSHA: res.Output.HeadSHA}, nil
	}
	return repoPrediction{Output: res.Output, HeadSHA: res.Output.HeadSHA}, nil
}

func reasonIndex(r SerialGroupReason) int {
	for i, x := range serialGroupReasonOrder {
		if x == r {
			return i
		}
	}
	return len(serialGroupReasonOrder)
}

// planDelegation は targets（課題の内部整数 ID）の委譲の実行計画を作る: 候補をリポジトリごとに
// 集め、実行中の課題を読み、予測の口を呼び、直列化グループを決める。委譲できない課題
// （承認済みの計画が宣言と合わない）は候補にしない。
func (s *Store) planDelegation(ctx context.Context, in DelegateInput, targets []int64) (*delegationPlan, error) {
	if err := s.ReapInterruptedRuns(ctx); err != nil {
		return nil, err
	}
	byRepo := map[string][]planCandidate{}
	for _, cid := range targets {
		one, ok, err := s.loadPlanCandidate(ctx, in, cid)
		if err != nil {
			return nil, err
		}
		if ok {
			byRepo[one.repo] = append(byRepo[one.repo], one.cand)
		}
	}
	plan := &delegationPlan{}
	if len(byRepo) == 0 {
		return plan, nil
	}
	running, err := s.loadRunningDelegations(ctx, in)
	if err != nil {
		return nil, err
	}
	for i := range in.ConnDecl.Repos {
		repo := in.ConnDecl.Repos[i]
		cands := byRepo[repo.Name]
		if len(cands) == 0 {
			continue
		}
		_, connector := in.ConnDecl.findConnectorRepo(repo.Name)
		if connector == nil {
			continue
		}
		if err := s.EnsureSlots(ctx, repo); err != nil {
			if errors.Is(err, ErrValidation) {
				continue // 委譲できない（従来どおり、この課題は起動せずに飛ばす）
			}
			return nil, err
		}
		pred, err := s.predictRepo(ctx, in, repo, connector, running[repo.Name], cands)
		if err != nil {
			return nil, err
		}
		plan.groups = append(plan.groups, buildRepoGroups(repo.Name, running[repo.Name], cands, pred)...)
	}
	return plan, nil
}

// --- 同時の起動の上限と枠待ち（M3P33） ---

// schedWaiter は枠を待つグループの先頭の課題。
type schedWaiter struct {
	repo   ConnectorRepo
	rank   int
	seq    int64
	queued bool
}

// delegationScheduler は 1 回の RunDelegation の中の起動を、全リポジトリの合計
// （max_parallel_runs）とリポジトリごとの使えるスロットの数で制限する。他のプロセスが起動した
// 終了していない委譲の run も、ストアを読んで数える。
type delegationScheduler struct {
	s           *Store
	maxParallel int

	mu      sync.Mutex
	pre     int // 許可したが run がまだストアに無い起動
	preRepo map[string]int
	own     int // 許可して、まだ終えていない起動
	waiters []*schedWaiter
}

func newDelegationScheduler(s *Store, in DelegateInput) *delegationScheduler {
	return &delegationScheduler{
		s: s, maxParallel: in.AgentDecl.MaxParallelRuns, preRepo: map[string]int{},
	}
}

func (sch *delegationScheduler) newWaiter(repo ConnectorRepo, c planCandidate) *schedWaiter {
	return &schedWaiter{repo: repo, rank: c.Rank, seq: c.Seq}
}

// enqueue は w を待ちの列へ入れる（優先度・ID の昇順）。
func (sch *delegationScheduler) enqueue(w *schedWaiter) {
	sch.mu.Lock()
	defer sch.mu.Unlock()
	sch.enqueueLocked(w)
}

func (sch *delegationScheduler) enqueueLocked(w *schedWaiter) {
	if w.queued {
		return
	}
	w.queued = true
	i := sort.Search(len(sch.waiters), func(i int) bool {
		x := sch.waiters[i]
		return x.rank > w.rank || (x.rank == w.rank && x.seq > w.seq)
	})
	sch.waiters = append(sch.waiters, nil)
	copy(sch.waiters[i+1:], sch.waiters[i:])
	sch.waiters[i] = w
}

func (sch *delegationScheduler) dequeue(w *schedWaiter) {
	sch.mu.Lock()
	defer sch.mu.Unlock()
	sch.dequeueLocked(w)
}

func (sch *delegationScheduler) dequeueLocked(w *schedWaiter) {
	if !w.queued {
		return
	}
	w.queued = false
	for i, x := range sch.waiters {
		if x == w {
			sch.waiters = append(sch.waiters[:i], sch.waiters[i+1:]...)
			return
		}
	}
}

type slotCapacity struct{ usable, dbTotal, dbRepo int }

// capacity は repo の使えるスロット（needs_attention でないもの。宣言に対して行がまだ無い
// 未払い出しのスロットを含む）の数と、終了していない委譲の run の数（全体・リポジトリ）を読む。
func (sch *delegationScheduler) capacity(ctx context.Context, repo ConnectorRepo) (slotCapacity, error) {
	var c slotCapacity
	err := sch.s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := loadSlotRows(ctx, tx)
		if err != nil {
			return err
		}
		cands := slotCandidates(rows, repo, sch.s.workspace)
		for _, r := range cands {
			if r.State != slotStateNeedsAttention {
				c.usable++
			}
		}
		declared := repo.Slots.Count
		if slotProvider(repo.Slots.Provider) == slotProviderClone {
			declared = len(repo.Slots.Paths)
		}
		if declared > len(cands) {
			c.usable += declared - len(cands)
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run WHERE kind = 'delegate' AND result IS NULL`).Scan(&c.dbTotal); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM run r JOIN slot sl ON sl.id = r.slot_id
			 WHERE r.kind = 'delegate' AND r.result IS NULL AND sl.repo = ?`, repo.Name).Scan(&c.dbRepo)
	})
	return c, classifyReadWriteErr(err)
}

func (sch *delegationScheduler) fitsLocked(c slotCapacity, repo string) bool {
	return c.usable > 0 && c.dbTotal+sch.pre < sch.maxParallel && c.dbRepo+sch.preRepo[repo] < c.usable
}

// admission は許可した起動 1 件。nil に対するメソッドは何もしない。
type admission struct {
	sch      *delegationScheduler
	repo     string
	inserted bool
	finished bool
}

// markInserted は run がストアに記録された（以後は DB の数に入る）ことを知らせる。
func (a *admission) markInserted() {
	if a == nil {
		return
	}
	a.sch.mu.Lock()
	defer a.sch.mu.Unlock()
	if !a.inserted {
		a.inserted = true
		a.sch.pre--
		a.sch.preRepo[a.repo]--
	}
}

// finish は起動を終えた（run が終わった、または起動しなかった）ことを知らせる。
func (a *admission) finish() {
	if a == nil {
		return
	}
	a.markInserted()
	a.sch.mu.Lock()
	defer a.sch.mu.Unlock()
	if !a.finished {
		a.finished = true
		a.sch.own--
	}
}

// admit は w の起動を許可するまで待つ。許可は、全リポジトリの終了していない委譲の run が
// max_parallel_runs 未満で、そのリポジトリの終了していない委譲の run が使えるスロット未満のとき。
// 使えるスロットが 1 本も無ければ待たずに ErrSlotUnavailable。wait が false なら、許可できなければ
// 待たずに ErrSlotUnavailable。待つのは、この呼び出しが起動した終了していない run が枠を占めている間
// だけ（【仮定】他のプロセスの run は、いつ終わるか分からず周を委譲の時間の上限まで止めうるので、
// 待たずに ErrSlotUnavailable にする）。複数が待つときは、先頭の課題の優先度・ID の昇順に許可する。
func (sch *delegationScheduler) admit(ctx context.Context, w *schedWaiter, wait bool) (*admission, error) {
	defer sch.dequeue(w)
	for {
		sch.mu.Lock()
		sch.enqueueLocked(w)
		c, err := sch.capacity(ctx, w.repo)
		if err != nil {
			sch.mu.Unlock()
			return nil, err
		}
		if c.usable == 0 {
			sch.mu.Unlock()
			return nil, ErrSlotUnavailable
		}
		fits := sch.fitsLocked(c, w.repo.Name)
		if fits {
			blocked := false
			for _, e := range sch.waiters {
				if e == w {
					break
				}
				ec, err := sch.capacity(ctx, e.repo)
				if err != nil {
					sch.mu.Unlock()
					return nil, err
				}
				if sch.fitsLocked(ec, e.repo.Name) {
					blocked = true
					break
				}
			}
			if !blocked {
				sch.pre++
				sch.preRepo[w.repo.Name]++
				sch.own++
				sch.mu.Unlock()
				return &admission{sch: sch, repo: w.repo.Name}, nil
			}
		}
		own := sch.own
		sch.mu.Unlock()
		// 枠が空いていて先に待つ課題に譲るだけなら、その課題がすぐ許可されるので待つ。枠が埋まって
		// いるのに、この呼び出しが起動した run が 1 つも無ければ、待っても空かない（他のプロセスの run）。
		if !wait || (!fits && own == 0) {
			return nil, ErrSlotUnavailable
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delegationWaitInterval):
		}
	}
}

// --- グループごとの並行な実行 ---

// delegateRun は delegateOne へ渡す、スケジューラと待ちの列の中の自分（nil なら制限しない）。
type delegateRun struct {
	sch *delegationScheduler
	w   *schedWaiter
	// waitCtx は枠待ちを打ち切る ctx（nil なら呼び出しの ctx）。
	waitCtx context.Context
}

// admit は枠の許可を得る。jc が nil（ID を指定した個別の操作）なら待たない。
func (r *delegateRun) admit(ctx context.Context, jc *JudgmentCycle) (*admission, error) {
	if r == nil || r.sch == nil {
		return nil, nil
	}
	if r.waitCtx != nil {
		ctx = r.waitCtx
	}
	return r.sch.admit(ctx, r.w, jc != nil)
}

type delegateOutcome struct {
	rank int
	seq  int64
	item *JudgmentAutoItem
	ns   *NotStarted
}

// executePlan は plan のグループを、グループごとに並行に（グループの中は順に）実行する。
func (s *Store) executePlan(ctx context.Context, in DelegateInput, jc *JudgmentCycle, plan *delegationPlan) (*DelegateResult, error) {
	sch := newDelegationScheduler(s, in)
	// 致命的なエラーでは、新しい起動と枠待ちだけを止める（すでに動いている他のグループの委譲は、
	// 親の ctx のまま最後まで動かして記録する）。
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	var outcomes []delegateOutcome
	var firstErr error
	record := func(o delegateOutcome) {
		mu.Lock()
		outcomes = append(outcomes, o)
		mu.Unlock()
	}
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		mu.Unlock()
	}

	// 先頭の課題は、グループを並行に始める前に待ちの列へ入れる（同時に始めたとき、優先度・ID の
	// 昇順を守るため）。
	heads := make([]*schedWaiter, len(plan.groups))
	repos := make([]ConnectorRepo, len(plan.groups))
	for i := range plan.groups {
		g := &plan.groups[i]
		repo, _ := in.ConnDecl.findConnectorRepo(g.Repo)
		if repo == nil {
			continue
		}
		repos[i] = *repo
		if len(g.Running) == 0 && len(g.Candidates) > 0 {
			heads[i] = sch.newWaiter(*repo, g.Candidates[0])
			sch.enqueue(heads[i])
		}
	}

	var wg sync.WaitGroup
	for i := range plan.groups {
		g := plan.groups[i]
		head := heads[i]
		repo := repos[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if head != nil {
				defer sch.dequeue(head)
			}
			for j, c := range g.Candidates {
				if len(g.Running) > 0 {
					record(delegateOutcome{rank: c.Rank, seq: c.Seq, ns: &NotStarted{ChallengeID: c.ID, Reason: NotStartedSerialized}})
					continue
				}
				if waitCtx.Err() != nil {
					return
				}
				ch, err := s.loadChallengeForAuto(ctx, c.ID)
				if err != nil {
					fail(err)
					return
				}
				if ch.Status != StatusInProgress {
					if j == 0 && head != nil {
						sch.dequeue(head)
					}
					continue
				}
				w := head
				if j > 0 || w == nil {
					w = sch.newWaiter(repo, c)
					sch.enqueue(w)
				}
				item, ns, err := s.delegateOne(ctx, in, jc, *ch, &delegateRun{sch: sch, w: w, waitCtx: waitCtx})
				sch.dequeue(w)
				if err != nil {
					// 1 件の課題の問題（承認済みの計画が今の宣言と合わない等）で、他の課題の委譲を
					// 止めない。ストアの障害など、それ以外のエラーだけが全体を打ち切る。
					if errors.Is(err, ErrRunInProgress) || errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrValidation) {
						continue
					}
					fail(err)
					return
				}
				record(delegateOutcome{rank: c.Rank, seq: c.Seq, item: item, ns: ns})
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	sort.SliceStable(outcomes, func(i, j int) bool {
		if outcomes[i].rank != outcomes[j].rank {
			return outcomes[i].rank < outcomes[j].rank
		}
		return outcomes[i].seq < outcomes[j].seq
	})
	result := &DelegateResult{SerialGroups: plan.serialGroups()}
	for _, o := range outcomes {
		appendDelegateOutcome(result, o.item, o.ns)
	}
	return result, nil
}

// runSingleDelegation は ID を指定した `run <C-ID>` の本体。同じ直列化グループに終了していない
// 委譲の run を持つ課題がいれば、委譲を起動せず ErrSerialized を返す。
func (s *Store) runSingleDelegation(ctx context.Context, in DelegateInput, ch Challenge) (*DelegateResult, error) {
	cid, _ := parseChallengeID(ch.ID)
	plan, err := s.planDelegation(ctx, in, []int64{cid})
	if err != nil {
		return nil, err
	}
	result := &DelegateResult{SerialGroups: plan.serialGroups()}
	g := plan.groupOf(ch.ID)
	if g == nil {
		// 委譲できない課題（承認済みの計画が宣言と合わない等）。従来どおりの検査でエラーにする。
		item, ns, err := s.delegateOne(ctx, in, nil, ch, nil)
		if err != nil {
			return nil, err
		}
		appendDelegateOutcome(result, item, ns)
		return result, nil
	}
	if len(g.Running) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrSerialized, ch.ID)
	}
	repo, _ := in.ConnDecl.findConnectorRepo(g.Repo)
	sch := newDelegationScheduler(s, in)
	var c planCandidate
	for _, x := range g.Candidates {
		if x.ID == ch.ID {
			c = x
		}
	}
	item, ns, err := s.delegateOne(ctx, in, nil, ch, &delegateRun{sch: sch, w: sch.newWaiter(*repo, c)})
	if err != nil {
		return nil, err
	}
	appendDelegateOutcome(result, item, ns)
	return result, nil
}

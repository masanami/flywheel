package core

// このファイルは委譲の段の実行計画を持つ: 委譲の候補の収集、候補ごとの並行な実行、
// 同時の起動の上限とスロットの枠待ち。課題どうしを順に並べるかは、記録した前提
// （challenge_dependency）・同時に走らせる数の上限（max_parallel_runs）・スロットの空きだけで決める。
// 個別の `flywheel run`（ID の指定の有無を問わない）も Store.RunDelegation を通るので、
// 同じ処理に乗る。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// delegationWaitInterval は枠待ちの再評価の間隔の既定値。
var delegationWaitInterval = slotWaitInterval

// plannedCandidate は委譲の候補 1 件と、その課題のリポジトリ。候補 1 件が実行の 1 つの単位。
type plannedCandidate struct {
	Repo string
	Cand planCandidate
}

// delegationPlan は委譲の段の実行計画。
type delegationPlan struct {
	// candidates は起動の順（優先度・ID の昇順）。
	candidates []plannedCandidate
	// notStarted は、承認済みなのに委譲の候補にできなかった課題（理由つき。黙って除外しない）。
	notStarted []NotStarted
}

// candidatesOf は課題 id の候補（起動するタスクごとに 1 件。起動の順）を返す。
func (p *delegationPlan) candidatesOf(id string) []plannedCandidate {
	var out []plannedCandidate
	for _, c := range p.candidates {
		if c.Cand.ID == id {
			out = append(out, c)
		}
	}
	return out
}

// planOne は planDelegation が候補 1 件について読む事実。
type planOne struct {
	cand planCandidate
	repo string
}

// loadPlanCandidates は課題 cid を委譲の候補として計画に載せるための事実を読む。候補は起動するタスクごとに 1 件
// （selectLaunchTasks が決める）。承認済みの計画が今の宣言と合わない（委譲できない）課題は ok=false で、reason に人が読む
// 理由を返す。
func (s *Store) loadPlanCandidates(ctx context.Context, in DelegateInput, cid int64) (ones []planOne, ok bool, reason string, err error) {
	var ch *Challenge
	var plan approvedPlan
	var planOK bool
	var binding *sourceBinding
	var sel *launchSelection
	err = s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		if ch, err = loadChallenge(ctx, tx, cid); err != nil {
			return err
		}
		if plan, planOK, err = loadApprovedPlan(ctx, tx, cid); err != nil || !planOK {
			return err
		}
		if sel, err = selectLaunchTasks(ctx, tx, in.ConnDecl, formatChallengeID(cid), cid); err != nil {
			return err
		}
		binding, err = loadSourceBindingByChallengeID(ctx, tx, cid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, false, "", err
	}
	if ch == nil || !planOK {
		return nil, false, "承認済みの計画を引けない", nil
	}
	validated, vok := validateJ2Output([]byte(plan.Spec), j2ValidationContext{Agent: in.AgentDecl, Conn: in.ConnDecl, HasSource: binding != nil})
	if !vok {
		return nil, false, "承認済みの計画の構造化した出力が今の宣言と合わない", nil
	}
	if validated.Verdict != J2VerdictPlan {
		return nil, false, "承認済みの計画の verdict が plan でない", nil
	}
	// 起動するタスクごとに候補を作る。対象のリポジトリはタスクの行のもの。
	if len(sel.Tasks) == 0 {
		return nil, false, "起動できるタスクが無い", nil
	}
	for i := range sel.Tasks {
		repo, _ := in.ConnDecl.findConnectorRepo(sel.Tasks[i].Repo)
		if repo == nil {
			return nil, false, "起動するタスクのリポジトリが今の宣言に無い", nil
		}
		c := planCandidate{ID: ch.ID, Rank: priorityRank(ch.Priority), Seq: cid, TaskSeq: taskIntID(&sel.Tasks[i])}
		ones = append(ones, planOne{cand: c, repo: repo.Name})
	}
	return ones, true, "", nil
}

// repoIdleWorkDir は契約のコマンドの作業ディレクトリ（リポジトリ単位）を返す。worktree は元のクローン、clone は
// 状態が idle の作業用クローンのうち paths の順で最初のもの。idle が無ければ ok=false。
func (s *Store) repoIdleWorkDir(ctx context.Context, repo ConnectorRepo) (string, bool, error) {
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

// planDelegation は targets（課題の内部整数 ID）の委譲の実行計画を作る: 委譲の候補を集め、
// 起動の順（優先度・ID の昇順）に並べる。委譲できない課題（承認済みの計画が宣言と合わない）は
// 候補にせず、理由つきで plan.notStarted に載せる。
func (s *Store) planDelegation(ctx context.Context, in DelegateInput, targets []int64) (*delegationPlan, error) {
	if err := s.ReapInterruptedRuns(ctx); err != nil {
		return nil, err
	}
	byRepo := map[string][]planCandidate{}
	var notStarted []NotStarted
	for _, cid := range targets {
		ones, ok, reason, err := s.loadPlanCandidates(ctx, in, cid)
		if err != nil {
			return nil, err
		}
		if ok {
			for _, one := range ones {
				byRepo[one.repo] = append(byRepo[one.repo], one.cand)
			}
		} else {
			notStarted = append(notStarted, NotStarted{ChallengeID: formatChallengeID(cid), Reason: NotStartedPlanUnavailable, Detail: reason})
		}
	}
	plan := &delegationPlan{notStarted: notStarted}
	for i := range in.ConnDecl.Repos {
		repo := in.ConnDecl.Repos[i]
		cands := byRepo[repo.Name]
		if len(cands) == 0 {
			continue
		}
		if _, connector := in.ConnDecl.findConnectorRepo(repo.Name); connector == nil {
			continue
		}
		if err := s.EnsureSlots(ctx, repo); err != nil {
			if errors.Is(err, ErrValidation) {
				// 委譲できない。起動せずに飛ばすが、黙って除外せず理由つきで返す。
				for _, c := range cands {
					plan.notStarted = append(plan.notStarted, NotStarted{ChallengeID: c.ID, Reason: NotStartedPlanUnavailable, Detail: err.Error()})
				}
				continue
			}
			return nil, err
		}
		for _, c := range cands {
			plan.candidates = append(plan.candidates, plannedCandidate{Repo: repo.Name, Cand: c})
		}
	}
	// 起動の順（優先度・ID の昇順）に並べる。
	all := make([]planCandidate, 0, len(plan.candidates))
	repoOf := map[string]string{}
	for _, pc := range plan.candidates {
		all = append(all, pc.Cand)
		repoOf[pc.Cand.ID] = pc.Repo
	}
	sortCandidates(all)
	plan.candidates = plan.candidates[:0]
	for _, c := range all {
		plan.candidates = append(plan.candidates, plannedCandidate{Repo: repoOf[c.ID], Cand: c})
	}
	return plan, nil
}

// --- 同時の起動の上限と枠待ち ---

// schedWaiter は枠を待つ候補 1 件。
type schedWaiter struct {
	repo ConnectorRepo
	rank int
	seq  int64
	// taskSeq は起動するタスクの内部整数 ID（同じ課題の候補の順）。
	taskSeq int64
	queued  bool
	// contract は form: cli の接続ツールの課題（スロットを使わず、全体の同時委譲数だけで許可される）。
	contract bool
}

// delegationScheduler は 1 回の RunDelegation の中の起動を、全リポジトリの合計
// （max_parallel_runs）とリポジトリごとの使えるスロットの数で制限する。他のプロセスが起動した
// 終了していない委譲の run も、ストアを読んで数える。
type delegationScheduler struct {
	s           *Store
	maxParallel int
	conn        *ConnectorsDeclaration

	mu      sync.Mutex
	pre     int // 許可したが run がまだストアに無い起動
	preRepo map[string]int
	own     int // 許可して、まだ終えていない起動
	waiters []*schedWaiter
}

// waitInterval は枠待ちの再評価の間隔（Store に差し替えが無ければ delegationWaitInterval）。
func (sch *delegationScheduler) waitInterval() time.Duration {
	if sch.s.delegationWait > 0 {
		return sch.s.delegationWait
	}
	return delegationWaitInterval
}

func newDelegationScheduler(s *Store, in DelegateInput) *delegationScheduler {
	return &delegationScheduler{
		s: s, maxParallel: in.AgentDecl.MaxParallelRuns, conn: in.ConnDecl, preRepo: map[string]int{},
	}
}

func (sch *delegationScheduler) newWaiter(repo ConnectorRepo, c planCandidate) *schedWaiter {
	w := &schedWaiter{repo: repo, rank: c.Rank, seq: c.Seq, taskSeq: c.TaskSeq}
	if sch.conn != nil {
		if _, conn := sch.conn.findConnectorRepo(repo.Name); conn != nil && conn.Form == ConnectorFormCLI {
			w.contract = true
		}
	}
	return w
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
		if x.rank != w.rank {
			return x.rank > w.rank
		}
		if x.seq != w.seq {
			return x.seq > w.seq
		}
		return x.taskSeq > w.taskSeq
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
		// 同時委譲数には、終わっていない契約の run（form: cli）を持つ課題も数える（start の最中で契約の run が
		// まだ無い課題を含む）。契約の run はスロットを使わないので、リポジトリごとの数え（dbRepo）には入れない。
		if c.dbTotal, err = activeDelegationCount(ctx, tx); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM run r JOIN slot sl ON sl.id = r.slot_id
			 WHERE r.kind = 'delegate' AND r.result IS NULL AND sl.repo = ?`, repo.Name).Scan(&c.dbRepo)
	})
	return c, classifyReadWriteErr(err)
}

// ownerWaitContractRunWhere は、契約の run（k）がオーナーの承認・回答を待っているだけ（waiting で、課題〔c〕が保留）の条件。
// 同時に走らせる数にだけ使う。課題ごとの「実行中か」の判定（challengeHasActiveRun）には混ぜない
// （保留の課題へ新しい委譲を起動させないため）。
const ownerWaitContractRunWhere = `(k.state = 'waiting' AND c.status = 'awaiting_human')`

// endRecordedContractRunWhere は、契約の run（k）の記録した状態が終わり（成功・失敗・中止）の条件。こうした行は、課題が保留の間などに
// 読み直しのために開けたまま残してあるだけで、委譲先では動いていない。同時に走らせる数には数えない。
const endRecordedContractRunWhere = `k.state IN ('succeeded', 'failed', 'cancelled')`

// activeDelegationCount は終わっていない委譲の run と、終わっていない契約の run（form: cli）を持つタスクの数
// （課題とタスクの組ごとに 1 と数える。タスクに結び付かない契約の run は、課題ごとに 1）。
// レビュー待ち（contractRunReviewWaitWhere）・オーナーの回答待ち（ownerWaitContractRunWhere）・記録した状態が終わり（endRecordedContractRunWhere）の契約の run は数えない。
func activeDelegationCount(ctx context.Context, tx *sql.Tx) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM run WHERE kind = 'delegate' AND result IS NULL)
		+ (SELECT COUNT(*) FROM (SELECT k.challenge_id, COALESCE(k.task_id, 0) FROM contract_run k JOIN challenge c ON c.id = k.challenge_id
			WHERE k.ended_at IS NULL AND NOT `+contractRunReviewWaitWhere+` AND NOT `+ownerWaitContractRunWhere+` AND NOT `+endRecordedContractRunWhere+`
			UNION SELECT challenge_id, COALESCE(task_id, 0) FROM run WHERE kind = 'contract' AND result IS NULL))`).Scan(&n)
	return n, err
}

func (sch *delegationScheduler) fitsLocked(c slotCapacity, repo string) bool {
	return c.usable > 0 && c.dbTotal+sch.pre < sch.maxParallel && c.dbRepo+sch.preRepo[repo] < c.usable
}

// earlierFitCountLocked は、待ちの列で w より前の課題のうち、今すぐ許可できるものの数（その課題が先に枠を取る）。
// w が列に無ければ（nil を含む）前の課題は無いものとする。契約の課題の許可には全体の同時委譲数 total（呼び出し側が読んだ値）だけを使う。
func (sch *delegationScheduler) earlierFitCountLocked(ctx context.Context, w *schedWaiter, total int) (int, error) {
	if w == nil || !w.queued {
		return 0, nil
	}
	n := 0
	for _, e := range sch.waiters {
		if e == w {
			break
		}
		if e.contract {
			// 契約の run はスロットを使わないので、全体の同時委譲数だけで許可される。
			if total+sch.pre < sch.maxParallel {
				n++
			}
			continue
		}
		ec, err := sch.capacity(ctx, e.repo)
		if err != nil {
			return 0, err
		}
		if sch.fitsLocked(ec, e.repo.Name) {
			n++
		}
	}
	return n, nil
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
	a.markInsertedLocked()
}

func (a *admission) markInsertedLocked() {
	if !a.inserted {
		a.inserted = true
		a.sch.pre--
		a.sch.preRepo[a.repo]--
	}
}

// Lock・Unlock・MarkInsertedLocked は、run の記録と許可の数（pre）の解放を、許可の数え（admitContract・admit）と同時に起きない
// 1 つの区間にする（runRecordingSection）。区間は Lock から Unlock までで、その中で run を記録し、確定したら MarkInsertedLocked で
// pre を解放する。記録の確定と解放の間に別の許可が数えると、確定した run を DB の数と pre で 2 回数えて、空いている枠を断る。
func (a *admission) Lock() { a.sch.mu.Lock() }

func (a *admission) Unlock() { a.sch.mu.Unlock() }

func (a *admission) MarkInsertedLocked() { a.markInsertedLocked() }

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
// だけ（【決定 B 2026-10-05 オーナー M3P33】他のプロセスの run が占めているときは待たずに
// ErrSlotUnavailable にする）。複数が待つときは、先頭の課題の優先度・ID の昇順に許可する。
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
			n, err := sch.earlierFitCountLocked(ctx, w, c.dbTotal)
			if err != nil {
				sch.mu.Unlock()
				return nil, err
			}
			if n == 0 {
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
		if hook := sch.s.delegationWaited; hook != nil {
			hook()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sch.waitInterval()):
		}
	}
}

// --- 候補ごとの並行な実行 ---

// delegateRun は delegateOne へ渡す、スケジューラと待ちの列の中の自分（nil なら制限しない）。
type delegateRun struct {
	sch *delegationScheduler
	w   *schedWaiter
	// waitCtx は枠待ちを打ち切る ctx（nil なら呼び出しの ctx）。
	waitCtx context.Context
	// taskID は起動するタスクの内部整数 ID（0 なら、起動できるタスクがちょうど 1 個の課題として決める）。
	taskID int64
}

// task は起動するタスクの内部整数 ID（nil の run は 0）。
func (r *delegateRun) task() int64 {
	if r == nil {
		return 0
	}
	return r.taskID
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

// admitContract は form: cli の契約の run の起動の許可を得る。スロットは使わないので、全体の同時委譲数
// （max_parallel_runs）だけを見る。空いていなければ待たずに ErrSlotUnavailable。
func (r *delegateRun) admitContract(ctx context.Context) (*admission, error) {
	if r == nil || r.sch == nil {
		return nil, nil
	}
	sch := r.sch
	sch.mu.Lock()
	defer sch.mu.Unlock()
	var total int
	err := sch.s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		total, err = activeDelegationCount(ctx, tx)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if total+sch.pre >= sch.maxParallel {
		return nil, ErrSlotUnavailable
	}
	// 待ちの列で前にいる課題が今すぐ許可できる分の枠は、その課題に譲る（優先度・ID の昇順）。譲ってもなお枠が残るなら許可する
	// （契約は待たないので、枠に余裕があるのに譲るだけで拒否しない）。
	n, err := sch.earlierFitCountLocked(ctx, r.w, total)
	if err != nil {
		return nil, err
	}
	if total+sch.pre+n >= sch.maxParallel {
		return nil, ErrSlotUnavailable
	}
	sch.pre++
	sch.preRepo[""]++
	sch.own++
	// 許可した候補は待ちの列から外す。外さないと、start が終わるまで列に残り、後ろの候補の earlierFitCountLocked が
	// 許可済みのこの候補を「今すぐ許可できる前の候補」として数え、pre と二重に数えて枠を塞ぐ。
	// dequeueLocked は列に無ければ何もしないので、executePlan の goroutine の defer の dequeue と重なっても安全。
	if r.w != nil {
		sch.dequeueLocked(r.w)
	}
	return &admission{sch: sch, repo: ""}, nil
}

type delegateOutcome struct {
	rank    int
	seq     int64
	taskSeq int64
	item    *JudgmentAutoItem
	ns      *NotStarted
}

// executePlan は plan の候補を、候補ごとに並行に実行する。枠の許可は優先度・ID の昇順。
func (s *Store) executePlan(ctx context.Context, in DelegateInput, jc *JudgmentCycle, plan *delegationPlan) (*DelegateResult, error) {
	sch := newDelegationScheduler(s, in)
	// 致命的なエラーでは、新しい起動と枠待ちだけを止める（すでに動いている他の委譲は、
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

	// 候補は、並行に始める前に待ちの列へ入れる（同時に始めたとき、優先度・ID の昇順を守るため）。
	waiters := make([]*schedWaiter, len(plan.candidates))
	for i, pc := range plan.candidates {
		repo, _ := in.ConnDecl.findConnectorRepo(pc.Repo)
		if repo == nil {
			continue
		}
		waiters[i] = sch.newWaiter(*repo, pc.Cand)
		sch.enqueue(waiters[i])
	}

	var wg sync.WaitGroup
	for i := range plan.candidates {
		c := plan.candidates[i].Cand
		w := waiters[i]
		if w == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer sch.dequeue(w)
			if waitCtx.Err() != nil {
				return
			}
			ch, err := s.loadChallengeForAuto(ctx, c.ID)
			if err != nil {
				fail(err)
				return
			}
			if ch.Status != StatusInProgress {
				// タスクを止める保留だけが開いている課題は、このタスクに保留が無いときだけ進める。
				var tp *int64
				if c.TaskSeq > 0 {
					tp = &c.TaskSeq
				}
				may, err := s.taskMayProceedFor(ctx, c.Seq, tp)
				if err != nil {
					fail(err)
					return
				}
				if !may {
					return
				}
			}
			item, ns, err := s.delegateOne(ctx, in, jc, *ch, &delegateRun{sch: sch, w: w, waitCtx: waitCtx, taskID: c.TaskSeq})
			if err != nil {
				// 1 件の課題の問題（承認済みの計画が今の宣言と合わない等）で、他の課題の委譲を
				// 止めない。ストアの障害など、それ以外のエラーだけが全体を打ち切る。
				if errors.Is(err, ErrRunInProgress) || errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrValidation) {
					return
				}
				fail(err)
				return
			}
			record(delegateOutcome{rank: c.Rank, seq: c.Seq, taskSeq: c.TaskSeq, item: item, ns: ns})
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
		if outcomes[i].seq != outcomes[j].seq {
			return outcomes[i].seq < outcomes[j].seq
		}
		return outcomes[i].taskSeq < outcomes[j].taskSeq
	})
	result := &DelegateResult{}
	for _, o := range outcomes {
		appendDelegateOutcome(result, o.item, o.ns)
	}
	return result, nil
}

// runSingleDelegation は ID を指定した `run <C-ID>` の本体。
func (s *Store) runSingleDelegation(ctx context.Context, in DelegateInput, ch Challenge) (*DelegateResult, error) {
	cid, _ := parseChallengeID(ch.ID)
	// 状態（呼び出し元の RunDelegation が検査済み）と計画の検査は、ID を省いた経路（selectDelegationTargets）と
	// 同じく、依存の判定より先に行う。通らない課題に `not_started dependency` を返して本来のエラーを隠さない。
	if err := s.checkApprovedPlanForRun(ctx, cid); err != nil {
		return nil, err
	}
	// 依存の判定は --resume の可否（decideLaunch）より前に置く。満たされていなければ何も消費しない。
	var depNS *NotStarted
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		eval, err := evaluateDependencies(ctx, tx)
		if err != nil {
			return err
		}
		depNS = dependencyNotStarted(eval, ch.ID, cid)
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if depNS != nil {
		return &DelegateResult{NotStarted: []NotStarted{*depNS}}, nil
	}
	// 起動できるタスクと統合先の使用。計画の検査より前で、記録した前提の後（ID を省いた段と同じ順）。
	var sel *launchSelection
	err = s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		sel, err = selectLaunchTasks(ctx, tx, in.ConnDecl, ch.ID, cid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	if sel.Blocked != "" {
		held, err := s.holdTasksBlocked(ctx, ch.ID, sel.Blocked)
		if err != nil {
			return nil, err
		}
		if held {
			return &DelegateResult{NotStarted: []NotStarted{tasksBlockedNotStarted(ch.ID)}}, nil
		}
	}
	if len(sel.Tasks) == 0 {
		if len(sel.NotStarted) == 0 {
			if sel.Busy {
				return nil, ErrRunInProgress
			}
			sel.NotStarted = []NotStarted{{ChallengeID: ch.ID, Reason: NotStartedNoTasks, Detail: "起動できるタスクが無い"}}
		}
		return &DelegateResult{NotStarted: sel.NotStarted}, nil
	}
	plan, err := s.planDelegation(ctx, in, []int64{cid})
	if err != nil {
		return nil, err
	}
	result := &DelegateResult{NotStarted: sel.NotStarted}
	pcs := plan.candidatesOf(ch.ID)
	if len(pcs) == 0 {
		// 委譲できない課題（承認済みの計画が宣言と合わない等）。従来どおりの検査でエラーにする。
		item, ns, err := s.delegateOne(ctx, in, nil, ch, nil)
		if err != nil {
			return nil, err
		}
		appendDelegateOutcome(result, item, ns)
		return result, nil
	}
	sch := newDelegationScheduler(s, in)
	for _, pc := range pcs {
		repo, _ := in.ConnDecl.findConnectorRepo(pc.Repo)
		item, ns, err := s.delegateOne(ctx, in, nil, ch, &delegateRun{sch: sch, w: sch.newWaiter(*repo, pc.Cand), taskID: pc.Cand.TaskSeq})
		if err != nil {
			// 先のタスクをもう起動したなら、後のタスクの枠・予算・状態の不足で全体を失敗にせず、起動できなかった理由として返す。
			if len(result.Items) > 0 && (errors.Is(err, ErrSlotUnavailable) || errors.Is(err, ErrBudgetExceeded) ||
				errors.Is(err, ErrRunInProgress) || errors.Is(err, ErrInvalidTransition)) {
				reason := NotStartedSlotUnavailable
				if errors.Is(err, ErrBudgetExceeded) {
					reason = NotStartedCycleBudget
				} else if !errors.Is(err, ErrSlotUnavailable) {
					reason = NotStartedInProgress
				}
				result.NotStarted = append(result.NotStarted, NotStarted{ChallengeID: ch.ID, Reason: reason,
					Detail: fmt.Sprintf("タスク %s は起動できなかった", formatTaskID(pc.Cand.TaskSeq))})
				continue
			}
			return nil, err
		}
		appendDelegateOutcome(result, item, ns)
	}
	return result, nil
}

// checkApprovedPlanForRun は、承認済みで構造化した出力を持つ計画があるかを検査する。無ければ
// loadDelegationContext と同じエラー（計画が使えない詳細つき、または ErrValidation）を返す。
func (s *Store) checkApprovedPlanForRun(ctx context.Context, cid int64) error {
	var unavailable error
	var ok bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		p, gap, detail, err := lookupApprovedPlan(ctx, tx, cid)
		if err != nil {
			return err
		}
		if gap != planGapNone && gap != planGapNoApproval {
			unavailable = &planUnavailableError{Detail: detail}
		}
		ok = gap == planGapNone && p.HasSpec
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return err
	}
	if ok {
		return nil
	}
	if unavailable != nil {
		return unavailable
	}
	return fmt.Errorf("%w: the challenge has no approved plan with a structured spec", ErrValidation)
}

package core

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// このファイルは課題のツリー（画面の「課題とそのタスクの進み」）を、core の記録だけから決定的に作る公開 API
// （ChallengeTree・ChallengeTreeNode）を持つ。表示の区分（TaskPhase）・タスクの進み・費用の合計・予算の見込み・
// 未のフォローアップの件数は、ここで決める（画面の側で決めない・数え直さない）。ストアへ書かず、LLM も上流（GitHub）も
// 呼ばない。

// TaskPhase はタスクの表示の区分（閉集合。上から順に当てはめ、最初に当たったものにする:
// cancelled → done → waiting → running → next → blocked）。
type TaskPhase string

// TaskPhase の値。
const (
	// TaskPhaseRunning は着手中か検証中（状態が in_progress か verifying）。
	TaskPhaseRunning TaskPhase = "running"
	// TaskPhaseWaiting は、そのタスクに答えていないオーナー宛ての保留がある（種類が親が直接行うの保留は数えない）。
	TaskPhaseWaiting TaskPhase = "waiting"
	// TaskPhaseNext は未着手で、前提が無いか全部済み（計画の承認の前を含む）。
	TaskPhaseNext TaskPhase = "next"
	// TaskPhaseBlocked は未着手で、済みでない前提がある（取りやめの前提は済みでないものとして扱う）。
	TaskPhaseBlocked TaskPhase = "blocked"
	// TaskPhaseDone は済み。
	TaskPhaseDone TaskPhase = "done"
	// TaskPhaseCancelled は取りやめ。
	TaskPhaseCancelled TaskPhase = "cancelled"
)

// TaskPhaseValues は TaskPhase の閉集合の写しを返す（画面と共有する値の集合の順）。
func TaskPhaseValues() []TaskPhase {
	return []TaskPhase{TaskPhaseRunning, TaskPhaseWaiting, TaskPhaseNext, TaskPhaseBlocked, TaskPhaseDone, TaskPhaseCancelled}
}

// taskPhase はタスクの表示の区分を決める。status が nil は承認の前（タスクの行が無い。未着手として扱う）、
// waiting はそのタスクに答えていないオーナー宛ての保留があるか、deps は前提のタスクの key、depStatus は key ごとの
// 前提の状態（nil は承認の前）。
func taskPhase(status *TaskStatus, waiting bool, deps []string, depStatus map[string]*TaskStatus) TaskPhase {
	st := TaskStatusPending
	if status != nil {
		st = *status
	}
	switch {
	case st == TaskStatusCancelled:
		return TaskPhaseCancelled
	case st == TaskStatusDone:
		return TaskPhaseDone
	case waiting:
		return TaskPhaseWaiting
	case st == TaskStatusInProgress || st == TaskStatusVerifying:
		return TaskPhaseRunning
	}
	for _, k := range deps {
		d, ok := depStatus[k]
		if !ok || d == nil || *d != TaskStatusDone {
			return TaskPhaseBlocked
		}
	}
	return TaskPhaseNext
}

// taskProgress は表示の区分が running のタスクの細かい進みを決める（それ以外は nil）。着手中は working。検証中は、
// 統合先を使う課題で、終わっていない最後の契約の実行が、統合先へ入れる依頼を出していれば（land_ref がある）landing、
// 検証を通っただけ（verified_ref だけがある）なら verified、それ以外（統合先を使わない課題を含む）は verifying。
// landing と verified の決め方は、タスクの詳細の統合先へ入れる進み（sent・verified）と同じ。
func taskProgress(phase TaskPhase, status *TaskStatus, usesIntegration *bool, verified, sent bool) *TaskProgress {
	if phase != TaskPhaseRunning || status == nil {
		return nil
	}
	var p TaskProgress
	switch {
	case *status == TaskStatusInProgress:
		p = TaskProgressWorking
	case *status == TaskStatusVerifying && usesIntegration != nil && *usesIntegration && sent:
		p = TaskProgressLanding
	case *status == TaskStatusVerifying && usesIntegration != nil && *usesIntegration && verified:
		p = TaskProgressVerified
	case *status == TaskStatusVerifying:
		p = TaskProgressVerifying
	default:
		return nil
	}
	return &p
}

// challengeProgress は統合先を使う課題の細かい進みを決める（統合先を使わない課題・どれにも当たらないときは nil）。
// 検証中は checking。着手中で、取りやめを除く全部のタスクが済みで、上げる依頼の終わっていない契約の実行があれば reflecting。
func challengeProgress(status Status, usesIntegration *bool, tasks []TreeTask, reflecting bool) *ChallengeProgress {
	if usesIntegration == nil || !*usesIntegration {
		return nil
	}
	var p ChallengeProgress
	switch status {
	case StatusVerifying:
		p = ChallengeProgressChecking
	case StatusInProgress:
		if !reflecting {
			return nil
		}
		for _, t := range tasks {
			if t.Phase != TaskPhaseDone && t.Phase != TaskPhaseCancelled {
				return nil
			}
		}
		p = ChallengeProgressReflecting
	default:
		return nil
	}
	return &p
}

// planTaskPhases は計画承認待ちの要約のタスク（前提は計画の中の key。状態は無い）の表示の区分を、tasks と同じ順で返す。
func planTaskPhases(tasks []PlanTaskView) []TaskPhase {
	out := make([]TaskPhase, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskPhase(nil, false, t.DependsOn, nil))
	}
	return out
}

// recentDoneLimit は課題のツリーの recent_done の件数の上限。
const recentDoneLimit = 10

// TreeTask は課題のツリーのタスク 1 件。ID・Status は計画の承認の後だけ（承認の前は nil）。DependsOn は前提のタスクの
// 計画の中の識別子（Key。承認の後でもタスクの ID ではない）。CostUSD は、そのタスクの行に付いた実行の費用の合計
// （承認の前は 0）。
type TreeTask struct {
	Key       string
	ID        *string
	Title     string
	DependsOn []string
	Status    *TaskStatus
	Phase     TaskPhase
	// Progress は Phase が running のタスクの細かい進み（それ以外は nil）。
	Progress *TaskProgress
	CostUSD  float64
}

// TreeDependency は課題の前提の課題 1 件。
type TreeDependency struct {
	ChallengeID string
	Title       string
	Status      Status
	Satisfied   bool
}

// TreeRelative は親か子の課題 1 件。
type TreeRelative struct {
	ChallengeID string
	Title       string
	Status      Status
}

// TreeChallenge は課題のツリーの課題 1 件。
type TreeChallenge struct {
	ID        string
	Title     string
	Status    Status
	UpdatedAt time.Time
	// NextHumanActions は人間の操作待ちの中身（課題の一覧のカードの next_human_actions と同じ値・並び。無ければ空で nil ではない）。
	NextHumanActions []CardAction
	// UsesIntegration は、計画の承認の前は最新の計画の値、承認の後は課題に記録した値（計画が無いか過去の形なら nil）。
	UsesIntegration *bool
	// Tasks は最新の計画のタスク。前の版の計画のタスクは入れない。無ければ空で nil ではない。
	Tasks []TreeTask
	// Progress は統合先を使う課題の細かい進み（それ以外は nil）。
	Progress   *ChallengeProgress
	TasksTotal int
	TasksDone  int
	// CostUSD は課題の実行の記録（判断点と委譲。前の版の計画のタスクに付いた実行も含む）の費用の合計。
	CostUSD float64
	// BudgetUSD は GetPlanView の BudgetUSD と同じ規則で決めた予算の見込み（決められないとき・計画が無いとき nil）。
	BudgetUSD     *float64
	FollowupsOpen int
	Dependencies  []TreeDependency
	Parent        *TreeRelative
	Children      []TreeRelative
}

// ChallengeTree は課題のツリー。Challenges は状態が完了でも取り下げでもない課題（ID の小さい順）、RecentDone は
// 完了の課題の新しい 10 件（updated_at の新しい順。同じ時刻は ID の大きい順。取り下げは入れない）。
type ChallengeTree struct {
	Challenges []TreeChallenge
	RecentDone []TreeChallenge
}

// ChallengeTree は now の時点の課題のツリーを作る。agent は予算の上限をサイズごとの既定から決めるための宣言（nil 可。
// nil で既定が要るときは BudgetUSD を nil にする）。
func (s *Store) ChallengeTree(ctx context.Context, agent *AgentDeclaration, now time.Time) (*ChallengeTree, error) {
	all, err := s.ListChallenges(ctx, ListOptions{})
	if err != nil {
		return nil, err
	}
	var open, done []Challenge
	for _, c := range all {
		switch c.Status {
		case StatusDone:
			done = append(done, c)
		case StatusWithdrawn:
		default:
			open = append(open, c)
		}
	}
	sort.SliceStable(done, func(i, j int) bool {
		if !done[i].UpdatedAt.Equal(done[j].UpdatedAt) {
			return done[i].UpdatedAt.After(done[j].UpdatedAt)
		}
		a, _ := parseChallengeID(done[i].ID)
		b, _ := parseChallengeID(done[j].ID)
		return a > b
	})
	if len(done) > recentDoneLimit {
		done = done[:recentDoneLimit]
	}
	picked := append(append([]Challenge{}, open...), done...)
	nodes, err := s.buildTreeNodes(ctx, all, picked, agent, now)
	if err != nil {
		return nil, err
	}
	return &ChallengeTree{Challenges: nodes[:len(open)], RecentDone: nodes[len(open):]}, nil
}

// ChallengeTreeNode は課題 1 件のツリーの要素を作る（完了・取り下げの課題も作る）。課題が無い・ID の形が不正なら ErrNotFound。
func (s *Store) ChallengeTreeNode(ctx context.Context, id string, agent *AgentDeclaration, now time.Time) (*TreeChallenge, error) {
	if _, ok := parseChallengeID(id); !ok {
		return nil, ErrNotFound
	}
	all, err := s.ListChallenges(ctx, ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, c := range all {
		if c.ID != id {
			continue
		}
		nodes, err := s.buildTreeNodes(ctx, all, []Challenge{c}, agent, now)
		if err != nil {
			return nil, err
		}
		return &nodes[0], nil
	}
	return nil, ErrNotFound
}

// treeShared は課題のツリーの全部の要素が使う、課題の全体にまたがる読み取り。
type treeShared struct {
	parents     map[int64]int64
	eval        *dependencyEvaluation
	costByChall map[int64]int64
	costByTask  map[int64]int64
	followups   map[int64]int
	waiting     map[int64]bool // 答えていないオーナー宛ての保留があるタスクの内部 ID
	// taskVerified は、タスクの最後の契約の実行（kind が task。終わっていないもの）に verified_ref があるタスクの内部 ID。
	// taskSent は同じ実行に land_ref（統合先へ入れる依頼）もあるタスクの内部 ID。
	taskVerified map[int64]bool
	taskSent     map[int64]bool
	// reflecting は、上げる依頼（kind が reflect）の終わっていない契約の実行がある課題の内部 ID。
	reflecting map[int64]bool
}

func (s *Store) buildTreeNodes(ctx context.Context, all, picked []Challenge, agent *AgentDeclaration, now time.Time) ([]TreeChallenge, error) {
	ov, err := s.GetOverviewFor(ctx, agent)
	if err != nil {
		return nil, err
	}
	actions := make([][]CardAction, len(picked))
	for i, c := range picked {
		runs, err := s.LoadCardRuns(ctx, c)
		if err != nil {
			return nil, err
		}
		card := runs.Derive(c, ov, now)
		actions[i] = append([]CardAction{}, card.NextHumanActions...)
	}
	byID := make(map[string]Challenge, len(all))
	for _, c := range all {
		byID[c.ID] = c
	}

	out := make([]TreeChallenge, 0, len(picked))
	err = s.db.Read(ctx, func(tx *sql.Tx) error {
		sh, err := loadTreeShared(ctx, tx)
		if err != nil {
			return err
		}
		for i, c := range picked {
			cid, _ := parseChallengeID(c.ID)
			n := TreeChallenge{
				ID: c.ID, Title: c.Title, Status: c.Status, UpdatedAt: c.UpdatedAt, NextHumanActions: actions[i],
				Tasks: []TreeTask{}, CostUSD: microsToUSD(sh.costByChall[cid]), FollowupsOpen: sh.followups[cid],
				Dependencies: []TreeDependency{}, Children: []TreeRelative{},
			}
			if err := fillTreeTasks(ctx, tx, cid, agent, sh, &n); err != nil {
				return err
			}
			// 課題の一覧（all）と、親子・前提（sh）は別の読み取りで読むので、その間に増えた課題を sh だけが指すことがある。
			// 一覧に無い課題は、ID と題名が空の要素にせず飛ばす（親は nil のまま）。次の読み直しで出る。
			for _, d := range sh.eval.Dependencies[cid] {
				dep, ok := byID[formatChallengeID(d.DependsOn)]
				if !ok {
					continue
				}
				n.Dependencies = append(n.Dependencies, TreeDependency{ChallengeID: dep.ID, Title: dep.Title, Status: dep.Status, Satisfied: d.Met})
			}
			if p, ok := sh.parents[cid]; ok {
				if pc, ok := byID[formatChallengeID(p)]; ok {
					n.Parent = &TreeRelative{ChallengeID: pc.ID, Title: pc.Title, Status: pc.Status}
				}
			}
			var kids []int64
			for child, parent := range sh.parents {
				if parent == cid {
					kids = append(kids, child)
				}
			}
			sort.Slice(kids, func(a, b int) bool { return kids[a] < kids[b] })
			for _, k := range kids {
				kc, ok := byID[formatChallengeID(k)]
				if !ok {
					continue
				}
				n.Children = append(n.Children, TreeRelative{ChallengeID: kc.ID, Title: kc.Title, Status: kc.Status})
			}
			out = append(out, n)
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

func loadTreeShared(ctx context.Context, tx *sql.Tx) (*treeShared, error) {
	sh := &treeShared{costByChall: map[int64]int64{}, costByTask: map[int64]int64{}, followups: map[int64]int{}, waiting: map[int64]bool{}, taskVerified: map[int64]bool{}, taskSent: map[int64]bool{}, reflecting: map[int64]bool{}}
	var err error
	if sh.parents, err = loadParentMap(ctx, tx); err != nil {
		return nil, err
	}
	if sh.eval, err = evaluateDependencies(ctx, tx); err != nil {
		return nil, err
	}
	// 費用は run の cost_usd（費用の記録が無い実行は 0）。委譲の実行も run に費用を記録する。
	if err := scanPairs(ctx, tx, `SELECT challenge_id, COALESCE(SUM(cost_usd), 0) FROM run WHERE challenge_id IS NOT NULL GROUP BY challenge_id`, func(k, v int64) { sh.costByChall[k] = v }); err != nil {
		return nil, err
	}
	if err := scanPairs(ctx, tx, `SELECT task_id, COALESCE(SUM(cost_usd), 0) FROM run WHERE task_id IS NOT NULL GROUP BY task_id`, func(k, v int64) { sh.costByTask[k] = v }); err != nil {
		return nil, err
	}
	// 未のフォローアップの数え方は、状況の followups.open と同じ（上げてあって、上げた先が取り下げでないものは数えない）。
	if err := scanPairs(ctx, tx, `SELECT f.challenge_id, COUNT(*) FROM followup f WHERE f.challenge_id IS NOT NULL AND `+openFollowupToTriageCond+`
		GROUP BY f.challenge_id`, func(k, v int64) { sh.followups[k] = int(v) }); err != nil {
		return nil, err
	}
	if err := scanPairs(ctx, tx, `SELECT DISTINCT task_id, 1 FROM hold WHERE task_id IS NOT NULL AND answer IS NULL AND closed_at IS NULL AND kind <> '`+string(HoldKindParentDirect)+`'`,
		func(k, _ int64) { sh.waiting[k] = true }); err != nil {
		return nil, err
	}
	if err := scanPairs(ctx, tx, `SELECT task_id, CASE WHEN land_ref IS NOT NULL AND land_ref <> '' THEN 2 ELSE 1 END FROM contract_run
		WHERE kind = 'task' AND task_id IS NOT NULL AND ended_at IS NULL AND verified_ref IS NOT NULL AND verified_ref <> ''
		AND id = (SELECT MAX(r.id) FROM contract_run r WHERE r.task_id = contract_run.task_id AND r.kind = 'task')`,
		func(k, v int64) {
			sh.taskVerified[k] = true
			if v == 2 {
				sh.taskSent[k] = true
			}
		}); err != nil {
		return nil, err
	}
	if err := scanPairs(ctx, tx, `SELECT DISTINCT challenge_id, 1 FROM contract_run WHERE kind = 'reflect' AND ended_at IS NULL`,
		func(k, _ int64) { sh.reflecting[k] = true }); err != nil {
		return nil, err
	}
	return sh, nil
}

func scanPairs(ctx context.Context, tx *sql.Tx, query string, put func(k, v int64)) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, v int64
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		put(k, v)
	}
	return rows.Err()
}

// fillTreeTasks は課題 cid の最新の計画から、tasks（progress を含む）・tasks_total・tasks_done・課題の progress・budget_usd・uses_integration を n へ入れる。
func fillTreeTasks(ctx context.Context, tx *sql.Tx, cid int64, agent *AgentDeclaration, sh *treeShared, n *TreeChallenge) error {
	pv, err := loadPlanView(ctx, tx, cid, agent)
	if err != nil {
		return err
	}
	if pv == nil {
		return nil
	}
	n.BudgetUSD = pv.BudgetUSD
	n.UsesIntegration = pv.UsesIntegration
	if !pv.Structured {
		return nil
	}
	// 承認の後は、タスクの ID から計画の中の識別子（key）を引く。前提は key で返す。
	keyByID := map[string]string{}
	statusByKey := map[string]*TaskStatus{}
	approved := false
	for _, t := range pv.Tasks {
		if t.ID != "" {
			approved = true
			st := t.Status
			keyByID[t.ID] = t.Key
			statusByKey[t.Key] = &st
		}
	}
	if approved {
		// 承認の後の課題の値は、承認が課題に記録した値。
		if n.UsesIntegration, err = challengeUsesIntegration(ctx, tx, cid); err != nil {
			return err
		}
	}
	for _, t := range pv.Tasks {
		deps := make([]string, 0, len(t.DependsOn))
		for _, d := range t.DependsOn {
			if k, ok := keyByID[d]; ok {
				d = k
			}
			deps = append(deps, d)
		}
		tt := TreeTask{Key: t.Key, Title: t.Title, DependsOn: deps}
		var tid int64
		if t.ID != "" {
			id, st := t.ID, t.Status
			tt.ID, tt.Status = &id, &st
			tid, _ = parseTaskID(t.ID)
			tt.CostUSD = microsToUSD(sh.costByTask[tid])
		}
		tt.Phase = taskPhase(tt.Status, tid != 0 && sh.waiting[tid], deps, statusByKey)
		if tt.Phase == TaskPhaseDone {
			n.TasksDone++
		}
		tt.Progress = taskProgress(tt.Phase, tt.Status, n.UsesIntegration, tid != 0 && sh.taskVerified[tid], tid != 0 && sh.taskSent[tid])
		n.Tasks = append(n.Tasks, tt)
	}
	n.TasksTotal = len(n.Tasks)
	n.Progress = challengeProgress(n.Status, n.UsesIntegration, n.Tasks, sh.reflecting[cid])
	return nil
}

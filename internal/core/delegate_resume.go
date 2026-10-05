package core

// このファイルは委譲の起動の形（新しいセッションか `--resume` か）の決定と、連続失敗の
// 上限（親要件チケット #98 §保留と再開・§失敗・差し戻しの上限。決定 M3P16・M3P17・M3P30・
// M3P38・M3P39・M3P40）を持つ。
//
// 起動の形は、課題の委譲の run の履歴と最後の保留だけから決める（run に起動の形の列を
// 足さない）:
//
//   - 最後の保留が最後の委譲の run より後に作られ、回答済みなら、保留の原因の run の
//     セッションへ回答を渡して再開する（原因の run が無い保留は新しいセッション）
//   - そうでなければ、最後の委譲の run の結果で決める（errored 等は同じセッションを
//     中断の文面で再開、launch_failed は失敗した起動と同じ形で起動し直す）
//
// 連続失敗は、同じ計画の版の、最後の回答済みの保留より後の委譲の run だけを数える。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// NotStartedFailureLimit は、連続失敗の上限に達したため委譲を起動せず、課題を人間対応待ちに
// したことを表す。`cycle` の not_started の閉集合（NotStartedReasonValues）に含まれる。
const NotStartedFailureLimit NotStartedReason = "failure_limit"

// NotStartedReworkLimit は、差し戻しの上限に達したため委譲を起動せず、課題を人間対応待ちに
// したことを表す（M3P17・M3P39）。`cycle` の not_started の閉集合（NotStartedReasonValues）に含まれる。
const NotStartedReworkLimit NotStartedReason = "rework_limit"

// ResumeKind は `--resume` で渡す固定の文面の種類。
type ResumeKind string

const (
	// ResumeKindAnswer は保留への回答を渡す再開。
	ResumeKindAnswer ResumeKind = "answer"
	// ResumeKindInterrupted は中断の事実と状態の報告を求める再開。
	ResumeKindInterrupted ResumeKind = "interrupted"
	// ResumeKindBudget は、実装枠の上限へ到達して止まった run を、`flywheel budget` で枠を増やした
	// 後に続けさせる再開（上限到達で中断した事実と、続行を求める）。
	ResumeKindBudget ResumeKind = "budget"
	// ResumeKindRework は、J5 が達成条件を満たさないと判定して着手中に戻した課題の、直前の委譲の
	// セッションへの再開（J5 の差し戻しの指摘を渡す）。
	ResumeKindRework ResumeKind = "rework"
)

// resumePlan は `--resume` で起動するときの宛先と入力。
type resumePlan struct {
	Kind ResumeKind
	// Target は再開するセッションを持つ run（launch_failed でない run）。
	Target runRow
	// Answer は Kind が answer のときの人間の回答。Question はその保留の問い。
	Answer   string
	Question string
	// Branch は子の最後の報告（無ければ照合）のブランチ名。無ければ ""。
	Branch string
	// Feedback は Kind が rework のときの J5 の差し戻しの指摘。
	Feedback string
}

// failureLimitHit は連続失敗の上限の検出。
type failureLimitHit struct {
	Count int
	Limit int
	// Last は最後に数えた失敗の run（保留の原因の run になる）。
	Last runRow
}

// reworkLimitHit は差し戻しの上限の検出。
type reworkLimitHit struct {
	Count int
	Limit int
	// Last は最後に数えた J5 の run（保留の原因の run になる）。
	Last runRow
}

// holdRef は保留の起動の形の決定に使う列（hold.run_id を含む）。
type holdRef struct {
	ID         int64
	Question   string
	RunID      *int64
	Answer     *string
	AnsweredAt *time.Time
	RaisedAt   time.Time
}

// launchHistory は課題の委譲の run（id 昇順。全部の計画の版）と保留（id 昇順）。
type launchHistory struct {
	Runs  []runRow
	Holds []holdRef
	// Verifications は課題の J5 の run（id 昇順。全部の計画の版）。
	Verifications []runRow
}

func loadLaunchHistory(ctx context.Context, tx *sql.Tx, challengeID int64) (*launchHistory, error) {
	h := &launchHistory{}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM run WHERE challenge_id = ? AND kind = 'delegate' ORDER BY id ASC`, challengeID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	for _, id := range ids {
		r, err := loadRunByID(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if r != nil {
			h.Runs = append(h.Runs, *r)
		}
	}

	jrows, err := tx.QueryContext(ctx, `SELECT id FROM run WHERE challenge_id = ? AND kind = 'judgment' AND judgment = 'J5' ORDER BY id ASC`, challengeID)
	if err != nil {
		return nil, err
	}
	var jids []int64
	for jrows.Next() {
		var id int64
		if err := jrows.Scan(&id); err != nil {
			_ = jrows.Close()
			return nil, err
		}
		jids = append(jids, id)
	}
	if err := jrows.Err(); err != nil {
		_ = jrows.Close()
		return nil, err
	}
	_ = jrows.Close()
	for _, id := range jids {
		r, err := loadRunByID(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if r != nil {
			h.Verifications = append(h.Verifications, *r)
		}
	}

	hrows, err := tx.QueryContext(ctx,
		`SELECT id, question, run_id, answer, answered_at, raised_at FROM hold WHERE challenge_id = ? ORDER BY id ASC`, challengeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = hrows.Close() }()
	for hrows.Next() {
		var (
			ref                    holdRef
			runID                  sql.NullInt64
			answer, answeredAtText sql.NullString
			raisedAtText           string
		)
		if err := hrows.Scan(&ref.ID, &ref.Question, &runID, &answer, &answeredAtText, &raisedAtText); err != nil {
			return nil, err
		}
		if runID.Valid {
			v := runID.Int64
			ref.RunID = &v
		}
		if answer.Valid {
			v := answer.String
			ref.Answer = &v
		}
		if answeredAtText.Valid {
			t, err := parseTimestamp(answeredAtText.String)
			if err != nil {
				return nil, err
			}
			ref.AnsweredAt = &t
		}
		if ref.RaisedAt, err = parseTimestamp(raisedAtText); err != nil {
			return nil, err
		}
		h.Holds = append(h.Holds, ref)
	}
	return h, hrows.Err()
}

func runIntID(r runRow) int64 {
	n, _ := parseRunID(r.ID)
	return n
}

// isCountedFailure は連続失敗に数える結果か（launch_failed を含む。budget_exhausted は
// 実装枠の規則〈予算ガード〉が扱い、ここでは数えない）。
func isCountedFailure(r RunResult) bool {
	switch r {
	case RunResultErrored, RunResultMalformed, RunResultInvalidOutput, RunResultTimedOut,
		RunResultInterrupted, RunResultLaunchFailed:
		return true
	}
	return false
}

// holdIsAfterRun は保留 h が委譲の run r より後に作られたか。原因の run が分かる保留は
// run の ID の順（単調増加）で、無い保留（人間の hold）は作られた時刻で比べる。
func holdIsAfterRun(h holdRef, r runRow) bool {
	if h.RunID != nil {
		return runIntID(r) <= *h.RunID
	}
	return !h.RaisedAt.Before(r.StartedAt)
}

// effectiveResumeTarget は run からセッションを引き継げる run を返す。launch_failed の run は
// セッションを使っていない（または元の宛先を再開しようとして失敗した）ため、その再開元をたどる。
// 新しいセッションの起動の失敗しか無ければ nil。
func (h *launchHistory) effectiveResumeTarget(r runRow) *runRow {
	for depth := 0; depth < len(h.Runs)+1; depth++ {
		if r.Result != RunResultLaunchFailed {
			if r.SessionID == "" {
				return nil
			}
			return &r
		}
		if r.ResumedFromRunID == nil {
			return nil
		}
		next := h.runByDisplayID(*r.ResumedFromRunID)
		if next == nil {
			return nil
		}
		r = *next
	}
	return nil
}

func (h *launchHistory) runByDisplayID(id string) *runRow {
	for i := range h.Runs {
		if h.Runs[i].ID == id {
			r := h.Runs[i]
			return &r
		}
	}
	return nil
}

// decideLaunch は課題の次の委譲の起動の形と、連続失敗の上限の検出を返す。resume が nil なら
// 新しいセッション。limit が非 nil なら、委譲を起動せず人間対応待ちにする。
func (h *launchHistory) decideLaunch(planVersion, failureLimit int) (resume *resumePlan, limit *failureLimitHit) {
	var runs []runRow
	for _, r := range h.Runs {
		if r.PlanVersion != nil && *r.PlanVersion == int64(planVersion) {
			runs = append(runs, r)
		}
	}
	var lastHold *holdRef
	if n := len(h.Holds); n > 0 && h.Holds[n-1].Answer != nil {
		lastHold = &h.Holds[n-1]
	}

	// 最後の回答済みの保留より後の run だけが、連続失敗の対象。
	var since []runRow
	for _, r := range runs {
		if lastHold == nil || !holdIsAfterRun(*lastHold, r) {
			since = append(since, r)
		}
	}

	if len(runs) > 0 && lastHold != nil && holdIsAfterRun(*lastHold, runs[len(runs)-1]) {
		// 回答を受けた直後の起動。原因の run が委譲の run で、使えるセッションがあるときだけ再開する。
		if lastHold.RunID == nil {
			return nil, nil
		}
		for _, r := range runs {
			if runIntID(r) != *lastHold.RunID {
				continue
			}
			if target := h.effectiveResumeTarget(r); target != nil {
				return &resumePlan{Kind: ResumeKindAnswer, Target: *target, Answer: *lastHold.Answer, Question: lastHold.Question}, nil
			}
		}
		return nil, nil
	}
	if len(since) == 0 {
		return nil, nil
	}

	// 連続失敗の数（枠超過の run は数えず、連続も切らない）。
	count := 0
	var last *runRow
	for i := len(since) - 1; i >= 0; i-- {
		r := since[i]
		if r.RateLimited {
			continue
		}
		if !isCountedFailure(r.Result) {
			break
		}
		count++
		if last == nil {
			rr := r
			last = &rr
		}
	}
	if last != nil && count >= failureLimit {
		return nil, &failureLimitHit{Count: count, Limit: failureLimit, Last: *last}
	}

	latest := since[len(since)-1]
	switch latest.Result {
	case RunResultBudgetExhausted:
		// 枠が増やされていなければ、委譲の起動の前に実装枠の残りの検査が止める。
		if latest.SessionID == "" {
			return nil, nil
		}
		return &resumePlan{Kind: ResumeKindBudget, Target: latest}, nil
	case RunResultErrored, RunResultMalformed, RunResultInvalidOutput, RunResultTimedOut, RunResultInterrupted:
		if latest.SessionID == "" {
			return nil, nil
		}
		return &resumePlan{Kind: ResumeKindInterrupted, Target: latest}, nil
	case RunResultLaunchFailed:
		if latest.ResumedFromRunID == nil {
			return nil, nil // 新しいセッションの起動の失敗は、新しいセッションで起動し直す
		}
		target := h.effectiveResumeTarget(latest)
		if target == nil {
			return nil, nil
		}
		kind := ResumeKindInterrupted
		if target.Result == RunResultBudgetExhausted {
			kind = ResumeKindBudget // 上限到達の後の再開が起動に失敗したら、同じ文面で起動し直す
		}
		plan := &resumePlan{Kind: kind, Target: *target}
		// 失敗した起動が回答を渡す再開だったか: 再開元の run を原因とする回答済みの保留があるか。
		for i := len(h.Holds) - 1; i >= 0; i-- {
			hd := h.Holds[i]
			if hd.Answer != nil && hd.RunID != nil && *hd.RunID >= runIntID(*target) && !holdIsAfterRun(hd, latest) {
				plan.Kind, plan.Answer, plan.Question = ResumeKindAnswer, *hd.Answer, hd.Question
				break
			}
		}
		return plan, nil
	}
	return nil, nil
}

// resumeBudgetGrant は、費用が取れない失敗（費用の出所が unknown）に続く `--resume` の再開に限り、
// その 1 回だけ起動を許す実装枠の上限（USD の 100 万分の 1）を返す。許さないときは 0。上限は
// 失敗した run に渡した上限額（＝失敗前の実装枠の残り）。失敗が費用の取れたものである・失敗した
// run 自体が費用の取れない失敗の再開だった（許可は 1 回）・失敗の後に費用を使った run がある、のいずれかなら許さない。
func (h *launchHistory) resumeBudgetGrant(planVersion int, resume *resumePlan) int64 {
	if resume == nil || resume.Kind != ResumeKindInterrupted {
		return 0
	}
	t := resume.Target
	if t.CostSource != costSourceUnknown || !isCountedFailure(t.Result) {
		return 0
	}
	// 失敗した run 自体が、費用の取れない失敗の再開だったなら、許可は使用済み。
	if t.ResumedFromRunID != nil {
		src := h.runByDisplayID(*t.ResumedFromRunID)
		if src == nil || (src.CostSource == costSourceUnknown && isCountedFailure(src.Result)) {
			return 0
		}
	}
	for _, r := range h.Runs {
		if r.PlanVersion == nil || *r.PlanVersion != int64(planVersion) || runIntID(r) <= runIntID(t) {
			continue
		}
		if r.CostUSD == nil || *r.CostUSD != 0 {
			return 0
		}
	}
	return t.MaxBudgetUSD
}

// decideRework は J5 の差し戻しに関する 2 つを返す。同じ計画の版の、最後の回答済みの保留より後の
// J5 の not_met の数が上限に達していれば limit（委譲を起動せず人間対応待ちにする。回数は保留への
// 回答の後の J5 から数え直す）。そうでなく、最後の J5 が not_met で、その後に委譲の run も回答済みの
// 保留も無ければ、直前の委譲のセッションを J5 の指摘つきで再開する resume。
func (h *launchHistory) decideRework(planVersion, reworkLimit int) (resume *resumePlan, limit *reworkLimitHit) {
	var lastHold *holdRef
	if n := len(h.Holds); n > 0 && h.Holds[n-1].Answer != nil {
		lastHold = &h.Holds[n-1]
	}
	var verdicts []runRow // 同じ計画の版の、検査を通った J5 の run
	parsed := map[string]*j5ValidatedOutput{}
	for _, r := range h.Verifications {
		if r.PlanVersion == nil || *r.PlanVersion != int64(planVersion) || r.Result != RunResultSucceeded {
			continue
		}
		v, ok := validateJ5Output([]byte(r.Output))
		if !ok {
			continue
		}
		verdicts = append(verdicts, r)
		parsed[r.ID] = v
	}

	count := 0
	var last *runRow
	for _, r := range verdicts {
		if parsed[r.ID].Verdict != J5VerdictNotMet {
			continue
		}
		if lastHold != nil && holdIsAfterRun(*lastHold, r) {
			continue
		}
		count++
		rr := r
		last = &rr
	}
	if last != nil && count >= reworkLimit {
		return nil, &reworkLimitHit{Count: count, Limit: reworkLimit, Last: *last}
	}

	if len(verdicts) == 0 {
		return nil, nil
	}
	latest := verdicts[len(verdicts)-1]
	if parsed[latest.ID].Verdict != J5VerdictNotMet || (lastHold != nil && holdIsAfterRun(*lastHold, latest)) {
		return nil, nil
	}
	var delegations []runRow
	for _, r := range h.Runs {
		if r.PlanVersion != nil && *r.PlanVersion == int64(planVersion) {
			delegations = append(delegations, r)
		}
	}
	if len(delegations) == 0 {
		return nil, nil
	}
	// J5 の後の委譲が起動に失敗しただけ（launch_failed）なら、指摘はまだ渡せていないので、同じ指摘で
	// 再開し直す。起動できた委譲（結果が launch_failed 以外）があれば、指摘は渡した後。
	for _, r := range delegations {
		if runIntID(r) > runIntID(latest) && r.Result != RunResultLaunchFailed {
			return nil, nil
		}
	}
	prev := delegations[len(delegations)-1]
	target := h.effectiveResumeTarget(prev)
	if target == nil {
		return nil, nil // セッションが無ければ新しいセッションで始める
	}
	return &resumePlan{Kind: ResumeKindRework, Target: *target, Feedback: *parsed[latest.ID].Feedback}, nil
}

// loadRunBranch は再開元の run の子の最後の報告のブランチ名（報告が無い・ブランチが null の
// ときは、照合が確かめたリモートのブランチ）を返す。既定ブランチは子が作ったブランチではない
// ので返さない。無ければ ""。
func loadRunBranch(ctx context.Context, tx *sql.Tx, r runRow, declared []HumanQuestionKind, defaultBranch string) (string, error) {
	if r.Result == RunResultSucceeded && r.Output != "" {
		if rep, ok := validateDelegationReport([]byte(r.Output), declared); ok && rep.Branch != nil && plausibleBranchName(strings.TrimSpace(*rep.Branch)) && strings.TrimSpace(*rep.Branch) != defaultBranch {
			return strings.TrimSpace(*rep.Branch), nil
		}
	}
	var ref sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT ref FROM run_artifact WHERE run_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, runIntID(r), string(artifactKindBranch)).Scan(&ref)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if ref.Valid && plausibleBranchName(ref.String) && ref.String != defaultBranch {
		return ref.String, nil
	}
	return "", nil
}

// formatQuestionsHold は結末 questions の報告の問いを、保留の問いにする整形した文にする。
func formatQuestionsHold(rep *DelegationReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "子が %d 件の問いを報告して止まった。\n", len(rep.Questions))
	if s := strings.TrimSpace(rep.Summary); s != "" {
		fmt.Fprintf(&b, "要約: %s\n", s)
	}
	for i, q := range rep.Questions {
		fmt.Fprintf(&b, "\n%d. [%s] %s\n", i+1, q.Kind, q.Text)
		if len(q.Options) > 0 {
			fmt.Fprintf(&b, "   選択肢: %s\n", strings.Join(q.Options, " / "))
		}
		if q.Recommendation != nil && strings.TrimSpace(*q.Recommendation) != "" {
			fmt.Fprintf(&b, "   推奨: %s\n", *q.Recommendation)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatBlockedHold は結末 blocked の報告を、保留の問いにする文にする。
func formatBlockedHold(rep *DelegationReport) string {
	return "子は作業を進められないと報告した（blocked）。\n要約: " + strings.TrimSpace(rep.Summary)
}

// failureLimitQuestion は連続失敗の上限で人間対応待ちにするときの保留の問い。
func failureLimitQuestion(hit *failureLimitHit) string {
	return fmt.Sprintf("連続失敗の上限に達したため、委譲を起動せず人間対応待ちにした（上限の種類: 連続失敗、回数: %d、上限: %d）。\n"+
		"直近の失敗: %s（結果: %s）。回答すると、次の周から数え直して委譲を起動する。",
		hit.Count, hit.Limit, hit.Last.ID, hit.Last.Result)
}

// reworkLimitQuestion は差し戻しの上限で人間対応待ちにするときの保留の問い。
func reworkLimitQuestion(hit *reworkLimitHit) string {
	return fmt.Sprintf("差し戻しの上限に達したため、委譲を起動せず人間対応待ちにした（上限の種類: 差し戻し、回数: %d、上限: %d）。\n"+
		"直近の検証: %s（達成条件を満たさないと判定された）。回答すると、次の周から数え直して委譲を起動する。",
		hit.Count, hit.Limit, hit.Last.ID)
}

// slotWaitInterval は、元のスロットが使用中の間、空くのを待つ確認の間隔（テストが短くする）。
var slotWaitInterval = 200 * time.Millisecond

// acquireDelegationSlot は委譲のスロットを割り当てる。新しいセッションの起動は通常の割り当て。
// `--resume` の再開は、元のスロット（再開元の run が使ったもの）を次のように使う
// （M3P16・M3P30・M3P38）:
//
//   - worktree: 元のスロットだけ。idle ならそれを使い、busy なら同じ周の中で空くのを待ち
//     （再開元の委譲の時間の上限まで）、needs_attention なら待たずに ErrSlotUnavailable
//   - clone で回答を渡す再開、かつブランチが push 済み: 元のスロットが idle ならそれを、そうでなければ他の空きを使う
//   - 中断の後の再開（仕様に定めが無い）: 作業ツリーの状態を引き継ぐため、clone でも元のスロットだけ
//   - clone でブランチが未 push: 元のスロットだけ。idle でなければ待たずに ErrSlotUnavailable
func (s *Store) acquireDelegationSlot(ctx context.Context, in DelegateInput, dc *delegationContext, bind SlotBinder) (*SlotAssignment, error) {
	if dc.Resume == nil || dc.Resume.Target.SlotID == nil {
		return s.AcquireSlot(ctx, in.Git, dc.Repo, bind)
	}
	original := *dc.Resume.Target.SlotID
	worktree := dc.Repo.Slots.Provider == string(slotProviderWorktree)
	if !worktree && dc.ResumeBranchPushed && (dc.Resume.Kind == ResumeKindAnswer || dc.Resume.Kind == ResumeKindRework) {
		return s.acquireSlot(ctx, in.Git, dc.Repo, bind, slotChoice{Prefer: original})
	}
	choice := slotChoice{Only: original}
	if !worktree {
		return s.acquireSlot(ctx, in.Git, dc.Repo, bind, choice)
	}
	deadline := time.Now().Add(time.Duration(in.AgentDecl.TimeoutSec.Delegate) * time.Second)
	return waitForOriginalSlot(ctx, deadline,
		func() (*SlotAssignment, error) { return s.acquireSlot(ctx, in.Git, dc.Repo, bind, choice) },
		func() (slotState, error) { return s.slotStateOf(ctx, original) })
}

// waitForOriginalSlot は元のスロットを取れるまで、取り直しを繰り返す。
// 取れなかった（ErrSlotUnavailable）後に読んだ状態が busy なら待って取り直す。idle なら、取れなかった
// 後に他のプロセスが解放したということなので、待たずに取り直す。needs_attention・行の欠落・
// 期限切れは ErrSlotUnavailable のまま返す。
func waitForOriginalSlot(ctx context.Context, deadline time.Time,
	acquire func() (*SlotAssignment, error), stateOf func() (slotState, error)) (*SlotAssignment, error) {
	for {
		a, err := acquire()
		if !errors.Is(err, ErrSlotUnavailable) {
			return a, err
		}
		state, serr := stateOf()
		if serr != nil {
			return nil, serr
		}
		if (state != slotStateBusy && state != slotStateIdle) || time.Now().After(deadline) {
			return nil, err
		}
		if state == slotStateIdle {
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(slotWaitInterval):
		}
	}
}

func (s *Store) slotStateOf(ctx context.Context, slotID string) (slotState, error) {
	id, ok := parseSlotID(slotID)
	if !ok {
		return "", ErrNotFound
	}
	var state slotState
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		r, err := loadSlotByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if r != nil {
			state = r.State
		}
		return nil
	})
	return state, classifyReadWriteErr(err)
}

// holdForFailureLimit は連続失敗の上限に達した課題を、委譲を起動せずに人間対応待ちにする
// （原因の run は最後の失敗の run）。課題が着手中でなくなっていれば何もしない。
func (s *Store) holdForFailureLimit(ctx context.Context, dc *delegationContext) (*JudgmentAutoItem, *NotStarted, error) {
	hit := dc.LimitHit
	status, _, err := s.mapReconciliation(ctx, dc, hit.Last.ID, OpHold, failureLimitQuestion(hit))
	if err != nil {
		return nil, nil, err
	}
	if status == nil {
		return nil, nil, nil // 課題が着手中でなくなっていた
	}
	return nil, &NotStarted{ChallengeID: dc.Challenge.ID, Reason: NotStartedFailureLimit,
		Detail: fmt.Sprintf("%d consecutive failures (limit %d)", hit.Count, hit.Limit)}, nil
}

// holdForReworkLimit は差し戻しの上限に達した課題を、委譲を起動せずに人間対応待ちにする
// （原因の run は最後の J5 の run）。課題が着手中でなくなっていれば何もしない。
func (s *Store) holdForReworkLimit(ctx context.Context, dc *delegationContext) (*JudgmentAutoItem, *NotStarted, error) {
	hit := dc.ReworkHit
	status, _, err := s.mapReconciliation(ctx, dc, hit.Last.ID, OpHold, reworkLimitQuestion(hit))
	if err != nil {
		return nil, nil, err
	}
	if status == nil {
		return nil, nil, nil // 課題が着手中でなくなっていた
	}
	return nil, &NotStarted{ChallengeID: dc.Challenge.ID, Reason: NotStartedReworkLimit,
		Detail: fmt.Sprintf("%d J5 rejections (limit %d)", hit.Count, hit.Limit)}, nil
}

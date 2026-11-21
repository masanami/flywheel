package core

// このファイルは、衝突の解消の確かめ（J5 の 3 つ目の動き方）の、対象選び・入力・実行の記録・結果の振り分けの口を持つ
// （docs/features/core-redesign.md §衝突の解消の確かめ（J5 の動き方））。
//
//   - 対象: 確かめ待ちの中身（contract_run.fix_pending_ref）を持ち、終わっていない契約の実行。統合先宛ては着手中の課題の、検証中のタスクの実行。
//     反映先宛ては完了の課題の、終わっていない反映の操作に対応する実行。保留中の課題の実行は選ばない。見直し前の対象（検証中の課題・検証中のタスク・
//     課題の単位の確かめ）の後に処理する。
//   - 入力: 通した中身から確かめ待ちの中身への変わり方を、上流の比較の口で読み直し、衝突の解消ありのときだけ組み立てる（そうでなければ run を作らない）。
//     作業ディレクトリは確かめ待ちの中身を取り出した一時の作業ツリー。
//   - 実行の記録: 統合先宛てはそのタスクの実行（task_id あり）、反映先宛ては課題の実行（task_id なし）。同じ単位に終わっていない実行があれば始めない。
//     費用は判断点の費用で、タスクの使った額には数えない。
//   - 結果: 満たした・満たしていない・判断できないを、書く直前の読み直し（j5FixStillCurrent）を通した上で、宛先ごとの書き込みの口
//     （j5FixWriters）へ振り分ける。列の更新・作業ログ・承認の取り消しは、その口の実装が行う（このファイルは書かない）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// 衝突の解消の確かめの入力の区画のラベル（J5 の指示文はこの名前で区画を指す）。
const (
	labelFixModeSection    = "衝突の解消の確かめ"
	labelFixPassedSection  = "通した中身"
	labelFixPendingSection = "確かめ待ちの中身"
	labelFixDiffSection    = "衝突の解消の差分"
	labelFixCheckoutNotice = "作業ディレクトリの注記"
)

// j5FixModeNote は区画「衝突の解消の確かめ」の本文。
const j5FixModeNote = "この確認は衝突の解消の確かめである。通した中身（すでに完了条件に照らして確かめた中身）から、確かめ待ちの中身へ変わるときに、" +
	"宛先の最新を取り込むマージで直された差分だけを確かめる。全体を検証し直さない。"

// j5FixDest は衝突の解消の確かめの宛先。
type j5FixDest string

const (
	// j5FixDestIntegration は統合先宛て（着手中の課題の、検証中のタスクの実行）。
	j5FixDestIntegration j5FixDest = "integration"
	// j5FixDestReflect は反映先宛て（完了の課題の、終わっていない反映の操作に対応する実行）。
	j5FixDestReflect j5FixDest = "reflect"
)

// j5FixTarget は衝突の解消の確かめの対象（契約の実行 1 件）。
type j5FixTarget struct {
	ChallengeID int64
	// RunRowID は契約の実行（contract_run）の行の ID。
	RunRowID int64
	Dest     j5FixDest
	// TaskID は統合先宛てのタスクの内部整数 ID（反映先宛ては nil。確かめの run の task_id になる）。
	TaskID *int64
	// OperationID は反映先宛ての、対応する反映の操作の内部整数 ID（統合先宛ては nil）。
	OperationID *int64
}

// j5FixScope は 1 回の確かめが始めたときに読んだ入力と、書く直前に照合する値。
type j5FixScope struct {
	Target     j5FixTarget
	Challenge  Challenge
	Plan       approvedPlan
	Task       *Task
	Source     *SourceChild
	PassedRef  string
	PendingRef string
	// Repo は比較の口へ渡すリポジトリ（"<owner>/<name>"）、Base は宛先のブランチ（統合先、または反映先の既定ブランチ）。
	Repo, Base string
	Change     HeadChange
}

// j5FixResult は確かめの結果。宛先ごとの書き込みの口（j5FixWriters）が受け取る。
type j5FixResult struct {
	Scope *j5FixScope
	// Verdict は J5 の出力の判定（met が満たしたまま、not_met が問題あり、uncertain が判断できない）。
	Verdict  J5Verdict
	Reason   string
	Feedback string
	Question string
	// RunID は確かめの run の表示形（"R-<n>"）。連続の失敗の上限で判断できないと同じに扱うときは、最後に失敗した J5 の run。
	RunID string
	// LimitReached は、J5 を呼ばず、連続の失敗の上限に達したために判断できないと同じに扱ったとき真。
	LimitReached bool
}

// j5FixWriter は確かめの結果を書く口。結果を書く直前の読み直しを通ったときだけ、その書き込みのトランザクションの中で呼ばれる。
type j5FixWriter func(ctx context.Context, tx *sql.Tx, rec *activityRecorder, r *j5FixResult) error

// j5FixWriters は宛先ごとの書き込みの口。nil の口は何も書かない（統合先側・反映先側の実装が、それぞれの口を足す）。
type j5FixWriters struct {
	Integration, Reflect j5FixWriter
}

func (w j5FixWriters) forDest(d j5FixDest) j5FixWriter {
	if d == j5FixDestIntegration {
		return w.Integration
	}
	return w.Reflect
}

// j5FixCandidate は確かめ待ちの中身を持つ、終わっていない契約の実行の行。
type j5FixCandidate struct {
	ID, ChallengeID int64
	TaskID, OpID    sql.NullInt64
	Kind            string
	ChallengeStatus string
}

const j5FixCandidateSelect = `SELECT k.id, k.challenge_id, k.task_id, k.operation_id, k.kind, c.status
	FROM contract_run k JOIN challenge c ON c.id = k.challenge_id
	WHERE k.fix_pending_ref IS NOT NULL AND k.fix_pending_ref <> '' AND k.ended_at IS NULL`

func scanJ5FixCandidates(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]j5FixCandidate, error) {
	rows, err := tx.QueryContext(ctx, j5FixCandidateSelect+where+` ORDER BY k.challenge_id, k.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []j5FixCandidate
	for rows.Next() {
		var c j5FixCandidate
		if err := rows.Scan(&c.ID, &c.ChallengeID, &c.TaskID, &c.OpID, &c.Kind, &c.ChallengeStatus); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// resolveJ5FixTarget は c が確かめの対象か（宛先の条件を満たすか）を返す。保留中・着手中でも完了でもない課題の実行は対象にしない。
func resolveJ5FixTarget(ctx context.Context, tx *sql.Tx, c j5FixCandidate) (*j5FixTarget, error) {
	switch Status(c.ChallengeStatus) {
	case StatusInProgress:
		if c.Kind != contractRunKindTask || !c.TaskID.Valid {
			return nil, nil
		}
		integ, err := challengeUsesIntegration(ctx, tx, c.ChallengeID)
		if err != nil {
			return nil, err
		}
		if integ == nil || !*integ {
			return nil, nil
		}
		var status string
		err = tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ? AND challenge_id = ?`, c.TaskID.Int64, c.ChallengeID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if TaskStatus(status) != TaskStatusVerifying {
			return nil, nil
		}
		tid := c.TaskID.Int64
		return &j5FixTarget{ChallengeID: c.ChallengeID, RunRowID: c.ID, Dest: j5FixDestIntegration, TaskID: &tid}, nil
	case StatusDone:
		opID, err := j5FixReflectOperation(ctx, tx, c)
		if err != nil || opID == 0 {
			return nil, err
		}
		return &j5FixTarget{ChallengeID: c.ChallengeID, RunRowID: c.ID, Dest: j5FixDestReflect, OperationID: &opID}, nil
	}
	return nil, nil
}

// j5FixReflectOperation は、完了の課題の契約の実行 c に対応する、終わっていない（承認待ち・承認済みの）反映の操作の内部整数 ID を返す
// （無ければ 0）。上げる依頼と、委譲先のフローを通らない成果物の反映の依頼は、実行に結び付く操作。統合先を使わない課題のタスクの実行は、
// 課題の、最後の終わっていない反映の操作。
func j5FixReflectOperation(ctx context.Context, tx *sql.Tx, c j5FixCandidate) (int64, error) {
	open := func(op *operationRow) bool {
		return op.Kind == string(OperationKindRelease) && (op.State == OperationStatePending || op.State == OperationStateApproved)
	}
	switch c.Kind {
	case contractRunKindReflect:
		if !c.OpID.Valid {
			return 0, nil
		}
		op, err := loadOperationRow(ctx, tx, c.OpID.Int64)
		if errors.Is(err, ErrNotFound) {
			return 0, nil
		}
		if err != nil || op.ChallengeID != c.ChallengeID || !open(op) {
			return 0, err
		}
		return op.ID, nil
	case contractRunKindTask:
		integ, err := challengeUsesIntegration(ctx, tx, c.ChallengeID)
		if err != nil || (integ != nil && *integ) {
			return 0, err
		}
		var id int64
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM operation WHERE challenge_id = ? AND kind = ? AND state IN (?, ?) ORDER BY id DESC LIMIT 1`,
			c.ChallengeID, string(OperationKindRelease), string(OperationStatePending), string(OperationStateApproved)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return id, err
	}
	return 0, nil
}

// j5FixHasActiveRun は、確かめを始めてはいけない、同じ単位の終わっていない実行（run）があるか。統合先宛てはそのタスクの実行と、タスクに
// 結び付かない実行。反映先宛ては、タスクに結び付かない実行。
func j5FixHasActiveRun(ctx context.Context, tx *sql.Tx, tg j5FixTarget) (bool, error) {
	if tg.TaskID != nil {
		if active, err := taskHasActiveRun(ctx, tx, tg.ChallengeID, *tg.TaskID); err != nil || active {
			return active, err
		}
	}
	return taskLessHasActiveRun(ctx, tx, tg.ChallengeID)
}

// selectJ5FixTargets は衝突の解消の確かめの対象を、課題の ID の昇順（課題の中では契約の実行の行の ID の昇順）で返す。only が非 nil なら、
// その課題（内部整数 ID）だけ。終わっていない実行を持つ単位は今は選ばない。取り込み元の食い違いで自動の対象から外す課題は、
// excluded に理由つきで返す（見直し前の J5 と同じ。only を指定した個別の操作では外さない）。
func selectJ5FixTargets(ctx context.Context, tx *sql.Tx, only *int64) (out []j5FixTarget, excluded []NotStarted, err error) {
	var cands []j5FixCandidate
	if only != nil {
		cands, err = scanJ5FixCandidates(ctx, tx, ` AND k.challenge_id = ?`, *only)
	} else {
		cands, err = scanJ5FixCandidates(ctx, tx, "")
	}
	if err != nil {
		return nil, nil, err
	}
	exclusion := map[int64]*NotStarted{}
	reported := map[int64]bool{}
	for _, c := range cands {
		tg, err := resolveJ5FixTarget(ctx, tx, c)
		if err != nil {
			return nil, nil, err
		}
		if tg == nil {
			continue
		}
		active, err := j5FixHasActiveRun(ctx, tx, *tg)
		if err != nil {
			return nil, nil, err
		}
		if active {
			continue
		}
		if only == nil {
			ex, seen := exclusion[tg.ChallengeID]
			if !seen {
				if ex, err = challengeAutoExclusion(ctx, tx, tg.ChallengeID); err != nil {
					return nil, nil, err
				}
				exclusion[tg.ChallengeID] = ex
			}
			if ex != nil {
				if !reported[tg.ChallengeID] {
					reported[tg.ChallengeID] = true
					excluded = append(excluded, *ex)
				}
				continue
			}
		}
		out = append(out, *tg)
	}
	return out, excluded, nil
}

// remoteOfRepoName は宣言のリポジトリの名前 name の remote（"<owner>/<name>"）。引けなければ空。
func remoteOfRepoName(decl *ConnectorsDeclaration, name string) string {
	if decl == nil {
		return ""
	}
	for i := range decl.Repos {
		if decl.Repos[i].Name == name {
			return decl.Repos[i].Remote
		}
	}
	return ""
}

// loadJ5FixScope は tg の確かめの入力のうち、ストアから読めるものを読む。tg が対象でなくなっている（行が終わった・確かめ待ちの中身が消えた・
// 課題が対象の状態でなくなった）ときは nil。承認済みの計画が無ければ ErrValidation（引けない理由があれば *planUnavailableError）。
// 通した中身・リポジトリ・宛先のブランチが引けないときも nil（比較できないので確かめを始めない）。
func loadJ5FixScope(ctx context.Context, tx *sql.Tx, in J5AutoInput, tg j5FixTarget) (*j5FixScope, error) {
	cands, err := scanJ5FixCandidates(ctx, tx, ` AND k.id = ?`, tg.RunRowID)
	if err != nil || len(cands) != 1 {
		return nil, err
	}
	cur, err := resolveJ5FixTarget(ctx, tx, cands[0])
	if err != nil || cur == nil || cur.Dest != tg.Dest {
		return nil, err
	}
	row, err := scanContractRun(tx.QueryRowContext(ctx, contractRunSelect+` WHERE id = ?`, tg.RunRowID))
	if err != nil {
		return nil, err
	}
	ch, err := loadChallenge(ctx, tx, tg.ChallengeID)
	if err != nil {
		return nil, err
	}
	plan, gap, detail, err := lookupApprovedPlan(ctx, tx, tg.ChallengeID)
	if err != nil {
		return nil, err
	}
	if gap != planGapNone {
		if gap != planGapNoApproval {
			return nil, &planUnavailableError{Detail: detail}
		}
		return nil, fmt.Errorf("%w: the challenge has no approved plan", ErrValidation)
	}
	sc := &j5FixScope{Target: tg, Challenge: *ch, Plan: plan, PendingRef: row.FixPendingRef}

	switch tg.Dest {
	case j5FixDestIntegration:
		sc.PassedRef = row.VerifiedRef
		task, err := loadTask(ctx, tx, *tg.TaskID)
		if err != nil {
			return nil, err
		}
		sc.Task = task
		sc.Repo = remoteOfRepoName(in.Connectors, task.Repo)
		var ref sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT integration_ref FROM challenge WHERE id = ?`, tg.ChallengeID).Scan(&ref); err != nil {
			return nil, err
		}
		sc.Base = ref.String
		if task.SourceKey != "" {
			children, err := loadSourceChildren(ctx, tx, tg.ChallengeID)
			if err != nil {
				return nil, err
			}
			for i := range children {
				if children[i].ExternalKey == task.SourceKey {
					sc.Source = &children[i]
				}
			}
		}
	default:
		sc.PassedRef = row.FixVerifiedRef
		op, err := loadOperationRow(ctx, tx, *tg.OperationID)
		if err != nil {
			return nil, err
		}
		if sc.PassedRef == "" {
			if sc.PassedRef, err = latestReleaseApprovedHead(ctx, tx, op.ID); err != nil {
				return nil, err
			}
		}
		if repo, _, ok := parseReleasePullRequestRef(op.Ref.String); ok && in.Connectors != nil {
			for i := range in.Connectors.Repos {
				if strings.EqualFold(in.Connectors.Repos[i].Remote, repo) {
					sc.Repo, sc.Base = in.Connectors.Repos[i].Remote, in.Connectors.Repos[i].DefaultBranch
				}
			}
		}
	}
	if sc.PassedRef == "" || sc.PendingRef == "" || sc.Repo == "" || sc.Base == "" {
		return nil, nil
	}
	return sc, nil
}

// j5FixStillCurrent は、確かめの結果を書く直前の読み直し（書き込みのトランザクションの中）。契約の実行がまだ終わっておらず、中身の識別
// （content_ref）と確かめ待ちの中身が、確かめを始めたときの確かめ待ちの中身と同じで、課題がまだ対象の状態（統合先宛ては着手中・反映先宛ては
// 完了。保留中ではない）なら真。違えば結果を捨てる。
func j5FixStillCurrent(ctx context.Context, tx *sql.Tx, sc *j5FixScope) (bool, error) {
	var contentRef, pending sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT content_ref, fix_pending_ref FROM contract_run WHERE id = ? AND ended_at IS NULL`,
		sc.Target.RunRowID).Scan(&contentRef, &pending)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !pending.Valid || pending.String != sc.PendingRef || !contentRef.Valid || contentRef.String != sc.PendingRef {
		return false, nil
	}
	cands, err := scanJ5FixCandidates(ctx, tx, ` AND k.id = ?`, sc.Target.RunRowID)
	if err != nil || len(cands) != 1 {
		return false, err
	}
	cur, err := resolveJ5FixTarget(ctx, tx, cands[0])
	if err != nil || cur == nil || cur.Dest != sc.Target.Dest {
		return false, err
	}
	if sc.Target.OperationID != nil && (cur.OperationID == nil || *cur.OperationID != *sc.Target.OperationID) {
		return false, nil
	}
	return true, nil
}

// formatExpectedScenes は「完了したら見えるもの」の本文（課題の予想を優先し、無ければ承認済みの計画の構造化した出力のもの）。
func formatExpectedScenes(ch Challenge, plan approvedPlan) string {
	var sp j2RawOutput
	if plan.HasSpec {
		_ = json.Unmarshal([]byte(plan.Spec), &sp)
	}
	expected := ch.Expected
	if len(expected) == 0 {
		expected = sp.Expected
	}
	if len(expected) == 0 {
		return "（記録なし）"
	}
	var b strings.Builder
	for _, e := range expected {
		fmt.Fprintf(&b, "- %s（変更前: %s → 変更後: %s）\n", e.Scene, e.Before, e.After)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatConflictFixes は区画「衝突の解消の差分」の本文。取り込みのマージのコミットごとに、両方の親と違う形になったファイルの名前と、通した中身の側の
// 親からの差分を書く。差分を上流が返さなかったファイルは、名前と、差分を読めなかった旨を書く。上限を超えた分は切り、切ったことを書く。
func formatConflictFixes(fixes []ConflictFix) string {
	var blocks []string
	for _, fx := range fixes {
		if len(fx.Files) == 0 {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "取り込みのマージのコミット: %s\n両方の親と違う形になったファイルと、通した中身の側の親からの差分:", fx.MergeCommit)
		for _, f := range fx.Files {
			if f.PatchUnavailable {
				fmt.Fprintf(&b, "\n\nファイル: %s\n（差分は上流が返さなかったので、読めなかった）", f.Filename)
				continue
			}
			fmt.Fprintf(&b, "\n\nファイル: %s\n%s", f.Filename, f.Patch)
		}
		blocks = append(blocks, b.String())
	}
	content, omitted := formatPullRequestDiffs(blocks)
	if omitted > 0 {
		content += fmt.Sprintf("\n\n（差分は合計 %d バイトあり、先頭の %d バイトまでを入れて残りを省略した）", omitted, maxPullRequestDiffBytes)
	}
	return content
}

// fixCheckSections は確かめの入力（データの区画）を組み立てる。checkoutNote は作業ディレクトリの注記。
func fixCheckSections(sc *j5FixScope, checkoutNote string, memoryIndex []JudgmentDataSection) []JudgmentDataSection {
	ch := sc.Challenge
	urgency, priority := "", ""
	if ch.Urgency != nil {
		urgency = string(*ch.Urgency)
	}
	if ch.Priority != nil {
		priority = string(*ch.Priority)
	}
	criteria, origin := "", ""
	if sc.Task != nil {
		criteria, origin = sc.Task.DoneCriteria, "タスクの完了条件"
	} else {
		criteria, origin = formatExpectedScenes(ch, sc.Plan), "計画の「完了したら見えるもの」"
	}
	sections := []JudgmentDataSection{
		{Label: "課題", Content: fmt.Sprintf("タイトル: %s\n\n説明:\n%s\n\n緊急度: %s\n優先度: %s\n起票者: %s",
			ch.Title, ch.Description, urgency, priority, ch.Reporter)},
		{Label: "達成条件", Content: criteria + "\n\n（出所: " + origin + "）"},
		{Label: labelFixModeSection, Content: j5FixModeNote},
	}
	if sc.Task != nil {
		sections = append(sections, JudgmentDataSection{Label: labelTaskSection, Content: formatTaskSection(*sc.Task)})
	}
	if sc.Source != nil {
		sections = append(sections, JudgmentDataSection{Label: labelTaskSourceSection, Content: formatTaskSourceSection(*sc.Source)})
	}
	sections = append(sections,
		JudgmentDataSection{Label: labelFixPassedSection, Content: sc.PassedRef},
		JudgmentDataSection{Label: labelFixPendingSection, Content: sc.PendingRef},
		JudgmentDataSection{Label: labelFixDiffSection, Content: formatConflictFixes(sc.Change.ConflictFixes)},
	)
	if checkoutNote != "" {
		sections = append(sections, JudgmentDataSection{Label: labelFixCheckoutNotice, Content: checkoutNote})
	}
	return append(sections, memoryIndex...)
}

// verifyFixTarget は衝突の解消の確かめ 1 件を実行する。対象でなくなっていれば ErrInvalidTransition、同じ単位に終わっていない実行があれば
// ErrRunInProgress、承認済みの計画が無ければ ErrValidation（または *planUnavailableError）を返す。変わり方が衝突の解消ありでない（比較の口が
// 無い・判定できない・取り込みだけ・関係のない変更あり）ときは、run を作らず、列も変えずに (nil, nil, nil)。
func (s *Store) verifyFixTarget(ctx context.Context, in J5AutoInput, jc *JudgmentCycle, tg j5FixTarget) (*JudgmentAutoItem, *NotStarted, error) {
	chID := formatChallengeID(tg.ChallengeID)
	if jc != nil && jc.RateLimited() {
		return nil, &NotStarted{ChallengeID: chID, Reason: NotStartedRateLimited}, nil
	}
	var scope *j5FixScope
	var active bool
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		if scope, err = loadJ5FixScope(ctx, tx, in, tg); err != nil || scope == nil {
			return err
		}
		active, err = j5FixHasActiveRun(ctx, tx, tg)
		return err
	})); err != nil {
		return nil, nil, err
	}
	if scope == nil {
		return nil, nil, ErrInvalidTransition
	}
	if active {
		return nil, nil, ErrRunInProgress // 上流の取得や予算の評価より先に（取り違えない）
	}
	// 通した中身から確かめ待ちの中身への変わり方を、上流の比較の口で自分で読み直す（委譲先の申告は使わない）。
	if in.Compare == nil {
		return nil, nil, nil
	}
	scope.Change = ClassifyHeadChange(context.WithoutCancel(ctx), in.Compare, scope.Repo, scope.Base, scope.PassedRef, scope.PendingRef)
	if classifyContentMove(scope.Change) != contentMoveConflictFix {
		return nil, nil, nil
	}

	// J5 の呼び出しが連続の失敗の上限に達していれば、呼ばずに、判断できないと同じに扱う。
	var hit *judgmentLimitHit
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		hit, err = loadJudgmentLimitHit(ctx, tx, tg.ChallengeID, scope.Challenge.Version, JudgmentJ5, judgmentFailureLimit(in.AgentDecl))
		return err
	})); err != nil {
		return nil, nil, err
	}
	if hit != nil {
		_, err := s.applyJ5Fix(ctx, hit.Last.ID, scope, &j5FixResult{
			Verdict: J5VerdictUncertain, LimitReached: true,
			Reason:   fmt.Sprintf("判断点 J5 の連続失敗の上限に達した（連続失敗: %d 回、上限: %d）", hit.Count, hit.Limit),
			Question: judgmentLimitQuestion(hit),
		})
		return nil, nil, err
	}

	j5Budget := in.AgentDecl.JudgmentBudgetFor(JudgmentJ5)
	if err := s.checkCycleBudget(ctx, in.CycleID, j5Budget); err != nil {
		if jc != nil && errors.Is(err, ErrBudgetExceeded) {
			return nil, &NotStarted{ChallengeID: chID, Reason: NotStartedCycleBudget}, nil
		}
		return nil, nil, err
	}

	// 作業ディレクトリは、確かめ待ちの中身を取り出した一時の作業ツリー。
	co, reason, err := s.checkoutJ5Commit(ctx, in, scope.Repo, scope.PendingRef)
	if err != nil {
		return nil, &NotStarted{ChallengeID: chID, Reason: NotStartedUpstreamFetchFailed, Detail: err.Error()}, nil
	}
	head := &j5Head{git: in.HeadGit, WorkDir: co.WorkDir, clone: co.clone, ref: co.ref}
	defer head.remove(ctx)
	note := fmt.Sprintf("作業ディレクトリは、確かめ待ちの中身（%s）を取り出した作業ツリーである。", scope.PendingRef)
	if reason != "" {
		note = fmt.Sprintf("確かめ待ちの中身（%s）を取り出せなかった（%s）。作業ディレクトリはワークスペースで、中身は差分の区画でしか読めない。", scope.PendingRef, reason)
	}

	planVersion := int64(scope.Plan.Version)
	runIn := RunJudgmentInput{
		ChallengeID:  chID,
		Judgment:     JudgmentJ5,
		MaxBudgetUSD: j5Budget,
		TimeoutSec:   in.AgentDecl.TimeoutSec.Judgment,
		Sections:     fixCheckSections(scope, note, memoryIndexSections(s.workspace, in.AgentDecl)),
		OutputSchema: j5OutputSchema(),
		Invoker:      in.Invoker,
		PlanVersion:  &planVersion,
		WorkDir:      head.WorkDir,
		TaskID:       tg.TaskID, // 統合先宛てはそのタスクの実行。反映先宛ては nil（課題の実行）
	}
	var res *RunJudgmentResult
	if jc != nil {
		var notStarted *NotStarted
		if res, notStarted, err = jc.RunJudgment(ctx, s, runIn); err != nil || notStarted != nil {
			return nil, notStarted, err
		}
	} else {
		cycleID := in.CycleID
		runIn.CycleID = &cycleID
		if res, err = s.RunJudgment(ctx, runIn); err != nil {
			return nil, nil, err
		}
	}
	item := &JudgmentAutoItem{ChallengeID: res.ChallengeID, RunID: res.RunID, Result: res.Result}
	if res.Result == RunResultSucceeded {
		item.Outcome, item.Status, item.Result, err = s.finalizeJ5FixOutput(ctx, res.RunID, scope, res.StructuredOutput)
		if err != nil {
			return nil, nil, err
		}
	}
	return item, nil, nil
}

// finalizeJ5FixOutput は run が succeeded のときに、確かめの出力を検査し直して振り分ける。検査に落ちたら run の結果を invalid_output にし、
// 何も書かない。書かなかった（直前の読み直しで対象が変わっていた）ときは status が nil。status は書いた後の課題の状態（確かめは課題の状態を動かさない）。
func (s *Store) finalizeJ5FixOutput(ctx context.Context, runIDDisplay string, scope *j5FixScope, structured []byte) (outcome string, status *string, result RunResult, err error) {
	v, ok := validateJ5Output(structured)
	if !ok {
		if err := s.invalidateRunOutput(ctx, runIDDisplay); err != nil {
			return "", nil, "", err
		}
		return "", nil, RunResultInvalidOutput, nil
	}
	r := &j5FixResult{Verdict: v.Verdict, Reason: v.Reason}
	if v.Feedback != nil {
		r.Feedback = *v.Feedback
	}
	if v.Question != nil {
		r.Question = *v.Question
	}
	mapped, err := s.applyJ5Fix(ctx, runIDDisplay, scope, r)
	if err != nil {
		return "", nil, "", err
	}
	outcome = string(v.Verdict)
	if !mapped {
		return outcome, nil, RunResultSucceeded, nil
	}
	st := string(scope.Challenge.Status)
	return outcome, &st, RunResultSucceeded, nil
}

// applyJ5Fix は確かめの結果 r を、書く直前の読み直し（j5FixStillCurrent）を通したときだけ、宛先ごとの書き込みの口へ振り分ける。
// 読み直しで対象が変わっていれば何も書かず mapped=false。
func (s *Store) applyJ5Fix(ctx context.Context, runIDDisplay string, scope *j5FixScope, r *j5FixResult) (mapped bool, err error) {
	runInt, ok := parseRunID(runIDDisplay)
	if !ok {
		return false, ErrValidation
	}
	actor, err := s.resolveActor()
	if err != nil {
		return false, err
	}
	r.Scope, r.RunID = scope, runIDDisplay
	err = s.mutateAsRun(context.WithoutCancel(ctx), actor, ChannelInvoker, VerificationNone, &runInt, func(tx *sql.Tx, rec *activityRecorder) error {
		cur, err := j5FixStillCurrent(ctx, tx, scope)
		if err != nil || !cur {
			return err
		}
		mapped = true
		if write := s.fixWriters.forDest(scope.Target.Dest); write != nil {
			return write(ctx, tx, rec, r)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return mapped, nil
}

package core

// このファイルは反映の段（release_exec.go）が、課題の終わっていない契約の実行をどう扱うかを持つ
// （docs/features/core-redesign.md §反映の段）:
//
//   - 対応する契約の実行の決め方（loadReleaseRun）: 反映の依頼なら記録した operation_id が反映の操作のもの、タスクの実行なら
//     取り込んだ成果物の参照に反映の操作の参照と同じものがあるもの。
//   - 先に commands.status で読む（readReleaseRun）。成果物は課題の成果物の記録へ取り込まず、予算の上限での中止もしない。
//   - 道「タスクの実行へ返す」（landToRun。反映の依頼へも返す）: 「入れてよい」を、通す中身（{approved_ref}）を付けて commands.resume で返す。
//
// 反映先へ入れるのは委譲先で、flywheel は入れない。「入れてよい」を返してよいかの判定（承認済み・通す中身・チェック）は core が持つ。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// 反映の試行の route の値（反映の依頼の経路）。waiting_outside の outside は ReleaseRouteOutside。
const (
	// ReleaseRouteWaitingRun は、対応する契約の実行がタスクの実行であること。
	ReleaseRouteWaitingRun = "waiting_run"
	// ReleaseRouteReflectRequest は、対応する契約の実行が反映の依頼であること。
	ReleaseRouteReflectRequest = "reflect_request"
)

// errReleaseContractBusy は、反映の段が契約のコマンドを呼ぶための実行の記録を作れなかった（その課題に終わっていない実行の記録がある）
// ことを表す。開いた試行を残さずに終え、始めなかった理由 in_progress を返す。
var errReleaseContractBusy = errors.New("core: release: the challenge has an unfinished run, so a contract command cannot be recorded")

// releaseRun は、反映の操作の課題の終わっていない契約の実行（課題ごとに高々 1 本）。
type releaseRun struct {
	row *contractRunRow
	// corresponds は、この反映の操作に対応する実行か。
	corresponds bool
	// fixEligible は、衝突の解消の確かめ（確かめ待ちの中身などの 3 列）の道に入ってよい実行か。対応する実行で、反映の依頼（operation_id で
	// 1 対 1 に結び付く）か、タスクの実行で対応する終わっていない反映の操作がちょうど 1 つのとき真。3 列は契約の実行ごとに 1 つで、
	// どの操作の中身かを持たないので、2 つ以上の操作が対応するときは入らない（反映の段は S3a のまま扱い、J5 も対象にしない）。
	fixEligible bool
	planVersion *int64
	binding     *sourceBinding
	// opID は、この実行を読む・取り消す反映の操作の内部整数 ID（契約のコマンドの run に印として付ける）。
	opID int64
}

// route は対応する実行の種類を route の値にしたもの（対応しない実行は空）。
func (r *releaseRun) route() string {
	switch {
	case r == nil || !r.corresponds:
		return ""
	case r.row.Kind == contractRunKindReflect:
		return ReleaseRouteReflectRequest
	}
	return ReleaseRouteWaitingRun
}

// releaseStageRunWhere は、反映の段が扱う契約の実行（contract_run）の条件。完了の課題の反映の依頼（kind が reflect）と、統合先を使わない
// 課題のタスクの実行。統合先を使う課題のタスクの実行と、完了でない課題の反映の依頼（上げる依頼）は、委譲の段の先頭の状態の確認が
// 進めるので、反映の段は扱わない。
const releaseStageRunWhere = `((kind = 'reflect' AND EXISTS (SELECT 1 FROM challenge ch WHERE ch.id = contract_run.challenge_id AND ch.status = 'done'))
	OR (kind <> 'reflect' AND NOT EXISTS (SELECT 1 FROM challenge ch WHERE ch.id = contract_run.challenge_id AND ch.uses_integration = 1)))`

// loadOpenRunInTx は課題 cid の、終わっていない契約の実行（課題ごとに高々 1 本。無ければ nil）を、取り込み元の結び付き・
// 承認済みの計画の版つきで読む。対応するかは決めない（corresponds は偽）。
func loadOpenRunInTx(ctx context.Context, tx *sql.Tx, cid int64) (*releaseRun, error) {
	cr, err := scanContractRun(tx.QueryRowContext(ctx, contractRunSelect+` WHERE challenge_id = ? AND ended_at IS NULL AND `+releaseStageRunWhere+` ORDER BY id LIMIT 1`, cid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &releaseRun{row: cr}
	if out.binding, err = loadTaskSourceBinding(ctx, tx, cr.ChallengeID, cr.TaskID); err != nil {
		return nil, err
	}
	if p, found, err := loadApprovedPlanAnySpec(ctx, tx, cr.ChallengeID); err != nil {
		return nil, err
	} else if found {
		v := int64(p.Version)
		out.planVersion = &v
	}
	return out, nil
}

// loadReleaseRun は反映の操作 row の課題の、終わっていない契約の実行を読む（無ければ nil）。
func (s *Store) loadReleaseRun(ctx context.Context, row *operationRow) (*releaseRun, error) {
	var out *releaseRun
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		if out, err = loadOpenRunInTx(ctx, tx, row.ChallengeID); err != nil || out == nil {
			return err
		}
		out.opID = row.ID
		cr := out.row
		if cr.Kind == contractRunKindReflect {
			out.corresponds = cr.OperationID != nil && *cr.OperationID == row.ID
		} else if row.Ref.Valid {
			out.corresponds, err = taskRunImportedRef(ctx, tx, cr, row.Ref.String)
		}
		if err != nil || !out.corresponds {
			return err
		}
		out.fixEligible = true
		if cr.Kind != contractRunKindReflect {
			ops, err := openReleaseOpsOfTaskRun(ctx, tx, cr)
			if err != nil {
				return err
			}
			out.fixEligible = len(ops) == 1
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

// taskRunImportedRef は、タスクの実行 cr が取り込んだ成果物（PR）の参照に、ref と同じものがあるか。見るのは、その契約の実行が
// 始まってから（start_run_id 以降）の契約のコマンドの実行が取り込んだ成果物だけで、終わった前の契約の実行の成果物は見ない。
func taskRunImportedRef(ctx context.Context, tx *sql.Tx, cr *contractRunRow, ref string) (bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT a.ref FROM run_artifact a JOIN run r ON r.id = a.run_id
		 WHERE r.challenge_id = ? AND r.kind = 'contract' AND r.id >= ? AND r.task_id IS ? AND a.kind = 'pr'`, cr.ChallengeID, cr.StartRunID, int64PtrColumn(cr.TaskID))
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	wantRepo, wantNumber, wantOK := parseReleasePullRequestRef(ref)
	for rows.Next() {
		var got string
		if err := rows.Scan(&got); err != nil {
			return false, err
		}
		if got == ref {
			return true, nil
		}
		if repo, number, ok := parseReleasePullRequestRef(got); ok && wantOK && number == wantNumber && strings.EqualFold(repo, wantRepo) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// openReleaseOpsOfTaskRun は、タスクの実行 cr に対応する、終わっていない（承認待ち・承認済みの）反映の操作の内部整数 ID を返す
// （承認済みを先に、その中では新しい順）。対応は、その実行が取り込んだ成果物の参照と同じ参照を持つこと（taskRunImportedRef）。
// 衝突の解消の確かめの道に入ってよいのは、これがちょうど 1 つのときだけ（反映の段と J5 の側で同じ数え方を使う）。
func openReleaseOpsOfTaskRun(ctx context.Context, tx *sql.Tx, cr *contractRunRow) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, COALESCE(ref, '') FROM operation WHERE challenge_id = ? AND kind = ? AND state IN (?, ?) ORDER BY (state = 'approved') DESC, id DESC`,
		cr.ChallengeID, string(OperationKindRelease), string(OperationStatePending), string(OperationStateApproved))
	if err != nil {
		return nil, err
	}
	type openOp struct {
		id  int64
		ref string
	}
	var ops []openOp
	for rows.Next() {
		var o openOp
		if err := rows.Scan(&o.id, &o.ref); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ops = append(ops, o)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []int64
	for _, o := range ops {
		if o.ref == "" {
			continue
		}
		ok, err := taskRunImportedRef(ctx, tx, cr, o.ref)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, o.id)
		}
	}
	return out, nil
}

// closeHeldEndWithoutOwner は、課題 challengeID の「終わりを読んだが、結果を書くまで開けている契約の実行」（state が終わりで
// ended_at が空。反映の段の status・resume だけが作る）を、それに対応する承認済みの反映の操作が 1 つも無いときに、終わった記録に
// する（閉じたら真）。結果を書く前に操作が承認済みでなくなった（試行が競合で閉じた）とき、閉じる者がいなくなるのを防ぐ。
// 対応する承認済みの操作があれば何もしない（その操作の次の反映の段が、終わりを使って結果を書く）。委譲先のコマンドは呼ばない。
func (s *Store) closeHeldEndWithoutOwner(ctx context.Context, challengeID int64) (bool, error) {
	closed := false
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		cr, err := scanContractRun(tx.QueryRowContext(ctx, contractRunSelect+` WHERE challenge_id = ? AND ended_at IS NULL AND `+releaseStageRunWhere+` ORDER BY id LIMIT 1`, challengeID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !cr.State.Terminal() {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, ref FROM operation WHERE challenge_id = ? AND kind = ? AND state = ? ORDER BY id`,
			challengeID, string(OperationKindRelease), string(OperationStateApproved))
		if err != nil {
			return err
		}
		type approved struct {
			id  int64
			ref sql.NullString
		}
		var ops []approved
		for rows.Next() {
			var o approved
			if err := rows.Scan(&o.id, &o.ref); err != nil {
				_ = rows.Close()
				return err
			}
			ops = append(ops, o)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, o := range ops {
			owns := false
			if cr.Kind == contractRunKindReflect {
				owns = cr.OperationID != nil && *cr.OperationID == o.id
			} else if o.ref.Valid {
				if owns, err = taskRunImportedRef(ctx, tx, cr, o.ref.String); err != nil {
					return err
				}
			}
			if owns {
				return nil
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE contract_run SET ended_at = ?, fix_pending_ref = NULL, fix_verified_ref = NULL, fix_rejected_ref = NULL WHERE id = ? AND ended_at IS NULL AND state IN ('succeeded', 'failed', 'cancelled')`,
			formatTimestamp(s.currentTime()), cr.ID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		closed = n == 1
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return false, err
	}
	return closed, nil
}

// releaseContractCommand は反映の段が、既存の契約の実行へ契約のコマンド（status・resume・cancel）を 1 回呼んで記録する。
// 契約の実行の記録を作れなかった（課題に終わっていない実行の記録がある）ときは errReleaseContractBusy を返す。
// holdEnd が真なら、終わりを読んでも契約の実行を終わった記録にしない（反映の試行の結果を書く書き込みが終わった記録にする。
// 反映の段の status と「入れてよい」の resume だけが真。完了から戻す前の読み・片付け・cancel は、その場で終わった記録にする）。
func (s *Store) releaseContractCommand(ctx context.Context, opts ReleaseExecOptions, run *releaseRun, name string, fill contractFill, holdEnd bool) (*ContractRunReport, string, error) {
	if opts.Contract == nil {
		return nil, "the contract client is not available", nil
	}
	connector, err := findCLIConnector(opts.Connectors, run.row.ConnectorID)
	if err != nil {
		return nil, err.Error(), nil
	}
	args := connector.Commands.Status
	switch name {
	case "resume":
		args = connector.Commands.Resume
	case "cancel":
		args = connector.Commands.Cancel
	}
	fill.ChallengeID = formatChallengeID(run.row.ChallengeID)
	fill.Source = run.binding
	fill.EmptySource = startTakesTaskFile(connector)
	if run.row.Kind == contractRunKindReflect {
		fill.Source, fill.EmptySource = nil, true // 反映の依頼の取り込み元の差し込みは空の文字列
	}
	fill.RunID = run.row.ExternalRunID
	command, err := fillContractArgsWith(args, fill)
	if err != nil {
		return nil, err.Error(), nil
	}
	agent := opts.Agent
	if agent == nil {
		agent = &AgentDeclaration{TimeoutSec: AgentTimeoutSec{Delegate: defaultTimeoutDelegateSec}}
	}
	var latest *contractRunRow
	if err := s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		latest, err = scanContractRun(tx.QueryRowContext(ctx, contractRunSelect+` WHERE id = ?`, run.row.ID))
		return err
	}); classifyReadWriteErr(err) != nil {
		return nil, "", classifyReadWriteErr(err)
	}
	opID := run.opID
	rep, err := s.runExistingContractCommandWith(ctx, opts.Contract, agent, existingContractWorkDir(latest, ""), latest, name, command, run.planVersion, opts.cycleID, "", &opID, holdEnd)
	if errors.Is(err, ErrRunInProgress) {
		return nil, "", errReleaseContractBusy
	}
	if err != nil {
		return nil, "", err
	}
	return rep, "", nil
}

// readReleaseRun は対応する契約の実行を、先に commands.status で読む。読めなかったとき、または実行が running のとき、反映の
// 試行の結果（waiting_delegate・upstream_failed）を返す。それ以外は nil の判定と読んだ状態を返す（上流の確認へ進む）。
func (s *Store) readReleaseRun(ctx context.Context, opts ReleaseExecOptions, run *releaseRun, holdEnd bool) (*ContractRunReport, *releaseJudgment, error) {
	rep, why, err := s.releaseContractCommand(ctx, opts, run, "status", contractFill{}, holdEnd)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case why != "":
		return nil, &releaseJudgment{result: ExecutionResultUpstreamFailed, detail: "could not read the contract run: " + why}, nil
	case rep.Result != RunResultSucceeded:
		return nil, &releaseJudgment{result: ExecutionResultUpstreamFailed,
			detail: fmt.Sprintf("could not read the contract run: %s: %s", rep.Result, rep.Error)}, nil
	case rep.State == ContractStateRunning:
		return rep, &releaseJudgment{result: ExecutionResultWaitingDelegate}, nil
	}
	return rep, nil, nil
}

// landRefusal は、「入れてよい」が届いた後（land_delivered が真）に、中身の識別が land_ref と同じレビュー待ちが戻ったとき、
// 結果 land_refused の判定を返す（委譲先が、宛先が動いた以外の理由で入れられなかった）。それ以外は nil。
// 間の試行の結果（waiting_delegate・upstream_failed ほか）には、よらない。求めの文が同じでも、中身の識別で見分ける。
func landRefusal(run *releaseRun, rep *ContractRunReport) *releaseJudgment {
	ra := rep.RequestedAction
	if run.row.LandRef == "" || !run.row.LandDelivered ||
		rep.State != ContractStateWaiting || ra == nil || ra.Kind != contractRequestedKindReview {
		return nil
	}
	if got, n := reviewContentRefs(rep.Artifacts); n != 1 || got != run.row.LandRef {
		return nil
	}
	return &releaseJudgment{result: ExecutionResultLandRefused, route: run.route(),
		detail: fmt.Sprintf("the delegate did not land %s: summary: %s; requested_action.text: %s", run.row.LandRef, rep.Summary, ra.Text)}
}

// cancelReleaseRunNote は、反映の操作が委譲先の succeeded を読む以外の道で終わるとき、対応する契約の実行（タスクの実行・反映の依頼）が
// 終わっていなければ commands.cancel で取り消す（どの結果で取り消すかは cancelsCorrespondingRun）。rep が nil なら先に commands.status で読む（片付け）。失敗は返す文に残す（結果は変えない。成功・不要は空）。
func (s *Store) cancelReleaseRunNote(ctx context.Context, opts ReleaseExecOptions, run *releaseRun, rep *ContractRunReport) string {
	if run == nil || !run.corresponds {
		return ""
	}
	if rep == nil && run.row.State.Terminal() {
		// 終わりを記録済み。委譲先は呼ばない。結果を書く書き込みが終わった記録にするが、持ち主の承認済みの操作が無ければ（片付け）ここで閉じる。
		if _, err := s.closeHeldEndWithoutOwner(ctx, run.row.ChallengeID); err != nil {
			return "could not close the ended contract run: " + err.Error()
		}
		return ""
	}
	if rep == nil {
		var early *releaseJudgment
		var err error
		if rep, early, err = s.readReleaseRun(ctx, opts, run, false); err != nil {
			return "could not read the contract run to cancel it: " + err.Error()
		}
		if early != nil && early.result == ExecutionResultUpstreamFailed {
			return "could not cancel the contract run: " + early.detail
		}
	}
	if rep.State.Terminal() {
		return ""
	}
	out, why, err := s.releaseContractCommand(ctx, opts, run, "cancel", contractFill{}, false)
	switch {
	case err != nil:
		return "could not cancel the contract run: " + err.Error()
	case why != "":
		return "could not cancel the contract run: " + why
	case out.Result != RunResultSucceeded || !out.State.Terminal():
		return fmt.Sprintf("could not cancel the contract run %s (result: %s, state: %s)", run.row.ExternalRunID, out.Result, out.State)
	}
	return ""
}

// appendDetail は試行の記録の詳細 base へ note を足す（どちらかが空ならもう一方）。
func appendDetail(base, note string) string {
	switch {
	case note == "":
		return base
	case base == "":
		return note
	}
	return base + "; " + note
}

// chooseReleasePath は成果物の確認とチェックを通った反映の操作の道を選ぶ。passRef は通す中身、observed は見た中身。
// run は課題の終わっていない契約の実行（無ければ nil）、rep はそれを先に読んだ状態（対応しない実行は nil）。
func (s *Store) chooseReleasePath(ctx context.Context, row *operationRow, opts ReleaseExecOptions, run *releaseRun, rep *ContractRunReport, passRef, observed string) (releaseJudgment, error) {
	if run == nil {
		// 課題に終わっていない契約の実行が無い: 口があれば反映の依頼を始め、無ければ flywheel の外での反映を待つ。
		return s.startReflectRequest(ctx, row, opts, observed)
	}
	if !run.corresponds {
		// 別の成果物の実行・別の反映の操作の反映の依頼。何も呼ばない。
		// 別の反映の操作が終わりを読んで、結果を書くまで開けている実行（終わりの状態で、まだ終わった記録でない）も、その操作が閉じるまで待つ。
		if run.row.State.Terminal() {
			// 開けている操作が承認済みでなくなっていれば（結果を書く者がいない）、ここで閉じて、実行が無いときの道へ進む。上限なしに待たない。
			closed, err := s.closeHeldEndWithoutOwner(ctx, row.ChallengeID)
			if err != nil {
				return releaseJudgment{}, err
			}
			if closed {
				return s.startReflectRequest(ctx, row, opts, observed)
			}
		}
		if run.row.State == ContractStateRunning || run.row.State.Terminal() {
			return releaseJudgment{result: ExecutionResultWaitingDelegate, observedHead: observed}, nil
		}
		return releaseJudgment{result: ExecutionResultUnsupported, observedHead: observed,
			detail: fmt.Sprintf("an unfinished contract run %s does not belong to this release and is not running (state=%s)", run.row.ExternalRunID, run.row.State)}, nil
	}
	ra := rep.RequestedAction
	if rep.State != ContractStateWaiting || ra == nil {
		return releaseJudgment{result: ExecutionResultUnsupported, observedHead: observed, route: run.route(),
			detail: fmt.Sprintf("contract run %s is %s and has no request", run.row.ExternalRunID, rep.State)}, nil
	}
	if ra.Kind != contractRequestedKindReview || !hasOption(ra.Options, contractChoiceLand) {
		return releaseJudgment{result: ExecutionResultUnsupported, observedHead: observed, route: run.route(),
			detail: fmt.Sprintf("contract run %s waits for %q without the %q option", run.row.ExternalRunID, ra.Kind, contractChoiceLand)}, nil
	}
	if _, n := reviewContentRefs(rep.Artifacts); run.row.Kind == contractRunKindReflect && n != 1 {
		return releaseJudgment{result: ExecutionResultUnsupported, observedHead: observed, route: run.route(),
			detail: fmt.Sprintf("contract run %s waits for review with %d content identifiers (exactly 1 is needed to return the land answer)", run.row.ExternalRunID, n)}, nil
	}
	return s.landToRun(ctx, row, opts, run, rep, passRef, observed)
}

func hasOption(options []string, want string) bool {
	for _, o := range options {
		if o == want {
			return true
		}
	}
	return false
}

// landToRun は対応する契約の実行（タスクの実行・反映の依頼）へ、「入れてよい」を commands.resume で返す。委譲先の言う中身が、通す中身と同じときだけ返す。
// 呼ぶ前に、契約の実行の記録へ通す中身（land_ref）を書く。届いた記録（land_delivered）は、結果 land_sent と同じ書き込みで真にする。
func (s *Store) landToRun(ctx context.Context, row *operationRow, opts ReleaseExecOptions, run *releaseRun, rep *ContractRunReport, passRef, observed string) (releaseJudgment, error) {
	j := releaseJudgment{observedHead: observed, route: run.route()}
	if got, n := reviewContentRefs(rep.Artifacts); n != 1 || got != passRef {
		j.result = ExecutionResultContentMismatch
		j.detail = fmt.Sprintf("the contract run says %q (%d candidates); upstream has %q", got, n, passRef)
		return j, nil
	}
	// 承認が取り消された・実行された操作へ「入れてよい」を返さない（読んだ後に別の経路が変えたときの確認）。
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		cur, err := loadOperationRow(ctx, tx, row.ID)
		if err != nil {
			return err
		}
		if cur.Version != row.Version || cur.State != OperationStateApproved {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE contract_run SET land_ref = ?, land_delivered = 0 WHERE id = ?`, passRef, run.row.ID)
		return err
	}); classifyReadWriteErr(err) != nil {
		return releaseJudgment{}, classifyReadWriteErr(err)
	}
	_, why, err := s.releaseLand(ctx, opts, run, passRef)
	if err != nil {
		return releaseJudgment{}, err
	}
	if why != "" {
		j.result, j.detail = ExecutionResultLandUnconfirmed, why
		return j, nil
	}
	j.result, j.landDeliveredRunID = ExecutionResultLandSent, run.row.ID
	return j, nil
}

// releaseLand は commands.resume で「入れてよい」を返す。返せた（resume が成功した）ときの理由は空。
func (s *Store) releaseLand(ctx context.Context, opts ReleaseExecOptions, run *releaseRun, passRef string) (*ContractRunReport, string, error) {
	rep, why, err := s.releaseContractCommand(ctx, opts, run, "resume",
		contractFill{Answer: contractAnswer{Choice: contractChoiceLand}, ApprovedRef: passRef}, true)
	if err != nil {
		return nil, "", err
	}
	switch {
	case why != "":
		return nil, "could not return the land answer: " + why, nil
	case rep.Result != RunResultSucceeded:
		return rep, fmt.Sprintf("could not return the land answer: %s: %s", rep.Result, rep.Error), nil
	}
	return rep, "", nil
}

// clearLandRefsForOperation は反映の操作 opID を取り消すとき、その操作に対応する契約の実行（反映の依頼なら operation_id が
// opID、タスクの実行なら取り込んだ成果物の参照が操作の参照と同じもの）だけの land_ref を消し、land_delivered を偽にし、衝突の解消の確かめの 3 列
// （fix_pending_ref・fix_verified_ref・fix_rejected_ref）も消す
// （承認し直した後は、もう一度「入れてよい」を返す）。契約のコマンドは呼ばない。
func clearLandRefsForOperation(ctx context.Context, tx *sql.Tx, challengeID, opID int64) error {
	var ref sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT ref FROM operation WHERE id = ?`, opID).Scan(&ref); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, contractRunSelect+` WHERE challenge_id = ? AND (land_ref IS NOT NULL OR land_delivered <> 0 OR fix_pending_ref IS NOT NULL OR fix_verified_ref IS NOT NULL OR fix_rejected_ref IS NOT NULL)`, challengeID)
	if err != nil {
		return err
	}
	var runs []*contractRunRow
	for rows.Next() {
		r, err := scanContractRun(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		runs = append(runs, r)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, r := range runs {
		match := r.OperationID != nil && *r.OperationID == opID
		if r.Kind == contractRunKindTask && ref.Valid {
			if match, err = taskRunImportedRef(ctx, tx, r, ref.String); err != nil {
				return err
			}
		}
		if !match {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET land_ref = NULL, land_delivered = 0, fix_pending_ref = NULL, fix_verified_ref = NULL, fix_rejected_ref = NULL WHERE id = ?`, r.ID); err != nil {
			return err
		}
	}
	return nil
}

// clearLandRefs は課題の契約の実行すべての land_ref と衝突の解消の確かめの 3 列を消す（完了から検証中へ戻すとき。T18）。
func clearLandRefs(ctx context.Context, tx *sql.Tx, challengeID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE contract_run SET land_ref = NULL, land_delivered = 0, fix_pending_ref = NULL, fix_verified_ref = NULL, fix_rejected_ref = NULL
		WHERE challenge_id = ? AND (land_ref IS NOT NULL OR land_delivered <> 0 OR fix_pending_ref IS NOT NULL OR fix_verified_ref IS NOT NULL OR fix_rejected_ref IS NOT NULL)`, challengeID)
	return err
}

// summarizeChecks は checks のうち、失敗したものがあるか・終わっていないものがあるかを返す（チェックの分類は classifyCheck）。
// 0 件は、どちらも偽（0 件をどう扱うかは呼び出し元が決める）。反映の操作の実行と、統合先へ入れる確かめが共用する。
func summarizeChecks(checks []UpstreamCheck) (failed, pending bool) {
	for _, c := range checks {
		switch classifyCheck(c) {
		case checkFail:
			failed = true
		case checkPending:
			pending = true
		}
	}
	return failed, pending
}

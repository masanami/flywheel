package core

// このファイルは、取り下げ（withdraw）の前に、課題の終わっていない契約の run（form: cli。タスクの実行が複数本・上げる依頼を含む）を
// 全部、宣言の commands.cancel で止める口を持つ。止めないまま進めると、接続ツール側の run が誰にも追われず動き続ける。
// 計画の作り直しの取り消しは、承認の後の状態の確認が行う（PollContractRuns。obsoleteContractRun）。

import (
	"context"
	"database/sql"
	"fmt"
)

// ContractCancelInput は CancelOpenContractRuns の入力。
type ContractCancelInput struct {
	ChallengeID string
	AgentDecl   *AgentDeclaration
	ConnDecl    *ConnectorsDeclaration
	Client      ContractClient
	// WorkDir が空なら課題のリポジトリの作業ディレクトリ（決められなければワークスペース）。
	WorkDir string
}

// HasOpenContractRun は、課題に終わっていない契約の run（接続ツール側で動いている・待っている run）があるか。
func (s *Store) HasOpenContractRun(ctx context.Context, challengeID string) (bool, error) {
	cid, ok := parseChallengeID(challengeID)
	if !ok {
		return false, ErrValidation
	}
	var n int
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND ended_at IS NULL`, cid).Scan(&n)
	})
	return n > 0, classifyReadWriteErr(err)
}

// CancelOpenContractRuns は、課題の終わっていない契約の run を宣言の commands.cancel で止め、止めた数を返す。
// 開いた run が無ければ何もしない（Client・宣言は不要）。止められなかった（コマンドの失敗・止めた後も終端でない）ときは
// ErrConflict で返し、呼び出し元は取り下げへ進まない。
func (s *Store) CancelOpenContractRuns(ctx context.Context, in ContractCancelInput) (int, error) {
	open, err := s.HasOpenContractRun(ctx, in.ChallengeID)
	if err != nil || !open {
		return 0, err
	}
	if in.Client == nil || in.AgentDecl == nil || in.ConnDecl == nil {
		return 0, ErrValidation
	}
	cid, _ := parseChallengeID(in.ChallengeID)
	var rows []*contractRunRow
	bindings := map[*contractRunRow]*sourceBinding{}
	err = s.db.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, contractRunSelect+` WHERE ended_at IS NULL AND challenge_id = ? ORDER BY id`, cid)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			r, err := scanContractRun(rs)
			if err != nil {
				return err
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
		for _, r := range rows {
			if bindings[r], err = loadTaskSourceBinding(ctx, tx, cid, r.TaskID); err != nil {
				return err
			}
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return 0, err
	}
	// どの実行も取り消しを試みる（1 本の失敗で残りを止めない）。止められなかったものがあれば、最初の失敗を返す。
	n := 0
	var firstErr error
	for _, row := range rows {
		row.discard = true // 取り消すだけの実行。成果物を取り込まず、課題を保留にしない
		if err := s.cancelOneOpenContractRun(ctx, in, row, bindings[row]); err != nil {
			firstErr = firstErrOr(firstErr, err)
			continue
		}
		n++
	}
	return n, firstErr
}

// cancelOneOpenContractRun は終わっていない契約の実行 1 本を commands.cancel で取り消す。止められなければ ErrConflict を含むエラー。
func (s *Store) cancelOneOpenContractRun(ctx context.Context, in ContractCancelInput, row *contractRunRow, binding *sourceBinding) error {
	if row.State.Terminal() {
		// 記録した状態が終わりの行（読み直しのために開けたまま残した行）は、委譲先では終わっている。中止を呼ばず、終わった記録にする。
		return s.endRecordedTerminalRun(ctx, row)
	}
	connector, err := findCLIConnector(in.ConnDecl, row.ConnectorID)
	if err != nil {
		return err
	}
	command, err := fillRowCommandArgs(connector, row, connector.Commands.Cancel, in.ChallengeID, binding, contractAnswer{})
	if err != nil {
		return err
	}
	workDir := existingContractWorkDir(row, in.WorkDir)
	rep, err := s.runExistingContractCommand(ctx, in.Client, in.AgentDecl, workDir, row, "cancel", command, nil, nil,
		"課題の取り下げのため、接続ツールの run の中止を依頼した。")
	if err != nil {
		return err
	}
	if rep.Result != RunResultSucceeded || !rep.State.Terminal() {
		return fmt.Errorf("%w: the contract run %s could not be cancelled (result: %s, state: %s); stop it on the connector side first",
			ErrConflict, row.ExternalRunID, rep.Result, rep.State)
	}
	// 止めた時点で接続ツールの run がちょうど成功していた（succeeded）ときは、保留の間の成功の印（held_success）が付く。
	// 取り下げのために止めた run の成果物は、提出し直さない。印を外さないと、計画の作り直しの検査（held_success = 1）が拒み続ける。
	return classifyReadWriteErr(s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE contract_run SET held_success = 0 WHERE id = ?`, row.ID)
		return err
	}))
}

// endRecordedTerminalRun は、記録した状態が終わり（成功・失敗・中止）の契約の実行（row）を、委譲先の中止を呼ばずに終わった記録にする。
// 保留の間の成功の印（held_success）も外す（取り下げのために止める実行の成果物は、提出し直さない）。
func (s *Store) endRecordedTerminalRun(ctx context.Context, row *contractRunRow) error {
	wctx := context.WithoutCancel(ctx)
	return classifyReadWriteErr(s.db.Write(wctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(wctx, `UPDATE contract_run SET ended_at = COALESCE(ended_at, ?), held_success = 0, fix_pending_ref = NULL WHERE id = ?`, formatTimestamp(s.currentTime()), row.ID)
		return err
	}))
}

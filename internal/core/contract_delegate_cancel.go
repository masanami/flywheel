package core

// このファイルは、取り下げ（withdraw）と計画の作り直し（answer --replan）の前に、課題の開いた契約の run（form: cli）を
// 宣言の commands.cancel で止める口を持つ。止めないまま進めると、接続ツール側の run が誰にも追われず動き続けたり、
// 作り直す前の計画の run が回答で再開されたりする。

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
// ErrConflict で返し、呼び出し元は取り下げ・計画の作り直しへ進まない。
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
	n := 0
	for _, row := range rows {
		connector, err := findCLIConnector(in.ConnDecl, row.ConnectorID)
		if err != nil {
			return n, err
		}
		command, err := fillContractArgs(connector.Commands.Cancel, in.ChallengeID, bindings[row], row.ExternalRunID, contractAnswer{})
		if err != nil {
			return n, err
		}
		workDir := existingContractWorkDir(row, in.WorkDir)
		rep, err := s.runExistingContractCommand(ctx, in.Client, in.AgentDecl, workDir, row, "cancel", command, nil, nil,
			"課題の取り下げまたは計画の作り直しのため、接続ツールの run の中止を依頼した。")
		if err != nil {
			return n, err
		}
		if rep.Result != RunResultSucceeded || !rep.State.Terminal() {
			return n, fmt.Errorf("%w: the contract run %s could not be cancelled (result: %s, state: %s); stop it on the connector side first",
				ErrConflict, row.ExternalRunID, rep.Result, rep.State)
		}
		// 止めた時点で接続ツールの run がちょうど成功していた（succeeded）ときは、保留の間の成功の印（held_success）が付く。
		// 取り下げ・作り直しのために止めた run の成果物は、保留が解けた後に提出し直さない（作り直す前の計画の成果物のため）。
		// 印を外さないと、計画の作り直しの検査（ended_at IS NULL OR held_success = 1）が拒み続ける。
		if err := s.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE contract_run SET held_success = 0 WHERE id = ?`, row.ID)
			return err
		}); err != nil {
			return n, classifyReadWriteErr(err)
		}
		n++
	}
	return n, nil
}

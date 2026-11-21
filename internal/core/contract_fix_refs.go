package core

import (
	"context"
	"database/sql"
	"fmt"
)

// このファイルは契約の実行（contract_run）の、衝突の解消の確かめ（S3c）の 3 列の読み書きの口を持つ。列と口だけで、使う動き（J5 の確かめ・
// 反映の段）は別のタスクが足す。
//
//   - fix_pending_ref  : 確かめ待ちの中身（衝突の解消の確かめを待っている、いまの中身の識別）。
//   - fix_verified_ref : 反映先宛てで、衝突の解消の確かめを通した中身。統合先宛てでは通した後に verified_ref を改めるので使わない。
//   - fix_rejected_ref : 反映先宛てで、確かめが通らなかった中身。J5 はこの列を書くだけで、反映の承認の取り消しは反映の段が行う。

// contractFixColumn は衝突の解消の確かめの列の名前。値は下の 3 つに限り、SQL の列名へそのまま使う。
type contractFixColumn string

const (
	contractFixPending  contractFixColumn = "fix_pending_ref"
	contractFixVerified contractFixColumn = "fix_verified_ref"
	contractFixRejected contractFixColumn = "fix_rejected_ref"
)

func (c contractFixColumn) valid() bool {
	return c == contractFixPending || c == contractFixVerified || c == contractFixRejected
}

// contractFixRefs は契約の実行の衝突の解消の確かめの 3 列。値が無い（NULL）列は空文字。
type contractFixRefs struct {
	Pending, Verified, Rejected string
}

// readContractFixRefs は契約の実行（内部整数 ID rowID）の 3 列を読む。行が無ければ sql.ErrNoRows を返す。
func readContractFixRefs(ctx context.Context, tx *sql.Tx, rowID int64) (contractFixRefs, error) {
	var r contractFixRefs
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(fix_pending_ref, ''), COALESCE(fix_verified_ref, ''), COALESCE(fix_rejected_ref, '')
		FROM contract_run WHERE id = ?`, rowID).Scan(&r.Pending, &r.Verified, &r.Rejected)
	return r, err
}

// writeContractFixRef は契約の実行の 1 列へ中身の識別 ref を書く。ref が空なら書かない（消すのは clearContractFixRefs）。
// 行が無いときは sql.ErrNoRows を返す。
func writeContractFixRef(ctx context.Context, tx *sql.Tx, rowID int64, col contractFixColumn, ref string) error {
	if !col.valid() || ref == "" {
		return fmt.Errorf("%w: contract_run fix column %q with ref %q", ErrValidation, col, ref)
	}
	res, err := tx.ExecContext(ctx, `UPDATE contract_run SET `+string(col)+` = ? WHERE id = ?`, ref, rowID)
	return requireOneRow(res, err)
}

// clearContractFixRefs は契約の実行の指定した列を NULL にする（列の指定が無ければ 3 列とも）。行が無いときは sql.ErrNoRows を返す。
func clearContractFixRefs(ctx context.Context, tx *sql.Tx, rowID int64, cols ...contractFixColumn) error {
	if len(cols) == 0 {
		cols = []contractFixColumn{contractFixPending, contractFixVerified, contractFixRejected}
	}
	set := ""
	for i, c := range cols {
		if !c.valid() {
			return fmt.Errorf("%w: contract_run fix column %q", ErrValidation, c)
		}
		if i > 0 {
			set += ", "
		}
		set += string(c) + " = NULL"
	}
	res, err := tx.ExecContext(ctx, `UPDATE contract_run SET `+set+` WHERE id = ?`, rowID)
	return requireOneRow(res, err)
}

func requireOneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

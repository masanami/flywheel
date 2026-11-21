package core

import "database/sql"

// 契約の実行（contract_run）の、衝突の解消の確かめ（S3c）の 3 列の意味。書き込みは、使う動きごとの SQL で行う
// （J5 の確かめの結果の書き込みは、確かめ待ちの中身を条件にする。反映の段・契約の実行の終わり・承認の取り消しの後始末は、行を指して消す）。
//
//   - fix_pending_ref  : 確かめ待ちの中身（衝突の解消の確かめを待っている、いまの中身の識別）。
//   - fix_verified_ref : 反映先宛てで、衝突の解消の確かめを通した中身。統合先宛てでは通した後に verified_ref を改めるので使わない。
//   - fix_rejected_ref : 反映先宛てで、確かめが通らなかった中身。J5 はこの列を書くだけで、反映の承認の取り消しは反映の段が行う。

// requireOneRow は、更新が 1 行に当たったことを確かめる。当たらなければ sql.ErrNoRows を返す（行が無い・条件に合わない）。
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

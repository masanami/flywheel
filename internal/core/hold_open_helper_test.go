package core

import "database/sql"

// closeOpenHoldsUnlessHeld は、試験の準備で課題の状態を直接書き換えるときに、その課題の開いている保留
// （回答が無く、閉じていない）を同じ書き込みで閉じる。
// 開いている保留は課題とタスクの組ごとに 1 つまで（索引 idx_hold_open_scope）で、本番の操作は課題が人間対応待ちを離れるとき保留を閉じる（回答する・取り下げる）。
// 状態だけを書き換えると本番では作れない「人間対応待ちでないのに保留が開いたまま」ができ、次の保留が索引に弾かれるため、それに合わせる。
// 書き換え後の状態が人間対応待ち（awaiting_human）のときは何もしない。
func closeOpenHoldsUnlessHeld(tx *sql.Tx, cid any, status string) error {
	if status == string(StatusAwaitingHuman) {
		return nil
	}
	_, err := tx.Exec(`UPDATE hold SET closed_at = '2026-11-23T00:00:00.000Z' WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL`, cid)
	return err
}

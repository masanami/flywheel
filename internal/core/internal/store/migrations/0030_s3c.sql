-- 0030_s3c: スキーマ版 30。
-- 衝突の解消を承認し直さずに通す区切り（S3c）で使う列を足す。後のタスクが使う列もここですべて足し、後のタスクはマイグレーションを足さない。
-- 過去の行は書き換えない（版 29 で作った行は 3 列とも NULL）。
-- 反映の試行の結果の値 waiting_fix_check と、作業ログの action の値 conflict_fix_passed は、どちらも CHECK 制約を持たず
-- core の定数で検査する（0004・0009 の流儀）ので、このマイグレーションで制約は改めない。

-- 確かめ待ちの中身（衝突の解消の確かめを待っている、いまの中身の識別）。
ALTER TABLE contract_run ADD COLUMN fix_pending_ref TEXT;
-- 反映先宛てで、衝突の解消の確かめを通した中身。
ALTER TABLE contract_run ADD COLUMN fix_verified_ref TEXT;
-- 反映先宛てで、確かめが通らなかった中身。
ALTER TABLE contract_run ADD COLUMN fix_rejected_ref TEXT;

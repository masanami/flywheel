-- 0020_contract_wait_hold: スキーマ版 20。
-- docs/features/m3-invoker-delegation.md §委譲の流れ（`form: cli`）後半。
--
-- 契約の run が waiting を返し、flywheel がそれをオーナーの保留へ写したとき、その保留の ID を持つ列。
-- 保留への答えを `commands.resume` で接続ツールへ返すとき、「その waiting を読んだ run に結び付いた保留」の答えだけを
-- 返すために使う（予算超過の中止の失敗など、別の理由で立った保留の答えを返さない）。答えを返す前にこの列を NULL にして、
-- 同じ答えを二重に返さない。既存の行の値は変えない（NULL）。

ALTER TABLE contract_run ADD COLUMN wait_hold_id INTEGER REFERENCES hold (id);

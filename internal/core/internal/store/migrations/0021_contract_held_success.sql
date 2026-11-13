-- 0021_contract_held_success: スキーマ版 21。
-- docs/features/m3-invoker-delegation.md §委譲の流れ（`form: cli`）の「保留の間に成功した契約の run」。
--
-- 課題が保留（awaiting_human）の間に、契約の run が succeeded まで進んだことを読んだ印。オーナーが答えて課題が着手中へ
-- 戻った後の周で、start も resume も呼ばずに検証中へ進めるために使う（進めたら 0 に戻す）。既存の行は 0。

ALTER TABLE contract_run ADD COLUMN held_success INTEGER NOT NULL DEFAULT 0;

-- すでに保留の間に succeeded を読んでいた課題（答えの後に 2 本目の委譲が起動してしまう）にも印を付ける。
UPDATE contract_run SET held_success = 1
 WHERE state = 'succeeded' AND challenge_id IN (SELECT id FROM challenge WHERE status = 'awaiting_human');

-- 0029_s3b: スキーマ版 29。
-- 複数のタスクと統合先（区切り S3b）で使う列を足し、「終わっていない実行は 1 本」の一意の索引を作り直す。
-- 後のタスクが使う列もここですべて足し、後のタスクはマイグレーションを足さない。
-- 過去の行は書き換えない。ただし run.reflect だけは、反映の操作を持つ行（operation_id がある）を 1 にする。

-- 終わっていない実行は「課題とタスクの組ごとに 1 本」。task_id が空の実行（計画・学び・課題の単位の確かめ・反映の依頼のコマンド）は、課題ごとに 1 本のまま。
DROP INDEX idx_run_active_challenge;
CREATE UNIQUE INDEX idx_run_active_challenge ON run (challenge_id, COALESCE(task_id, 0)) WHERE result IS NULL;

DROP INDEX idx_contract_run_active_challenge;
CREATE UNIQUE INDEX idx_contract_run_active_challenge ON contract_run (challenge_id, COALESCE(task_id, 0)) WHERE ended_at IS NULL;

-- タスクごとの予算（マイクロドル）。版 28 で作った行は NULL のまま。
ALTER TABLE task ADD COLUMN budget_impl_micros INTEGER;
ALTER TABLE task ADD COLUMN budget_review_micros INTEGER;

ALTER TABLE challenge ADD COLUMN integration_ref TEXT;

ALTER TABLE contract_run ADD COLUMN verified_ref TEXT;

ALTER TABLE approval ADD COLUMN task_id INTEGER REFERENCES task(id);

-- 反映の操作のために呼んだ実行の印（0 か 1）。
ALTER TABLE run ADD COLUMN reflect INTEGER NOT NULL DEFAULT 0;
UPDATE run SET reflect = 1 WHERE operation_id IS NOT NULL;

-- 0028_s3a: スキーマ版 28。
-- 契約の実行（contract_run）と成果物（run_artifact）に、反映を委譲先に任せる区切り（S3a）で使う列を足す。
-- 後のタスクが使う列もここですべて足し、後のタスクはマイグレーションを足さない。
-- 過去の行は書き換えない（版 27 の行は kind が既定の task、facts_unmet_count と land_delivered が 0、他は NULL になる）。
-- 索引と一意の制約は変えない。operation_execution は変えない。列を足すだけで、読み書きは各タスクが持つ。

ALTER TABLE run_artifact ADD COLUMN role TEXT;

ALTER TABLE contract_run ADD COLUMN kind TEXT NOT NULL DEFAULT 'task';
ALTER TABLE contract_run ADD COLUMN operation_id INTEGER REFERENCES operation(id);
ALTER TABLE contract_run ADD COLUMN requested_options TEXT;
ALTER TABLE contract_run ADD COLUMN content_ref TEXT;
ALTER TABLE contract_run ADD COLUMN facts TEXT;
ALTER TABLE contract_run ADD COLUMN facts_unmet_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE contract_run ADD COLUMN land_ref TEXT;
ALTER TABLE contract_run ADD COLUMN land_delivered INTEGER NOT NULL DEFAULT 0;

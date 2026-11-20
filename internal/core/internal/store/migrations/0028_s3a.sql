-- 0028_s3a: スキーマ版 28。
-- 契約の実行（contract_run）・成果物（run_artifact）・保留（hold）に、反映を委譲先に任せる区切り（S3a）で使う列を足す。
-- 後のタスクが使う列もここですべて足し、後のタスクはマイグレーションを足さない。
-- run にも反映の操作の印の列を足す。
-- 過去の行は書き換えない（版 27 の行は kind が既定の task、facts_unmet_count と land_delivered が 0、他は NULL になる）。
-- 索引と一意の制約は変えない。operation_execution は変えない。列を足すだけで、読み書きは各タスクが持つ。

ALTER TABLE run_artifact ADD COLUMN role TEXT;

-- 保留の理由。land_unavailable・content_ref_invalid・facts_unmet の保留だけが値を持ち、ほかの保留と過去の行は NULL。
ALTER TABLE hold ADD COLUMN reason TEXT;

-- 反映の操作のために呼んだ実行（反映の段・完了から戻す前の読み・片付けが作る、種類 contract の実行）の印。NULL は委譲の段の実行と過去の行。
-- 印の付いた実行は成果物を持たず、課題の最後の委譲の実行・最後の契約のコマンドを読む所からは外す。
ALTER TABLE run ADD COLUMN operation_id INTEGER REFERENCES operation(id);

ALTER TABLE contract_run ADD COLUMN kind TEXT NOT NULL DEFAULT 'task';
ALTER TABLE contract_run ADD COLUMN operation_id INTEGER REFERENCES operation(id);
ALTER TABLE contract_run ADD COLUMN requested_options TEXT;
ALTER TABLE contract_run ADD COLUMN content_ref TEXT;
ALTER TABLE contract_run ADD COLUMN facts TEXT;
ALTER TABLE contract_run ADD COLUMN facts_unmet_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE contract_run ADD COLUMN land_ref TEXT;
ALTER TABLE contract_run ADD COLUMN land_delivered INTEGER NOT NULL DEFAULT 0;

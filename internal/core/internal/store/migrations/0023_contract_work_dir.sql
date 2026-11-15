-- 0023_contract_work_dir: スキーマ版 23。
-- docs/features/m3-invoker-delegation.md §委譲の流れ（`form: cli`）の「作業ディレクトリ（#591）」。
-- `contract start` を実行した作業ディレクトリ。status・resume・cancel は決め直さずにこの値で実行する。
-- 既存の行（この列を足す前に始めた契約の run）は NULL で、ワークスペースで実行する。

ALTER TABLE contract_run ADD COLUMN work_dir TEXT;

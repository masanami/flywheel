-- 0024_contract_resolvable_by: スキーマ版 24。
-- docs/features/m3-invoker-delegation.md §契約の `requested_action.resolvable_by`。
-- 最後に読んだ求められた操作の resolvable_by（answer | terminal | 閉集合の外の値。契約の出力に無ければ NULL）。
-- 既存の行は NULL で、今までどおり answer として扱う。

ALTER TABLE contract_run ADD COLUMN requested_resolvable_by TEXT;

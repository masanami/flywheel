-- 0040_flow_choice: スキーマ版 40。
-- フローの移植 S2。計画が選んだフロー（task.planned_flow）とその理由（task.flow_reason）、
-- ステップ 1 回ぶんの effort（contract_run_step.effort）を足す。どれも無ければ NULL。
-- フローと理由はタスクごと、effort はステップの実行ごとの値で、いまの表の粒度に合う。過去の行は書き換えない（どれも NULL のまま）。

ALTER TABLE task ADD COLUMN planned_flow TEXT;
ALTER TABLE task ADD COLUMN flow_reason TEXT;
ALTER TABLE contract_run_step ADD COLUMN effort TEXT;

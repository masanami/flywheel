-- 0006_approval_plan_version: スキーマ版 6。
-- 計画の承認が、承認した計画の版（task_plan.version）も記録する。
-- target_version は課題の版のまま変えない（承認は課題の版に束ねる）。
-- 計画の版を持たない既存の承認の行は NULL のままにする（推測で計画を割り当てない）。

ALTER TABLE approval ADD COLUMN plan_version INTEGER;

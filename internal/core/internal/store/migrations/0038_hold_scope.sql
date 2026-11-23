-- 0038_hold_scope: スキーマ版 38。
-- 区切り S4c-3 の 1。保留に止める範囲（scope）を足す。task はそのタスクだけを止める保留、NULL は課題を止める保留。
-- 開いている保留（回答が無く、閉じていない）には、課題と止めるタスク（課題を止める保留は 0）の組ごとに 1 つの部分一意索引を置く。
-- 過去の行は書き換えない（既存の保留の scope は NULL。課題を止める保留として読む）。
-- スライスごとに 1 版。

ALTER TABLE hold ADD COLUMN scope TEXT CHECK (scope IS NULL OR (scope = 'task' AND task_id IS NOT NULL));

CREATE UNIQUE INDEX idx_hold_open_scope ON hold (challenge_id, CASE WHEN scope = 'task' THEN task_id ELSE 0 END)
    WHERE answer IS NULL AND closed_at IS NULL;

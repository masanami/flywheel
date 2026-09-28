-- 0004_run_cycle_lock: スキーマ版 4。
-- 親要件チケット #77・#78（docs/features/m3-invoker-delegation.md）
-- §クリティカル設計決定 1（オーナー M3H7・2026-09-28）に従う。
--
-- run（判断の呼び出し 1 回、または委譲の起動 1 回の実行記録）・cycle
-- （flywheel cycle 等・1 周の実行記録）・lock（周の排他ロック）の表を足し、
-- task_plan.spec・hold.run_id・activity.run_id の列を足す。0005（スキーマ版
-- 4 → 5）で slot・run_artifact の表と run.slot_id・run.decider・
-- run.decider_row・flywheel budget の上書きを足す（このマイグレーションでは
-- 足さない）。
--
-- 閉集合（run.kind・run.judgment・run.result・run.cost_source・
-- run.budget_bucket・cycle.result）は 0001・0002 の流儀どおり CHECK 制約を
-- 付けず core 層（run_store.go）で検証する。
--
-- 金額（run.max_budget_usd・run.cost_usd・run.reported_total_cost_usd・
-- cycle.budget_usd・cycle.spent_usd）は USD の 100 万分の 1 を単位とする整数で
-- 持つ（和の誤差を避ける。§クリティカル設計決定 1 の【仮定】）。JSON 出力では
-- USD の数値に戻す（この変換は internal/cli 側の責務で、このマイグレーションの
-- 範囲外）。
--
-- run・cycle・lock の書き込みは作業ログ（activity）に載せない
-- （§クリティカル設計決定 1）。既存の課題・計画・承認・保留・作業ログ・対応の
-- 記録は変えない（AC-166）。既存の計画の spec・保留と作業ログの run_id は
-- NULL のまま増える（AC-167）。

-- cycle は run.cycle_id・lock.holder から参照されるため run より先に作る。
CREATE TABLE cycle (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger    TEXT NOT NULL,
    started_at TEXT NOT NULL,
    ended_at   TEXT,
    result     TEXT,
    budget_usd INTEGER NOT NULL,
    spent_usd  INTEGER NOT NULL DEFAULT 0
);

-- run: 判断の呼び出し 1 回・委譲の起動 1 回（--resume の再開も 1 回）を 1 行に
-- 記録する。課題ごとに終了していない run（result IS NULL）は高々 1 つ
-- （下の部分一意索引 idx_run_active_challenge。AC-168）。
CREATE TABLE run (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    cycle_id                INTEGER REFERENCES cycle (id),
    kind                    TEXT NOT NULL,
    judgment                TEXT,
    challenge_id            INTEGER NOT NULL REFERENCES challenge (id),
    challenge_version       INTEGER NOT NULL,
    plan_version            INTEGER,
    session_id              TEXT NOT NULL,
    session_id_mismatch     INTEGER NOT NULL DEFAULT 0,
    resumed_from_run_id     INTEGER REFERENCES run (id),
    pid                     INTEGER NOT NULL,
    host                    TEXT NOT NULL,
    heartbeat_at            TEXT NOT NULL,
    started_at              TEXT NOT NULL,
    ended_at                TEXT,
    result                  TEXT,
    rate_limited            INTEGER NOT NULL DEFAULT 0,
    max_budget_usd          INTEGER NOT NULL,
    budget_bucket           TEXT NOT NULL,
    cost_usd                INTEGER,
    cost_source             TEXT,
    reported_total_cost_usd INTEGER,
    output                  TEXT,
    error                   TEXT
);

CREATE INDEX idx_run_challenge_id ON run (challenge_id);
CREATE INDEX idx_run_cycle_id ON run (cycle_id);

-- AC-168: 1 つの課題に終了していない run を 2 つ書き込もうとするとストアが
-- 拒否する（部分一意索引。「終了していない」＝ result IS NULL）。
CREATE UNIQUE INDEX idx_run_active_challenge ON run (challenge_id) WHERE result IS NULL;

CREATE TABLE lock (
    name         TEXT PRIMARY KEY,
    holder       INTEGER NOT NULL REFERENCES cycle (id),
    pid          INTEGER NOT NULL,
    host         TEXT NOT NULL,
    acquired_at  TEXT NOT NULL,
    heartbeat_at TEXT NOT NULL
);

ALTER TABLE task_plan ADD COLUMN spec TEXT;
ALTER TABLE hold ADD COLUMN run_id INTEGER REFERENCES run (id);
ALTER TABLE activity ADD COLUMN run_id INTEGER REFERENCES run (id);

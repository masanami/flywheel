-- 0009_release_execution: スキーマ版 9。
-- 親要件チケット #303（docs/features/m4.5-learning-and-self-improvement.md）の S1。
-- §クリティカル設計決定 1（M45H2・M45H4・M45H5）に従う。
--
-- 承認済みの release を core が決定的に実行するための土台を足す:
--   approval.head_sha          承認の ① で取った PR の head（kind=release の承認の行だけが値を持つ）
--   operation.executed_at      実行済みにした日時
--   operation.merge_commit     マージのコミット SHA
--   run_artifact.verified_head_sha  J5 の met の入力にした head（M45H11）
--   operation_execution        実行の試行（1 回の試行 = 1 行）
--
-- どの列も NULL 可で、既存の行は NULL のままにする（マイグレーションの前に承認された
-- release の head_sha は NULL のまま残る）。
-- operation.state の閉集合に executed を足すが、0004 の流儀どおり CHECK 制約は持たず
-- core 層で検査する。operation_execution.result も同様に CHECK 制約を持たない。

ALTER TABLE approval ADD COLUMN head_sha TEXT;
ALTER TABLE operation ADD COLUMN executed_at TEXT;
ALTER TABLE operation ADD COLUMN merge_commit TEXT;
ALTER TABLE run_artifact ADD COLUMN verified_head_sha TEXT;

-- operation_execution: 実行の試行。終了していない試行（ended_at IS NULL）は
-- 操作ごとに高々 1 つ（下の部分一意索引。0004 の run と同じ方式）。
CREATE TABLE operation_execution (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id  INTEGER NOT NULL REFERENCES operation (id),
    started_at    TEXT NOT NULL,
    ended_at      TEXT,
    result        TEXT,
    approved_head TEXT,
    observed_head TEXT,
    detail        TEXT,
    pid           INTEGER NOT NULL,
    host          TEXT NOT NULL
);

CREATE INDEX idx_operation_execution_operation_id ON operation_execution (operation_id);
CREATE UNIQUE INDEX idx_operation_execution_active ON operation_execution (operation_id) WHERE ended_at IS NULL;

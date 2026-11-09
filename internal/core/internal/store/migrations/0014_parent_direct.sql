-- 0014_parent_direct: スキーマ版 14。
-- 課題 #452（親が直接行った作業を完了として記録する道）。
--
-- hold に種類（kind）を足す。値は core の定数で検査する（0004・0009・0011 の流儀。CHECK 制約にしない）:
--   human         人の対応を待つ保留（従来のすべての保留。既存の行はこの値で埋まる）
--   parent_direct 対話のセッションの親が直接行う課題の保留（自動の計画・委譲から外すために立てる）
-- 既存の保留の行の他の列（問い・回答・回答の記録）は変えない。
--
-- 直接行った完了の記録（direct_completion）と、その成果物の参照（direct_completion_artifact）を足す。
-- 成果物の参照は run を持たない（run_artifact は run の成果物で、こちらは run を介さない）。
-- direct_completion が 1 行あることが「親が直接行った」印で、課題が完了した後も残る。
-- 参照の個数の下限（1 件以上）は core 層で検査する。

ALTER TABLE hold ADD COLUMN kind TEXT NOT NULL DEFAULT 'human';

CREATE TABLE direct_completion (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    challenge_id INTEGER NOT NULL REFERENCES challenge (id),
    hold_id      INTEGER NOT NULL REFERENCES hold (id),
    note         TEXT,
    created_at   TEXT    NOT NULL
);

CREATE INDEX idx_direct_completion_challenge_id ON direct_completion (challenge_id);

CREATE TABLE direct_completion_artifact (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    direct_completion_id INTEGER NOT NULL REFERENCES direct_completion (id),
    position             INTEGER NOT NULL,
    ref                  TEXT    NOT NULL,
    UNIQUE (direct_completion_id, position)
);

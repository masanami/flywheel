-- 0037_followup_task: スキーマ版 37。
-- 区切り S4c-2 の 1。フォローアップを足す依頼の出どころにするため、足す依頼に followup_id と出どころがフォローアップのときの枠を、
-- フォローアップに上げた先のタスク（promoted_task_id）を足す。出どころ followup を入れられるよう、足す依頼の origin の検査を広げる。
-- 過去の行は書き換えない（新しい列は NULL。既存のフォローアップの promoted_task_id は NULL）。
-- スライスごとに 1 版。

-- 他の表が task_addition を指しているので、表を作り直す間は外部キーの検査をコミットまで遅らせる。
PRAGMA defer_foreign_keys = ON;

-- 行を退避してから表を作り直し、同じ id で戻す（戻した行が、task.addition_id・hold.task_addition_id の指す先を満たす）。
CREATE TEMP TABLE task_addition_backup AS SELECT * FROM task_addition;
CREATE TEMP TABLE task_addition_seq_backup AS SELECT seq FROM sqlite_sequence WHERE name = 'task_addition';

DROP TABLE task_addition;

CREATE TABLE task_addition (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    challenge_id         INTEGER NOT NULL REFERENCES challenge(id),
    origin               TEXT NOT NULL CHECK (origin IN ('expand', 'followup')),
    origin_task_id       INTEGER REFERENCES task(id),
    state                TEXT NOT NULL CHECK (state IN ('pending', 'awaiting_owner', 'applied', 'none', 'declined', 'superseded')),
    spec                 TEXT,
    total_after_micros   INTEGER,
    limit_micros         INTEGER,
    hold_id              INTEGER REFERENCES hold(id),
    created_at           TEXT NOT NULL,
    decided_at           TEXT,
    -- 出どころが followup のとき、元のフォローアップの id と、その枠（実装・レビュー対応。NULL 可）。expand の行は NULL。
    followup_id          INTEGER REFERENCES followup(id),
    budget_impl_micros   INTEGER,
    budget_review_micros INTEGER
);

INSERT INTO task_addition (id, challenge_id, origin, origin_task_id, state, spec, total_after_micros, limit_micros, hold_id, created_at, decided_at)
SELECT id, challenge_id, origin, origin_task_id, state, spec, total_after_micros, limit_micros, hold_id, created_at, decided_at FROM task_addition_backup;

DROP TABLE task_addition_backup;

-- AUTOINCREMENT の採番の記録を、作り直す前の状態へ戻す（行が無かった表には記録を作らない）。
DELETE FROM sqlite_sequence WHERE name = 'task_addition';
INSERT INTO sqlite_sequence (name, seq) SELECT 'task_addition', seq FROM task_addition_seq_backup;
DROP TABLE task_addition_seq_backup;

-- 同じ出どころのタスクについて、origin = 'expand' の行は 1 つ。
CREATE UNIQUE INDEX task_addition_expand_origin ON task_addition (origin_task_id) WHERE origin = 'expand';
CREATE INDEX task_addition_challenge ON task_addition (challenge_id);
CREATE INDEX task_addition_hold ON task_addition (hold_id) WHERE hold_id IS NOT NULL;
CREATE INDEX task_addition_followup ON task_addition (followup_id) WHERE followup_id IS NOT NULL;

-- 上げた先のタスク（出どころが followup の足す依頼で足したタスク）。無ければ NULL。
ALTER TABLE followup ADD COLUMN promoted_task_id INTEGER REFERENCES task (id);

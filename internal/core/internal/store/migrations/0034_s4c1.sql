-- 0034_s4c1: スキーマ版 34。
-- 区切り S4c-1 の 1。計画の後にタスクを足す記録（task_addition）と、課題の上限・タスクの印を足す。
-- followup_id・budget_impl_micros・budget_review_micros・followup.promoted_task_id は S4c-2 のマイグレーションで足す。
-- 過去の行は書き換えない（課題の上限の列は NULL・タスクの expand_after は 0・addition_id は NULL）。
-- スライスごとに 1 版。main へ入るときに、いまの最新の次の版へ並べ直す。

-- 足す依頼。出どころは S4c-1 では expand（続きを決めるタスクが済んだ）だけ。
-- state: pending（仕様待ち）・awaiting_owner（オーナー待ち）・applied（足した）・none（足さない）・declined（見送り）・superseded（取り替え）。
-- total_after_micros・limit_micros は最後に判定したときの値（記録のため。判定には使わない。判定していない足す依頼は NULL）。
CREATE TABLE task_addition (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    challenge_id       INTEGER NOT NULL REFERENCES challenge(id),
    origin             TEXT NOT NULL CHECK (origin IN ('expand')),
    origin_task_id     INTEGER REFERENCES task(id),
    state              TEXT NOT NULL CHECK (state IN ('pending', 'awaiting_owner', 'applied', 'none', 'declined', 'superseded')),
    spec               TEXT,
    total_after_micros INTEGER,
    limit_micros       INTEGER,
    hold_id            INTEGER REFERENCES hold(id),
    created_at         TEXT NOT NULL,
    decided_at         TEXT
);

-- 同じ出どころのタスクについて、origin = 'expand' の行は 1 つ。
CREATE UNIQUE INDEX task_addition_expand_origin ON task_addition (origin_task_id) WHERE origin = 'expand';
CREATE INDEX task_addition_challenge ON task_addition (challenge_id);
CREATE INDEX task_addition_hold ON task_addition (hold_id) WHERE hold_id IS NOT NULL;

-- 足されたタスクの印（足す依頼の id）。計画の承認で作ったタスクは NULL。
ALTER TABLE task ADD COLUMN addition_id INTEGER REFERENCES task_addition(id);
-- 続きを決めるタスクの印（既定 0）。
ALTER TABLE task ADD COLUMN expand_after INTEGER NOT NULL DEFAULT 0;

-- 課題の上限（実装の枠とレビュー対応の枠を足した 1 つの額）とその理由。記録が無い課題は、見込みと同じ額として読む。
ALTER TABLE challenge ADD COLUMN budget_limit_micros INTEGER;
ALTER TABLE challenge ADD COLUMN budget_limit_reason TEXT;

-- 上限だけを変えた承認の印（既定 0）。1 の承認は、枠の使い切り（budget_exhausted）の止まりを解かない。
-- 枠を増やした承認・過去の承認は 0。
ALTER TABLE approval ADD COLUMN limit_only INTEGER NOT NULL DEFAULT 0 CHECK (limit_only IN (0, 1));

-- 足す依頼の保留（task_add_question・task_add_over_limit）が、どの足す依頼の問いかを指す。それ以外の保留は NULL。
ALTER TABLE hold ADD COLUMN task_addition_id INTEGER REFERENCES task_addition(id);
CREATE INDEX hold_task_addition ON hold (task_addition_id) WHERE task_addition_id IS NOT NULL;

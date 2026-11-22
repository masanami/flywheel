-- 0035_supervisor: スキーマ版 35。
-- 区切り S4b-2 の 1。スーパーバイザーのその場の対処のために、保留の止まり方と処理の状態、対処の記録の表、
-- フォローアップのタスクと出どころ、タスクの検証の連続の数え、課題の照合のやり直しの印、契約の実行の上流を読めなかった回りの数を足す。
-- 過去の行は書き換えない（新しい列は NULL・0）。対処の記録の questions の列は足さない（S4b-3）。
-- スライスごとに 1 版。main へ入るときに、いまの最新の次の版へ並べ直す。

-- 保留の止まり方（スーパーバイザーを呼ぶきっかけ。渡されなければ NULL）。
ALTER TABLE hold ADD COLUMN stop_kind TEXT;
-- スーパーバイザーの処理の状態（NULL・pending・escalated・handled）。
ALTER TABLE hold ADD COLUMN supervisor_state TEXT;
ALTER TABLE hold ADD COLUMN supervisor_findings TEXT;
ALTER TABLE hold ADD COLUMN supervisor_recommendation TEXT;
-- その保留を閉じた・上げた対処の記録。無ければ NULL。
ALTER TABLE hold ADD COLUMN supervisor_action_id INTEGER REFERENCES supervisor_action (id);
-- 最後に見送ったサイクルと、見送りの連続の数え。
ALTER TABLE hold ADD COLUMN deferred_cycle_id INTEGER REFERENCES cycle (id);
ALTER TABLE hold ADD COLUMN deferred_count INTEGER NOT NULL DEFAULT 0;

-- 対処の記録（スーパーバイザーの判断 1 回と、core がその手を実行した結果）。
CREATE TABLE supervisor_action (
    id            INTEGER PRIMARY KEY,
    cycle_id      INTEGER NOT NULL REFERENCES cycle (id),
    challenge_id  INTEGER NOT NULL REFERENCES challenge (id),
    task_id       INTEGER REFERENCES task (id),
    hold_id       INTEGER REFERENCES hold (id),
    run_id        INTEGER REFERENCES run (id),
    stop_kind     TEXT NOT NULL,
    hand          TEXT,
    findings      TEXT NOT NULL,
    note          TEXT NOT NULL,
    basis         TEXT NOT NULL,
    conditions    TEXT,
    result        TEXT NOT NULL,
    reject_reason TEXT,
    created_at    TEXT NOT NULL
);

CREATE INDEX supervisor_action_challenge ON supervisor_action (challenge_id, id);
CREATE INDEX supervisor_action_task ON supervisor_action (task_id, id);

-- フォローアップがぶら下がるタスク（NULL 可）。出どころ（source）の値に supervisor を足すが、source は検査の無い文字列で、列の変更は要らない。
ALTER TABLE followup ADD COLUMN task_id INTEGER REFERENCES task (id);

-- スーパーバイザーの reverify による「照合をやり直す」印（その対処の記録。無ければ NULL）。
ALTER TABLE challenge ADD COLUMN reconcile_supervisor_action_id INTEGER REFERENCES supervisor_action (id);

-- タスクの検証の連続の「満たしていない」の数え。
ALTER TABLE task ADD COLUMN verify_not_met_streak INTEGER NOT NULL DEFAULT 0;

-- 契約の実行の、上流を読めなかった回りの数。
ALTER TABLE contract_run ADD COLUMN upstream_unreadable_rounds INTEGER NOT NULL DEFAULT 0;

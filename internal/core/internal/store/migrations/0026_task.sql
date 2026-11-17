-- 0026_task: スキーマ版 26。
-- 課題の下にタスクを持つ形にする（課題 ＞ タスク ＞ フォローアップ）。タスク（task）・タスクの前提
-- （task_dependency）・計画の問いへの答え（plan_answer）の表と、タスクへの参照の列
-- （run.task_id・contract_run.task_id・hold.task_id）・統合先を使うかの列（challenge.uses_integration）を足す。
-- 過去の行は書き換えない（task_id は NULL のまま、uses_integration は NULL＝未決か過去の課題）。
-- 既存の索引と一意の制約（idx_run_active_challenge・idx_contract_run_active_challenge）は変えない。

CREATE TABLE task (
    id            INTEGER PRIMARY KEY,
    challenge_id  INTEGER NOT NULL REFERENCES challenge(id),
    plan_version  INTEGER NOT NULL,
    key           TEXT,
    title         TEXT NOT NULL,
    body          TEXT NOT NULL,
    done_criteria TEXT NOT NULL,
    non_goals     TEXT,
    repo          TEXT NOT NULL,
    operation     TEXT NOT NULL,
    status        TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

-- 同じ課題・同じ計画の版の中で key は重ならない。key が NULL の行どうしは重なりと見ない（SQLite の一意索引は NULL を区別する）。
CREATE UNIQUE INDEX task_challenge_plan_key ON task (challenge_id, plan_version, key);

CREATE INDEX task_challenge ON task (challenge_id);

CREATE TABLE task_dependency (
    task_id       INTEGER NOT NULL REFERENCES task(id),
    depends_on_id INTEGER NOT NULL REFERENCES task(id),
    PRIMARY KEY (task_id, depends_on_id)
);

CREATE TABLE plan_answer (
    id              INTEGER PRIMARY KEY,
    challenge_id    INTEGER NOT NULL REFERENCES challenge(id),
    plan_version    INTEGER NOT NULL,
    question_number INTEGER NOT NULL,
    choice          INTEGER,
    text            TEXT,
    recommended     INTEGER NOT NULL DEFAULT 0,
    actor           TEXT NOT NULL,
    channel         TEXT NOT NULL,
    verification    TEXT NOT NULL,
    relay_session   TEXT,
    relay_quote     TEXT,
    answered_at     TEXT NOT NULL
);

CREATE INDEX plan_answer_challenge_plan ON plan_answer (challenge_id, plan_version, question_number);

ALTER TABLE run ADD COLUMN task_id INTEGER REFERENCES task(id);
ALTER TABLE contract_run ADD COLUMN task_id INTEGER REFERENCES task(id);
ALTER TABLE hold ADD COLUMN task_id INTEGER REFERENCES task(id);
ALTER TABLE challenge ADD COLUMN uses_integration INTEGER;

-- 0001_initial: スキーマ版 1。
-- 親要件チケット #4 §データモデル（決定 2026-09-21・オーナー）のエンティティに従う。
-- 列名・型は実装判断（エンティティの切り方と属性の意味は変えていない）。

CREATE TABLE challenge (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    title         TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    done_criteria TEXT NOT NULL DEFAULT '',
    urgency       TEXT,
    priority      TEXT,
    status        TEXT NOT NULL,
    version       INTEGER NOT NULL DEFAULT 1,
    reporter      TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE TABLE task_plan (
    challenge_id INTEGER NOT NULL REFERENCES challenge (id),
    version      INTEGER NOT NULL,
    body         TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    PRIMARY KEY (challenge_id, version)
);

CREATE TABLE operation (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    challenge_id INTEGER NOT NULL REFERENCES challenge (id),
    kind         TEXT NOT NULL,
    summary      TEXT NOT NULL,
    ref          TEXT,
    state        TEXT NOT NULL,
    version      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL
);

CREATE TABLE approval (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    challenge_id   INTEGER NOT NULL REFERENCES challenge (id),
    operation_id   INTEGER REFERENCES operation (id),
    kind           TEXT NOT NULL,
    decision       TEXT NOT NULL,
    target_version INTEGER NOT NULL,
    actor          TEXT NOT NULL,
    channel        TEXT NOT NULL,
    verification   TEXT NOT NULL,
    reason         TEXT,
    decided_at     TEXT NOT NULL
);

CREATE TABLE hold (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    challenge_id INTEGER NOT NULL REFERENCES challenge (id),
    question     TEXT NOT NULL,
    from_status  TEXT NOT NULL,
    raised_at    TEXT NOT NULL,
    answer       TEXT,
    answered_at  TEXT,
    answered_by  TEXT
);

CREATE TABLE activity (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    at           TEXT NOT NULL,
    actor        TEXT NOT NULL,
    channel      TEXT NOT NULL,
    verification TEXT NOT NULL,
    entity       TEXT NOT NULL,
    entity_id    INTEGER NOT NULL,
    action       TEXT NOT NULL,
    before       TEXT,
    after        TEXT
);

CREATE INDEX idx_task_plan_challenge_id ON task_plan (challenge_id);
CREATE INDEX idx_operation_challenge_id ON operation (challenge_id);
CREATE INDEX idx_approval_challenge_id ON approval (challenge_id);
CREATE INDEX idx_hold_challenge_id ON hold (challenge_id);
CREATE INDEX idx_activity_entity ON activity (entity, entity_id);

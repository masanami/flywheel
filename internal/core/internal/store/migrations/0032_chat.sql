-- 0031_chat: スキーマ版 31。
-- 画面の対話の欄（区切り S4d-1）の会話と発言の表を足す。ストアはエージェントごとなので、エージェントの列は持たない。
-- 実行エンジンの側の会話のファイルには頼らず、会話・発言・費用はこの 2 表が正本。
-- 金額（chat_message.cost_usd）は他の表と同じく USD の 100 万分の 1 を単位とする整数。

CREATE TABLE chat_session (
    id             INTEGER PRIMARY KEY,
    engine_session TEXT NOT NULL,
    engine_started INTEGER NOT NULL DEFAULT 0,
    started_at     TEXT NOT NULL,
    closed_at      TEXT
);

-- 開いている会話（closed_at が NULL）は高々 1 つ。
CREATE UNIQUE INDEX chat_session_open ON chat_session ((1)) WHERE closed_at IS NULL;

CREATE TABLE chat_message (
    id                   INTEGER PRIMARY KEY,
    session_id           INTEGER NOT NULL REFERENCES chat_session(id),
    role                 TEXT NOT NULL,
    body                 TEXT NOT NULL,
    state                TEXT NOT NULL,
    cost_usd             INTEGER NOT NULL DEFAULT 0,
    viewing_challenge_id TEXT,
    viewing_task_id      TEXT,
    viewing_title        TEXT,
    activity             TEXT NOT NULL DEFAULT '[]',
    created_at           TEXT NOT NULL,
    ended_at             TEXT
);

CREATE INDEX chat_message_session ON chat_message (session_id);

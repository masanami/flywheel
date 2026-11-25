-- 0041_chat_session_process: スキーマ版 41。
-- 対話の欄の作り替え（docs/features/chat-session.md「DB スキーマ」）。会話ごとに実行エンジンを起動したまま続ける形のための、
-- 確認と紐づけの表と、費用の列を足す。金額は他の表と同じく USD の 100 万分の 1 を単位とする整数。
-- chat_message.activity の列は変えず、要素の形を {id, tool, target, state} に変える（過去の {command, ok} は読むときに読み替える）。

-- 実行エンジンが返した費用の累計のうち、最後に記録した値。返事の費用はこの値との差。
ALTER TABLE chat_session ADD COLUMN engine_cost_total INTEGER NOT NULL DEFAULT 0;
-- これまでの返事の cost_usd は実行エンジンが返した累計なので、費用のある最後の返事の値が、最後に記録した累計。
-- 埋めないと、既存の会話を続けた最初の返事の差に、累計の全額が入る。
UPDATE chat_session SET engine_cost_total = COALESCE(
    (SELECT m.cost_usd FROM chat_message m WHERE m.session_id = chat_session.id AND m.cost_usd > 0 ORDER BY m.id DESC LIMIT 1), 0);
-- この返事の費用を仕様づくりとして数えた課題。
ALTER TABLE chat_message ADD COLUMN charged_challenge_id INTEGER REFERENCES challenge(id);

-- 許可の確認と問い。進行中の返事の行に結び付ける。
CREATE TABLE chat_prompt (
    id          INTEGER PRIMARY KEY,
    message_id  INTEGER NOT NULL REFERENCES chat_message(id),
    request_id  TEXT NOT NULL,
    kind        TEXT NOT NULL,
    tool        TEXT NOT NULL,
    target      TEXT NOT NULL,
    questions   TEXT,
    state       TEXT NOT NULL,
    answer      TEXT,
    created_at  TEXT NOT NULL,
    answered_at TEXT
);

CREATE INDEX chat_prompt_message ON chat_prompt (message_id);

-- 会話と課題の紐づけ。
CREATE TABLE chat_link (
    id           INTEGER PRIMARY KEY,
    session_id   INTEGER NOT NULL REFERENCES chat_session(id),
    challenge_id INTEGER NOT NULL REFERENCES challenge(id),
    action       TEXT NOT NULL,
    message_id   INTEGER NOT NULL REFERENCES chat_message(id),
    created_at   TEXT NOT NULL
);

CREATE UNIQUE INDEX chat_link_pair ON chat_link (session_id, challenge_id);

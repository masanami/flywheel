-- 0045_requirement: スキーマ版 45。
-- 要求の記録（requirement。表示は RQ-<id>。消さない）と、区切りを分ける記録（slice_split。課題ごとに高々 1 件）の表と、
-- 課題が要求の記録を指す欄（challenge.requirement_id。NULL は指さない）。origin と state の値は core の定数で検査する。
-- proposal は案の JSON。既存の課題は NULL。
CREATE TABLE requirement (
    id                  INTEGER PRIMARY KEY,
    origin              TEXT    NOT NULL,
    title               TEXT    NOT NULL,
    external_key        TEXT,
    url                 TEXT,
    chat_session_id     INTEGER REFERENCES chat_session(id),
    origin_challenge_id INTEGER REFERENCES challenge(id),
    evidence_path       TEXT    NOT NULL,
    created_at          TEXT    NOT NULL,
    resolved_at         TEXT
);
CREATE TABLE slice_split (
    challenge_id    INTEGER PRIMARY KEY REFERENCES challenge(id),
    state           TEXT    NOT NULL,
    classify_reason TEXT    NOT NULL,
    proposal        TEXT,
    evidence_path   TEXT,
    requirement_id  INTEGER REFERENCES requirement(id),
    hold_id         INTEGER REFERENCES hold(id),
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);
ALTER TABLE challenge ADD COLUMN requirement_id INTEGER REFERENCES requirement(id);

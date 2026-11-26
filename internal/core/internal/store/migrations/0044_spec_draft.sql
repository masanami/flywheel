-- 0044_spec_draft: スキーマ版 44。
-- 下書きづくりの記録（課題ごとに高々 1 件）。state と hold_reason の値は core の定数で検査する。
-- questions・answers は JSON の配列。hold_id は開いている下書きづくりの保留。
CREATE TABLE spec_draft (
    challenge_id INTEGER PRIMARY KEY REFERENCES challenge(id),
    path         TEXT    NOT NULL,
    base_path    TEXT,
    repo         TEXT    NOT NULL,
    reason       TEXT    NOT NULL,
    state        TEXT    NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 0,
    questions    TEXT,
    answers      TEXT,
    hold_id      INTEGER REFERENCES hold(id),
    hold_reason  TEXT,
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);

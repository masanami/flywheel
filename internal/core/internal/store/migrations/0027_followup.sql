-- 0027_followup: スキーマ版 27。
-- フォローアップ（followup）と、取り込み元の親の Issue の子（source_child）の表、タスクと取り込み元の子の
-- 対応の列（task.source_key）を足す。フォローアップは課題の状態機械に乗せず、課題の表に種類を足さない。
-- 過去の行は書き換えない（出どころが followup の過去の課題をフォローアップの行へ移さない。
-- task.source_key は NULL のまま）。source_child と task.source_key は列と表を作るだけで、読み書きは取り込みの課題が持つ。

CREATE TABLE followup (
    id                    INTEGER PRIMARY KEY,
    challenge_id          INTEGER REFERENCES challenge(id),
    title                 TEXT NOT NULL,
    detail                TEXT,
    source                TEXT NOT NULL,
    status                TEXT NOT NULL,
    note                  TEXT,
    reason                TEXT,
    promoted_challenge_id INTEGER REFERENCES challenge(id),
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);

CREATE INDEX followup_challenge ON followup (challenge_id);
CREATE INDEX followup_status ON followup (status);

CREATE TABLE source_child (
    id                  INTEGER PRIMARY KEY,
    challenge_id        INTEGER NOT NULL REFERENCES challenge(id),
    source_id           TEXT NOT NULL,
    external_key        TEXT NOT NULL,
    parent_external_key TEXT,
    url                 TEXT,
    title               TEXT,
    body                TEXT,
    upstream_state      TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE UNIQUE INDEX source_child_external_key ON source_child (external_key);
CREATE INDEX source_child_challenge ON source_child (challenge_id);

ALTER TABLE task ADD COLUMN source_key TEXT;

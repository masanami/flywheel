-- 0011_learning: スキーマ版 11。
-- 親要件チケット #350（docs/features/m4.5-learning-and-self-improvement.md）の S3。
-- §クリティカル設計決定 4（M45P5）・M45P24 に従う。
--
-- 発生（occurrence）の表と、run の flywheel_version、challenge の origin・followup_of を足す。
-- 閉集合（種類・出どころ・原因・層・良し悪し・origin）は CHECK 制約にせず core の定数で検査する
-- （0004・0009 の流儀）。CHECK を持つのは「参照は 5 つのうちちょうど 1 つ」という構造だけ。
-- 既存の行の値は変えない（challenge.origin を除く）。

CREATE TABLE occurrence (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    challenge_id     INTEGER NOT NULL REFERENCES challenge (id),
    cycle_id         INTEGER REFERENCES cycle (id),
    cause_run_id     INTEGER REFERENCES run (id),
    kind             TEXT    NOT NULL,
    j6_kind          TEXT,
    approval_id      INTEGER REFERENCES approval (id),
    hold_id          INTEGER REFERENCES hold (id),
    run_id           INTEGER REFERENCES run (id),
    execution_id     INTEGER REFERENCES operation_execution (id),
    activity_id      INTEGER REFERENCES activity (id),
    outcome          TEXT    NOT NULL,
    source           TEXT    NOT NULL,
    cause            TEXT,
    layer            TEXT,
    target           TEXT,
    signal           TEXT,
    classified_run_id INTEGER REFERENCES run (id),
    handled          TEXT,
    created_at       TEXT    NOT NULL,
    CHECK ((approval_id IS NOT NULL) + (hold_id IS NOT NULL) + (run_id IS NOT NULL)
         + (execution_id IS NOT NULL) + (activity_id IS NOT NULL) = 1)
);

CREATE INDEX idx_occurrence_challenge_id ON occurrence (challenge_id);
CREATE INDEX idx_occurrence_created_at ON occurrence (created_at);

ALTER TABLE run ADD COLUMN flywheel_version TEXT;

ALTER TABLE challenge ADD COLUMN followup_of TEXT;
ALTER TABLE challenge ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual';
UPDATE challenge SET origin = 'ingest'
 WHERE EXISTS (SELECT 1 FROM source_binding sb WHERE sb.challenge_id = challenge.id);

-- 0005_slot_run_artifact: スキーマ版 5。
-- 親要件チケット #98・#99（docs/features/m3-invoker-delegation.md）
-- §クリティカル設計決定 1（M3H7。M3H8・M3H10 で拡張）に従う。
--
-- slot（作業スロット）・run_artifact（run の成果物）の表を足し、run に
-- slot_id・decider・decider_row・repo の列を足す。run.kind の predict・
-- run.budget_bucket の predict は閉集合の値の追加であり、0004 の流儀どおり
-- CHECK 制約を持たず core 層（run_store.go）で検証する。task_plan には
-- flywheel budget の上書き（実装枠・レビュー対応枠）を列で持つ。
--
-- predict の run は challenge_id・challenge_version・session_id を持たない
-- （NULL）。0004 の run はこの 3 列を NOT NULL で作っているため、run 表を
-- 作り直して NOT NULL を外す（SQLite は列の NOT NULL を ALTER できない）。
-- 既存の run 行は列の値のまま移す（退避表 run_bak へ写し、run を作り直して
-- 戻す。リネームで入れ替えないのは、外部キーの検査をコミットまで遅らせる
-- 方式では、同じ名前の run へ行を戻すことで参照先がそろうため）。
-- hold.run_id・activity.run_id・run.resumed_from_run_id が run を参照して
-- いるので、外部キーの検査をコミットまで遅らせる。
--
-- slot・run_artifact（と run・cycle・lock）の書き込みは作業ログ（activity）に
-- 載せない（M3P11）。既存の課題・計画・承認・保留・作業ログ・対応の記録・
-- run・周は変えない（AC-371）。
-- 金額は USD の 100 万分の 1 を単位とする整数で持つ。

PRAGMA defer_foreign_keys = ON;

-- slot: 作業スロット。id の表示形は SL-<正の整数>。run_id は使用中の run
-- （使用中でなければ NULL）。attention_reason は state が needs_attention の
-- 理由（払い出しの失敗・パスが無い・未コミットの変更など。FR64・M3P45）で、
-- 自由記述。needs_attention でないときは NULL。
CREATE TABLE slot (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    repo             TEXT NOT NULL,
    provider         TEXT NOT NULL,
    path             TEXT NOT NULL,
    state            TEXT NOT NULL,
    run_id           INTEGER REFERENCES run (id),
    attention_reason TEXT,
    -- 1 つの作業ツリーを指すスロットは 1 つ（1 スロット 1 セッションの前提を
    -- ストアの層で守る）。
    UNIQUE (repo, path)
);

CREATE TABLE run_bak AS SELECT * FROM run;

DROP TABLE run;

CREATE TABLE run (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    cycle_id                INTEGER REFERENCES cycle (id),
    kind                    TEXT NOT NULL,
    judgment                TEXT,
    challenge_id            INTEGER REFERENCES challenge (id),
    challenge_version       INTEGER,
    plan_version            INTEGER,
    session_id              TEXT,
    session_id_mismatch     INTEGER NOT NULL DEFAULT 0,
    resumed_from_run_id     INTEGER REFERENCES run (id),
    pid                     INTEGER NOT NULL,
    host                    TEXT NOT NULL,
    heartbeat_at            TEXT NOT NULL,
    started_at              TEXT NOT NULL,
    ended_at                TEXT,
    result                  TEXT,
    rate_limited            INTEGER NOT NULL DEFAULT 0,
    max_budget_usd          INTEGER NOT NULL,
    budget_bucket           TEXT NOT NULL,
    cost_usd                INTEGER,
    cost_source             TEXT,
    reported_total_cost_usd INTEGER,
    output                  TEXT,
    error                   TEXT,
    slot_id                 INTEGER REFERENCES slot (id),
    decider                 TEXT,
    decider_row             INTEGER,
    repo                    TEXT
);

INSERT INTO run (id, cycle_id, kind, judgment, challenge_id, challenge_version, plan_version,
    session_id, session_id_mismatch, resumed_from_run_id, pid, host, heartbeat_at, started_at,
    ended_at, result, rate_limited, max_budget_usd, budget_bucket, cost_usd, cost_source,
    reported_total_cost_usd, output, error)
SELECT id, cycle_id, kind, judgment, challenge_id, challenge_version, plan_version,
    session_id, session_id_mismatch, resumed_from_run_id, pid, host, heartbeat_at, started_at,
    ended_at, result, rate_limited, max_budget_usd, budget_bucket, cost_usd, cost_source,
    reported_total_cost_usd, output, error
FROM run_bak;

DROP TABLE run_bak;

CREATE INDEX idx_run_challenge_id ON run (challenge_id);
CREATE INDEX idx_run_cycle_id ON run (cycle_id);
CREATE INDEX idx_run_slot_id ON run (slot_id);

-- AC-168: 1 つの課題に終了していない run を 2 つ書き込もうとするとストアが
-- 拒否する（predict の run は challenge_id が NULL で、NULL は互いに異なる
-- ので対象外）。
CREATE UNIQUE INDEX idx_run_active_challenge ON run (challenge_id) WHERE result IS NULL;

-- AC-280: 1 つのスロットを使う終了していない run は高々 1 つ（部分一意索引。
-- 「終了していない」＝ result IS NULL）。
CREATE UNIQUE INDEX idx_run_active_slot ON run (slot_id) WHERE result IS NULL AND slot_id IS NOT NULL;

-- run_artifact: run が作った成果物（ブランチ・PR・コミット）。state は PR の
-- とき open | closed | merged、それ以外は NULL。
CREATE TABLE run_artifact (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      INTEGER NOT NULL REFERENCES run (id),
    kind        TEXT NOT NULL,
    ref         TEXT NOT NULL,
    state       TEXT,
    base        TEXT,
    verified_at TEXT
);

CREATE INDEX idx_run_artifact_run_id ON run_artifact (run_id);

-- flywheel budget の上書き（実装枠・レビュー対応枠。USD の 100 万分の 1 を
-- 単位とする整数。NULL は上書きなし）。
ALTER TABLE task_plan ADD COLUMN impl_budget_usd INTEGER;
ALTER TABLE task_plan ADD COLUMN review_budget_usd INTEGER;

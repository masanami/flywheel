-- 0019_contract_run: スキーマ版 19。
-- docs/features/m3-invoker-delegation.md §委譲の流れ（`form: cli` の接続ツール。前半）。
--
-- `form: cli` の接続ツールの run（契約の実行 ID）を、周をまたいで持つ表。`contract start` が返した
-- 実行 ID を保存し、次の周以降の `contract status` の読みで state・要約・求められた操作・費用の累計を
-- 更新する。flywheel の run（判断の呼び出し・委譲の起動の実行記録）とは別で、start と status の各呼び出しは
-- それぞれ run として記録する（start_run_id は start を呼んだ run）。
--
-- cost_usd は契約が最後に報告した `cost_usd` の累計（孫を含む。USD の 100 万分の 1 を単位とする整数）。
-- 状態を何度読んでも二重に数えないため、run の費用には「今回の累計 − この列の値」の差分だけを記録し、
-- この列は累計で置き換える（減らない）。
-- ended_at は state が succeeded | failed | cancelled のときだけ入る。終わっていない契約の run は
-- 課題ごとに高々 1 つ（部分一意索引）。既存の表の値は変えない。

CREATE TABLE contract_run (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    challenge_id     INTEGER NOT NULL REFERENCES challenge (id),
    connector_id     TEXT    NOT NULL,
    external_run_id  TEXT    NOT NULL,
    start_run_id     INTEGER NOT NULL REFERENCES run (id),
    state            TEXT    NOT NULL,
    summary          TEXT    NOT NULL,
    requested_kind   TEXT,
    requested_decider TEXT,
    requested_text   TEXT,
    cost_usd         INTEGER NOT NULL DEFAULT 0,
    max_budget_usd   INTEGER NOT NULL,
    started_at       TEXT    NOT NULL,
    observed_at      TEXT    NOT NULL,
    ended_at         TEXT,
    UNIQUE (connector_id, external_run_id)
);

CREATE UNIQUE INDEX idx_contract_run_active_challenge ON contract_run (challenge_id) WHERE ended_at IS NULL;

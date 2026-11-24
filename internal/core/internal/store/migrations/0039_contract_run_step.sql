-- 0039_contract_run_step: スキーマ版 39。
-- フローの移植 S1 の 8。委譲の実行（contract_run）にぶら下がる、ステップ 1 回ぶんの行（contract_run_step）と、
-- フローの名前（contract_run.flow。無ければ NULL）を足す。契約の出力の任意の欄 steps・flow を写す先で、
-- 記録の正本はファイル（.flywheel/flow/runs/）であり、この表はその写し。
-- 費用（cost_usd）は USD の 100 万分の 1 を単位にした整数で、無ければ NULL（ほかの費用の列と同じ単位）。
-- cost_unknown は費用が上限額での計上かの印。models_used は使ったモデルの JSON の配列の文字列。
-- 委譲の実行の ID と seq の組は重ならない。過去の行は書き換えない（既存の contract_run の flow は NULL）。

ALTER TABLE contract_run ADD COLUMN flow TEXT;

CREATE TABLE contract_run_step (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    contract_run_id INTEGER NOT NULL REFERENCES contract_run (id),
    seq             INTEGER NOT NULL CHECK (seq >= 1),
    step            TEXT    NOT NULL,
    kind            TEXT    NOT NULL,
    attempt         INTEGER NOT NULL CHECK (attempt >= 1),
    state           TEXT    NOT NULL CHECK (state IN ('running', 'finished', 'cancelled', 'interrupted')),
    outcome         TEXT,
    started_at      TEXT    NOT NULL,
    ended_at        TEXT,
    cost_usd        INTEGER,
    cost_unknown    INTEGER NOT NULL DEFAULT 0 CHECK (cost_unknown IN (0, 1)),
    model           TEXT,
    models_used     TEXT    NOT NULL DEFAULT '[]',
    UNIQUE (contract_run_id, seq)
);

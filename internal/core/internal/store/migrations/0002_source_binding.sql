-- 0002_source_binding: スキーマ版 2。
-- 親要件チケット #51（docs/features/m2-github-issue-ingest.md）
-- §クリティカル設計決定 1（オーナー QH1・2026-09-24）に従う。既存の表は変えない。
--
-- 課題 1 つに対応（source_binding）は高々 1 つ（challenge_id が主キー）。
-- 照合は external_key だけで行う（source_id は由来の記録。一意性は external_key
-- 単独に付け、(source_id, external_key) にはしない）。
-- upstream_state・policy_state の閉集合は 0001 の流儀（status 等）に合わせ、
-- CHECK 制約は付けず core 層で検証する。last_synced_at は持たない（QH1）。

CREATE TABLE source_binding (
    challenge_id   INTEGER PRIMARY KEY REFERENCES challenge (id),
    source_id      TEXT NOT NULL,
    external_key   TEXT NOT NULL UNIQUE,
    url            TEXT NOT NULL,
    fingerprint    TEXT NOT NULL,
    upstream_state TEXT NOT NULL,
    policy_state   TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

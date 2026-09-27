-- 0003_source_binding_observations: スキーマ版 3。
-- 親要件チケット #51（docs/features/m2-github-issue-ingest.md）
-- §クリティカル設計決定 1（オーナー QH9・QH10・2026-09-26）に従う。
--
-- 上流の観測値（コメント数・更新日時）と、最後に読んだ時点の値を
-- source_binding に足す。既存の行（0002 の適用後・このマイグレーション適用前に
-- 作られた対応）は comments_count・read_comments_count を 0、
-- upstream_updated_at・read_upstream_updated_at を NULL（未設定）にする
-- （§クリティカル設計決定 1 の采用案）。NULL は「まだ観測していない」ことを
-- core 層（空文字列 "" を内部の未設定センチネルとして扱う）が読み分ける。

ALTER TABLE source_binding ADD COLUMN comments_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_binding ADD COLUMN upstream_updated_at TEXT;
ALTER TABLE source_binding ADD COLUMN read_comments_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_binding ADD COLUMN read_upstream_updated_at TEXT;

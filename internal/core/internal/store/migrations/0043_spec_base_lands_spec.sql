-- 0043_spec_base_lands_spec: スキーマ版 43。
-- 課題の置き換える元の文書の場所（リポジトリの中の相対のパス）と、タスクが仕様の下書きをリポジトリへ入れるタスクかの印。
-- 既存の行は、置き換える元の無い課題（NULL）・入れるタスクでないタスク（0）。
ALTER TABLE challenge ADD COLUMN spec_base_path TEXT;
ALTER TABLE task ADD COLUMN lands_spec INTEGER NOT NULL DEFAULT 0 CHECK (lands_spec IN (0, 1));

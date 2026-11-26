-- 0042_spec_path_covers: スキーマ版 42。
-- 課題の仕様の文書の場所（ワークスペースから見た相対のパス）と、タスクが受け持つ受入基準の識別（JSON の配列）。
-- 既存の行は NULL のまま（場所の無い課題・受け持ちの無いタスク）。
ALTER TABLE challenge ADD COLUMN spec_path TEXT;
ALTER TABLE task ADD COLUMN covers TEXT;

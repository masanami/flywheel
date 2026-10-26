-- 0012_improvement: スキーマ版 12。
-- 親要件チケット #384（docs/features/m4.5-learning-and-self-improvement.md）の S4。
--
-- 改修課題の鍵（improvement_layer・improvement_target）と、保留を閉じた時刻（hold.closed_at）を足す。
-- 閉集合は CHECK 制約にせず core の定数で検査する（0004・0009・0011 の流儀）。
-- 既存の行の値は変えない（新しい列は NULL）。

ALTER TABLE challenge ADD COLUMN improvement_layer TEXT;
ALTER TABLE challenge ADD COLUMN improvement_target TEXT;

ALTER TABLE hold ADD COLUMN closed_at TEXT;

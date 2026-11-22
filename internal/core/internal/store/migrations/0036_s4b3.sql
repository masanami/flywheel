-- 区切り S4b-3（委譲先の問いに答える）。対処の記録に、子の問いと答え（JSON。無ければ NULL）を足す。
-- 過去の行は書き換えない（新しい列は NULL）。
ALTER TABLE supervisor_action ADD COLUMN questions TEXT;

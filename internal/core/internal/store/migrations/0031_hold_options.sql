-- 0031_hold_options: スキーマ版 31。
-- 区切り S4b-1 の 1。保留に選択肢の並びと「識別の無い回答を断る」印を持たせる。残りの列（stop_kind・supervisor_*・deferred_* など）は S4b-2。
-- 過去の行は書き換えない（options・answer_option は NULL、option_required は 0）。
-- スライスごとに 1 版。main へ入るときに、いまの最新の次の版へ並べ直す。

-- 選択肢の並び（JSON の配列。要素は id・label・description）。無ければ NULL。
ALTER TABLE hold ADD COLUMN options TEXT;
-- 選んだ選択肢の識別。選んでいなければ NULL。
ALTER TABLE hold ADD COLUMN answer_option TEXT;
-- 識別の無い回答を断る印（既定 0）。
ALTER TABLE hold ADD COLUMN option_required INTEGER NOT NULL DEFAULT 0;

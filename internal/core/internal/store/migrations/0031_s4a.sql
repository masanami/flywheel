-- 0031_s4a: スキーマ版 31。
-- サイクルの記録（cycle）に、終わった理由・回りの数・止める指示の時刻を足す（S4a）。後のタスクが使う列もここですべて足し、
-- 後のタスクはマイグレーションを足さない。過去の行は書き換えない（end_reason は NULL のまま・rounds は 0）。
-- 止める指示はプロセスへのシグナルにせず、ストアに残してサイクルが自分で読む（stop_requested_at）。

ALTER TABLE cycle ADD COLUMN end_reason TEXT;
ALTER TABLE cycle ADD COLUMN rounds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE cycle ADD COLUMN stop_requested_at TEXT;

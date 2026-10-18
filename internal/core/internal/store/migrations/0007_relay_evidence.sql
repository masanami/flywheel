-- 0007_relay_evidence: スキーマ版 7。
-- 対話で受けた承認・回答（経路 cli・方式 relayed）の会話の手がかり
-- （relay_session＝会話の識別・relay_quote＝発言の要旨）と、保留への回答の経路・方式を記録する。
-- 既存の行の追加列は NULL のままにする（推測で値を割り当てない）。

ALTER TABLE approval ADD COLUMN relay_session TEXT;
ALTER TABLE approval ADD COLUMN relay_quote TEXT;

ALTER TABLE hold ADD COLUMN relay_session TEXT;
ALTER TABLE hold ADD COLUMN relay_quote TEXT;
ALTER TABLE hold ADD COLUMN answered_channel TEXT;
ALTER TABLE hold ADD COLUMN answered_verification TEXT;

-- 0008_challenge_card_stages: スキーマ版 8。
-- 課題カードの「一言で」「位置づけ」（headline・context）と段階の欄
-- （issues・expected・actual・non_goals は JSON の配列を入れた TEXT、figure は Mermaid の TEXT）、
-- 論点を出した保留への参照（issues_hold_id）、差し戻しのずれた欄（approval.rejected_fields。
-- JSON の配列を入れた TEXT）を足す。どの列も NULL 可。既存の行は NULL のままにする。

ALTER TABLE challenge ADD COLUMN headline TEXT;
ALTER TABLE challenge ADD COLUMN context TEXT;
ALTER TABLE challenge ADD COLUMN issues TEXT;
ALTER TABLE challenge ADD COLUMN issues_hold_id INTEGER REFERENCES hold (id);
ALTER TABLE challenge ADD COLUMN expected TEXT;
ALTER TABLE challenge ADD COLUMN actual TEXT;
ALTER TABLE challenge ADD COLUMN non_goals TEXT;
ALTER TABLE challenge ADD COLUMN figure TEXT;

ALTER TABLE approval ADD COLUMN rejected_fields TEXT;

-- 0025_drop_challenge_conflict: スキーマ版 25。
-- 衝突の記録（challenge_conflict）の表と索引を消す。委譲の段は衝突を書かず、関係グラフと
-- 「待っている理由」も読まなくなったため。過去の衝突の記録は消える（オーナーの決定）。
-- challenge の行は変えない。

DROP INDEX challenge_conflict_with;
DROP TABLE challenge_conflict;

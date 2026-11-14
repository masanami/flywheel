-- 0022_hold_replan: スキーマ版 22。
-- docs/features/m1-core.md §状態機械（T12）の「範囲が変わる」印。保留への回答に付けた印で、1 なら、その回答は
-- 委譲を再開せず、課題を計画の作り直し（分類済）へ戻した。既存の行は 0。

ALTER TABLE hold ADD COLUMN replan INTEGER NOT NULL DEFAULT 0;

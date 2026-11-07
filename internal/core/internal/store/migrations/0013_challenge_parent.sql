-- 0013_challenge_parent: スキーマ版 13。
-- docs/features/ui-relation-graph.md §親子（core）。
--
-- 課題の親子（challenge_id の課題の親が parent_id の課題）を足す。親は 0 個か 1 個
-- （主キーが challenge_id）。親子は表示（関係グラフの入れ子）にだけ使い、状態機械・委譲の起動の判定・
-- 依存の評価には使わない。自己参照と循環の拒否は CHECK 制約ではなく core 層で検査する。
-- 既存の行の値は変えない。

CREATE TABLE challenge_parent (
    challenge_id INTEGER PRIMARY KEY REFERENCES challenge(id),
    parent_id    INTEGER NOT NULL REFERENCES challenge(id),
    created_at   TEXT    NOT NULL
);

CREATE INDEX challenge_parent_parent ON challenge_parent (parent_id);

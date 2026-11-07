-- 0014_challenge_conflict: スキーマ版 14。
-- docs/features/ui-relation-graph.md §衝突（core）。
--
-- 衝突の予測（predict-conflicts）の結果として、同時に委譲しない（順に走らせる）と決めた課題の組を持つ。
-- 前提の依存（challenge_dependency）とは別の表で、依存の評価・委譲の起動の判定は読まない。
-- 関係グラフの API が衝突の辺（kind=conflict）として返すためだけに使う。
--
-- directed = 1 は「challenge_id の課題を後回しにして、conflicts_with_id の課題を先に委譲した」（向きあり。
-- 後回し → 先に委譲）。directed = 0 は、衝突と予測されたがまだどちらも委譲していない向きの無い関係で、
-- challenge_id が ID の小さい側（組の重複を作らない）。cycle_id・run_id は、組を記録した予測の周と、
-- 予測の口を呼んだ run。自己参照の拒否は CHECK 制約で行う。既存の行の値は変えない。

CREATE TABLE challenge_conflict (
    challenge_id      INTEGER NOT NULL REFERENCES challenge(id),
    conflicts_with_id INTEGER NOT NULL REFERENCES challenge(id),
    directed          INTEGER NOT NULL CHECK (directed IN (0, 1)),
    cycle_id          INTEGER REFERENCES cycle(id),
    run_id            INTEGER REFERENCES run(id),
    created_at        TEXT    NOT NULL,
    PRIMARY KEY (challenge_id, conflicts_with_id),
    CHECK (challenge_id <> conflicts_with_id),
    CHECK (directed = 1 OR challenge_id < conflicts_with_id)
);

CREATE INDEX challenge_conflict_with ON challenge_conflict (conflicts_with_id);

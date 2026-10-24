-- 0010_challenge_dependency: スキーマ版 10。
-- 親要件チケット #324（docs/features/m4.5-learning-and-self-improvement.md）の S2。
-- §クリティカル設計決定 2（M45H12）に従う。
--
-- 課題どうしの依存（challenge_id の課題が depends_on_id の課題を依存先に持つ）を足す。
-- 状態機械（遷移表）は変えず、「依存待ち」の状態も足さない。既存の行の値は変えない。
-- 主キーは組。自己参照と循環の拒否は CHECK 制約ではなく core 層で検査する
-- （ストアに直接入れた循環を status の食い違いとして見つけられるようにするため）。

CREATE TABLE challenge_dependency (
    challenge_id  INTEGER NOT NULL REFERENCES challenge(id),
    depends_on_id INTEGER NOT NULL REFERENCES challenge(id),
    created_at    TEXT    NOT NULL,
    PRIMARY KEY (challenge_id, depends_on_id)
);

CREATE INDEX challenge_dependency_depends_on ON challenge_dependency (depends_on_id);

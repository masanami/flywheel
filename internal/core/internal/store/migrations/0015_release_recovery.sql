-- 0015_release_recovery: スキーマ版 15。
-- 課題 #485（完了の承認まで済んだ PR が、本番反映でマージできなくなったとき、検証を通って立て直す道）。
--
-- operation（不可逆操作）に足す列:
--   closed_reason             閉じた理由（core の定数で検査する閉集合: superseded / merged_outside / merged_unapproved）。
--                             state が closed の行だけが値を持つ
--   closed_at                 閉じた日時
--   supersedes_operation_id   この操作が置き換える古い操作（本番反映のやり直しで作った新しい操作が持つ）
-- operation.state の閉集合に closed を足すが、0004 の流儀どおり CHECK 制約は持たず core 層で検査する。
--
-- approval（承認）に足す列:
--   merge_in_verified_head    release を、検証の head から「既定ブランチを取り込んだだけ」と判定して承認したときの
--                             検証の head。それ以外の承認は NULL
--   superseded_at             完了の承認が「本番反映のやり直しで置き換えられた」日時
--   superseded_reason         置き換えた理由（core の定数で検査する閉集合: not_mergeable / limit_reached / content_changed）
--   superseded_by_approval_id 置き換えた次の完了の承認
--
-- challenge_reopen: 完了から着手中へ戻した記録（本番反映でマージできなくなった場合に限る）。
-- どの列も NULL 可で、既存の行は NULL のままにする。

ALTER TABLE operation ADD COLUMN closed_reason TEXT;
ALTER TABLE operation ADD COLUMN closed_at TEXT;
ALTER TABLE operation ADD COLUMN supersedes_operation_id INTEGER REFERENCES operation (id);

ALTER TABLE approval ADD COLUMN merge_in_verified_head TEXT;
ALTER TABLE approval ADD COLUMN superseded_at TEXT;
ALTER TABLE approval ADD COLUMN superseded_reason TEXT;
ALTER TABLE approval ADD COLUMN superseded_by_approval_id INTEGER REFERENCES approval (id);

CREATE TABLE challenge_reopen (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    challenge_id           INTEGER NOT NULL REFERENCES challenge (id),
    reason                 TEXT    NOT NULL,
    operation_id           INTEGER NOT NULL REFERENCES operation (id),
    new_operation_id       INTEGER NOT NULL REFERENCES operation (id),
    superseded_approval_id INTEGER REFERENCES approval (id),
    created_at             TEXT    NOT NULL
);

CREATE INDEX idx_challenge_reopen_challenge_id ON challenge_reopen (challenge_id);

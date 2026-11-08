-- 0015_completion_direct: スキーマ版 15。
-- 課題 #481（親が直接した完了の差し戻しを、着手中ではなく「親が直接やる」保留へ戻す）。
--
-- challenge に completion_direct を足す。「いまの完了確認待ちが、親が直接行った完了（T17）で入ったものか」の真偽
-- （0 か 1。値は core が遷移のたびに書く）:
--   T17 で完了確認待ちへ入ったとき 1、ほかのすべての遷移で 0（T8 で入ったときも 0。完了確認待ちを出るときに 0 へ戻る）。
-- 以前は作業ログの最後の complete_direct と verify_met を読んで判定していた。その値を持たせる。
--
-- 既存の行の埋め戻し: 状態が完了確認待ちで、作業ログの最後（complete_direct と verify_met のうち）が
-- complete_direct の課題だけを 1 にする（従来の判定と結果が変わらない）。ほかは既定値の 0 のまま。

ALTER TABLE challenge ADD COLUMN completion_direct INTEGER NOT NULL DEFAULT 0;

UPDATE challenge SET completion_direct = 1
 WHERE status = 'awaiting_completion_approval'
   AND (SELECT a.action FROM activity a
         WHERE a.entity = 'challenge' AND a.entity_id = challenge.id AND a.action IN ('complete_direct', 'verify_met')
         ORDER BY a.id DESC LIMIT 1) = 'complete_direct';

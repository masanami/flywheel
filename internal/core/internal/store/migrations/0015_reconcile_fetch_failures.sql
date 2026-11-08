-- 0015_reconcile_fetch_failures: スキーマ版 15。
-- 課題 #487（完了の照合で、GitHub からの取得の失敗と「見つからない」を分ける）。
--
-- challenge に、完了の照合（成果物の種類 pr）の取得の失敗の連続回数（reconcile_fetch_failures）と、
-- その照合で調べたブランチ（reconcile_fetch_branch。次の周のやり直しで同じブランチを調べるため。
-- 調べるブランチが無かったときは NULL）を足す。
-- 回数は「成果物あり」か「見つからない」の判定が出たら 0 に戻り、ブランチも NULL に戻る（core が書く）。
-- 既存の行は 0・NULL で埋まる。

ALTER TABLE challenge ADD COLUMN reconcile_fetch_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE challenge ADD COLUMN reconcile_fetch_branch TEXT;

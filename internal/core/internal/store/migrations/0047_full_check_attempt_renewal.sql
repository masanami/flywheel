-- 反映の試行の生存を更新した時刻。上げる前のフルの検査を実行している間、止まった試行とみなす経過時間をここから数える
-- （NULL は更新していないこと。started_at から数える）。過去の行は書き換えない。
ALTER TABLE operation_execution ADD COLUMN renewed_at TEXT;

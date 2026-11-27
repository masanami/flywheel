-- 0046_contract_run_definition: スキーマ版 46。
-- フローのカスタマイズ S1。委譲の実行（contract_run）に、その実行が動いたフローの定義の出どころ（definition_source。
-- workspace＝ワークスペースに置いた定義・builtin＝組み込み）と、定義の中身のハッシュ（definition_sha256。16 進の sha256）を足す。
-- 契約の出力の任意の欄 definition を写す先で、出力に無ければ NULL のまま。値の閉集合は core の定数で検査する。
-- 過去の行は書き換えない（既存の contract_run の 2 列は NULL）。

ALTER TABLE contract_run ADD COLUMN definition_source TEXT;
ALTER TABLE contract_run ADD COLUMN definition_sha256 TEXT;

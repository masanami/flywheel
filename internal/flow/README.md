# internal/flow

claude-harness の「フロー（YAML）と、それを進める仕組み」を flywheel へ写したもの。

## 写した元

- リポジトリ: [masanami/claude-harness](https://github.com/masanami/claude-harness)
- コミット: `b4d6154`（main）
- 範囲: そのコミットの `runtime/`

## assets/

`runtime/workflows/` の `ticket.yaml`・`reflect.yaml` と、その 2 つが使う `prompts/`・`schemas/`・`scripts/` のファイルを、中身を変えずに `assets/files/workflows/` へ写して `go:embed` したもの。`conflict-predict-issue.yaml`・`list-tests.yaml` とその 2 つだけが使うファイルは写していない。プラグインのスクリプト・エージェント定義も埋め込まない。文書の `prompts/README.md` も写していない。

- `assets/SHA256SUMS`: 写した元のファイルごとの SHA-256（`assets/files/` からのパス）。試験が埋め込んだ資材と過不足なく突き合わせる。資材のファイルを足す・消す・直すときは、この一覧も更新する。
- 展開先は `<ワークスペース>/.flywheel/flow/runtime/<版>/`（版は flywheel のバイナリのコミット）。置き場は `--workspace` の値だけから決め、環境変数は読まない。中身の印（`.bundle-sha256`）で確かめ、違えば上書きしない。

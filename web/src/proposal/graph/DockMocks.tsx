// 欄の中身の 2 案の見た目だけを作る偽の中身（2026-10-14 オーナー: どちらのパターンも見たい）。本物の接続はしない。
// ターミナルは CLI の画面がそのまま流れる形、チャットは Claude デスクトップ風の豊かな表示。依存は足さず手で書く。

// 端末風の固定の出力。`claude` を動かしている途中の画面を模す。
export function TerminalMock({ ws }: { ws: string }) {
  return (
    <div className="term" aria-label={`${ws} のターミナル（偽の出力）`}>
      <pre>
        <span className="term-prompt">masami@flywheel ~/work/{ws} %</span>{" "}
        claude
        {"\n"}
        <span className="term-dim">
          {"╭────────────────────────────────\n│ "}
        </span>
        <span className="term-accent">✻</span>
        {" Welcome to Claude Code!\n"}
        <span className="term-dim">
          {"│   /help for help, /status for setup\n│   cwd: "}
        </span>
        {`/mock/${ws}\n`}
        <span className="term-dim">
          {"╰────────────────────────────────\n"}
        </span>
        {"\n"}
        <span className="term-user">
          {"> C-5 のビルド時間を計って、遅い段を教えて"}
        </span>
        {"\n\n"}
        <span className="term-accent">⏺</span>
        {" Bash(make build PROFILE=1)\n"}
        <span className="term-dim">{"  ⎿  "}</span>
        {"compile   1m48s\n       bundle    2m02s\n       test      0m22s\n\n"}
        <span className="term-accent">⏺</span>
        {
          " bundle の段が全体の半分を占めています。依存のキャッシュ（C-16）が\n  入れば 1 分ほど縮む見込みです。\n\n"
        }
        <span className="term-user">{"> "}</span>
        <span className="term-cursor">█</span>
        {"\n"}
        <span className="term-dim">
          {"  ? for shortcuts                        ⏵⏵ auto mode"}
        </span>
      </pre>
    </div>
  );
}

const noop = () => {};

// Claude デスクトップ風のチャット。本文の装飾・ツール呼び出しのカード（畳める）・差分・実行の許可・入力欄を見せる。
export function ChatMock({ ws }: { ws: string }) {
  return (
    <div className="chat" aria-label={`${ws} とのチャット（偽の中身）`}>
      <div className="chat-scroll">
        <p className="chat-to">
          宛先: <b>{ws}</b> のエージェント（試作の偽の中身）
        </p>
        <div className="chat-msg chat-user">
          C-17 のキャッシュキーの案を 2 つ出して、比べてください。
        </div>
        <div className="chat-msg chat-assistant">
          <details className="chat-tool">
            <summary>
              <span className="chat-tool-name">Read</span>
              <code>build/cache.ts</code>
              <span className="chat-tool-meta">42 行</span>
            </summary>
            <pre>
              {
                "export function cacheKey(lock: string) {\n  return sha256(lock);\n}"
              }
            </pre>
          </details>
          <details className="chat-tool">
            <summary>
              <span className="chat-tool-name">Grep</span>
              <code>cacheKey</code>
              <span className="chat-tool-meta">3 件</span>
            </summary>
            <pre>
              {"build/cache.ts:12\nbuild/restore.ts:8\nscripts/warm.ts:21"}
            </pre>
          </details>
          <h4>キャッシュキーの 2 案</h4>
          <p>
            いまのキーはロックファイルだけから作っているため、実行環境の版が変わっても古いキャッシュを使ってしまいます。
          </p>
          <ol>
            <li>
              <b>ロックファイル＋実行環境の版</b>:
              変わったときだけ作り直す。キーが安定し、当たりやすい
            </li>
            <li>
              <b>ロックファイル＋日付</b>:
              毎日作り直す。古いものが残らないが、朝の最初の 1 回が遅い
            </li>
          </ol>
          <pre className="chat-code">
            <code>{"key = sha256(lockfile) + ':' + runtimeVersion"}</code>
          </pre>
          <p>1 の案で書き換えると、差分は次のとおりです。</p>
          <div className="chat-diff">
            <div className="chat-diff-head">
              <code>build/cache.ts</code>
              <span className="chat-diff-add">+2</span>
              <span className="chat-diff-del">−1</span>
            </div>
            <pre>
              <span className="chat-diff-ctx">
                {" export function cacheKey(lock: string) {\n"}
              </span>
              <span className="chat-diff-line-del">
                {"-  return sha256(lock);\n"}
              </span>
              <span className="chat-diff-line-add">
                {"+  const v = process.version;\n"}
              </span>
              <span className="chat-diff-line-add">
                {"+  return `${sha256(lock)}:${v}`;\n"}
              </span>
              <span className="chat-diff-ctx">{" }"}</span>
            </pre>
          </div>
          <section className="chat-permit" aria-label="実行の許可（案）">
            <p>
              コマンドの実行を許可しますか？
              <code>make build PROFILE=1</code>
            </p>
            <div className="chat-permit-actions">
              <button
                type="button"
                className="chat-allow"
                onClick={noop}
                title="試作では押しても何も起きない"
              >
                許可
              </button>
              <button
                type="button"
                onClick={noop}
                title="試作では押しても何も起きない"
              >
                拒否
              </button>
            </div>
          </section>
        </div>
      </div>
      <div className="chat-composer">
        <textarea
          aria-label={`${ws} へのメッセージ`}
          placeholder={`${ws} に返信…（試作では送れない）`}
          rows={2}
        />
        <button type="button" disabled>
          送信
        </button>
      </div>
    </div>
  );
}

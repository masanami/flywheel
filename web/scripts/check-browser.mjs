// make e2e の前に、Playwright のブラウザが用意されているかを確かめる。無ければ、何をすればよいかを出して終了コード 1 で落ちる。
// PLAYWRIGHT_CHANNEL（chrome 等）を使う環境では、ブラウザの実体を Playwright 自身が探すので、ここでは確かめない。
import { existsSync } from "node:fs";
import { chromium } from "@playwright/test";

if (process.env.PLAYWRIGHT_CHANNEL) {
  process.exit(0);
}
const path = chromium.executablePath();
if (!path || !existsSync(path)) {
  console.error(
    [
      "Playwright の Chromium が用意されていません（make e2e はブラウザで画面を操作します）。",
      "  次のどちらかを実行してから、もう一度 make e2e を実行してください。",
      "    cd web && npx playwright install chromium            # 同梱の Chromium を落とす（Linux で依存が足りないときは --with-deps）",
      "    PLAYWRIGHT_CHANNEL=chrome make e2e                    # 落とせない環境では、手元の Google Chrome を使う",
      `  探した場所: ${path}`,
    ].join("\n"),
  );
  process.exit(1);
}

import { defineConfig, devices } from "@playwright/test";

// ブラウザの E2E（flywheel#176 の関係グラフ）。偽の API（`vite --mode mock`）の上で画面を操作する。
// make check には入れず、`make e2e`（または `npm run e2e`）で流す。初回は `npx playwright install chromium` が要る。
const PORT = 5199;

export default defineConfig({
  testDir: "e2e",
  fullyParallel: true,
  // CI で test.only が残っていたら失敗にする（1 件だけ流して通ったことにしない）
  forbidOnly: !!process.env.CI,
  reporter: [["list"]],
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: "retain-on-failure",
  },
  // 同梱の Chromium を落とせない環境では、PLAYWRIGHT_CHANNEL=chrome で手元の Google Chrome を使う。
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        channel: process.env.PLAYWRIGHT_CHANNEL || undefined,
      },
    },
  ],
  webServer: {
    command: `npx vite --mode mock --port ${PORT} --strictPort`,
    url: `http://127.0.0.1:${PORT}`,
    reuseExistingServer: false,
    timeout: 60_000,
  },
});

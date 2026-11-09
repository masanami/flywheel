import { defineConfig, devices } from "@playwright/test";

// 本物の `flywheel serve` に向けたブラウザの E2E（flywheel#463。関係グラフ）。偽の API の E2E
// （playwright.config.ts・e2e/）とは設定も置き場も分け、`make e2e-real`（ui-build と build の後）で流す。
// make check と `make e2e` には入らない。準備の段（e2e-real/global-setup.ts）が一時ディレクトリに
// HOME・XDG_CONFIG_HOME・作業ディレクトリ・ワークスペースを作り、Go のテスト（TestRelationGraphSeed）で
// 種のストアを作ってから、build/flywheel を `serve --port 0 --workspace <一時のワークスペース>` で起動する。
// 委譲の子の環境（FLYWHEEL_DELEGATED_RUN がある）では serve が拒否されて失敗する（想定どおり。CI で確かめる）。
export default defineConfig({
  testDir: "e2e-real",
  // 1 つの serve と 1 つの種を共有する（試験は種を書き換えない）
  fullyParallel: false,
  workers: 1,
  reporter: [["list"]],
  globalSetup: "./e2e-real/global-setup.ts",
  use: {
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        channel: process.env.PLAYWRIGHT_CHANNEL || undefined,
      },
    },
  ],
});

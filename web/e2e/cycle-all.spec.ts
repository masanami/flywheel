import { type Page, expect, test } from "@playwright/test";

// サイクルの画面と「すべて」の画面を、偽のデータ（mock モード。alpha・beta・読めない gamma）で確かめる。#806

test.use({ viewport: { width: 360, height: 740 } });

// ページ全体が横にスクロールしない（文書の幅が表示の幅を超えない）。
async function noHorizontalScroll(page: Page) {
  const m = await page.evaluate(() => ({
    doc: document.documentElement.scrollWidth,
    docClient: document.documentElement.clientWidth,
    body: document.body.scrollWidth,
    inner: window.innerWidth,
    // 本文の欄の中で横にあふれていないか（本文が自前で横スクロールを持つ形で隠さない）
    main: (() => {
      const el = document.querySelector(".shell-body");
      return el ? el.scrollWidth - el.clientWidth : 0;
    })(),
  }));
  expect(m.main).toBeLessThanOrEqual(0);
  expect(m.doc).toBeLessThanOrEqual(m.docClient);
  expect(m.body).toBeLessThanOrEqual(m.inner);
}

test("AC-437 #806: 幅 360px で、サイクルの画面は、ページ全体が横にスクロールしない", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/cycle");
  await expect(
    page.getByRole("heading", { name: "サイクル", exact: true }),
  ).toBeVisible();
  await expect(page.getByTestId("cycle-running-started")).toBeVisible();
  await expect(page.getByTestId("cycle-work-total")).toHaveText("3");
  await noHorizontalScroll(page);
});

test("#1183: 走っているサイクルで「サイクルを止める」を押すと、止める指示を出した旨に変わる（偽のデータ）", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/cycle");
  await expect(page.getByTestId("cycle-watching-row").first()).toBeVisible();
  await page.getByRole("button", { name: "サイクルを止める" }).click();
  await expect(page.getByTestId("cycle-stop-requested")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "サイクルを止める" }),
  ).toHaveCount(0);
});

test("AC-438 #806: 幅 360px で、「すべて」の画面は、ページ全体が横にスクロールしない", async ({
  page,
}) => {
  await page.goto("/#/all");
  await expect(
    page.getByRole("heading", { name: "すべてのエージェント" }),
  ).toBeVisible();
  await expect(page.getByTestId("all-agent-row")).toHaveCount(3);
  await expect(page.getByTestId("all-agent-unreadable")).toHaveText(
    "読めません: ストアが見つかりません",
  );
  await noHorizontalScroll(page);
});

// 以下は幅を既定に戻し、サイクルの画面・「すべて」の画面の中身を偽のデータで確かめる。

test.describe("中身（既定の幅）", () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test("AC-376〜381 #806: サイクルの画面に見出し・時刻・結果・合計・ボタンが出て、止めるボタンは無い", async ({
    page,
  }) => {
    await page.goto("/#/a/beta/cycle");
    await expect(
      page.getByRole("heading", { name: "サイクル", exact: true }),
    ).toBeVisible();
    await expect(page.getByTestId("cycle-screen-state")).toHaveText(
      "走っていない",
    );
    await expect(page.getByTestId("cycle-last-started")).not.toHaveText(
      "まだありません",
    );
    await expect(page.getByTestId("cycle-last-ended")).not.toHaveText(
      "まだありません",
    );
    await expect(page.getByTestId("cycle-last-result")).toHaveText(
      "異常なく終わった",
    );
    await expect(page.getByTestId("cycle-work-total")).toHaveText("2");
    await expect(
      page.getByRole("button", { name: "サイクルを回す" }),
    ).toBeEnabled();
    // 対話の欄（#1002）のボタンは数えない。
    await expect(
      page.locator('button:not([data-testid="chat-panel"] *)'),
    ).toHaveCount(1);
  });

  test("AC-382〜387 #806: 「すべて」に見出し・名前つきの待っているもの・エージェントの区分・読めない理由が出て、名前から「いま進められること」へ移れる", async ({
    page,
  }) => {
    await page.goto("/#/all");
    await expect(
      page.getByRole("heading", { name: "すべてのエージェント" }),
    ).toBeVisible();
    const waiting = page.getByTestId("all-waiting-row");
    await expect(waiting.first()).toBeVisible();
    const hrefs = await waiting
      .locator("a")
      .evaluateAll((as) => as.map((a) => a.getAttribute("href")));
    expect(hrefs.length).toBeGreaterThan(0);
    for (const h of hrefs) expect(h).toMatch(/^#\/a\/[^/]+\//);
    const rows = page.getByTestId("all-agent-row");
    await expect(rows).toHaveCount(3);
    await expect(rows.nth(0).getByTestId("all-agent-cycle")).toContainText(
      "サイクル:",
    );
    await expect(rows.nth(0).getByTestId("all-agent-waiting")).toContainText(
      "あなたを待っている",
    );
    await expect(page.getByTestId("all-agent-unreadable")).toHaveText(
      "読めません: ストアが見つかりません",
    );
    await rows.nth(1).getByRole("link", { name: "beta" }).click();
    await expect(page).toHaveURL(/#\/a\/beta\/now$/);
    await expect(
      page.getByRole("heading", { name: "いま進められること" }),
    ).toBeVisible();
  });
});

test("#1183: 走っているサイクルに、見ているタスク・構造の図・スーパーバイザーの記録・費用の内訳が出る（偽のデータ）", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/cycle");
  await expect(page.getByTestId("cycle-running-cost")).toContainText("上限");
  await expect(page.getByTestId("cycle-watching-row")).toHaveCount(2);
  await expect(page.getByTestId("cycle-structure")).toBeVisible();
  await expect(page.getByTestId("cycle-waiting")).toBeVisible();
  await expect(page.getByTestId("cycle-supervisor-row")).toHaveCount(2);
  await expect(page.getByTestId("cycle-cost-row")).toHaveCount(2);
});

test("#1183: 走っていないエージェントでは、止めるボタンが出ず、最後のサイクルの終わりが出る（偽のデータ）", async ({
  page,
}) => {
  await page.goto("/#/a/beta/cycle");
  await expect(page.getByTestId("cycle-last-ended")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "サイクルを止める" }),
  ).toHaveCount(0);
});

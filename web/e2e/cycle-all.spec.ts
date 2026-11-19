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
  await expect(page.getByRole("heading", { name: "サイクル" })).toBeVisible();
  await expect(page.getByTestId("cycle-last-started")).toBeVisible();
  await expect(page.getByTestId("cycle-work-total")).toHaveText("3");
  await noHorizontalScroll(page);
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

import { expect, test } from "@playwright/test";
import { BASE_URL_ENV } from "./global-setup";

// 本物の `flywheel serve` と種のストアで、枠が開くことだけを確かめる最小の筋書き（作り替えは後の課題）。#803
test.use({ baseURL: process.env[BASE_URL_ENV] });

test("本物の serve で枠が開く（左の列・上の帯・本文）", async ({ page }) => {
  await page.goto("/");
  await expect(
    page
      .getByRole("navigation", { name: "エージェント" })
      .getByRole("link", { name: /seed/ }),
  ).toBeVisible();
  await expect(page.getByRole("navigation", { name: "画面" })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "いま進められること" }),
  ).toBeVisible();
});

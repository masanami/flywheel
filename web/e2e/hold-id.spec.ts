import { expect, test } from "@playwright/test";

// 区切り S4c-3（#1171）: 保留の ID（H-<番号>）を、偽のデータ（mock モード）で確かめる。
// AC-125: 課題の詳細の待ちに保留の ID が出る。AC-127: タスクの詳細に保留の ID が出る。
// AC-126（開いている保留が 2 つのときの --hold つきの案内）は、偽のデータに 2 つ開いた課題が無いので vitest（detail.test.tsx・task.test.tsx・guidance.test.ts）が正本。

test("AC-125 S4c-3: 課題の詳細の「あなたを待っているもの」に、開いている保留の ID が出る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/challenges/C-8");
  const waiting = page.getByTestId("detail-waiting");
  await expect(waiting).toBeVisible();
  await expect(waiting.getByTestId("hold-id")).toContainText("H-1");
  // 開いている保留が 1 つなので、案内に --hold は無い。
  await expect(waiting).not.toContainText("--hold");
});

test("AC-127 S4c-3: タスクの詳細に、答え待ちの保留と過去の保留の ID が出る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/challenges/C-28/tasks/T-44");
  await expect(page.getByTestId("task-hold-id")).toContainText("H-44");
  await expect(page.getByTestId("task-hold-listed-id")).toContainText("H-44");
  await page.goto("/#/a/alpha/challenges/C-28/tasks/T-43");
  await expect(page.getByTestId("task-hold-listed-id")).toContainText("H-43");
});

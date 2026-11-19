import { expect, it } from "vitest";
import { safeHref } from "./safeHref.ts";

// #814 掃除 4: http(s) だけをリンクにする判定は、画面の中で 1 つ。
it("#814: safeHref は http:・https: だけを返し、それ以外・読めない値・空は null", () => {
  expect(safeHref("https://example.com/a")).toBe("https://example.com/a");
  expect(safeHref("http://example.com")).toBe("http://example.com");
  for (const bad of [
    "javascript:alert(1)",
    "data:text/html,x",
    "ftp://x",
    "/relative",
    "",
    "not a url",
  ])
    expect(safeHref(bad), bad).toBeNull();
  expect(safeHref(null)).toBeNull();
  expect(safeHref(undefined)).toBeNull();
});

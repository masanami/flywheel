import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { App } from "../App.tsx";
import { resetApi } from "../api/client.ts";
import { screens } from "../screens.tsx";
import { installMock } from "./install.ts";

afterEach(() => resetApi());

// mock モードの入れ方（main.tsx と同じ）で、追加した画面がすべて開く。
it("mock モード: 偽の API で全画面を開ける", async () => {
  const before = screens.length;
  installMock();
  expect(screens.length).toBeGreaterThan(before);
  render(<App />);
  await waitFor(() => expect(screen.getByText(/接続中/)).toBeInTheDocument());
  for (const s of screens) {
    fireEvent.click(screen.getByRole("button", { name: s.title }));
    await waitFor(() =>
      expect(screen.getByRole("region", { name: s.title })).toBeInTheDocument(),
    );
  }
  // 関係グラフ（案）の既定はグラフで、列のボードへ切り替えると人間の操作待ちの帯がある
  fireEvent.click(screen.getByRole("button", { name: "関係グラフ（案）" }));
  expect(
    await screen.findByRole("group", { name: "alpha の課題の関係グラフ" }),
  ).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "列のボード" }));
  expect(
    await screen.findByRole("region", { name: "人間の操作待ち（案）" }),
  ).toBeInTheDocument();
});

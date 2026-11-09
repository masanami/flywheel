import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { App } from "../App.tsx";
import { resetApi } from "../api/client.ts";
import { installMock } from "./install.ts";

afterEach(() => resetApi());

// mock モードの入れ方（main.tsx と同じ）で、本番の画面が偽の API で開く。
// 既定はグラフで、ボードへ切り替えると人間の操作待ちの帯がある。
it("mock モード: 偽の API で関係グラフとボードを開ける", async () => {
  installMock();
  render(<App />);
  await waitFor(() => expect(screen.getByText(/接続中/)).toBeInTheDocument());
  expect(
    await screen.findByRole("group", { name: "alpha の課題の関係グラフ" }),
  ).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "ボード" }));
  expect(
    await screen.findByRole("region", { name: "人間待ちの帯" }),
  ).toBeInTheDocument();
});

// `?mock=large` では、大きなまとまりの偽のデータ（課題 100 件。flywheel#470）に差し替わる。
it("mock モード: ?mock=large で大きなまとまりのワークスペースを開ける", async () => {
  installMock("?mock=large&latency=0");
  render(<App />);
  expect(
    await screen.findByRole("group", { name: "large の課題の関係グラフ" }),
  ).toBeInTheDocument();
  expect(
    screen.getByRole("option", { name: "large-flat" }),
  ).toBeInTheDocument();
});

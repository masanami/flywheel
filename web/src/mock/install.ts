import { setApi } from "../api/client.ts";
import { createMockApi } from "./api.ts";
import { createMockEventSource } from "./events.ts";
import { MockStore, seed, seedLarge } from "./store.ts";

// 開発時の mock モード（`npm run dev:mock`）だけが呼ぶ。偽の API・偽の EventSource を入れる。
// URL に `?mock=large` を付けると、大きなまとまりの偽のデータ（課題 100 件。flywheel#470）に差し替える。
// `latency=<ミリ秒>` で偽の API の待ち時間を変える（描画の速さを測るときに 0 にする）。
export function installMock(search: string = window.location.search): void {
  const q = new URLSearchParams(search);
  const store = new MockStore(q.get("mock") === "large" ? seedLarge() : seed());
  const latency = Number(q.get("latency"));
  setApi(
    createMockApi(
      store,
      q.has("latency") && Number.isFinite(latency)
        ? { latencyMs: latency }
        : {},
    ),
  );
  window.EventSource = createMockEventSource(store);
}

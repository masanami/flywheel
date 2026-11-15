import { setApi } from "../api/client.ts";
import { createMockApi } from "./api.ts";
import { createMockEventSource } from "./events.ts";
import { MockStore, seed, seedLarge, seedParentDirect } from "./store.ts";

// 開発時の mock モード（`npm run dev:mock`）だけが呼ぶ。偽の API・偽の EventSource を入れる。
// URL に `?mock=large` を付けると、大きなまとまりの偽のデータ（課題 100 件。flywheel#470）に差し替える。
// `?mock=parent-direct` は、課題 C-8 が「親が直接行う」保留の例（flywheel#502）。
// `latency=<ミリ秒>` で偽の API の待ち時間を変える（描画の速さを測るときに 0 にする）。
export function installMock(search: string = window.location.search): void {
  const q = new URLSearchParams(search);
  const mode = q.get("mock");
  const store = new MockStore(
    mode === "large"
      ? seedLarge()
      : mode === "parent-direct"
        ? seedParentDirect()
        : seed(),
  );
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

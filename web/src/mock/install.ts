import { setApi } from "../api/client.ts";
import { createMockApi } from "./api.ts";
import { createMockEventSource } from "./events.ts";
import { MockStore } from "./store.ts";

// 開発時の mock モード（`npm run dev:mock`）だけが呼ぶ。偽の API・偽の EventSource を入れる。
export function installMock(): void {
  const store = new MockStore();
  setApi(createMockApi(store));
  window.EventSource = createMockEventSource(store);
}

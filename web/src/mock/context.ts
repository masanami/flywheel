// 偽の API の口ごとのファイルが共有する、偽のサーバの状態への入口（口ごとのファイルは api.ts から、これを受け取って組み立てる）。

import type { MockChallenge, MockStore, MockWorkspace } from "./store.ts";

export interface MockContext {
  store: MockStore;
  /** 偽の API の待ち時間（0 なら待たない）。 */
  wait: () => Promise<void> | undefined;
  /** エージェントの偽の状態。無い・開けないときは本物と同じ ApiError を投げる。 */
  wsOf: (name: string) => MockWorkspace;
  /** 課題の偽の状態。無ければ not_found の ApiError を投げる。 */
  itemOf: (name: string, id: string) => MockChallenge;
}

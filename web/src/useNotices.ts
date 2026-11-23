import { api } from "./api/client.ts";
import type { Notice } from "./api/types.gen.ts";
import type { LiveState } from "./live/useLive.ts";
import { useScreenData } from "./shell/useScreenData.ts";

// 知らせの口を読む。変化の通し番号が進むたびに読み直す（useScreenData の決まり）。読めなければ 0 件として扱う。
export function useNotices(live: LiveState, agentName: string): Notice[] {
  const { result } = useScreenData(live, agentName, () =>
    api.notices(agentName),
  );
  return result.state === "ready" ? result.data.notices : [];
}

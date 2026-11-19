import { useLive } from "./live/useLive.ts";
import { Shell } from "./shell/Shell.tsx";

export function App() {
  const live = useLive();
  return <Shell live={live} />;
}

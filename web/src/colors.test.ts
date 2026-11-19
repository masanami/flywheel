import { readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, it } from "vitest";
import { TASK_PHASES } from "./labels.ts";

// AC-458 #814: 明るい配色と暗い配色のどちらでも、タスクの表示の区分の色を、目で見分けられる。
// 目で見て確かめるのはオーケストレーターとオーナー。ここでは見分けの前提（6 つの区分の背景色が、
// 配色ごとに互いに十分に離れている・どの区分も色の規則を持つ）を、styles.css から機械で確かめる。
const css = readFileSync(join(import.meta.dirname, "styles.css"), "utf8");
const DARK_AT = css.indexOf("@media (prefers-color-scheme: dark)");

function colorOf(scope: string, phase: string): [number, number, number] {
  const m = new RegExp(`--phase-${phase}-bg:\\s*#([0-9a-fA-F]{6})`).exec(scope);
  if (!m?.[1]) throw new Error(`--phase-${phase}-bg が見つからない`);
  const n = Number.parseInt(m[1], 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}
const dist = (a: number[], b: number[]) =>
  Math.hypot(...a.map((v, i) => v - (b[i] ?? 0)));

// RGB の距離の下限。最も近い組は明るい配色の blocked と done（約 17.7。灰色と淡い緑）なので、それより下に置く。
// 近い組は、色だけでなく区分の語（PHASE_LABELS）も箱の中に出る。見分けられるかの判定は目で行う（要人間判定）。
const MIN_DISTANCE = 15;

it("AC-458 #814: 明るい配色でも暗い配色でも、6 つの表示の区分の背景色は互いに MIN_DISTANCE 以上離れている", () => {
  expect(DARK_AT).toBeGreaterThan(0);
  expect(TASK_PHASES).toHaveLength(6);
  const schemes = {
    light: css.slice(0, DARK_AT),
    dark: css.slice(DARK_AT),
  };
  for (const [name, scope] of Object.entries(schemes)) {
    for (const a of TASK_PHASES) {
      for (const b of TASK_PHASES) {
        if (a >= b) continue;
        expect(
          dist(colorOf(scope, a), colorOf(scope, b)),
          `${name}: ${a} と ${b}`,
        ).toBeGreaterThanOrEqual(MIN_DISTANCE);
      }
    }
  }
});

it("AC-458 #814: どの表示の区分も、箱と印（pill）の色の規則を持つ", () => {
  for (const p of TASK_PHASES) {
    expect(css, p).toContain(`.task-box[data-phase="${p}"]`);
    expect(css, p).toContain(`.phase-pill[data-phase="${p}"]`);
  }
});

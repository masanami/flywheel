// 外部由来の行き先は http:・https: だけをリンクにする。それ以外（相対・javascript:・data: など）と、
// URL として読めない値は null（行き先を持たない）。画面の中でこの判定は、ここの 1 つだけ。
export function safeHref(url: string | null | undefined): string | null {
  if (!url) return null;
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:" ? url : null;
  } catch {
    return null;
  }
}

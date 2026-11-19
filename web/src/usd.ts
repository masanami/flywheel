// 費用は小数 2 桁までで、末尾の 0 を落とす（30 は「30」、11.2 は「11.2」）。
export const usd = (n: number): string => String(Number(n.toFixed(2)));

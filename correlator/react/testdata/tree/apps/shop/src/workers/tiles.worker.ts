import { RATES } from "@fixture/money/rates";

export function draw(n: number): number {
  return n * (RATES.EUR ?? 1);
}

self.onmessage = (e: MessageEvent<number>) => self.postMessage(draw(e.data));

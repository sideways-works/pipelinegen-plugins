export function startTiles(): Worker {
  return new Worker(new URL("./workers/tiles.worker.ts", import.meta.url), { type: "module" });
}

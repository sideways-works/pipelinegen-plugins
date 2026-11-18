// A module whose top level runs at load: a table, and a registration.
export const TABLE: Record<string, number> = { a: 1 };

(globalThis as { registered?: boolean }).registered = true;

export function lookup(key: string): number {
  return TABLE[key] ?? 0;
}

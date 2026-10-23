// Kept apart from the linker so describe loads none of it.

export const narrowings = [
  {
    name: "ts-functions",
    off_at: 4,
    note: "a unit binds the functions it ran, not every unit of its file; coverage sees every function that runs, so what it leaves out did not run in the recording",
  },
  {
    name: "types-erased",
    off_at: 5,
    note: "type annotations, interfaces and type aliases bind no browser or Node test: the build erases them before anything runs; the type check runs on its own",
  },
] as const;

export type NarrowingName = (typeof narrowings)[number]["name"];

// A page no spec opens, loaded on demand like the about page.
import topic from "./legacy-help.cjs";

export function showHelp(): void {
  document.title = topic();
}

import { aboutWords } from "./about-words";
import legacy from "./legacy.cjs";

export function showAbout(): void {
  const p = document.createElement("p");
  p.id = "about-text";
  p.textContent = aboutWords();
  p.dataset.legacy = legacy.tag;
  document.body.append(p);
}

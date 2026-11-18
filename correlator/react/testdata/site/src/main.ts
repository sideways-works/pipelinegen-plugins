import { addToCart, PRICES } from "./cart";
import { fmt } from "./format";
import { desktopLayout, phoneLayout } from "./layout";
import { lookup } from "./registry";

if (window.innerWidth < 600) phoneLayout();
else desktopLayout();

const button = document.createElement("button");
button.id = "add";
button.textContent = "Add";
button.addEventListener("click", () => {
  document.title = String(addToCart("tea", PRICES.tea));
});
document.body.append(button);

const about = document.createElement("a");
about.id = "about";
about.textContent = "About";
about.addEventListener("click", async () => {
  const { showAbout } = await import("./pages/about");
  showAbout();
});
document.body.append(about);

// a control no spec uses: what it calls is loaded and never run
const never = document.createElement("button");
never.id = "never";
never.addEventListener("click", () => {
  document.title = fmt(lookup("a"));
});
document.body.append(never);

// a page no spec opens, loaded on demand
const help = document.createElement("a");
help.id = "help";
help.addEventListener("click", async () => {
  const { showHelp } = await import("./pages/help");
  showHelp();
});
document.body.append(help);

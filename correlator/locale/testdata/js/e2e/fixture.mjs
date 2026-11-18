// The site the specs open, served through Playwright's routing: no server,
// no network. /app calls the recorder's hook as a recording build does;
// /plain is the same app built without the flag.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { test as base } from "@playwright/test";
import { withLocaleKeys } from "../recorder/playwright.mjs";

const root = resolve(import.meta.dirname, "..");
const i18next = readFileSync(resolve(root, "node_modules/i18next/dist/esm/i18next.js"), "utf8");
const en = readFileSync(resolve(root, "app/locales/en/home.json"), "utf8");
const de = readFileSync(resolve(root, "app/locales/de/home.json"), "utf8");

const page = (flag) => `<!doctype html><body><p id="out"></p><script type="module">
import i18next from "/i18next.js";
const lng = new URLSearchParams(location.search).get("lng") ?? "en";
if (${flag}) globalThis.__pipelinegenLocales?.(i18next, "app/locales");
await i18next.init({ lng, fallbackLng: "en", ns: ["home"], defaultNS: "home", resources: { en: { home: ${en} }, de: { home: ${de} } } });
document.getElementById("out").textContent = [i18next.t("title"), i18next.t("cart.items", { count: 1 }), i18next.t("greeting")].join(" | ");
</script></body>`;

const site = base.extend({
  context: async ({ context }, use) => {
    await context.route("http://app.test/**", (route) => {
      const u = new URL(route.request().url());
      if (u.pathname === "/i18next.js") return route.fulfill({ contentType: "text/javascript", body: i18next });
      return route.fulfill({ contentType: "text/html", body: page(u.pathname === "/app") });
    });
    await use(context);
  },
});

export const test = withLocaleKeys(site, {
  out: process.env.LOCALE_OUT,
  root,
  expect: [/^http:\/\/app\.test\//],
});
export { expect } from "@playwright/test";

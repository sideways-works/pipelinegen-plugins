import i18next from "i18next";
import { expect, test } from "vitest";
import de from "./locales/de/home.json";
import en from "./locales/en/home.json";

test("German, English where German has nothing", async () => {
  const i18n = i18next.createInstance();
  await i18n.init({ lng: "de", fallbackLng: "en", ns: ["home"], defaultNS: "home", resources: { de: { home: de }, en: { home: en } } });
  expect(i18n.t("title")).toBe("Willkommen");
  expect(i18n.t("cart.items", { count: 2 })).toBe("2 Tickets");
  // de has no greeting: its context and plain forms miss, English answers, and nests title
  expect(i18n.t("greeting", { context: "male" })).toBe("Hello, sir");
  expect(i18n.t("greeting")).toBe("Hello, Willkommen");
  expect(i18n.t("steps", { returnObjects: true })).toEqual(["Datum wählen", "Bezahlen"]);
  expect(i18n.t("nothing.here")).toBe("nothing.here");
});

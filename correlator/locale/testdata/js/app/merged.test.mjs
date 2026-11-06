import i18next from "i18next";
import { expect, test } from "vitest";
import de from "./locales/de/home.json";

test("a bundle with a key of the test's own added can't be named", async () => {
  const i18n = i18next.createInstance();
  await i18n.init({ lng: "de", ns: ["home"], defaultNS: "home", resources: { de: { home: { ...de, extra: "x" } } } });
  expect(i18n.t("title")).toBe("Willkommen");
});

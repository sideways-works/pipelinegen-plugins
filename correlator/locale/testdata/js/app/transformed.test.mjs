import i18next from "i18next";
import { expect, test } from "vitest";
import de from "./locales/de/home.json";

// a build step that changes a catalog's values as it imports it, its keys
// left alone (soft hyphens put into long words)
const hyphenated = JSON.parse(JSON.stringify(de).replaceAll("Willkommen", "Will­kommen"));

test("a bundle whose values a build step changed is still its file's", async () => {
  const i18n = i18next.createInstance();
  await i18n.init({ lng: "de", ns: ["home"], defaultNS: "home", resources: { de: { home: hyphenated } } });
  expect(i18n.t("title")).toBe("Will­kommen");
});

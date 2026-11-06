import i18next from "i18next";
import { expect, test } from "vitest";

test("a bundle of the test's own, no catalog's", async () => {
  const i18n = i18next.createInstance();
  await i18n.init({ lng: "en", resources: { en: { translation: { hi: "Hi" } } } });
  expect(i18n.t("hi")).toBe("Hi");
});

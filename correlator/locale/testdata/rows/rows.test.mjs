// The lookups behind each row of the README's table, one test file per
// slice. locale-correlator's rows test holds the probes i18next makes for
// them; run with the vitest setup, this file's recording must equal those.
import i18next from "i18next";
import { expect, test } from "vitest";
import de from "./app/locales/de/home.json";
import en from "./app/locales/en/home.json";

const make = async (lng) => {
  const i = i18next.createInstance();
  await i.init({ lng, fallbackLng: "en", ns: ["home"], defaultNS: "home", resources: { de: { home: de }, en: { home: en } } });
  return i;
};

test(process.env.ROW, async () => {
  switch (process.env.ROW) {
    case "title-de":
      expect((await make("de")).t("title")).toBe("Willkommen");
      break;
    case "greeting-de":
      expect((await make("de")).t("greeting")).toBe("Hello");
      break;
    case "greeting-male-de":
      expect((await make("de")).t("greeting", { context: "male" })).toBe("Hello, sir");
      break;
    case "one-en":
      expect((await make("en")).t("cart.items", { count: 1 })).toBe("1 ticket");
      break;
    case "zero-en":
      expect((await make("en")).t("cart.items", { count: 0 })).toBe("0 tickets");
      break;
    case "title-fr":
      expect((await make("fr")).t("title")).toBe("Welcome");
      break;
    case "nothing":
      await make("en");
      break;
  }
});

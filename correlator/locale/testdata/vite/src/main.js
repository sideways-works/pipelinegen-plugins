import i18n from "i18next";

// a test build names this instance's catalog to the test recorder;
// production builds compile it out
if (import.meta.env.VITE_PIPELINEGEN_LOCALES) globalThis.__pipelinegenLocales?.(i18n, "app/locales");

await i18n.init({ lng: "en", resources: { en: { translation: { title: "Welcome" } } } });
document.getElementById("out").textContent = i18n.t("title");

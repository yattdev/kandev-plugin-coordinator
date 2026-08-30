import en from "../locales/en.json";
import fr from "../locales/fr.json";
import qpsPloc from "../locales/qps-ploc.json";

export const coordinatorCatalogs: Record<string, Record<string, string>> = {
  en,
  fr,
  "qps-ploc": qpsPloc,
};

export function localizedLabel(locale: string): string {
  const normalized = locale.toLowerCase();
  const catalog = normalized.startsWith("fr") ? fr : normalized === "qps-ploc" ? qpsPloc : en;
  return catalog["coordinator_label"];
}

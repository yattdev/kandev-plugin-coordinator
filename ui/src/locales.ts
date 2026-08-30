import en from "../locales/en.json";

export const coordinatorCatalogs: Record<string, Record<string, string>> = {
  en,
};

export function localizedLabel(): string {
  return en["coordinator_label"];
}

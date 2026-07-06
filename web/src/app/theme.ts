// Theme attribute owner (doc 08 §6.1): dark mode defaults from prefers-color-scheme,
// overridable via data-theme persisted in localStorage. Components never branch on theme —
// they consume tokens; this module only flips the attribute on <html>.

export type ThemePreference = "light" | "dark" | "system";

const STORAGE_KEY = "wf.theme";

export function getThemePreference(): ThemePreference {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

function resolve(pref: ThemePreference): "light" | "dark" {
  if (pref !== "system") return pref;
  return typeof matchMedia !== "undefined" && matchMedia("(prefers-color-scheme: dark)").matches
    ? "dark"
    : "light";
}

export function applyTheme(pref: ThemePreference): void {
  document.documentElement.setAttribute("data-theme", resolve(pref));
}

export function setThemePreference(pref: ThemePreference): void {
  try {
    if (pref === "system") localStorage.removeItem(STORAGE_KEY);
    else localStorage.setItem(STORAGE_KEY, pref);
  } catch {
    /* persistence unavailable; the attribute still applies for this tab */
  }
  applyTheme(pref);
}

/** Call once at boot: applies the persisted/system theme and tracks OS changes while the
 * preference is "system". */
export function initTheme(): void {
  applyTheme(getThemePreference());
  if (typeof matchMedia !== "undefined") {
    matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
      if (getThemePreference() === "system") applyTheme("system");
    });
  }
}

/** Cycle light -> dark -> light (explicit preference; "system" returns on storage clear). */
export function toggleTheme(): void {
  const current = document.documentElement.getAttribute("data-theme");
  setThemePreference(current === "dark" ? "light" : "dark");
}

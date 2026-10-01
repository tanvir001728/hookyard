import { useCallback, useEffect, useState } from "react";

export type Theme = "light" | "dark" | "system";
const storageKey = "hookyard-theme";

function readTheme(): Theme {
  try {
    const v = localStorage.getItem(storageKey);
    if (v === "light" || v === "dark" || v === "system") return v;
  } catch {
    // Storage can be unavailable (private mode, blocked site data).
  }
  return "system";
}

function resolve(theme: Theme): "light" | "dark" {
  if (theme !== "system") return theme;
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

/** Applies a theme to <html> by toggling the .dark class. */
export function applyTheme(theme: Theme = readTheme()): void {
  document.documentElement.classList.toggle("dark", resolve(theme) === "dark");
}

/** Returns the chosen theme, a setter, and the theme actually shown. */
export function useTheme(): [Theme, (t: Theme) => void, "light" | "dark"] {
  const [theme, setThemeState] = useState<Theme>(readTheme);
  const [resolved, setResolved] = useState(() => resolve(theme));

  useEffect(() => {
    applyTheme(theme);
    setResolved(resolve(theme));
    if (theme !== "system") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => {
      applyTheme("system");
      setResolved(resolve("system"));
    };
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [theme]);

  const setTheme = useCallback((t: Theme) => {
    try {
      localStorage.setItem(storageKey, t);
    } catch {
      // Not persisted; the choice still applies for this visit.
    }
    setThemeState(t);
  }, []);

  return [theme, setTheme, resolved];
}

// Tema claro/escuro: preferência salva no navegador; padrão segue o sistema.
const KEY = "doomctl-theme";

export function currentTheme() {
  return document.documentElement.dataset.theme === "dark" ? "dark" : "light";
}

export function savedTheme() {
  try { return localStorage.getItem(KEY); } catch (_) { return null; }
}

export function applyTheme(t, persist = false) {
  const theme = t === "dark" || t === "light" ? t : (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  document.documentElement.dataset.theme = theme;
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute("content", theme === "dark" ? "#0a0d14" : "#ffffff");
  if (persist) { try { localStorage.setItem(KEY, theme); } catch (_) { /* modo privado */ } }
}

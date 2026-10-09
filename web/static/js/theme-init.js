// Aplica o tema antes da renderização (evita "piscar" claro→escuro).
(function () {
  var t = null;
  try { t = localStorage.getItem("doomctl-theme"); } catch (e) { /* ignore */ }
  if (t !== "dark" && t !== "light") t = window.matchMedia && matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  document.documentElement.setAttribute("data-theme", t);
})();

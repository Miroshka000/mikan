// The panel's theme before the first paint, so a dark theme never flashes light. A plain
// file, not inline: the panel's CSP allows scripts from itself only. The lists are the
// ones in src/components/theme.tsx, which takes over once the app runs.
(function () {
  var dark = ["midnight", "graphite", "abyss", "ember", "plum", "moss", "terminal"];
  var all = ["mikan", "ocean", "sakura", "forest"].concat(dark);
  var theme = "mikan";
  try {
    var saved = localStorage.getItem("mikan-theme");
    if (saved === "system") {
      theme = window.matchMedia && matchMedia("(prefers-color-scheme: dark)").matches ? "midnight" : "mikan";
    } else if (all.indexOf(saved) >= 0) {
      theme = saved;
    }
  } catch (e) {
    // Storage blocked: the default theme.
  }
  var root = document.documentElement;
  root.setAttribute("data-theme", theme);
  root.setAttribute("data-tone", dark.indexOf(theme) >= 0 ? "dark" : "light");
})();

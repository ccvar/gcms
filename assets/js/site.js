(function () {
  "use strict";

  var langSwitches = Array.prototype.slice.call(document.querySelectorAll(".lang-switch"));
  if (!langSwitches.length) return;

  function closeOthers(current) {
    langSwitches.forEach(function (el) {
      if (el !== current) el.removeAttribute("open");
    });
  }

  langSwitches.forEach(function (el) {
    el.addEventListener("toggle", function () {
      if (el.open) closeOthers(el);
    });
  });

  document.addEventListener("click", function (event) {
    langSwitches.forEach(function (el) {
      if (el.open && !el.contains(event.target)) {
        el.removeAttribute("open");
      }
    });
  });

  document.addEventListener("keydown", function (event) {
    if (event.key !== "Escape") return;
    langSwitches.forEach(function (el) {
      el.removeAttribute("open");
    });
  });
})();

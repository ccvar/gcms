(function () {
  "use strict";

  function escapeHTML(value) {
    return String(value || "").replace(/[&<>"']/g, function (ch) {
      return {
        "&": "&amp;",
        "<": "&lt;",
        ">": "&gt;",
        '"': "&quot;",
        "'": "&#39;"
      }[ch];
    });
  }

  function staticSearch() {
    var root = document.querySelector("[data-static-search]");
    if (!root || !window.fetch) return;
    var params = new URLSearchParams(window.location.search);
    var q = (params.get("q") || root.dataset.query || "").trim();
    if (!q) return;
    var serverRendered = root.querySelector(".post-item");
    if (serverRendered) return;
    var indexURL = root.dataset.indexUrl || "/search-index.json";
    var summary = document.querySelector("[data-search-summary]");
    var lower = q.toLowerCase();
    fetch(indexURL, { headers: { "Accept": "application/json" } })
      .then(function (r) {
        if (!r.ok) throw new Error("search index unavailable");
        return r.json();
      })
      .then(function (items) {
        var results = (Array.isArray(items) ? items : []).filter(function (item) {
          var haystack = [item.title, item.excerpt, item.category, item.type].join(" ").toLowerCase();
          return haystack.indexOf(lower) !== -1;
        }).slice(0, 50);
        if (summary) {
          var tpl = root.dataset.foundTemplate || "Found %d results";
          summary.textContent = tpl.replace("%d", String(results.length));
          summary.hidden = false;
        }
        if (!results.length) {
          var none = (root.dataset.noneTemplate || root.dataset.none || "No results.").replace("%s", q);
          root.innerHTML = '<p class="muted">' + escapeHTML(none) + "</p>";
          return;
        }
        root.innerHTML = results.map(function (item, index) {
          var meta = "";
          if (item.category) meta += '<span class="tag">' + escapeHTML(item.category) + "</span>";
          if (item.date) meta += (meta ? '<span class="sep">·</span>' : "") + '<time datetime="' + escapeHTML(item.date) + '">' + escapeHTML(item.date) + "</time>";
          return '<article class="post-item">' +
            '<div class="num">' + String(index + 1).padStart(2, "0") + "</div>" +
            "<div>" +
            (meta ? '<div class="meta">' + meta + "</div>" : "") +
            '<h3><a href="' + escapeHTML(item.url) + '">' + escapeHTML(item.title) + "</a></h3>" +
            (item.excerpt ? "<p>" + escapeHTML(item.excerpt) + "</p>" : "") +
            "</div>" +
            "</article>";
        }).join("");
      })
      .catch(function () {});
  }

  staticSearch();

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

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
          var haystack = [item.title, item.excerpt, item.category, item.keywords, item.meta_desc, item.type].join(" ").toLowerCase();
          return haystack.indexOf(lower) !== -1;
        }).slice(0, 50);
        if (summary) {
          var tpl = root.dataset.foundTemplate || "Found %d results";
          summary.textContent = tpl.replace("%d", String(results.length));
          summary.hidden = false;
        }
        if (!results.length) {
          var none = (root.dataset.noneTemplate || root.dataset.none || "No results.").replace("%s", q);
          root.innerHTML = '<p class="muted search-empty">' + escapeHTML(none) + "</p>";
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

  function knowledgeDocsTabs() {
    var root = document.querySelector("[data-knowledge-docs]");
    if (!root) return;
    var nav = root.querySelector("[data-knowledge-docs-nav]");
    if (!nav) return;
    var tabs = Array.prototype.slice.call(nav.querySelectorAll("[data-docs-tab]"));
    var panels = Array.prototype.slice.call(root.querySelectorAll("[data-docs-panel]"));
    if (!tabs.length || !panels.length) return;

    function activate(key) {
      var found = false;
      panels.forEach(function (panel) {
        var active = panel.getAttribute("data-docs-panel") === key;
        panel.hidden = !active;
        if (active) found = true;
      });
      if (!found) return false;
      tabs.forEach(function (tab) {
        var active = tab.getAttribute("data-docs-tab") === key;
        tab.classList.toggle("active", active);
        tab.setAttribute("aria-selected", active ? "true" : "false");
        if (active) tab.setAttribute("aria-current", "page");
        else tab.removeAttribute("aria-current");
      });
      return true;
    }

    nav.addEventListener("click", function (event) {
      var tab = event.target.closest && event.target.closest("[data-docs-tab]");
      if (!tab || !nav.contains(tab)) return;
      if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button === 1) return;
      var key = tab.getAttribute("data-docs-tab");
      if (!key || !activate(key)) return;
      event.preventDefault();
    });
  }

  function navToggle() {
    var header = document.querySelector(".site-header");
    if (!header) return;
    var btn = header.querySelector(".menu-toggle");
    var nav = header.querySelector(".nav");
    if (!btn || !nav) return;
    function close() {
      header.classList.remove("nav-open");
      btn.setAttribute("aria-expanded", "false");
    }
    btn.addEventListener("click", function (e) {
      e.stopPropagation();
      var open = header.classList.toggle("nav-open");
      btn.setAttribute("aria-expanded", open ? "true" : "false");
    });
    nav.addEventListener("click", function (e) {
      if (e.target.closest && e.target.closest("a")) close();
    });
    document.addEventListener("click", function (e) {
      if (header.classList.contains("nav-open") && !header.contains(e.target)) close();
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") close();
    });
  }

  knowledgeDocsTabs();
  navToggle();

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

/* 星图 · Constellation：分类芯片 + 实时搜索筛选（渐进增强；无 JS 时分类是链接、全部展示） */
(function () {
  var root = document.querySelector("[data-cst]");
  if (!root) return;
  var chips = [].slice.call(root.querySelectorAll(".cst-chip"));
  var cards = [].slice.call(root.querySelectorAll(".cst-card"));
  var search = root.querySelector(".cst-search");
  var empty = root.querySelector(".cst-empty");
  if (!cards.length) return;
  if (search) search.hidden = false;
  var activeCat = "all";
  function apply() {
    var q = ((search && search.value) || "").trim().toLowerCase();
    var shown = 0;
    for (var i = 0; i < cards.length; i++) {
      var c = cards[i];
      var okCat = activeCat === "all" || (c.getAttribute("data-cat") || "") === activeCat;
      var okText = !q || (c.getAttribute("data-text") || "").toLowerCase().indexOf(q) !== -1;
      var show = okCat && okText;
      c.hidden = !show;
      if (show) shown++;
    }
    if (empty) empty.hidden = shown !== 0;
  }
  chips.forEach(function (ch) {
    ch.addEventListener("click", function (e) {
      e.preventDefault();
      activeCat = ch.getAttribute("data-cat") || "all";
      chips.forEach(function (x) { x.classList.toggle("is-active", x === ch); });
      apply();
    });
  });
  if (search) search.addEventListener("input", apply);
})();

// ---------- 回到顶部：全站生效 ----------
// 逻辑原在 toc.js（只有文章/链接页引入），但所有详情模板都渲染 .to-top：
// 商品/generic/doc 详情上按钮永远隐形，却以 z-index 90 挡在右下角吞掉
// 浮动询盘按钮的点击。挪到 site.js（每页都载），配合 CSS 的 pointer-events 修复。
(function () {
  var toTop = document.querySelector(".to-top");
  if (!toTop) return;
  function onScroll() {
    var st = window.scrollY || document.documentElement.scrollTop || 0;
    toTop.classList.toggle("show", st > 600);
  }
  toTop.addEventListener("click", function () {
    window.scrollTo({ top: 0, behavior: "smooth" });
  });
  window.addEventListener("scroll", onScroll, { passive: true });
  onScroll();
})();

// ---------- 联系方式：浮动按钮面板 + 微信二维码弹层（JS class/hidden 开关，不用 :target） ----------
(function () {
  var float = document.querySelector("[data-contact-float]");
  if (float) {
    var toggle = float.querySelector("[data-contact-toggle]");
    if (toggle) {
      toggle.addEventListener("click", function () {
        var open = float.classList.toggle("open");
        toggle.setAttribute("aria-expanded", open ? "true" : "false");
      });
      document.addEventListener("click", function (e) {
        if (float.classList.contains("open") && !float.contains(e.target)) {
          float.classList.remove("open");
          toggle.setAttribute("aria-expanded", "false");
        }
      });
    }
  }
  var modal = document.querySelector("[data-wechat-modal]");
  if (!modal) return;
  function openModal() { modal.hidden = false; document.documentElement.classList.add("wechat-modal-open"); }
  function closeModal() { modal.hidden = true; document.documentElement.classList.remove("wechat-modal-open"); }
  document.addEventListener("click", function (e) {
    var opener = e.target.closest && e.target.closest("[data-wechat-open]");
    if (opener) { e.preventDefault(); openModal(); return; }
    var closer = e.target.closest && e.target.closest("[data-wechat-close]");
    if (closer) { e.preventDefault(); closeModal(); }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !modal.hidden) closeModal();
  });
})();

// ---------- 商品详情（工厂骨架）：缩略图切换主图 + 图集灯箱（JS class/hidden 开关，绝不用 :target） ----------
(function () {
  var main = document.querySelector("[data-pd-main]");
  var mainLink = main && main.closest ? main.closest("[data-lightbox]") : null;
  var thumbs = [].slice.call(document.querySelectorAll("[data-pd-thumb]"));
  thumbs.forEach(function (btn) {
    btn.addEventListener("click", function () {
      var src = btn.getAttribute("data-pd-thumb");
      if (!src || !main) return;
      main.removeAttribute("width");
      main.removeAttribute("height");
      main.src = src;
      if (mainLink) mainLink.setAttribute("href", src);
      thumbs.forEach(function (x) { x.classList.toggle("is-active", x === btn); });
    });
  });

  var modal = document.querySelector("[data-lightbox-modal]");
  if (!modal) return;
  var img = modal.querySelector("[data-lightbox-img]");
  // 大图尺寸由 JS 显式算出（≤90vw/90vh、等比、视口居中），不能交给 CSS 的
  // width/height:auto：SVG 封面往往只有 viewBox 没有固有宽高，在「figure 收缩
  // 包裹图片、图片 max-width 又依赖 figure」的循环里会被浏览器解析成 0×0——
  // 表现为点开只剩遮罩和关闭钮、大图隐形。位图与 SVG 统一按 naturalWidth/Height
  // 的比例适配：小图放大、大图收缩，改窗口尺寸时重排。
  function fitBox() {
    if (!img || modal.hidden) return;
    var nw = img.naturalWidth, nh = img.naturalHeight;
    if (!nw || !nh) return; // 尚未加载完成：等 load 事件再排
    var vw = window.innerWidth || document.documentElement.clientWidth;
    var vh = window.innerHeight || document.documentElement.clientHeight;
    if (!vw || !vh) return; // 视口尺寸不可用（隐藏标签页等）：等 resize 再排
    var scale = Math.min(Math.min(vw * 0.9, 1100) / nw, vh * 0.9 / nh);
    img.style.width = Math.round(nw * scale) + "px";
    img.style.height = Math.round(nh * scale) + "px";
  }
  if (img) img.addEventListener("load", fitBox);
  window.addEventListener("resize", fitBox);
  function openBox(src) {
    if (!src || !img) return;
    img.style.width = "";
    img.style.height = "";
    img.src = src;
    modal.hidden = false;
    document.documentElement.classList.add("pd-lightbox-open");
    fitBox(); // 已在缓存里（不触发 load）时立即排版
  }
  function closeBox() {
    modal.hidden = true;
    document.documentElement.classList.remove("pd-lightbox-open");
  }
  document.addEventListener("click", function (e) {
    var opener = e.target.closest && e.target.closest("[data-lightbox]");
    if (opener) {
      e.preventDefault();
      openBox(opener.getAttribute("href") || (main && main.src) || "");
      return;
    }
    var closer = e.target.closest && e.target.closest("[data-lightbox-close]");
    if (closer) {
      e.preventDefault();
      closeBox();
    }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !modal.hidden) closeBox();
  });
})();

/* 工厂 vision/herofold 骨架：导航透明悬浮 / 嵌入门楣，滚动后加实底（渐进增强）。
   首页 hero 带 [data-fnav-hero] 标记时才启用：html 加 js-fnav 进入悬浮态，
   滚动越过阈值加 fnav-solid 变实底。无 JS / 内页（无标记）时页头恒实底可用。
   纯 class 开关，绝不用 :target。 */
(function () {
  var hero = document.querySelector("[data-fnav-hero]");
  if (!hero) return;
  var root = document.documentElement;
  var layout = root.getAttribute("data-theme-layout");
  if (layout !== "factory-vision" && layout !== "factory-herofold") return;
  var mode = hero.getAttribute("data-fnav-hero") || "vision";
  var header = document.querySelector(".site-header");
  root.classList.add("js-fnav");
  function threshold() {
    if (mode === "fold") {
      // 门楣：滚动离开首屏容器（hero 底缘越过导航高度）后剥离吸顶。
      var h = header ? header.offsetHeight : 64;
      return Math.max(hero.offsetTop + hero.offsetHeight - h, 1);
    }
    // 沉浸：一离开顶部就上实底，压图文字不与导航打架。
    return 32;
  }
  function onScroll() {
    var y = window.scrollY || document.documentElement.scrollTop || 0;
    root.classList.toggle("fnav-solid", y > threshold());
  }
  window.addEventListener("scroll", onScroll, { passive: true });
  window.addEventListener("resize", onScroll);
  onScroll();
})();

/* 工厂主题 FAQ 手风琴渐进增强：原生 details 零 JS 可用；这里补「开一条收起其余」。
   纯 open 属性开关，绝不用 :target。 */
(function () {
  var faqs = document.querySelectorAll("[data-factory-faq]");
  if (!faqs.length) return;
  faqs.forEach(function (faq) {
    faq.addEventListener("toggle", function (e) {
      var opened = e.target;
      if (!opened || !opened.open || !opened.classList || !opened.classList.contains("f-faq-item")) return;
      faq.querySelectorAll("details.f-faq-item[open]").forEach(function (item) {
        if (item !== opened) item.open = false;
      });
    }, true); // toggle 不冒泡，用捕获阶段统一监听
  });
})();

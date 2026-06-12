// 文章页增强（渐进增强）：页眉高度测量、阅读进度、回到顶部、大纲滚动高亮。
// 无 JS 时大纲仍是可用锚点链接。
(function () {
  "use strict";

  // 1) 测量粘性页眉高度 → --header-h，修复不同主题下大纲/锚点被页眉遮挡。
  var header = document.querySelector(".site-header");
  function setHeaderH() {
    if (header) {
      var h = Math.round(header.getBoundingClientRect().height);
      document.documentElement.style.setProperty("--header-h", h + "px");
    }
  }
  setHeaderH();
  window.addEventListener("resize", setHeaderH);
  if (window.ResizeObserver && header) {
    new ResizeObserver(setHeaderH).observe(header);
  }

  // 2) 阅读进度条 + 回到顶部
  var bar = document.querySelector(".read-progress > i");
  var toTop = document.querySelector(".to-top");
  function onScroll() {
    var st = window.scrollY || document.documentElement.scrollTop || 0;
    var max = document.documentElement.scrollHeight - window.innerHeight;
    var p = max > 0 ? st / max : 0;
    if (bar) bar.style.transform = "scaleX(" + p.toFixed(4) + ")";
    if (toTop) toTop.classList.toggle("show", st > 600);
  }
  if (toTop) {
    toTop.addEventListener("click", function () {
      window.scrollTo({ top: 0, behavior: "smooth" });
    });
  }
  window.addEventListener("scroll", onScroll, { passive: true });
  onScroll();

  // 3) 大纲滚动高亮
  var toc = document.querySelector(".toc");
  if (!toc) return;
  var links = Array.prototype.slice.call(toc.querySelectorAll("a"));
  if (!links.length || !("IntersectionObserver" in window)) return;

  var byId = {};
  links.forEach(function (a) {
    var id = decodeURIComponent((a.getAttribute("href") || "").slice(1));
    if (id) byId[id] = a;
  });
  var headings = Array.prototype.slice.call(
    document.querySelectorAll(".prose h2[id], .prose h3[id]")
  );
  if (!headings.length) return;

  var current = null;
  function setActive(a) {
    if (a === current) return;
    if (current) current.classList.remove("active");
    if (a) a.classList.add("active");
    current = a || null;
  }
  var visible = {};
  var observer = new IntersectionObserver(
    function (entries) {
      entries.forEach(function (e) {
        visible[e.target.id] = e.isIntersecting;
      });
      for (var i = 0; i < headings.length; i++) {
        if (visible[headings[i].id]) {
          setActive(byId[headings[i].id]);
          return;
        }
      }
    },
    { rootMargin: "-90px 0px -70% 0px", threshold: 0 }
  );
  headings.forEach(function (h) {
    observer.observe(h);
  });
  links.forEach(function (a) {
    a.addEventListener("click", function () {
      setActive(a);
    });
  });
})();

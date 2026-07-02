// 后台交互（原生 JS，无依赖）：上传(含前端转 WebP)、自定义下拉、主题微调(按主题)、
// Markdown ⇄ Medium 式富文本（气泡工具栏 / 链接弹层 / 加号菜单：图片·表格·分割线）。
(function () {
  "use strict";
  var CSRF = (document.body && document.body.dataset.csrf) || "";

  function copyTextToClipboard(text) {
    text = String(text || "");
    if (!text) return Promise.reject(new Error("empty"));
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    return new Promise(function (resolve, reject) {
      var area = document.createElement("textarea");
      area.value = text;
      area.setAttribute("readonly", "");
      area.style.position = "fixed";
      area.style.left = "-9999px";
      area.style.top = "0";
      document.body.appendChild(area);
      area.select();
      try {
        document.execCommand("copy") ? resolve() : reject(new Error("copy failed"));
      } catch (err) {
        reject(err);
      } finally {
        area.remove();
      }
    });
  }

  function markCopyDone(button, fallback) {
    if (!button) return;
    var label = button.querySelector("[data-copy-label-text]");
    var old = button.getAttribute("aria-label") || (label ? label.textContent : button.textContent);
    var done = button.getAttribute("data-copy-done") || fallback || "已复制";
    button.classList.add("is-copied");
    button.setAttribute("aria-label", done);
    if (button.dataset.copyReplaceText === "1") {
      if (label) label.textContent = done;
      else button.textContent = done;
    }
    window.setTimeout(function () {
      button.classList.remove("is-copied");
      button.setAttribute("aria-label", old);
      if (button.dataset.copyReplaceText === "1") {
        var restored = button.getAttribute("data-copy-label") || old;
        if (label) label.textContent = restored;
        else button.textContent = restored;
      }
    }, 1200);
  }

  /* ---------- 通用轻提示：接管按钮/链接原生 title ---------- */
  (function () {
    var selector = "[data-tooltip],button[title],a[title],[role='button'][title]";
    var tip = null;
    var current = null;
    function ensureTip() {
      if (tip) return tip;
      tip = document.createElement("div");
      tip.className = "ui-tooltip";
      tip.setAttribute("role", "tooltip");
      tip.hidden = true;
      document.body.appendChild(tip);
      return tip;
    }
    function tooltipText(el) {
      if (!el) return "";
      var text = el.getAttribute("data-tooltip") || el.getAttribute("title") || "";
      if (el.hasAttribute("title")) {
        if (!el.hasAttribute("data-tooltip")) el.setAttribute("data-tooltip", text);
        el.removeAttribute("title");
      }
      return text.trim();
    }
    function targetFromEvent(e) {
      return e.target && e.target.closest ? e.target.closest(selector) : null;
    }
    function place(el) {
      if (!tip || tip.hidden || !el) return;
      var rect = el.getBoundingClientRect();
      var gap = 8;
      var pad = 10;
      var width = tip.offsetWidth;
      var height = tip.offsetHeight;
      var left = rect.left + rect.width / 2 - width / 2;
      var top = rect.top - height - gap;
      if (top < pad) top = rect.bottom + gap;
      left = Math.max(pad, Math.min(left, window.innerWidth - width - pad));
      top = Math.max(pad, Math.min(top, window.innerHeight - height - pad));
      tip.style.left = left + "px";
      tip.style.top = top + "px";
    }
    function show(el) {
      var text = tooltipText(el);
      if (!text || (el && el.disabled)) return;
      current = el;
      ensureTip().textContent = text;
      tip.hidden = false;
      place(el);
    }
    function hide(el) {
      if (el && current && el !== current) return;
      if (tip) tip.hidden = true;
      current = null;
    }
    document.querySelectorAll("button[title],a[title],[role='button'][title]").forEach(tooltipText);
    document.addEventListener("pointerover", function (e) {
      var el = targetFromEvent(e);
      if (!el || (e.relatedTarget && el.contains(e.relatedTarget))) return;
      show(el);
    });
    document.addEventListener("pointerout", function (e) {
      var el = targetFromEvent(e);
      if (!el || (e.relatedTarget && el.contains(e.relatedTarget))) return;
      hide(el);
    });
    document.addEventListener("focusin", function (e) {
      var el = targetFromEvent(e);
      if (el) show(el);
    });
    document.addEventListener("focusout", function (e) {
      var el = targetFromEvent(e);
      if (el) hide(el);
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") hide();
    });
    window.addEventListener("resize", function () { if (current) place(current); });
    window.addEventListener("scroll", function () { if (current) place(current); }, true);
  })();

  /* ---------- 上传：浏览器支持时先把 png/jpg 转成 WebP ---------- */
  function maybeWebp(file) {
    return new Promise(function (resolve) {
      if (!/^image\/(png|jpeg)$/.test(file.type)) { resolve(file); return; }
      var canvas = document.createElement("canvas");
      if (!canvas.toBlob) { resolve(file); return; }
      var img = new Image();
      var url = URL.createObjectURL(file);
      img.onload = function () {
        canvas.width = img.naturalWidth; canvas.height = img.naturalHeight;
        try { canvas.getContext("2d").drawImage(img, 0, 0); }
        catch (e) { URL.revokeObjectURL(url); resolve(file); return; }
        canvas.toBlob(function (blob) {
          URL.revokeObjectURL(url);
          if (blob && blob.size > 0) {
            var base = (file.name || "image").replace(/\.[^.]+$/, "");
            resolve(new File([blob], base + ".webp", { type: "image/webp" }));
          } else resolve(file);
        }, "image/webp", 0.9);
      };
      img.onerror = function () { URL.revokeObjectURL(url); resolve(file); };
      img.src = url;
    });
  }
  function uploadFile(file) {
    return maybeWebp(file).then(function (f) {
      var fd = new FormData();
      fd.append("file", f);
      fd.append("_csrf", CSRF);
      return fetch("/admin/upload", { method: "POST", body: fd, credentials: "same-origin" })
        .then(function (r) { return r.json().then(function (j) { return { ok: r.ok, j: j }; }); });
    });
  }

  /* ---------- 图片上传组件 ---------- */
  function setUploaderValue(box, url) {
    var hidden = box.querySelector('input[type="hidden"]');
    var preview = box.querySelector(".up-preview");
    var urlInput = box.querySelector(".up-url");
    if (hidden) hidden.value = url || "";
    if (urlInput && urlInput.value !== url) urlInput.value = url || "";
    if (preview) {
      if (url) { preview.style.backgroundImage = "url('" + url.replace(/'/g, "%27") + "')"; preview.classList.add("has"); }
      else { preview.style.backgroundImage = ""; preview.classList.remove("has"); }
    }
    if (hidden) hidden.dispatchEvent(new Event("input", { bubbles: true })); // 通知脏检查
  }
  document.querySelectorAll(".uploader").forEach(function (box) {
    var preview = box.querySelector(".up-preview");
    var file = box.querySelector('input[type="file"]');
    var urlInput = box.querySelector(".up-url");
    var removeBtn = box.querySelector(".up-remove");
    var status = box.querySelector(".up-status");
    // 点击占位/图片即选图（点删除按钮除外）
    if (preview && file) {
      preview.addEventListener("click", function (e) {
        if (removeBtn && removeBtn.contains(e.target)) return;
        file.click();
      });
      preview.addEventListener("keydown", function (e) {
        if (e.key === "Enter" || e.key === " ") { e.preventDefault(); file.click(); }
      });
    }
    if (file) file.addEventListener("change", function () {
      if (!file.files || !file.files[0]) return;
      if (status) status.textContent = "上传中…";
      uploadFile(file.files[0]).then(function (res) {
        if (res.ok && res.j.url) { setUploaderValue(box, res.j.url); if (status) status.textContent = ""; }
        else if (status) status.textContent = (res.j && res.j.error) || "上传失败";
      }).catch(function () { if (status) status.textContent = "上传失败"; });
      file.value = "";
    });
    if (urlInput) urlInput.addEventListener("change", function () { setUploaderValue(box, urlInput.value.trim()); });
    if (removeBtn) removeBtn.addEventListener("click", function (e) { e.stopPropagation(); setUploaderValue(box, ""); });
    var hidden = box.querySelector('input[type="hidden"]');
    if (hidden && hidden.value) setUploaderValue(box, hidden.value);
  });

  /* ---------- 图集：左侧批量选图上传 + 缩略图网格（可拖拽排序、单删） ---------- */
  document.querySelectorAll(".gallery-up").forEach(function (box) {
    var data = box.querySelector(".gu-data");
    var grid = box.querySelector("[data-gu-grid]");
    var file = box.querySelector("[data-gu-file]");
    var status = box.querySelector("[data-gu-status]");
    if (!data || !grid || !file) return;
    var multiple = box.getAttribute("data-multiple") === "1";
    function urls() {
      return data.value.split("\n").map(function (s) { return s.trim(); }).filter(Boolean);
    }
    function setUrls(list) {
      if (!multiple) list = list.slice(-1);
      data.value = list.join("\n");
      data.dispatchEvent(new Event("input", { bubbles: true })); // 通知脏检查
      render();
    }
    function setStatus(text, bad) {
      if (!status) return;
      status.textContent = text || "";
      status.classList.toggle("bad", !!bad);
    }
    function makeTile(url, i) {
      var cell = document.createElement("div");
      cell.className = "gu-cell";
      cell.draggable = multiple;
      cell.setAttribute("data-i", i);
      var img = document.createElement("img");
      img.src = url; img.loading = "lazy"; img.alt = "";
      var del = document.createElement("button");
      del.type = "button"; del.className = "gu-del";
      del.setAttribute("aria-label", "移除");
      del.setAttribute("title", "移除");
      del.innerHTML = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round"><path d="M6 6l12 12M18 6 6 18"/></svg>';
      del.addEventListener("click", function () { var l = urls(); l.splice(i, 1); setUrls(l); });
      cell.appendChild(img); cell.appendChild(del);
      return cell;
    }
    function makeAdd() {
      var add = document.createElement("button");
      add.type = "button"; add.className = "gu-add";
      add.innerHTML = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg><span>' + (multiple ? "添加图片" : "选择图片") + "</span>";
      add.addEventListener("click", function () { file.click(); });
      return add;
    }
    function render() {
      grid.innerHTML = "";
      var list = urls();
      list.forEach(function (u, i) { grid.appendChild(makeTile(u, i)); });
      if (multiple || list.length === 0) grid.appendChild(makeAdd());
    }
    file.addEventListener("change", function () {
      var files = Array.prototype.slice.call(file.files || []);
      if (!files.length) return;
      var total = files.length, done = 0, failed = 0, added = urls();
      setStatus("上传中… (0/" + total + ")");
      files.reduce(function (chain, f) {
        return chain.then(function () {
          return uploadFile(f).then(function (res) {
            if (res.ok && res.j && res.j.url) added.push(res.j.url); else failed++;
            done++;
            setStatus("上传中… (" + done + "/" + total + ")");
          }).catch(function () { failed++; done++; });
        });
      }, Promise.resolve()).then(function () {
        setUrls(added);
        setStatus(failed ? (failed + " 张上传失败") : "", failed > 0);
      });
      file.value = "";
    });
    // 拖拽排序
    var dragI = null;
    grid.addEventListener("dragstart", function (e) {
      var cell = e.target.closest && e.target.closest(".gu-cell");
      if (!cell) return;
      dragI = parseInt(cell.getAttribute("data-i"), 10);
      setTimeout(function () { cell.classList.add("dragging"); }, 0);
    });
    grid.addEventListener("dragover", function (e) { if (dragI !== null) e.preventDefault(); });
    grid.addEventListener("drop", function (e) {
      if (dragI === null) return;
      e.preventDefault();
      var cell = e.target.closest && e.target.closest(".gu-cell");
      var list = urls();
      var item = list.splice(dragI, 1)[0];
      var to = cell ? parseInt(cell.getAttribute("data-i"), 10) : list.length;
      if (to < 0) to = list.length;
      list.splice(to, 0, item);
      dragI = null;
      setUrls(list);
    });
    grid.addEventListener("dragend", function () {
      dragI = null;
      grid.querySelectorAll(".gu-cell").forEach(function (c) { c.classList.remove("dragging"); });
    });
    render();
  });

  /* ---------- 后台文档列表：按分类折叠子章节（默认收起，避免章节多时列表过长） ---------- */
  (function () {
    var rows = Array.prototype.slice.call(document.querySelectorAll("tr[data-doc-depth]"));
    if (!rows.length) return;
    var groups = [], cur = null;
    rows.forEach(function (tr) {
      var d = parseInt(tr.getAttribute("data-doc-depth"), 10) || 0;
      if (d === 0) { cur = { head: tr, children: [] }; groups.push(cur); }
      else if (cur) { cur.children.push(tr); }
    });
    groups.forEach(function (g) {
      if (!g.children.length) return;
      var collapsed = true;
      g.children.forEach(function (tr) { tr.hidden = true; });
      var link = g.head.querySelector(".title-cell .t");
      if (!link) return;
      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "doc-collapse";
      btn.setAttribute("aria-expanded", "false");
      btn.setAttribute("aria-label", "展开 / 收起子章节");
      btn.innerHTML = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><path d="m9 18 6-6-6-6"/></svg><span class="doc-collapse-n">' + g.children.length + '</span>';
      link.parentNode.insertBefore(btn, link);
      btn.addEventListener("click", function () {
        collapsed = !collapsed;
        g.children.forEach(function (tr) { tr.hidden = collapsed; });
        btn.setAttribute("aria-expanded", collapsed ? "false" : "true");
        btn.classList.toggle("open", !collapsed);
      });
    });
  })();

  /* ---------- 后台文档列表：同级拖动排序（拖整棵子树，落点限同级，保存 extra.order） ---------- */
  (function () {
    var table = document.querySelector("table[data-reorder-url]");
    if (!table) return;
    var reorderURL = table.getAttribute("data-reorder-url");
    function rows() { return Array.prototype.slice.call(table.querySelectorAll("tr[data-doc-id]")); }
    function depthOf(tr) { return parseInt(tr.getAttribute("data-doc-depth"), 10) || 0; }
    function parentOf(tr) { return tr.getAttribute("data-doc-parent") || "0"; }
    // 一行的「块」= 它本身 + 紧随其后、depth 更深的所有行（整棵子树）
    function blockOf(tr) {
      var rs = rows(), i = rs.indexOf(tr), d = depthOf(tr), out = [tr];
      for (var j = i + 1; j < rs.length; j++) { if (depthOf(rs[j]) > d) out.push(rs[j]); else break; }
      return out;
    }
    var dragRow = null, dragBlock = null;
    table.addEventListener("mousedown", function (e) {
      var h = e.target.closest && e.target.closest(".doc-drag");
      rows().forEach(function (r) { r.draggable = false; });
      if (h) { var r = h.closest("tr"); if (r) r.draggable = true; }
    });
    table.addEventListener("dragstart", function (e) {
      var tr = e.target.closest && e.target.closest("tr[data-doc-id]");
      if (!tr || !tr.draggable) return;
      dragRow = tr; dragBlock = blockOf(tr);
      if (e.dataTransfer) { e.dataTransfer.effectAllowed = "move"; e.dataTransfer.setData("text/plain", tr.getAttribute("data-doc-id") || ""); }
      setTimeout(function () { dragBlock.forEach(function (r) { r.classList.add("dragging"); }); }, 0);
    });
    table.addEventListener("dragover", function (e) {
      if (!dragRow) return;
      var tr = e.target.closest && e.target.closest("tr[data-doc-id]");
      if (!tr || dragBlock.indexOf(tr) >= 0) return;        // 不能落在自己的块里
      if (parentOf(tr) !== parentOf(dragRow)) return;        // 只允许同级
      e.preventDefault();
      var rect = tr.getBoundingClientRect();
      var after = e.clientY > rect.top + rect.height / 2;
      var tb = tr.parentNode;
      var tblock = blockOf(tr);
      var ref = after ? tblock[tblock.length - 1].nextSibling : tr;
      dragBlock.forEach(function (r) { tb.insertBefore(r, ref); });
    });
    table.addEventListener("drop", function (e) {
      if (!dragRow) return;
      e.preventDefault();
      var pid = parentOf(dragRow);
      var data = new URLSearchParams();
      data.set("_csrf", CSRF);
      rows().forEach(function (r) { if (parentOf(r) === pid) data.append("ids", r.getAttribute("data-doc-id")); });
      fetch(reorderURL, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8", "X-Requested-With": "XMLHttpRequest" }, body: data.toString() });
    });
    table.addEventListener("dragend", function () {
      if (dragBlock) dragBlock.forEach(function (r) { r.classList.remove("dragging"); r.draggable = false; });
      dragRow = null; dragBlock = null;
    });
  })();

  /* ---------- 扩展卡片：齿轮菜单 + 归档页文案弹窗 ---------- */
  (function () {
    var menus = document.querySelectorAll("details.ext-menu");
    var modals = document.querySelectorAll(".ext-modal");
    if (!menus.length && !modals.length) return;
    // 点外部 / Esc 收起齿轮菜单
    document.addEventListener("click", function (e) {
      document.querySelectorAll("details.ext-menu[open]").forEach(function (d) { if (!d.contains(e.target)) d.open = false; });
    });
    function modalFor(key) { return document.querySelector('[data-archive-modal="' + (key || "").replace(/"/g, '') + '"]'); }
    function closeModal(m) { if (m) m.hidden = true; }
    document.querySelectorAll("[data-archive-open]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var d = btn.closest("details.ext-menu"); if (d) d.open = false;
        var m = modalFor(btn.getAttribute("data-archive-open"));
        if (!m) return;
        m.hidden = false;
        var first = m.querySelector("input, textarea");
        if (first) setTimeout(function () { first.focus(); }, 0);
      });
    });
    modals.forEach(function (m) {
      m.querySelectorAll("[data-modal-close]").forEach(function (c) { c.addEventListener("click", function () { closeModal(m); }); });
      var form = m.querySelector("[data-archive-form]");
      var status = m.querySelector("[data-archive-status]");
      if (form) form.addEventListener("submit", function (e) {
        e.preventDefault();
        if (status) status.textContent = "保存中…";
        fetch(form.getAttribute("action"), {
          method: "POST", credentials: "same-origin",
          headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8", "X-Requested-With": "XMLHttpRequest" },
          body: new URLSearchParams(new FormData(form)).toString()
        }).then(function (r) { if (!r.ok) throw new Error("bad"); return r.json().catch(function () { return {}; }); })
          .then(function () { if (status) status.textContent = "已保存"; setTimeout(function () { closeModal(m); if (status) status.textContent = ""; }, 650); })
          .catch(function () { if (status) status.textContent = "保存失败，请重试"; });
      });
    });
    document.addEventListener("keydown", function (e) {
      if (e.key !== "Escape") return;
      document.querySelectorAll("details.ext-menu[open]").forEach(function (d) { d.open = false; });
      document.querySelectorAll(".ext-modal:not([hidden])").forEach(closeModal);
    });
  })();

  /* ---------- 自定义下拉（替代 <select>） ---------- */
  var openDD = null;
  function closeDD() {
    if (openDD) { openDD.classList.remove("open"); openDD.querySelector(".dd-toggle").setAttribute("aria-expanded", "false"); openDD = null; }
  }
  function initDropdown(dd) {
    if (!dd || dd.dataset.ddReady === "1") return;
    var toggle = dd.querySelector(".dd-toggle");
    var label = dd.querySelector(".dd-label");
    var hidden = dd.querySelector('input[type="hidden"]');
    var items = Array.prototype.slice.call(dd.querySelectorAll(".dd-menu li"));
    if (!toggle) return;
    dd.dataset.ddReady = "1";
    function select(li) {
      if (!li) return;
      items.forEach(function (x) { x.setAttribute("aria-selected", "false"); });
      li.setAttribute("aria-selected", "true");
      if (hidden) hidden.value = li.getAttribute("data-value");
      if (label) label.textContent = li.textContent;
      dd.dispatchEvent(new CustomEvent("dd:change", { bubbles: true, detail: { value: li.getAttribute("data-value") } }));
    }
    toggle.addEventListener("click", function (e) {
      e.stopPropagation();
      var willOpen = !dd.classList.contains("open");
      closeDD();
      if (willOpen) { dd.classList.add("open"); toggle.setAttribute("aria-expanded", "true"); openDD = dd; }
    });
    items.forEach(function (li) { li.addEventListener("click", function () { select(li); closeDD(); }); });
    dd.addEventListener("keydown", function (e) {
      var cur = items.findIndex(function (x) { return x.getAttribute("aria-selected") === "true"; });
      if (e.key === "Escape") { closeDD(); toggle.focus(); }
      else if (e.key === "ArrowDown") { e.preventDefault(); select(items[Math.min(items.length - 1, cur + 1)] || items[0]); }
      else if (e.key === "ArrowUp") { e.preventDefault(); select(items[Math.max(0, cur - 1)] || items[0]); }
    });
  }
  document.querySelectorAll(".dropdown").forEach(initDropdown);
  window.adminInitDropdown = initDropdown;
  document.addEventListener("click", closeDD);

  /* 列表里的发布状态快捷菜单：点击空白处收起，避免表格里同时打开多个菜单。 */
  function closeStatusMenus(except) {
    document.querySelectorAll(".status-menu[open]").forEach(function (menu) {
      if (!except || menu !== except) menu.open = false;
    });
  }
  document.addEventListener("click", function (e) {
    var menu = e.target && e.target.closest ? e.target.closest(".status-menu") : null;
    if (menu) {
      closeStatusMenus(menu);
      return;
    }
    closeStatusMenus();
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") closeStatusMenus();
  });

  /* 状态=定时发布 时显示发布时间输入 */
  var statusDD = document.querySelector(".dropdown[data-status]");
  var schedField = document.querySelector(".schedule-field");
  if (statusDD && schedField) {
    statusDD.addEventListener("dd:change", function (e) {
      schedField.hidden = e.detail.value !== "scheduled";
    });
  }

  /* Hero 右侧视觉类型切换：仅显示对应控件（默认/图片/SVG 代码） */
  var heroDD = document.querySelector(".dropdown[data-hero-type]");
  if (heroDD) {
    var heroBlocks = document.querySelectorAll("[data-hero-when]");
    heroDD.addEventListener("dd:change", function (e) {
      heroBlocks.forEach(function (b) { b.hidden = b.getAttribute("data-hero-when") !== e.detail.value; });
    });
  }

  /* ---------- 可视化编辑：后台 iframe 内点选前台文案并保存 ---------- */
  (function () {
    var root = document.querySelector("[data-visual-editor]");
    if (!root) return;
    var frame = root.querySelector(".visual-frame");
    var stage = root.querySelector(".visual-stage");
    var form = root.querySelector("[data-visual-form]");
    var empty = root.querySelector("[data-visual-empty]");
    var keyInput = root.querySelector("[data-visual-key]");
    var valueInput = root.querySelector("[data-visual-value]");
    var labelEl = root.querySelector("[data-visual-label]");
    var statusEl = root.querySelector("[data-visual-status]");
    var hintEl = root.querySelector("[data-visual-field-hint]");
    var langHintEl = root.querySelector("[data-visual-lang-hint]");
    var checkEl = root.querySelector("[data-visual-check]");
    var saveBtn = root.querySelector("[data-visual-save]");
    var resetBtn = root.querySelector("[data-visual-reset]");
    var currentToggle = root.querySelector("[data-visual-current]");
    var imageTools = root.querySelector("[data-visual-image-tools]");
    var imagePreview = root.querySelector("[data-visual-image-preview]");
    var uploadBtn = root.querySelector("[data-visual-upload]");
    var fileInput = root.querySelector("[data-visual-file]");
    var historyBox = root.querySelector("[data-visual-history]");
    var pageLabel = root.querySelector("[data-visual-page-label]");
    var selectedKey = "";
    var originalValue = "";
    var currentMode = "edit";
    var saving = false;
    var pendingScroll = null;
    var frameKeys = {};
    var draggedSort = null;

    function fieldButton(key) {
      var found = null;
      root.querySelectorAll("[data-visual-pick]").forEach(function (btn) {
        if (!found && btn.getAttribute("data-key") === key) found = btn;
      });
      return found;
    }
    function setStatus(text, bad) {
      if (!statusEl) return;
      statusEl.textContent = text || "";
      statusEl.classList.toggle("is-bad", !!bad);
    }
    function setCheck(text, bad) {
      if (!checkEl) return false;
      checkEl.textContent = text || "";
      checkEl.classList.toggle("is-bad", !!bad);
      return !!bad;
    }
    function frameDoc() {
      try { return frame && frame.contentDocument; } catch (e) { return null; }
    }
    function frameScroll() {
      var doc = frameDoc();
      if (!doc || !doc.defaultView) return 0;
      return doc.defaultView.pageYOffset || doc.documentElement.scrollTop || doc.body.scrollTop || 0;
    }
    function reloadFrame(keepScroll) {
      if (!frame || !frame.contentWindow) return;
      pendingScroll = keepScroll ? frameScroll() : null;
      frame.contentWindow.location.reload();
    }
    function currentKind() {
      var btn = fieldButton(selectedKey);
      return (btn && btn.getAttribute("data-kind")) || "text";
    }
    function isListLabelGroup(group) {
      return group === "nav" || group === "categorynav" || group === "linkcatnav";
    }
    function formLabelFor(btn, fallback) {
      if (!btn) return fallback || selectedKey;
      var group = btn.getAttribute("data-group");
      if (group === "nav") return "导航名称";
      if (group === "categorynav") return "分类导航按钮";
      if (group === "linkcatnav") return "链接分类导航按钮";
      return fallback || btn.getAttribute("data-label") || selectedKey;
    }
    function setImagePreview(value) {
      if (!imagePreview) return;
      if (value) {
        imagePreview.style.backgroundImage = "url('" + value.replace(/'/g, "%27") + "')";
        imagePreview.classList.add("has");
      } else {
        imagePreview.style.backgroundImage = "";
        imagePreview.classList.remove("has");
      }
    }
    function setButtonThumb(btn, value) {
      var thumb = btn && btn.querySelector(".visual-thumb");
      if (!thumb) return;
      if (value) {
        thumb.style.backgroundImage = "url('" + value.replace(/'/g, "%27") + "')";
        thumb.textContent = "";
      } else {
        thumb.style.backgroundImage = "";
        thumb.textContent = "图片";
      }
    }
    function validate() {
      if (!selectedKey || !valueInput) {
        if (saveBtn) saveBtn.disabled = true;
        return false;
      }
      var value = valueInput.value.trim();
      var bad = false;
      var msg = "";
      if (selectedKey === "site.name" && !value) {
        msg = "站点名称不能为空。";
        bad = true;
      } else if (selectedKey.indexOf("nav.") === 0 && value.length > 8) {
        msg = "导航名称偏长，手机端可能换行。";
      } else if (selectedKey === "site.hero_title" && value.replace(/\s/g, "").length > 36) {
        msg = "Hero 大标题偏长，部分主题第一屏可能显得拥挤。";
      } else if (selectedKey === "site.description" && value.length > 160) {
        msg = "站点描述偏长，搜索结果里可能被截断。";
      } else if (currentKind() === "image" && value && !/^(https?:\/\/|\/)/.test(value)) {
        msg = "图片地址建议使用 /uploads/... 或 https://...。";
      }
      setCheck(msg, bad);
      if (saveBtn) saveBtn.disabled = saving || bad || valueInput.value === originalValue;
      return !bad;
    }
    function markSelected(key) {
      var doc = frameDoc();
      if (doc) {
        doc.querySelectorAll("[data-visual-edit]").forEach(function (el) {
          el.classList.toggle("ve-selected", el.getAttribute("data-visual-edit") === key);
        });
      }
      root.querySelectorAll("[data-visual-pick]").forEach(function (btn) {
        btn.classList.toggle("active", btn.getAttribute("data-key") === key);
      });
    }
    function setGroupOpen(group, open) {
      if (!group) return;
      group.classList.toggle("open", !!open);
      var toggle = group.querySelector("[data-visual-group-toggle]");
      if (toggle) toggle.setAttribute("aria-expanded", open ? "true" : "false");
    }
    function openGroup(id, scroll) {
      var group = id ? root.querySelector('[data-visual-group="' + id + '"]') : null;
      setGroupOpen(group, true);
      if (scroll && group) group.scrollIntoView({ block: "nearest" });
    }
    function refreshFieldVisibility() {
      var onlyCurrent = currentToggle && currentToggle.checked;
      root.querySelectorAll("[data-visual-pick]").forEach(function (btn) {
        var key = btn.getAttribute("data-key");
        var contextual = btn.getAttribute("data-context") === "1";
        btn.hidden = (contextual && !frameKeys[key]) || (!!onlyCurrent && !frameKeys[key]);
      });
      root.querySelectorAll("[data-visual-group]").forEach(function (group) {
        var visibleCount = 0;
        group.querySelectorAll("[data-visual-pick]").forEach(function (btn) {
          if (!btn.hidden) visibleCount++;
        });
        var count = group.querySelector("[data-visual-group-toggle] small");
        if (count) count.textContent = visibleCount;
        group.hidden = visibleCount === 0;
      });
    }
    function selectField(key, label, value, multiline) {
      selectedKey = key || "";
      if (!selectedKey) return;
      var btn = fieldButton(selectedKey);
      var kind = "text";
      if (btn) {
        label = label || btn.getAttribute("data-label");
        if (!value) value = btn.getAttribute("data-value");
        multiline = btn.getAttribute("data-multiline") === "1";
        kind = btn.getAttribute("data-kind") || "text";
        openGroup(btn.getAttribute("data-group"), true);
      }
      if (empty) empty.hidden = true;
      if (form) form.hidden = false;
      if (keyInput) keyInput.value = selectedKey;
      if (labelEl) labelEl.textContent = formLabelFor(btn, label);
      if (hintEl) hintEl.textContent = (btn && btn.getAttribute("data-hint")) || "";
      if (langHintEl && btn) {
        var localized = btn.getAttribute("data-localized") === "1";
        var inherited = btn.getAttribute("data-inherited") === "1";
        langHintEl.hidden = !localized;
        langHintEl.textContent = inherited ? "当前语种还没有单独文案，正在沿用默认语种；保存后会只影响当前语种。" : "这个字段会保存到当前语种。";
      }
      if (valueInput) {
        valueInput.value = value || "";
        valueInput.rows = multiline ? 6 : 3;
        valueInput.placeholder = kind === "image" ? "/uploads/example.svg 或 https://example.com/image.png" : "";
        setTimeout(function () { valueInput.focus(); valueInput.select(); }, 0);
      }
      originalValue = valueInput ? valueInput.value : "";
      if (imageTools) imageTools.hidden = kind !== "image";
      setImagePreview(kind === "image" ? originalValue : "");
      setStatus("");
      markSelected(selectedKey);
      validate();
    }
    function syncSelectedValue(value) {
      var btn = fieldButton(selectedKey);
      if (!btn) return;
      btn.setAttribute("data-value", value || "");
      var small = btn.querySelector("small");
      var group = btn.getAttribute("data-group");
      if (small) small.textContent = isListLabelGroup(group) ? (btn.getAttribute("data-meta") || "") : (value || "");
      var thumb = btn.querySelector(".visual-thumb");
      if (thumb) setButtonThumb(btn, value || "");
      var title = btn.querySelector(".visual-card-body strong");
      if (title && isListLabelGroup(group)) {
        title.textContent = value || (group === "nav" ? "未命名导航" : "未命名分类");
        btn.setAttribute("data-label", title.textContent);
        if (labelEl) labelEl.textContent = formLabelFor(btn, title.textContent);
      }
    }
    function addHistoryItem(item) {
      if (!historyBox || !item || !item.id) return;
      var emptyText = historyBox.querySelector(":scope > p");
      if (emptyText) emptyText.remove();
      var f = document.createElement("form");
      f.method = "post";
      f.action = "/admin/visual/undo";
      f.setAttribute("data-visual-undo", "");
      var csrf = document.createElement("input");
      csrf.type = "hidden";
      csrf.name = "_csrf";
      csrf.value = CSRF;
      var id = document.createElement("input");
      id.type = "hidden";
      id.name = "id";
      id.value = item.id;
      var b = document.createElement("button");
      b.type = "submit";
      var label = document.createElement("span");
      label.textContent = "撤回 " + (item.label || item.key || "修改");
      var at = document.createElement("small");
      at.textContent = item.at || "刚刚";
      b.appendChild(label);
      b.appendChild(at);
      f.appendChild(csrf);
      f.appendChild(id);
      f.appendChild(b);
      var head = historyBox.querySelector(".group-head");
      if (head && head.nextSibling) historyBox.insertBefore(f, head.nextSibling);
      else historyBox.appendChild(f);
    }
    function addVisualParam(url) {
      try {
        var u = new URL(url, frame.contentWindow.location.href);
        if (u.origin !== frame.contentWindow.location.origin) return url;
        u.searchParams.set("visual_edit", "1");
        return u.pathname + u.search + u.hash;
      } catch (e) {
        return url;
      }
    }
    function setMode(mode) {
      currentMode = mode === "browse" ? "browse" : "edit";
      root.querySelectorAll("[data-visual-mode]").forEach(function (btn) {
        btn.classList.toggle("active", btn.getAttribute("data-visual-mode") === currentMode);
      });
      var doc = frameDoc();
      if (doc && doc.documentElement) doc.documentElement.classList.toggle("ve-browse", currentMode === "browse");
      setStatus(currentMode === "browse" ? "浏览模式：可以在预览里跳转页面。" : "");
    }
    function setSize(size) {
      size = /^(tablet|mobile)$/.test(size) ? size : "desktop";
      root.setAttribute("data-visual-size", size);
      root.querySelectorAll("[data-visual-size]").forEach(function (btn) {
        btn.classList.toggle("active", btn.getAttribute("data-visual-size") === size);
      });
    }
    function saveVisualKey(key, value, label) {
      var data = new URLSearchParams();
      data.set("_csrf", CSRF);
      data.set("lang", root.getAttribute("data-lang") || "");
      data.set("key", key);
      data.set("value", value || "");
      setStatus("正在保存" + (label || "设置") + "...");
      return fetch(root.getAttribute("data-save-url"), {
        method: "POST",
        credentials: "same-origin",
        headers: {"Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8", "X-Requested-With": "XMLHttpRequest"},
        body: data.toString()
      }).then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (j) {
          if (!r.ok) throw j;
          return j;
        });
      });
    }
    function updatePageLabel() {
      if (!pageLabel) return;
      var doc = frameDoc();
      if (!doc || !doc.defaultView) return;
      var path = doc.defaultView.location.pathname || "/";
      var query = doc.defaultView.location.search || "";
      var lang = root.getAttribute("data-lang") || "";
      if (lang && path.indexOf("/" + lang) === 0) path = path.slice(lang.length + 1) || "/";
      if (path === "/") pageLabel.textContent = "首页";
      else if (path.indexOf("/links") === 0 && query.indexOf("cat=") >= 0) pageLabel.textContent = "链接分类页";
      else if (path.indexOf("/links") === 0) pageLabel.textContent = "链接页";
      else if (path.indexOf("/about") === 0) pageLabel.textContent = "关于页";
      else if (path.indexOf("/category/") === 0) pageLabel.textContent = "分类页";
      else if (path.indexOf("/posts/") === 0) pageLabel.textContent = "文章页";
      else if (path.indexOf("/search") === 0) pageLabel.textContent = "搜索页";
      else pageLabel.textContent = "当前页";
    }
    function collectFrameKeys() {
      frameKeys = {};
      var doc = frameDoc();
      if (!doc) return;
      doc.querySelectorAll("[data-visual-edit]").forEach(function (el) {
        frameKeys[el.getAttribute("data-visual-edit")] = true;
      });
      refreshFieldVisibility();
    }
    function injectFrameTools() {
      var doc = frameDoc();
      if (!doc || !doc.body) return;
      if (!doc.getElementById("visual-edit-style")) {
        var style = doc.createElement("style");
        style.id = "visual-edit-style";
        style.textContent = [
          "[data-visual-edit]{outline:2px dashed rgba(154,59,47,.55);outline-offset:4px;cursor:text;border-radius:4px;transition:outline-color .15s,background-color .15s}",
          "[data-visual-edit]:hover{outline-color:#9a3b2f;background:rgba(154,59,47,.07)}",
          "[data-visual-edit].ve-selected{outline:3px solid #9a3b2f;background:rgba(154,59,47,.10)}",
          ".ve-browse [data-visual-edit]{outline-color:transparent;cursor:pointer;background:transparent}",
          ".ve-browse [data-visual-edit]:hover{outline-color:rgba(154,59,47,.22)}"
        ].join("");
        doc.head.appendChild(style);
      }
      doc.documentElement.classList.toggle("ve-browse", currentMode === "browse");
      doc.body.addEventListener("click", function (e) {
        if (currentMode === "browse") {
          var link = e.target.closest && e.target.closest("a[href]");
          if (link && !link.target) {
            try {
              var u = new URL(link.href);
              if (u.origin === doc.defaultView.location.origin) {
                e.preventDefault();
                e.stopPropagation();
                doc.defaultView.location.href = addVisualParam(link.href);
              }
            } catch (err) {}
          }
          return;
        }
        var target = e.target.closest && e.target.closest("[data-visual-edit]");
        if (!target) return;
        e.preventDefault();
        e.stopPropagation();
        var key = target.getAttribute("data-visual-edit");
        var kind = target.getAttribute("data-visual-kind") || (fieldButton(key) && fieldButton(key).getAttribute("data-kind")) || "text";
        var rawValue = target.getAttribute("data-visual-value");
        var val = rawValue !== null ? rawValue : (kind === "image" ? (target.getAttribute("src") || "") : target.innerText.trim());
        selectField(key, target.getAttribute("data-visual-label"), val, false);
      }, true);
      collectFrameKeys();
      updatePageLabel();
      if (pendingScroll !== null && doc.defaultView) {
        setTimeout(function () { doc.defaultView.scrollTo(0, pendingScroll); pendingScroll = null; }, 0);
      }
      if (selectedKey) markSelected(selectedKey);
    }
    if (frame) frame.addEventListener("load", injectFrameTools);
    root.querySelectorAll("[data-visual-group-toggle]").forEach(function (toggle) {
      toggle.addEventListener("click", function () {
        var group = toggle.closest("[data-visual-group]");
        setGroupOpen(group, !(group && group.classList.contains("open")));
      });
    });
    root.querySelectorAll("[data-visual-pick]").forEach(function (btn) {
      setButtonThumb(btn, btn.getAttribute("data-value") || "");
      btn.addEventListener("click", function () {
        selectField(
          btn.getAttribute("data-key"),
          btn.getAttribute("data-label"),
          btn.getAttribute("data-value"),
          btn.getAttribute("data-multiline") === "1"
        );
      });
    });
    function sortableButtons(groupID) {
      return Array.prototype.slice.call(root.querySelectorAll('[data-visual-pick][data-group="' + groupID + '"][data-sortable="1"]'));
    }
    function reindexNavButtons() {
      sortableButtons("nav").forEach(function (btn, i) {
        var oldKey = btn.getAttribute("data-key");
        var nextKey = "nav." + i;
        btn.setAttribute("data-key", nextKey);
        if (selectedKey === oldKey) {
          selectedKey = nextKey;
          if (keyInput) keyInput.value = nextKey;
        }
      });
    }
    function saveSortOrder(groupID) {
      var isNav = groupID === "nav";
      var url = isNav ? root.getAttribute("data-reorder-url") : root.getAttribute("data-category-reorder-url");
      if (!url) return;
      var data = new URLSearchParams();
      data.set("_csrf", CSRF);
      data.set("group", groupID);
      sortableButtons(groupID).forEach(function (btn) { data.append("keys", btn.getAttribute("data-key")); });
      setStatus(isNav ? "正在保存导航顺序..." : "正在保存分类顺序...");
      fetch(url, {
        method: "POST",
        credentials: "same-origin",
        headers: {"Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8", "X-Requested-With": "XMLHttpRequest"},
        body: data.toString()
      }).then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (j) {
          if (!r.ok) throw j;
          return j;
        });
      }).then(function (j) {
        if (isNav) reindexNavButtons();
        markSelected(selectedKey);
        setStatus((j && j.message) || (isNav ? "导航顺序已保存。" : "分类顺序已保存。"));
        reloadFrame(true);
      }).catch(function (err) {
        setStatus((err && (err.message || err.error)) || (isNav ? "导航顺序保存失败。" : "分类顺序保存失败。"), true);
      });
    }
    function dragAfterSort(groupID, y) {
      var after = null;
      sortableButtons(groupID).forEach(function (btn) {
        if (btn === draggedSort || btn.hidden) return;
        var rect = btn.getBoundingClientRect();
        if (!after && y < rect.top + rect.height / 2) after = btn;
      });
      return after;
    }
    function initSortableGroup(groupID) {
      var group = root.querySelector('[data-visual-group="' + groupID + '"]');
      if (!group) return;
      group.addEventListener("dragstart", function (e) {
        var btn = e.target.closest && e.target.closest('[data-visual-pick][data-group="' + groupID + '"][data-sortable="1"]');
        if (!btn) return;
        draggedSort = btn;
        if (e.dataTransfer) {
          e.dataTransfer.effectAllowed = "move";
          e.dataTransfer.setData("text/plain", btn.getAttribute("data-key") || "");
        }
        setTimeout(function () { btn.classList.add("dragging"); }, 0);
      });
      group.addEventListener("dragover", function (e) {
        if (!draggedSort) return;
        e.preventDefault();
        var after = dragAfterSort(groupID, e.clientY);
        if (after) group.insertBefore(draggedSort, after);
        else group.appendChild(draggedSort);
      });
      group.addEventListener("drop", function (e) {
        if (!draggedSort) return;
        e.preventDefault();
        saveSortOrder(groupID);
      });
      group.addEventListener("dragend", function () {
        if (draggedSort) draggedSort.classList.remove("dragging");
        draggedSort = null;
      });
    }
    ["nav", "categorynav", "linkcatnav"].forEach(initSortableGroup);
    root.querySelectorAll("[data-visual-mode]").forEach(function (btn) {
      btn.addEventListener("click", function () { setMode(btn.getAttribute("data-visual-mode")); });
    });
    root.querySelectorAll("[data-visual-size]").forEach(function (btn) {
      btn.addEventListener("click", function () { setSize(btn.getAttribute("data-visual-size")); });
    });
    var widthDD = root.querySelector("[data-visual-width]");
    if (widthDD) {
      widthDD.addEventListener("dd:change", function (e) {
        saveVisualKey("layout.width", e.detail.value || "", "页面宽度").then(function () {
          setStatus("页面宽度已保存，正在刷新预览。");
          reloadFrame(true);
        }).catch(function (err) {
          setStatus((err && (err.message || err.error)) || "页面宽度保存失败。", true);
        });
      });
    }
    if (currentToggle) currentToggle.addEventListener("change", refreshFieldVisibility);
    if (valueInput) valueInput.addEventListener("input", function () {
      if (currentKind() === "image") setImagePreview(valueInput.value.trim());
      validate();
    });
    if (uploadBtn && fileInput) uploadBtn.addEventListener("click", function () { fileInput.click(); });
    if (fileInput) fileInput.addEventListener("change", function () {
      if (!fileInput.files || !fileInput.files[0]) return;
      setStatus("上传中...");
      uploadFile(fileInput.files[0]).then(function (res) {
        if (res.ok && res.j.url) {
          valueInput.value = res.j.url;
          setImagePreview(res.j.url);
          validate();
          setStatus("已上传，保存后生效。");
        } else {
          setStatus((res.j && res.j.error) || "上传失败。", true);
        }
      }).catch(function () {
        setStatus("上传失败。", true);
      });
      fileInput.value = "";
    });
    if (resetBtn && frame) resetBtn.addEventListener("click", function () {
      setStatus("已重新载入预览。");
      reloadFrame(false);
    });
    if (form) form.addEventListener("submit", function (e) {
      e.preventDefault();
      if (!validate()) return;
      saving = true;
      if (saveBtn) saveBtn.disabled = true;
      setStatus("保存中...");
      var data = new URLSearchParams(new FormData(form));
      fetch(root.getAttribute("data-save-url"), {
        method: "POST",
        credentials: "same-origin",
        headers: {"Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8", "X-Requested-With": "XMLHttpRequest"},
        body: data.toString()
      }).then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (j) {
          if (!r.ok) throw j;
          return j;
        });
      }).then(function (j) {
        syncSelectedValue(valueInput ? valueInput.value : "");
        addHistoryItem(j && j.history);
        originalValue = valueInput ? valueInput.value : "";
        validate();
        setStatus("已保存，正在刷新预览。");
        reloadFrame(true);
      }).catch(function (err) {
        setStatus((err && (err.message || err.error)) || "保存失败，请稍后重试。", true);
      }).finally(function () {
        saving = false;
        validate();
      });
    });
    if (historyBox) historyBox.addEventListener("submit", function (e) {
      var f = e.target.closest && e.target.closest("[data-visual-undo]");
      if (!f) return;
      e.preventDefault();
      var data = new URLSearchParams(new FormData(f));
      fetch(f.action, {
        method: "POST",
        credentials: "same-origin",
        headers: {"Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8", "X-Requested-With": "XMLHttpRequest"},
        body: data.toString()
      }).then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (j) {
          if (!r.ok) throw j;
          return j;
        });
      }).then(function (j) {
        f.remove();
        if (j && j.key) {
          selectedKey = j.key;
          syncSelectedValue(j.value || "");
          if (valueInput && keyInput && keyInput.value === j.key) {
            valueInput.value = j.value || "";
            originalValue = valueInput.value;
            validate();
          }
        }
        setStatus("已撤回，正在刷新预览。");
        reloadFrame(true);
      }).catch(function (err) {
        setStatus((err && (err.message || err.error)) || "撤回失败，请稍后重试。", true);
      });
    });
    setMode("edit");
    setSize("desktop");
    refreshFieldVisibility();
    validate();
  })();

  /* 社交链接行：增 / 删 */
  (function () {
    var box = document.querySelector("[data-social-rows]");
    var addBtn = document.querySelector("[data-social-add]");
    if (!box || !addBtn) return;
    addBtn.addEventListener("click", function () {
      var row = document.createElement("div");
      row.className = "social-row";
      row.innerHTML = '<input name="social_url" placeholder="https://github.com/you 或 mailto:you@x.com" inputmode="url"><input name="social_label" placeholder="名称（可选）"><button type="button" class="social-del" data-social-del data-confirm="删除这条社交链接？保存站点信息后生效。" title="删除" aria-label="删除"><svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/><path d="M10 11v6M14 11v6"/></svg></button>';
      box.appendChild(row);
      var inp = row.querySelector("input"); if (inp) inp.focus();
    });
    box.addEventListener("click", function (e) {
      var del = e.target.closest("[data-social-del]");
      if (del) { var r = del.closest(".social-row"); if (r) r.remove(); }
    });
  })();

  /* 站外链接策略：域名规则增 / 删 */
  (function () {
    var list = document.querySelector("[data-external-rule-list]");
    var tpl = document.querySelector("[data-external-rule-template]");
    var addBtn = document.querySelector("[data-external-rule-add]");
    if (!list || !tpl || !addBtn) return;
    var next = parseInt(list.getAttribute("data-next-index") || "0", 10);
    if (!isFinite(next) || next < 0) next = 0;
    addBtn.addEventListener("click", function () {
      var html = tpl.innerHTML.replace(/__INDEX__/g, String(next++));
      list.setAttribute("data-next-index", String(next));
      var wrap = document.createElement("div");
      wrap.innerHTML = html.trim();
      var row = wrap.firstElementChild;
      if (!row) return;
      list.appendChild(row);
      var input = row.querySelector("input[inputmode='url']");
      if (input) input.focus();
    });
    list.addEventListener("click", function (e) {
      var btn = e.target.closest("[data-external-rule-del]");
      if (!btn) return;
      var row = btn.closest("[data-external-rule]");
      if (row) row.remove();
    });
  })();

  /* 导航菜单：常用目标 / 自定义路径 / 增删 / 拖动排序 */
  (function () {
    var box = document.querySelector("[data-menu-rows]");
    var addBtn = document.querySelector("[data-menu-add]");
    if (!box) return;
    var langs = (box.getAttribute("data-langs") || "").split(",").filter(Boolean);
    var names = (box.getAttribute("data-lang-names") || "").split(",");
    var livePreview = document.querySelector("[data-menu-live-preview]");
    var previewList = livePreview ? livePreview.querySelector("[data-menu-preview-list]") : null;
    var previewLang = (livePreview && livePreview.getAttribute("data-preview-lang")) || langs[0] || "";
    var targets = [];
    try { targets = JSON.parse(box.getAttribute("data-targets") || "[]") || []; } catch (e) { targets = []; }
    var trash = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/><path d="M10 11v6M14 11v6"/></svg>';
    function text(name, fallback) {
      return box.getAttribute("data-text-" + name) || fallback;
    }
    function esc(s) {
      return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
        return ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c];
      });
    }
    function optionByValue(value) {
      for (var i = 0; i < targets.length; i++) {
        if (targets[i].value === value) return targets[i];
      }
      return targets[0] || { value: "__custom__", label: "自定义站内路径", kind: "custom", url: "", labels: {} };
    }
    function targetLabels(opt) {
      return (opt && opt.labels && typeof opt.labels === "object") ? opt.labels : {};
    }
    function optionLabelsFromLI(li) {
      if (!li) return {};
      try { return JSON.parse(li.getAttribute("data-labels") || "{}") || {}; } catch (e) { return {}; }
    }
    function labelsHTML(values) {
      values = values || {};
      return langs.map(function (code, i) {
        return '<label class="menu-label"><span class="ml-lang">' + esc(names[i] || code) + '</span><input name="nav_label_' + esc(code) + '" value="' + esc(values[code] || "") + '" placeholder="' + esc(text("label-placeholder", "名称")) + '"></label>';
      }).join("");
    }
    function targetDropdownHTML(selected) {
      var cur = optionByValue(selected || "__custom__");
      var items = targets.map(function (t) {
        var labels = JSON.stringify(targetLabels(t));
        return '<li data-value="' + esc(t.value) + '" data-url="' + esc(t.url || "") + '" data-kind="' + esc(t.kind || "preset") + '" data-labels="' + esc(labels) + '" aria-selected="' + (t.value === cur.value ? "true" : "false") + '">' + esc(t.label || t.value) + '</li>';
      }).join("");
      return '<div class="dropdown menu-target" data-menu-target><button type="button" class="dd-toggle" aria-haspopup="listbox" aria-expanded="false"><span class="dd-label">' + esc(cur.label || cur.value) + '</span><span class="dd-caret" aria-hidden="true">⌄</span></button><input type="hidden" class="menu-target-value" value="' + esc(cur.value) + '"><ul class="dd-menu" role="listbox">' + items + '</ul></div>';
    }
    function setPreview(row, value) {
      var path = row.querySelector("[data-menu-path]");
      if (path) path.textContent = (value || "").trim() || text("pending", "待填写");
    }
    function rowInputByLang(row, code) {
      var input = null;
      [].some.call(row.querySelectorAll(".menu-label input"), function (el) {
        if (el.name === "nav_label_" + code) { input = el; return true; }
        return false;
      });
      return input;
    }
    function rowLabel(row) {
      var input = previewLang ? rowInputByLang(row, previewLang) : null;
      var label = input ? input.value.trim() : "";
      if (!label) {
        for (var i = 0; i < langs.length; i++) {
          input = rowInputByLang(row, langs[i]);
          label = input ? input.value.trim() : "";
          if (label) break;
        }
      }
      if (!label) {
        var ddLabel = row.querySelector("[data-menu-target] .dd-label");
        label = ddLabel ? ddLabel.textContent.trim() : "";
      }
      if (!label) {
        var hidden = row.querySelector(".menu-url");
        label = hidden && hidden.value ? hidden.value.trim() : text("unnamed", "未命名");
      }
      return label;
    }
    function rowURL(row) {
      var hidden = row.querySelector(".menu-url");
      return hidden ? hidden.value.trim() : "";
    }
    function isExternalMenuRow(row) {
      var url = rowURL(row);
      var val = (row.querySelector(".menu-target-value") || {}).value || "";
      return val === "__external__" || /^https?:\/\//.test(url) || /^mailto:/i.test(url);
    }
    function updateMenuPreview() {
      if (!previewList) return;
      var rows = [].slice.call(box.querySelectorAll(".menu-row"));
      if (!rows.length) {
        previewList.innerHTML = '<span class="menu-preview-empty">' + esc(text("empty", "暂无菜单项")) + '</span>';
        return;
      }
      previewList.innerHTML = rows.map(function (row) {
        var label = rowLabel(row);
        var url = rowURL(row) || text("pending", "待填写");
        var ext = isExternalMenuRow(row);
        return '<span class="menu-preview-item' + (ext ? ' is-external' : '') + '" title="' + esc(url) + '"><span>' + esc(label) + '</span>' + (ext ? '<span class="menu-preview-ext" aria-hidden="true">↗</span>' : '') + '</span>';
      }).join("");
    }
    function markMenuDirty() {
      var form = box.closest("form");
      if (form) form.dispatchEvent(new Event("input", { bubbles: true }));
    }
    function setLabels(row, labels) {
      if (!labels || !Object.keys(labels).length) return;
      langs.forEach(function (code) {
        var input = rowInputByLang(row, code);
        if (input) input.value = labels[code] || "";
      });
    }
    function decodeMenuValue(value) {
      try {
        return decodeURIComponent(value);
      } catch (err) {
        return value;
      }
    }
    function cleanCustomMenuURL(value) {
      value = String(value == null ? "" : value).trim();
      if (!value) return "";
      var decoded = decodeMenuValue(value).trim();
      var placeholder = text("custom-url-placeholder", "/docs 或 https://example.com").trim();
      var customLabel = text("custom-url", "自定义地址").trim();
      if (value === placeholder || decoded === placeholder || decoded === customLabel) return "";
      return value;
    }
    function syncCustomURL(row) {
      var custom = row.querySelector(".menu-custom-url");
      var hidden = row.querySelector(".menu-url");
      if (!custom || !hidden) return;
      var cleaned = cleanCustomMenuURL(custom.value);
      if (custom.value.trim() !== cleaned) custom.value = cleaned;
      hidden.value = cleaned;
      setPreview(row, hidden.value);
      updateMenuPreview();
    }
    function applyTarget(row, value, fillLabels) {
      var selected = null;
      [].some.call(row.querySelectorAll("[data-menu-target] li"), function (li) {
        if (li.getAttribute("data-value") === value) { selected = li; return true; }
        return false;
      });
      var opt = optionByValue(value);
      var kind = (selected && selected.getAttribute("data-kind")) || opt.kind || "preset";
      var url = (selected && selected.getAttribute("data-url")) || opt.url || "";
      var hidden = row.querySelector(".menu-url");
      var customWrap = row.querySelector("[data-menu-custom-wrap]");
      var custom = row.querySelector(".menu-custom-url");
      var isCustom = kind === "custom" || kind === "external";
      if (customWrap) customWrap.hidden = !isCustom;
      if (hidden) {
        if (isCustom) {
          if (fillLabels && row.dataset.customTarget !== "1" && custom) custom.value = "";
          var customURL = custom ? cleanCustomMenuURL(custom.value) : "";
          if (custom && custom.value.trim() !== customURL) custom.value = customURL;
          hidden.value = customURL;
        } else {
          hidden.value = url;
          if (custom) custom.value = url;
        }
        setPreview(row, hidden.value);
      }
      row.dataset.customTarget = isCustom ? "1" : "0";
      if (fillLabels && !isCustom) setLabels(row, selected ? optionLabelsFromLI(selected) : targetLabels(opt));
      if (isCustom && custom && fillLabels) custom.focus();
      updateMenuPreview();
    }
    if (addBtn) addBtn.addEventListener("click", function () {
      var row = document.createElement("div");
      row.className = "menu-row";
      row.dataset.customTarget = "1";
      row.innerHTML = '<span class="drag-handle" aria-hidden="true">⠿</span><div class="menu-fields"><div class="menu-main"><div class="menu-target-wrap"><label>' + esc(text("target", "指向哪里")) + '</label>' + targetDropdownHTML("__custom__") + '</div><div class="menu-path-preview"><span>' + esc(text("path", "路径")) + '</span><code data-menu-path>' + esc(text("pending", "待填写")) + '</code></div></div><input class="menu-url" type="hidden" name="nav_url"><div class="menu-custom-wrap" data-menu-custom-wrap><label>' + esc(text("custom-url", "自定义地址")) + '</label><input class="menu-custom-url" placeholder="' + esc(text("custom-url-placeholder", "/docs 或 https://example.com")) + '" inputmode="url"></div><details class="menu-label-details" open><summary>' + esc(text("labels", "菜单文字")) + '</summary><div class="menu-labels">' + labelsHTML() + '</div></details></div><button type="button" class="menu-del" data-menu-del data-confirm="' + esc(text("delete-confirm", "删除这个菜单项？保存菜单后生效。")) + '" title="' + esc(text("delete", "删除")) + '" aria-label="' + esc(text("delete", "删除")) + '">' + trash + '</button>';
      box.appendChild(row);
      if (window.adminInitDropdown) window.adminInitDropdown(row.querySelector(".dropdown"));
      applyTarget(row, "__custom__", false);
      updateMenuPreview();
      markMenuDirty();
      var inp = row.querySelector(".menu-custom-url"); if (inp) inp.focus();
    });
    box.addEventListener("click", function (e) {
      var del = e.target.closest("[data-menu-del]");
      if (del) { var r = del.closest(".menu-row"); if (r) r.remove(); updateMenuPreview(); markMenuDirty(); }
    });
    box.addEventListener("dd:change", function (e) {
      var dd = e.target.closest("[data-menu-target]");
      if (!dd) return;
      var row = dd.closest(".menu-row");
      if (row) applyTarget(row, e.detail.value, true);
    });
    box.addEventListener("input", function (e) {
      if (e.target.closest(".menu-custom-url")) {
        var row = e.target.closest(".menu-row");
        if (row) syncCustomURL(row);
      } else if (e.target.closest(".menu-label input")) {
        updateMenuPreview();
      }
    });
    [].forEach.call(box.querySelectorAll(".menu-row"), function (row) {
      var dd = row.querySelector("[data-menu-target]");
      var val = dd ? (dd.querySelector(".menu-target-value") || {}).value : "";
      applyTarget(row, val || "__custom__", false);
    });
    updateMenuPreview();
    // 仅在按下手柄时让该行可拖；其余区域可正常编辑输入框
    box.addEventListener("mousedown", function (e) {
      var inHandle = e.target.closest(".drag-handle");
      [].forEach.call(box.querySelectorAll(".menu-row"), function (r) { r.draggable = false; });
      if (inHandle) { var r = inHandle.closest(".menu-row"); if (r) r.draggable = true; }
    });
    var dragEl = null;
    box.addEventListener("dragstart", function (e) {
      var r = e.target.closest(".menu-row");
      if (!r || !r.draggable) return;
      dragEl = r; setTimeout(function () { r.classList.add("dragging"); }, 0);
    });
    box.addEventListener("dragend", function () {
      if (dragEl) { dragEl.classList.remove("dragging"); dragEl.draggable = false; }
      dragEl = null;
      markMenuDirty();
    });
    box.addEventListener("dragover", function (e) {
      if (!dragEl) return;
      e.preventDefault();
      var rows = [].slice.call(box.querySelectorAll(".menu-row")), after = null;
      for (var i = 0; i < rows.length; i++) {
        if (rows[i] === dragEl) continue;
        var rect = rows[i].getBoundingClientRect();
        if (e.clientY < rect.top + rect.height / 2) { after = rows[i]; break; }
      }
      if (after) box.insertBefore(dragEl, after); else box.appendChild(dragEl);
      updateMenuPreview();
    });
  })();

  /* ---------- 主题微调：实时标签 + 切卡同步（按主题） ---------- */
  var colorIn = document.getElementById("theme_accent");
  var hexOut = document.querySelector(".color-hex");
  var radiusIn = document.getElementById("theme_radius");
  var radiusOut = document.querySelector(".radius-val");
  var customCb = document.querySelector('input[name="theme_custom"]');
  if (colorIn && hexOut) colorIn.addEventListener("input", function () { hexOut.textContent = colorIn.value; });
  if (radiusIn && radiusOut) radiusIn.addEventListener("input", function () { radiusOut.textContent = radiusIn.value + "px"; });
  document.querySelectorAll('.theme-picker input[type="radio"][name="theme"]').forEach(function (rb) {
    rb.addEventListener("change", function () {
      if (colorIn && rb.dataset.accent) { colorIn.value = rb.dataset.accent; if (hexOut) hexOut.textContent = rb.dataset.accent; }
      if (radiusIn && rb.dataset.radius !== undefined) { radiusIn.value = rb.dataset.radius; if (radiusOut) radiusOut.textContent = rb.dataset.radius + "px"; }
      if (customCb) customCb.checked = rb.dataset.custom === "1";
      document.querySelectorAll(".theme-card").forEach(function (c) { c.classList.remove("sel"); });
      var card = rb.closest(".theme-card"); if (card) card.classList.add("sel");
      var curEl = document.querySelector("[data-cur-theme]");
      if (curEl && rb.dataset.themeName) curEl.textContent = rb.dataset.themeName;
    });
  });

  /* 主题真实缩略图：进入视口附近再加载，加载前显示骨架态 */
  (function () {
    var previews = Array.prototype.slice.call(document.querySelectorAll("[data-theme-preview]"));
    if (!previews.length) return;

    function loadPreview(box) {
      if (!box || box.dataset.loaded === "1") return;
      var frame = box.querySelector("iframe[data-src]");
      if (!frame) return;
      box.dataset.loaded = "1";
      box.classList.add("is-loading");
      frame.addEventListener("load", function () {
        box.classList.remove("is-loading");
        box.classList.add("is-loaded");
      }, { once: true });
      frame.addEventListener("error", function () {
        box.classList.remove("is-loading");
        box.classList.add("is-error");
      }, { once: true });
      frame.src = frame.dataset.src;
    }

    if ("IntersectionObserver" in window) {
      var observer = new IntersectionObserver(function (entries) {
        entries.forEach(function (entry) {
          if (!entry.isIntersecting) return;
          loadPreview(entry.target);
          observer.unobserve(entry.target);
        });
      }, { rootMargin: "240px 0px" });
      previews.forEach(function (box) { observer.observe(box); });
    } else {
      previews.forEach(function (box, i) {
        setTimeout(function () { loadPreview(box); }, i * 90);
      });
    }
  })();

  /* 主题放大预览：卡片角上的放大按钮 → 弹窗大图（缩放适配），可直接「选用此主题」 */
  (function () {
    var modal = document.querySelector("[data-tp-modal]");
    if (!modal) return;
    var frame = modal.querySelector("[data-tp-frame]");
    var stage = modal.querySelector("[data-tp-stage]");
    var titleEl = modal.querySelector("[data-tp-title]");
    var selectBtn = modal.querySelector("[data-tp-select]");
    var FW = 1120, FH = 720, curId = null;
    function fit() {
      if (modal.hidden || !stage) return;
      var w = stage.clientWidth;
      if (!w) return; // 尚未布局：fitSoon / ResizeObserver 会再触发
      var s = w / FW;
      frame.style.transform = "scale(" + s + ")";
      stage.style.height = Math.round(FH * s) + "px";
    }
    function fitSoon() { // 弹窗刚显示时轮询到舞台有宽度再缩放（setTimeout 在后台标签页也会跑）
      var n = 0;
      (function loop() {
        if (modal.hidden) return;
        if (stage && stage.clientWidth) { fit(); return; }
        if (n++ < 30) setTimeout(loop, 60);
      })();
    }
    if (window.ResizeObserver && stage) new ResizeObserver(fit).observe(stage);
    function open(id, name) {
      curId = id;
      if (titleEl && name) titleEl.textContent = name;
      frame.src = "/admin/theme-preview/" + encodeURIComponent(id);
      modal.hidden = false;
      fitSoon(); // 等布局完成后再按舞台宽度缩放
    }
    if (frame) frame.addEventListener("load", fit);
    function close() { modal.hidden = true; frame.src = "about:blank"; curId = null; }
    document.querySelectorAll("[data-tp-open]").forEach(function (btn) {
      btn.addEventListener("click", function (e) {
        e.preventDefault(); e.stopPropagation();
        open(btn.getAttribute("data-tp-id"), btn.getAttribute("data-tp-name"));
      });
    });
    modal.querySelectorAll("[data-tp-close]").forEach(function (c) { c.addEventListener("click", close); });
    if (selectBtn) selectBtn.addEventListener("click", function () {
      if (curId) {
        var r = document.querySelector('.theme-picker input[type=radio][value="' + curId + '"]');
        if (r) { r.checked = true; r.dispatchEvent(new Event("change", { bubbles: true })); }
      }
      close();
    });
    document.addEventListener("keydown", function (e) { if (e.key === "Escape" && !modal.hidden) close(); });
    window.addEventListener("resize", fit);
  })();

  /* ---------- Markdown ⇄ 富文本编辑器 ---------- */
  initEditor();
  function initEditor() {
    var form = document.getElementById("post-form") || document.getElementById("ext-form");
    if (!form) return;
    var editor = form.querySelector("[data-editor]");
    if (!editor) return;
    var textarea = editor.querySelector("textarea");
    var richWrap = editor.querySelector(".rich-wrap");
    var rich = editor.querySelector(".rich");
    var segBtns = Array.prototype.slice.call(form.querySelectorAll(".seg-btn"));
    var bubble = document.querySelector(".bubble");
    var linkPop = document.querySelector(".link-pop");
    // 待上传图片：粘贴/插入时先以本地 blob 预览，提交保存时才统一上传（key=blobURL → File）
    var pendingImages = new Map();
    if (bubble) document.body.appendChild(bubble);
    if (linkPop) document.body.appendChild(linkPop);
    var mode = "markdown";
    var modeInput = form.querySelector('input[name="editor_mode"]');

    function markSeg() { segBtns.forEach(function (b) { b.classList.toggle("is-on", b.dataset.mode === mode); }); }
    function toRich() {
      return fetch("/admin/render", { method: "POST", body: textarea.value, credentials: "same-origin", headers: { "Content-Type": "text/plain;charset=utf-8" } })
        .then(function (r) { return r.text(); })
        .then(function (html) { rich.innerHTML = html.trim() || "<p><br></p>"; });
    }
    function toMarkdown() { textarea.value = htmlToMarkdown(rich); }
    function setMode(m) {
      if (m === mode) return;
      if (modeInput) modeInput.value = m;
      if (m === "rich") {
        toRich().then(function () { textarea.hidden = true; richWrap.hidden = false; mode = "rich"; markSeg(); });
      } else {
        toMarkdown(); richWrap.hidden = true; textarea.hidden = false; mode = "markdown"; markSeg(); hideBubble(); if (linkPop) linkPop.hidden = true;
      }
    }
    segBtns.forEach(function (b) { b.addEventListener("click", function () { setMode(b.dataset.mode); }); });

    // 提交保存时：富文本里仍是本地 blob 的图片，此刻才统一上传并替换为服务端 URL，再转 Markdown 提交。
    function uploadPending(img) {
      var src = img.getAttribute("src") || "";
      var file = pendingImages.get(src);
      if (!file) { img.removeAttribute("data-pending"); return Promise.resolve(); }
      return uploadFile(file).then(function (res) {
        if (res.ok && res.j && res.j.url) {
          img.setAttribute("src", res.j.url);
          pendingImages.delete(src);
          try { URL.revokeObjectURL(src); } catch (e) {}
        }
        img.removeAttribute("data-pending");
      }).catch(function () { img.removeAttribute("data-pending"); });
    }
    form.addEventListener("submit", function (e) {
      if (mode !== "rich") return;
      var pend = rich ? Array.prototype.slice.call(rich.querySelectorAll('img[data-pending="1"]')) : [];
      if (pend.length) {
        e.preventDefault();
        Promise.all(pend.map(uploadPending)).then(function () {
          toMarkdown();
          if (typeof form.requestSubmit === "function") form.requestSubmit(); else form.submit();
        });
        return;
      }
      toMarkdown();
    });
    // 记住上次编辑方式：进入时若为富文本则自动切换
    if (modeInput && modeInput.value === "rich") setMode("rich");

    // 粘贴剪贴板图片 → 本地 blob 预览（保存时才上传）
    if (rich) rich.addEventListener("paste", function (e) {
      var items = (e.clipboardData && e.clipboardData.items) || [];
      for (var i = 0; i < items.length; i++) {
        if (items[i].type && items[i].type.indexOf("image") === 0) {
          var file = items[i].getAsFile();
          if (file) {
            e.preventDefault();
            var url = URL.createObjectURL(file);
            pendingImages.set(url, file);
            document.execCommand("insertHTML", false, '<img src="' + url + '" data-pending="1" alt="">');
          }
          return;
        }
      }
    });

    /* 气泡工具栏 */
    function hideBubble() { if (bubble) bubble.hidden = true; }
    function showBubble() {
      if (linkPop && !linkPop.hidden) return;
      var sel = window.getSelection();
      if (!sel.rangeCount || sel.isCollapsed) { hideBubble(); return; }
      var range = sel.getRangeAt(0);
      if (!rich.contains(range.commonAncestorContainer)) { hideBubble(); return; }
      var rect = range.getBoundingClientRect();
      if (!rect.width) { hideBubble(); return; }
      bubble.hidden = false; // 先显示以测量高度
      var below = rect.top < (bubble.offsetHeight + 76); // 上方空间不足（含吸顶栏）→ 翻到选区下方
      bubble.classList.toggle("below", below);
      bubble.style.left = (rect.left + rect.width / 2 + window.scrollX) + "px";
      bubble.style.top = ((below ? rect.bottom : rect.top) + window.scrollY) + "px";
    }
    // 选区「完成」时再显示气泡：松开鼠标 / 用 Shift+方向键选择。避免拖动过程中频繁闪烁导致选区被打断。
    if (rich) {
      rich.addEventListener("mouseup", function () { if (mode === "rich") setTimeout(showBubble, 0); });
      rich.addEventListener("keyup", function (e) {
        if (mode !== "rich") return;
        if (e.shiftKey || e.key.indexOf("Arrow") === 0) showBubble(); else hideBubble();
      });
    }
    // 点击编辑器与气泡之外时收起气泡
    document.addEventListener("mousedown", function (e) {
      if (bubble && !bubble.hidden && !bubble.contains(e.target) && !(rich && rich.contains(e.target))) hideBubble();
    });

    function curBlock() {
      var s = window.getSelection(); if (!s.rangeCount) return null;
      var n = s.getRangeAt(0).startContainer; if (n.nodeType === 3) n = n.parentNode;
      while (n && n !== rich) { var t = n.tagName && n.tagName.toLowerCase(); if (["h2", "h3", "blockquote", "p", "li"].indexOf(t) >= 0) return n; n = n.parentNode; }
      return null;
    }
    function fmtBlock(tag) {
      var b = curBlock(); var cur = b ? b.tagName.toLowerCase() : "";
      document.execCommand("formatBlock", false, cur === tag ? "p" : tag);
    }

    /* 链接弹层（替代系统 prompt） */
    var savedRange = null;
    var linkInput = linkPop && linkPop.querySelector(".link-input");
    function openLinkPop() {
      var sel = window.getSelection(); if (!sel.rangeCount) return;
      savedRange = sel.getRangeAt(0).cloneRange();
      var rect = savedRange.getBoundingClientRect();
      hideBubble();
      linkPop.hidden = false;
      var below = rect.top < (linkPop.offsetHeight + 76);
      linkPop.classList.toggle("below", below);
      linkPop.style.left = (rect.left + rect.width / 2 + window.scrollX) + "px";
      linkPop.style.top = ((below ? rect.bottom : rect.top) + window.scrollY) + "px";
      linkInput.value = ""; setTimeout(function () { linkInput.focus(); }, 0);
    }
    function applyLink() {
      var url = linkInput.value.trim();
      if (url && savedRange) {
        var sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(savedRange);
        document.execCommand("createLink", false, url);
      }
      linkPop.hidden = true;
    }
    function closeLinkPop() { if (linkPop) linkPop.hidden = true; }
    if (linkPop) {
      linkPop.querySelector(".link-apply").addEventListener("click", applyLink);
      var linkCloseBtn = linkPop.querySelector(".link-close");
      if (linkCloseBtn) linkCloseBtn.addEventListener("click", closeLinkPop);
      linkInput.addEventListener("keydown", function (e) {
        if (e.key === "Enter") { e.preventDefault(); applyLink(); }
        else if (e.key === "Escape") { e.preventDefault(); closeLinkPop(); }
      });
      // 点击弹层外部即关闭
      document.addEventListener("mousedown", function (e) {
        if (!linkPop.hidden && !linkPop.contains(e.target)) closeLinkPop();
      });
      // 全局 Esc 关闭链接弹层与气泡
      document.addEventListener("keydown", function (e) {
        if (e.key === "Escape") { closeLinkPop(); hideBubble(); }
      });
    }

    if (bubble) bubble.querySelectorAll("button").forEach(function (btn) {
      btn.addEventListener("mousedown", function (e) { e.preventDefault(); });
      btn.addEventListener("click", function () {
        var c = btn.getAttribute("data-cmd");
        if (c === "bold") document.execCommand("bold");
        else if (c === "italic") document.execCommand("italic");
        else if (c === "h2") fmtBlock("h2");
        else if (c === "h3") fmtBlock("h3");
        else if (c === "quote") fmtBlock("blockquote");
        else if (c === "link") { openLinkPop(); return; }
        showBubble();
      });
    });

    /* 加号插入菜单（图片 / 表格 / 分割线，SVG 图标） */
    var plus = document.createElement("button");
    plus.type = "button"; plus.className = "plus"; plus.setAttribute("aria-label", "插入");
    plus.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>';
    var menu = document.createElement("div"); menu.className = "plus-menu";
    menu.innerHTML =
      '<button type="button" data-ins="image"><svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="m21 15-5-5L5 21"/></svg>图片</button>' +
      '<button type="button" data-ins="table"><svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18M3 15h18M9 3v18"/></svg>表格</button>' +
      '<button type="button" data-ins="hr"><svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M4 12h16"/></svg>分割线</button>';
    document.body.appendChild(plus); document.body.appendChild(menu);
    var plusBlock = null;
    function placePlus() {
      var b = curBlock();
      if (mode === "rich" && b && b.tagName.toLowerCase() === "p" && !b.textContent.trim()) {
        var rect = b.getBoundingClientRect();
        plus.style.left = (rect.left - 40 + window.scrollX) + "px";
        plus.style.top = (rect.top + window.scrollY) + "px";
        plus.classList.add("show"); plusBlock = b;
      } else { plus.classList.remove("show"); menu.classList.remove("show"); }
    }
    if (rich) { rich.addEventListener("keyup", placePlus); rich.addEventListener("click", placePlus); rich.addEventListener("focus", placePlus); }
    plus.addEventListener("mousedown", function (e) { e.preventDefault(); });
    plus.addEventListener("click", function () {
      var rect = plus.getBoundingClientRect();
      menu.style.left = (rect.left + window.scrollX) + "px";
      menu.style.top = (rect.bottom + 4 + window.scrollY) + "px";
      menu.classList.toggle("show");
    });
    function insertAtBlock(html) {
      if (plusBlock) { plusBlock.outerHTML = html; } else { document.execCommand("insertHTML", false, html); }
      menu.classList.remove("show"); plus.classList.remove("show");
    }
    menu.querySelectorAll("button").forEach(function (btn) {
      btn.addEventListener("mousedown", function (e) { e.preventDefault(); });
      btn.addEventListener("click", function () {
        var kind = btn.getAttribute("data-ins");
        if (kind === "hr") insertAtBlock("<hr><p><br></p>");
        else if (kind === "table") insertAtBlock("<table><thead><tr><th>列一</th><th>列二</th></tr></thead><tbody><tr><td>　</td><td>　</td></tr><tr><td>　</td><td>　</td></tr></tbody></table><p><br></p>");
        else if (kind === "image") {
          var fi = document.createElement("input"); fi.type = "file"; fi.accept = "image/*";
          fi.onchange = function () {
            if (!fi.files || !fi.files[0]) return;
            var url = URL.createObjectURL(fi.files[0]);
            pendingImages.set(url, fi.files[0]);
            insertAtBlock('<img src="' + url + '" data-pending="1" alt=""><p><br></p>');
          };
          fi.click();
        }
      });
    });
    document.addEventListener("click", function (e) {
      if (!plus.contains(e.target) && !menu.contains(e.target)) menu.classList.remove("show");
    });

    /* 表格行列操作把手（光标在表格单元格内时出现） */
    var tableTools = document.createElement("div");
    tableTools.className = "table-tools";
    tableTools.innerHTML =
      '<button type="button" data-tt="addRow">＋行</button>' +
      '<button type="button" data-tt="addCol">＋列</button>' +
      '<button type="button" data-tt="delRow">－行</button>' +
      '<button type="button" data-tt="delCol">－列</button>';
    document.body.appendChild(tableTools);
    var ttCell = null, ttHideTimer;
    function showToolsForCell(cell) {
      ttCell = cell;
      var rect = cell.getBoundingClientRect();
      tableTools.style.left = (rect.left + window.scrollX) + "px";
      tableTools.style.top = (rect.top + window.scrollY - 32) + "px";
      tableTools.classList.add("show");
    }
    function scheduleToolsHide() { clearTimeout(ttHideTimer); ttHideTimer = setTimeout(function () { tableTools.classList.remove("show"); }, 250); }
    // 悬停某行/列的单元格时，把手就近出现在该单元格上方
    if (rich) {
      rich.addEventListener("mouseover", function (e) {
        if (mode !== "rich") return;
        var cell = e.target.closest && e.target.closest("td,th");
        if (cell && rich.contains(cell)) { clearTimeout(ttHideTimer); showToolsForCell(cell); }
      });
      rich.addEventListener("mouseout", function (e) {
        if (e.target.closest && e.target.closest("td,th")) scheduleToolsHide();
      });
    }
    tableTools.addEventListener("mouseenter", function () { clearTimeout(ttHideTimer); });
    tableTools.addEventListener("mouseleave", scheduleToolsHide);
    function cellIndex(c) { return Array.prototype.indexOf.call(c.parentNode.children, c); }
    tableTools.querySelectorAll("button").forEach(function (btn) {
      btn.addEventListener("mousedown", function (e) { e.preventDefault(); });
      btn.addEventListener("click", function () {
        if (!ttCell || !ttCell.closest("table")) return;
        var table = ttCell.closest("table"), row = ttCell.parentNode, ci = cellIndex(ttCell), act = btn.getAttribute("data-tt");
        if (act === "addRow") {
          var nr = document.createElement("tr");
          for (var i = 0; i < row.children.length; i++) { var td = document.createElement("td"); td.innerHTML = "　"; nr.appendChild(td); }
          row.parentNode.insertBefore(nr, row.nextSibling);
        } else if (act === "addCol") {
          Array.prototype.forEach.call(table.querySelectorAll("tr"), function (tr) {
            var head = tr.parentNode.tagName.toLowerCase() === "thead";
            var c = document.createElement(head ? "th" : "td"); c.innerHTML = head ? "列" : "　";
            var ref = tr.children[ci];
            tr.insertBefore(c, ref ? ref.nextSibling : null);
          });
        } else if (act === "delRow") {
          if (row.parentNode.tagName.toLowerCase() === "tbody" && table.querySelectorAll("tbody tr").length > 1) row.remove();
        } else if (act === "delCol") {
          if (table.querySelector("tr").children.length > 1)
            Array.prototype.forEach.call(table.querySelectorAll("tr"), function (tr) { if (tr.children[ci]) tr.children[ci].remove(); });
        }
        if (ttCell && ttCell.closest("table") && rich.contains(ttCell)) showToolsForCell(ttCell);
        else tableTools.classList.remove("show");
      });
    });
    document.addEventListener("click", function (e) {
      if (!rich.contains(e.target) && !tableTools.contains(e.target)) tableTools.classList.remove("show");
    });

    /* 块拖动排序：悬停块左侧浮出把手，按住拖动到目标位置（把手与指示线浮于编辑器外，不写入内容） */
    if (rich) (function blockDrag() {
      var handle = document.createElement("div");
      handle.className = "blk-handle";
      handle.title = "拖动以调整顺序";
      handle.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="6" r="1.6"/><circle cx="15" cy="6" r="1.6"/><circle cx="9" cy="12" r="1.6"/><circle cx="15" cy="12" r="1.6"/><circle cx="9" cy="18" r="1.6"/><circle cx="15" cy="18" r="1.6"/></svg>';
      var drop = document.createElement("div"); drop.className = "blk-drop";
      document.body.appendChild(handle); document.body.appendChild(drop);
      var hoverBlock = null, dragBlock = null, targetBlock = null, after = false, hideTimer, ghost = null;
      function topBlock(node) {
        while (node && node.parentNode !== rich) node = node.parentNode;
        return node && node.parentNode === rich ? node : null;
      }
      function place(block) {
        if (!block || mode !== "rich") { handle.classList.remove("show"); hoverBlock = null; return; }
        var r = block.getBoundingClientRect();
        var cs = getComputedStyle(block);
        var padTop = parseFloat(cs.paddingTop) || 0;
        var lineH = parseFloat(cs.lineHeight); if (!lineH) lineH = (parseFloat(cs.fontSize) || 18) * 1.5;
        var hH = handle.offsetHeight || 24;
        // 垂直对齐到块「第一行」中心（含 padding-top，修正 H2 等大间距块的偏移）
        handle.style.left = (r.left - 28 + window.scrollX) + "px";
        handle.style.top = (r.top + padTop + lineH / 2 - hH / 2 + window.scrollY) + "px";
        handle.classList.add("show"); hoverBlock = block;
      }
      function scheduleHide() { clearTimeout(hideTimer); hideTimer = setTimeout(function () { if (!dragBlock) handle.classList.remove("show"); }, 220); }
      rich.addEventListener("mousemove", function (e) { if (!dragBlock) { clearTimeout(hideTimer); var b = topBlock(e.target); if (b) place(b); } });
      rich.addEventListener("mouseleave", scheduleHide);
      handle.addEventListener("mouseenter", function () { clearTimeout(hideTimer); });
      handle.addEventListener("mouseleave", scheduleHide);
      function onMove(e) {
        if (ghost) { ghost.style.left = (e.clientX + 16) + "px"; ghost.style.top = (e.clientY + 10) + "px"; }
        var blocks = Array.prototype.slice.call(rich.children);
        var y = e.clientY, best = null, aft = false;
        for (var i = 0; i < blocks.length; i++) {
          var r = blocks[i].getBoundingClientRect();
          if (y < r.top + r.height / 2) { best = blocks[i]; aft = false; break; }
          best = blocks[i]; aft = true;
        }
        targetBlock = best; after = aft;
        if (best) {
          var br = best.getBoundingClientRect();
          drop.style.left = (br.left + window.scrollX) + "px";
          drop.style.width = br.width + "px";
          drop.style.top = ((aft ? br.bottom : br.top) + window.scrollY - 1) + "px";
          drop.classList.add("show");
        }
      }
      function onUp() {
        document.removeEventListener("mousemove", onMove);
        document.removeEventListener("mouseup", onUp);
        drop.classList.remove("show"); handle.classList.remove("grabbing");
        document.body.classList.remove("blk-dragging-active");
        if (ghost) { ghost.remove(); ghost = null; }
        if (dragBlock) dragBlock.classList.remove("blk-dragging");
        if (dragBlock && targetBlock && targetBlock !== dragBlock) {
          if (after) targetBlock.after(dragBlock); else targetBlock.before(dragBlock);
          rich.dispatchEvent(new Event("input", { bubbles: true })); // 顺序变化 → 标脏
        }
        dragBlock = null; targetBlock = null; handle.classList.remove("show");
      }
      handle.addEventListener("mousedown", function (e) {
        e.preventDefault();
        dragBlock = hoverBlock; if (!dragBlock) return;
        handle.classList.add("grabbing");
        document.body.classList.add("blk-dragging-active");
        // 创建跟随光标的拖动幻影（克隆源块），再把源块标记为「抬起」
        var r = dragBlock.getBoundingClientRect();
        ghost = dragBlock.cloneNode(true);
        ghost.className = "blk-ghost";
        ghost.style.width = Math.min(r.width, 460) + "px";
        ghost.style.left = (e.clientX + 16) + "px";
        ghost.style.top = (e.clientY + 10) + "px";
        document.body.appendChild(ghost);
        dragBlock.classList.add("blk-dragging");
        document.addEventListener("mousemove", onMove);
        document.addEventListener("mouseup", onUp);
      });
    })();
  }

  /* ---------- 分类模态框 ---------- */
  (function () {
    var modal = document.getElementById("cat-modal");
    if (!modal) return;
    var titleEl = modal.querySelector("#cat-modal-title");
    var idEl = modal.querySelector("#cat-id");
    var nameEl = modal.querySelector("#cat-name");
    var slugEl = modal.querySelector("#cat-slug");
    var descEl = modal.querySelector("#cat-desc");
    function open(edit) {
      if (edit) {
        titleEl.textContent = "编辑分类";
        idEl.value = edit.id; nameEl.value = edit.name; slugEl.value = edit.slug; descEl.value = edit.desc;
      } else {
        titleEl.textContent = "新增分类";
        idEl.value = ""; nameEl.value = ""; slugEl.value = ""; descEl.value = "";
      }
      modal.hidden = false;
      setTimeout(function () { nameEl.focus(); }, 0);
    }
    function close() { modal.hidden = true; }
    var addBtn = document.querySelector("[data-cat-add]");
    if (addBtn) addBtn.addEventListener("click", function () { open(null); });
    document.querySelectorAll("[data-cat-edit]").forEach(function (b) {
      b.addEventListener("click", function () {
        open({ id: b.dataset.id, name: b.dataset.name, slug: b.dataset.slug, desc: b.dataset.desc });
      });
    });
    modal.querySelectorAll("[data-cat-close]").forEach(function (b) { b.addEventListener("click", close); });
    document.addEventListener("keydown", function (e) { if (e.key === "Escape" && !modal.hidden) close(); });
  })();

  /* ---------- 分类“全部”入口模态框 ---------- */
  (function () {
    var modal = document.getElementById("cat-all-modal");
    if (!modal) return;
    var titleEl = modal.querySelector("#cat-all-title");
    var labelEl = modal.querySelector("#cat-all-label");
    var slugEl = modal.querySelector("#cat-all-slug");
    var descEl = modal.querySelector("#cat-all-desc");
    function open(btn) {
      titleEl.value = btn.dataset.title || "";
      labelEl.value = btn.dataset.label || "";
      slugEl.value = btn.dataset.slug || "";
      descEl.value = btn.dataset.desc || "";
      modal.hidden = false;
      setTimeout(function () { titleEl.focus(); titleEl.select(); }, 0);
    }
    function close() { modal.hidden = true; }
    document.querySelectorAll("[data-cat-all-edit]").forEach(function (b) {
      b.addEventListener("click", function () { open(b); });
    });
    modal.querySelectorAll("[data-cat-all-close]").forEach(function (b) { b.addEventListener("click", close); });
    document.addEventListener("keydown", function (e) { if (e.key === "Escape" && !modal.hidden) close(); });
  })();

  /* ---------- 自动化密钥：一次性查看弹窗 ---------- */
  (function () {
    if (window.history && window.history.replaceState && /^\/admin\/settings\/automation\/keys/.test(window.location.pathname)) {
      window.history.replaceState(null, document.title, "/admin/settings/automation");
    }
    var modal = document.querySelector("[data-secret-modal]");
    if (!modal) return;
    var firstInput = modal.querySelector("#new-api-secret");
    function open() {
      modal.hidden = false;
      if (firstInput) setTimeout(function () { firstInput.focus(); firstInput.select(); }, 0);
    }
    function close() {
      modal.hidden = true;
    }
    document.querySelectorAll("[data-secret-open]").forEach(function (btn) {
      btn.addEventListener("click", open);
    });
    modal.querySelectorAll("[data-secret-close]").forEach(function (btn) {
      btn.addEventListener("click", close);
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !modal.hidden) close();
    });
    if (!modal.hidden && firstInput) setTimeout(function () { firstInput.focus(); firstInput.select(); }, 0);
  })();

  /* ---------- 自动化权限：全选 ---------- */
  (function () {
    document.querySelectorAll("[data-scope-tools]").forEach(function (tools) {
      var form = tools.closest("form");
      if (!form) return;
      var checks = Array.prototype.slice.call(form.querySelectorAll('input[name="scopes"]'));
      var all = tools.querySelector("[data-scope-all]");
      if (!all) return;
      function enabledChecks() {
        return checks.filter(function (input) { return !input.disabled; });
      }
      function update() {
        var enabled = enabledChecks();
        var checked = enabled.filter(function (input) { return input.checked; }).length;
        all.checked = enabled.length > 0 && checked === enabled.length;
        all.indeterminate = checked > 0 && checked < enabled.length;
        all.disabled = enabled.length === 0;
      }
      all.addEventListener("change", function () {
        var shouldCheck = all.checked;
        all.indeterminate = false;
        enabledChecks().forEach(function (input) {
          input.checked = shouldCheck;
        });
        update();
        form.dispatchEvent(new Event("input", { bubbles: true }));
      });
      checks.forEach(function (input) {
        input.addEventListener("change", update);
      });
      form.addEventListener("input", function (e) {
        if (e.target === all) return;
        update();
      });
      form.addEventListener("change", function (e) {
        if (e.target === all) return;
        update();
      });
      update();
    });
  })();

  /* ---------- 自动化密钥：修改用途和权限 ---------- */
  (function () {
    var modal = document.getElementById("api-key-edit-modal");
    if (!modal) return;
    var idEl = modal.querySelector("#api-key-edit-id");
    var nameEl = modal.querySelector("#api-key-edit-name");
    var form = modal.querySelector("form");
    var checks = Array.prototype.slice.call(modal.querySelectorAll("[data-scope-edit]"));
    function close() {
      modal.hidden = true;
    }
    function open(btn) {
      var scopes = {};
      (btn.dataset.scopes || "").split(",").forEach(function (scope) {
        scope = scope.trim();
        if (scope) scopes[scope] = true;
      });
      if (idEl) idEl.value = btn.dataset.id || "";
      if (nameEl) nameEl.value = btn.dataset.name || "";
      checks.forEach(function (input) {
        input.checked = !!scopes[input.value];
      });
      modal.hidden = false;
      if (form) form.dispatchEvent(new Event("input", { bubbles: true }));
      setTimeout(function () { if (nameEl) nameEl.focus(); }, 0);
    }
    document.querySelectorAll("[data-key-edit]").forEach(function (btn) {
      btn.addEventListener("click", function () { open(btn); });
    });
    modal.querySelectorAll("[data-key-edit-close]").forEach(function (btn) {
      btn.addEventListener("click", close);
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !modal.hidden) close();
    });
  })();

  /* ---------- 当前语种 Hero 图：上传后自动改为单独设置 ---------- */
  (function () {
    document.querySelectorAll("[data-hero-lang-visual]").forEach(function (box) {
      var custom = box.querySelector('input[name="hero_image_mode"][value="custom"]');
      var inherit = box.querySelector('input[name="hero_image_mode"][value="inherit"]');
      var uploader = box.querySelector(".hero-lang-uploader");
      var hidden = box.querySelector('input[name="hero_image_lang"]');
      function sync() {
        if (!uploader || !custom) return;
        uploader.classList.toggle("is-muted", !custom.checked);
      }
      [custom, inherit].forEach(function (radio) {
        if (radio) radio.addEventListener("change", sync);
      });
      if (hidden && custom) {
        hidden.addEventListener("input", function () {
          if (hidden.value) {
            custom.checked = true;
            sync();
          }
        });
      }
      sync();
    });
  })();

  /* ---------- 复制文本 ---------- */
  (function () {
    document.querySelectorAll("[data-copy-target]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var target = document.getElementById(btn.getAttribute("data-copy-target"));
        if (!target) return;
        var text = target.value || target.textContent || "";
        function done() {
          if (btn.classList && btn.classList.contains("icon-btn")) {
            var oldTitle = btn.getAttribute("title") || "";
            btn.setAttribute("title", "已复制");
            btn.setAttribute("aria-label", "已复制");
            setTimeout(function () {
              btn.setAttribute("title", oldTitle || "复制");
              btn.setAttribute("aria-label", oldTitle || "复制");
            }, 1600);
            return;
          }
          var old = btn.textContent;
          btn.textContent = "已复制";
          setTimeout(function () { btn.textContent = old; }, 1600);
        }
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text).then(done).catch(function () {
            target.select();
            document.execCommand("copy");
            done();
          });
        } else {
          target.select();
          document.execCommand("copy");
          done();
        }
      });
    });
  })();

  /* ---------- 分类拖动排序 ---------- */
  (function () {
    var tbody = document.getElementById("cat-sortable");
    if (!tbody) return;
    var dragEl = null, saveTimer;
    function rows() { return Array.prototype.slice.call(tbody.querySelectorAll("tr[draggable]")); }
    function afterEl(y) {
      var closest = { offset: -Infinity, el: null };
      rows().forEach(function (r) {
        if (r === dragEl) return;
        var box = r.getBoundingClientRect();
        var offset = y - box.top - box.height / 2;
        if (offset < 0 && offset > closest.offset) closest = { offset: offset, el: r };
      });
      return closest.el;
    }
    function save() {
      clearTimeout(saveTimer);
      saveTimer = setTimeout(function () {
        var ids = rows().map(function (r) { return r.dataset.id; }).join(",");
        fetch("/admin/settings/categories/reorder", {
          method: "POST", credentials: "same-origin",
          headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body: "_csrf=" + encodeURIComponent(CSRF) + "&order=" + encodeURIComponent(ids)
        });
      }, 200);
    }
    rows().forEach(function (tr) {
      tr.addEventListener("dragstart", function () { dragEl = tr; setTimeout(function () { tr.classList.add("dragging"); }, 0); });
      tr.addEventListener("dragend", function () { tr.classList.remove("dragging"); dragEl = null; save(); });
    });
    tbody.addEventListener("dragover", function (e) {
      e.preventDefault();
      if (!dragEl) return;
      var after = afterEl(e.clientY);
      if (after == null) tbody.appendChild(dragEl); else tbody.insertBefore(dragEl, after);
    });
  })();

  /* ---------- 站点管理：创建站点弹窗 ---------- */
  (function () {
    var modal = document.querySelector("[data-site-create-modal]");
    var openBtn = document.querySelector("[data-site-create-open]");
    if (!modal || !openBtn) return;
    var firstInput = modal.querySelector("input[name='slug']");
    function open() {
      modal.hidden = false;
      if (firstInput) setTimeout(function () { firstInput.focus(); firstInput.select(); }, 0);
    }
    function close() { modal.hidden = true; }
    openBtn.addEventListener("click", open);
    modal.querySelectorAll("[data-site-create-close]").forEach(function (btn) {
      btn.addEventListener("click", close);
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !modal.hidden) close();
    });
  })();

  /* ---------- 站点管理：详情浮层 ---------- */
  (function () {
    var details = Array.prototype.slice.call(document.querySelectorAll(".site-card-details"));
    if (!details.length) return;
    function closeAll(except) {
      details.forEach(function (d) {
        if (d !== except) d.open = false;
      });
    }
    details.forEach(function (d) {
      d.addEventListener("toggle", function () {
        if (d.open) closeAll(d);
      });
    });
    document.addEventListener("pointerdown", function (e) {
      if (!details.some(function (d) { return d.open; })) return;
      if (e.target.closest && e.target.closest(".site-card-details")) return;
      closeAll();
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") closeAll();
    });
  })();

  /* ---------- 顶部站点切换器 ---------- */
  (function () {
    var switcher = document.querySelector(".site-switcher");
    if (!switcher) return;
    var summary = switcher.querySelector("summary");
    var menu = switcher.querySelector(".site-switcher-menu");
    var raf = 0;

    function resetMenuPosition() {
      switcher.classList.remove("is-floating");
      if (!menu) return;
      menu.style.removeProperty("--site-switcher-left");
      menu.style.removeProperty("--site-switcher-top");
      menu.style.removeProperty("--site-switcher-width");
      menu.style.removeProperty("--site-switcher-max-height");
    }

    function positionMenu() {
      raf = 0;
      if (!summary || !menu || !switcher.open) {
        resetMenuPosition();
        return;
      }

      var margin = window.matchMedia("(max-width: 720px)").matches ? 10 : 12;
      var gap = 8;
      var viewportWidth = document.documentElement.clientWidth || window.innerWidth;
      var viewportHeight = document.documentElement.clientHeight || window.innerHeight;
      var width = Math.min(320, Math.max(240, viewportWidth - margin * 2));
      var rect = summary.getBoundingClientRect();
      var left = Math.min(Math.max(rect.left, margin), viewportWidth - width - margin);
      var below = Math.max(120, viewportHeight - rect.bottom - margin - gap);
      var above = Math.max(120, rect.top - margin - gap);
      var naturalHeight;
      var maxHeight;
      var top;
      var opensUp;

      switcher.classList.add("is-floating");
      menu.style.setProperty("--site-switcher-width", width + "px");
      menu.style.setProperty("--site-switcher-max-height", Math.max(120, viewportHeight - margin * 2) + "px");
      naturalHeight = menu.scrollHeight;
      opensUp = below < Math.min(naturalHeight, 220) && above > below;
      maxHeight = opensUp ? above : below;
      top = opensUp ? Math.max(margin, rect.top - gap - Math.min(naturalHeight, maxHeight)) : rect.bottom + gap;

      menu.style.setProperty("--site-switcher-left", left + "px");
      menu.style.setProperty("--site-switcher-top", top + "px");
      menu.style.setProperty("--site-switcher-max-height", Math.max(120, maxHeight) + "px");
    }

    function schedulePosition() {
      if (!switcher.open) return;
      if (raf) return;
      raf = window.requestAnimationFrame(positionMenu);
    }

    switcher.addEventListener("toggle", function () {
      if (switcher.open) schedulePosition();
      else resetMenuPosition();
    });
    document.addEventListener("click", function (e) {
      if (!switcher.contains(e.target)) switcher.open = false;
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") switcher.open = false;
    });
    window.addEventListener("resize", schedulePosition);
    window.addEventListener("scroll", schedulePosition, true);
  })();

  /* ---------- 通用复制按钮 ---------- */
  (function () {
    document.addEventListener("click", function (e) {
      var button = e.target.closest && e.target.closest("[data-copy-text]");
      if (!button) return;
      e.preventDefault();
      copyTextToClipboard(button.getAttribute("data-copy-text")).then(function () {
        markCopyDone(button, button.getAttribute("data-copy-done"));
      }).catch(function () {});
    });
  })();

  /* ---------- 统一确认弹层（替代系统 confirm；拦截带 data-confirm 的表单和按钮） ---------- */
  (function () {
    var modal = document.getElementById("confirm-modal");
    if (!modal) return;
    var msgEl = modal.querySelector("[data-confirm-msg]");
    var okBtn = modal.querySelector("[data-confirm-ok]");
    var titleEl = modal.querySelector("#confirm-title");
    var inputWrap = modal.querySelector("[data-confirm-input-wrap]");
    var inputLabel = modal.querySelector("[data-confirm-input-label]");
    var inputEl = modal.querySelector("[data-confirm-input]");
    var inputError = modal.querySelector("[data-confirm-input-error]");
    var inputCopy = modal.querySelector("[data-confirm-input-copy]");
    var pendingInput = null;
    var pendingAction = null;
    function close() {
      modal.hidden = true;
      pendingAction = null;
      pendingInput = null;
      if (inputWrap) inputWrap.hidden = true;
      if (inputEl) inputEl.value = "";
      if (inputError) { inputError.hidden = true; inputError.textContent = ""; }
      if (inputCopy) {
        var copyLabel = inputCopy.querySelector("[data-copy-label-text]");
        inputCopy.hidden = true;
        if (copyLabel) copyLabel.textContent = "";
        inputCopy.removeAttribute("data-copy-text");
      }
    }
    function open(target, action) {
      pendingAction = action;
      if (msgEl) msgEl.textContent = target.getAttribute("data-confirm") || "确定执行此操作？";
      if (okBtn) okBtn.textContent = target.getAttribute("data-confirm-ok") || "删除";
      if (titleEl) titleEl.textContent = target.getAttribute("data-confirm-title") || "确认删除";
      pendingInput = null;
      var inputName = target.getAttribute("data-confirm-input-name") || "";
      if (inputWrap && inputEl && inputName) {
        var copyValue = target.getAttribute("data-confirm-input-copy") || "";
        pendingInput = {
          name: inputName,
          match: target.getAttribute("data-confirm-input-match") || "",
          target: target
        };
        if (inputLabel) inputLabel.textContent = target.getAttribute("data-confirm-input-label") || "输入确认内容";
        inputEl.value = "";
        inputEl.placeholder = target.getAttribute("data-confirm-input-placeholder") || "";
        inputEl.setAttribute("name", inputName);
        inputWrap.hidden = false;
        if (inputCopy && copyValue) {
          var copyLabel = inputCopy.querySelector("[data-copy-label-text]");
          inputCopy.hidden = false;
          if (copyLabel) copyLabel.textContent = target.getAttribute("data-confirm-input-copy-label") || "复制";
          else inputCopy.textContent = target.getAttribute("data-confirm-input-copy-label") || "复制";
          inputCopy.setAttribute("data-copy-text", copyValue);
          inputCopy.setAttribute("data-copy-label", target.getAttribute("data-confirm-input-copy-label") || "复制");
          inputCopy.setAttribute("data-copy-done", target.getAttribute("data-confirm-input-copied") || "已复制");
          inputCopy.setAttribute("data-copy-replace-text", "1");
        } else if (inputCopy) {
          inputCopy.hidden = true;
          inputCopy.removeAttribute("data-copy-text");
        }
        if (inputError) { inputError.hidden = true; inputError.textContent = ""; }
      } else if (inputWrap) {
        inputWrap.hidden = true;
        if (inputCopy) inputCopy.hidden = true;
      }
      modal.hidden = false;
      if (inputEl && inputWrap && !inputWrap.hidden) setTimeout(function () { inputEl.focus(); }, 0);
      else if (okBtn) setTimeout(function () { okBtn.focus(); }, 0);
    }
    function applyPendingInput() {
      if (!pendingInput || !inputEl) return true;
      var value = inputEl.value.trim();
      if (!value) {
        if (inputError) {
          inputError.textContent = pendingInput.target.getAttribute("data-confirm-input-required") || "请先输入确认内容。";
          inputError.hidden = false;
        }
        inputEl.focus();
        return false;
      }
      if (pendingInput.match && value !== pendingInput.match) {
        if (inputError) {
          inputError.textContent = pendingInput.target.getAttribute("data-confirm-input-mismatch") || "输入内容不匹配。";
          inputError.hidden = false;
        }
        inputEl.focus();
        inputEl.select();
        return false;
      }
      var form = pendingInput.target.closest && pendingInput.target.closest("form");
      if (form) {
        var field = form.querySelector('input[name="' + pendingInput.name.replace(/"/g, '\\"') + '"]');
        if (!field) {
          field = document.createElement("input");
          field.type = "hidden";
          field.name = pendingInput.name;
          form.appendChild(field);
        }
        field.value = value;
      }
      return true;
    }
    // 捕获阶段优先拦截，未确认前阻断其它提交监听器（如防重复点击）
    document.addEventListener("submit", function (e) {
      var form = e.target;
      if (!form || !form.matches) return;
      var submitter = e.submitter && e.submitter.matches && e.submitter.matches("[data-confirm]") ? e.submitter : null;
      var confirmTarget = submitter || (form.matches("[data-confirm]") ? form : null);
      if (!confirmTarget) return;
      if (form.dataset.confirmed === "1") { delete form.dataset.confirmed; return; }
      e.preventDefault();
      e.stopImmediatePropagation();
      open(confirmTarget, function () {
        form.dataset.confirmed = "1";
        if (submitter && typeof form.requestSubmit === "function") form.requestSubmit(submitter);
        else if (typeof form.requestSubmit === "function") form.requestSubmit();
        else form.submit();
      });
    }, true);
    // 普通按钮也能复用同一个确认框，例如：清空图片、移除菜单项、删除未保存的社交链接行。
    document.addEventListener("click", function (e) {
      var target = e.target.closest && e.target.closest("[data-confirm]");
      if (!target || !target.matches || target.matches("form")) return;
      if (target.closest("form[data-confirm]")) return;
      if (target.dataset.confirmed === "1") { delete target.dataset.confirmed; return; }
      e.preventDefault();
      e.stopImmediatePropagation();
      open(target, function () {
        target.dataset.confirmed = "1";
        target.click();
      });
    }, true);
    if (okBtn) okBtn.addEventListener("click", function () {
      if (!applyPendingInput()) return;
      var action = pendingAction; close();
      if (action) action();
    });
    modal.querySelectorAll("[data-confirm-cancel]").forEach(function (b) { b.addEventListener("click", close); });
    document.addEventListener("keydown", function (e) { if (e.key === "Escape" && !modal.hidden) close(); });
  })();

  /* ---------- 移动端：顶栏折叠菜单 ---------- */
  (function () {
    var toggle = document.querySelector(".admin-burger");
    var nav = document.querySelector(".admin-nav");
    if (!toggle || !nav) return;
    toggle.addEventListener("click", function () {
      var open = nav.classList.toggle("open");
      toggle.setAttribute("aria-expanded", open ? "true" : "false");
    });
    nav.addEventListener("click", function (e) {
      if (e.target.closest("a")) { nav.classList.remove("open"); toggle.setAttribute("aria-expanded", "false"); }
    });
  })();

  /* ---------- 平台设置子导航：移动端把当前项带入视野 ---------- */
  (function () {
    var subnav = document.querySelector(".platform-settings-subnav");
    var active = subnav && subnav.querySelector(".active");
    if (!subnav || !active || subnav.scrollWidth <= subnav.clientWidth) return;
    requestAnimationFrame(function () {
      active.scrollIntoView({ block: "nearest", inline: "nearest" });
    });
  })();

  /* ---------- 默认密码提示：关闭（本会话内不再提示，下次登录重新出现） ---------- */
  (function () {
    var btn = document.querySelector("[data-pw-dismiss]");
    if (!btn) return;
    btn.addEventListener("click", function () {
      var warn = document.getElementById("pw-warn");
      if (warn) warn.remove();
      fetch("/admin/dismiss-pw", {
        method: "POST", credentials: "same-origin",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: "_csrf=" + encodeURIComponent(CSRF)
      });
    });
  })();

  // 找到某表单关联的提交按钮（含通过 form= 属性放在表单外的「保存」）
  function submitBtnsFor(form) {
    return Array.prototype.filter.call(document.querySelectorAll('button[type="submit"]'), function (b) { return b.form === form; });
  }

  /* ---------- 提交防重复点击 ---------- */
  document.querySelectorAll(".admin-main form").forEach(function (form) {
    form.addEventListener("submit", function (e) {
      if (form.matches("[data-no-busy]")) return;
      if (e.defaultPrevented) return; // 被 confirm 取消则不锁
      var btn = e.submitter || submitBtnsFor(form)[0];
      if (btn && !btn.disabled) {
        var orig = btn.textContent;
        setTimeout(function () { btn.disabled = true; btn.dataset.busy = "1"; btn.textContent = orig.indexOf("上传") >= 0 ? "上传中…" : "处理中…"; }, 0);
      }
    });
  });

  /* ---------- 编辑表单脏检查：与初始一致时「保存」置灰禁用 ---------- */
  (function () {
    var SKIP = /\/(delete|pin|translate|reorder|logout|login)\b/;
    var IGNORE = { _csrf: 1, editor_mode: 1 }; // 不参与「是否改动」比较的字段
    document.querySelectorAll(".admin-main form, form#post-form").forEach(function (form) {
      if (form.matches("[data-confirm], [data-no-dirty]")) return; // data-no-dirty：允许原样重新提交（如域名绑定会重跑 DNS）
      if (SKIP.test(form.getAttribute("action") || "")) return;
      var saveBtns = submitBtnsFor(form);
      if (!saveBtns.length) return;
      // 必须含可编辑字段，排除纯隐藏域的操作表单（删除/置顶/翻译等）
      if (!form.querySelector("input:not([type=hidden]):not([type=submit]):not([type=button]), textarea, select, [data-name], .rich")) return;
      var rich = form.querySelector(".rich");
      function sig() {
        var parts = [];
        new FormData(form).forEach(function (v, k) { if (!IGNORE[k]) parts.push(k + "=" + v); });
        return parts.sort().join("&");
      }
      var base = sig(), contentTouched = false;
      function recheck() {
        var dirty = contentTouched || sig() !== base;
        saveBtns.forEach(function (b) { if (!b.dataset.busy) b.disabled = !dirty; });
      }
      form.addEventListener("input", recheck);
      form.addEventListener("change", recheck);
      form.addEventListener("dd:change", recheck);
      if (rich) rich.addEventListener("input", function () { contentTouched = true; recheck(); });
      recheck(); // 初始：未改动 → 禁用保存
    });
  })();

  /* ---------- HTML → Markdown ---------- */
  function inlineMd(node) {
    var out = "";
    node.childNodes.forEach(function (n) {
      if (n.nodeType === 3) { out += n.nodeValue; return; }
      if (n.nodeType !== 1) return;
      var tag = n.tagName.toLowerCase();
      if (tag === "br") out += "\n";
      else if (tag === "strong" || tag === "b") out += "**" + inlineMd(n) + "**";
      else if (tag === "em" || tag === "i") out += "_" + inlineMd(n) + "_";
      else if (tag === "code") out += "`" + n.textContent + "`";
      else if (tag === "a") out += "[" + inlineMd(n) + "](" + (n.getAttribute("href") || "") + ")";
      else if (tag === "img") out += "![" + (n.getAttribute("alt") || "") + "](" + (n.getAttribute("src") || "") + ")";
      else out += inlineMd(n);
    });
    return out;
  }
  function cellsOf(tr) {
    return tr ? Array.prototype.map.call(tr.children, function (td) { return inlineMd(td).trim().replace(/\|/g, "\\|") || " "; }) : [];
  }
  function tableMd(table) {
    var thead = table.querySelector("thead");
    var header = cellsOf(thead ? thead.querySelector("tr") : table.querySelector("tr"));
    if (!header.length) return "";
    var lines = ["| " + header.join(" | ") + " |", "| " + header.map(function () { return "---"; }).join(" | ") + " |"];
    var tbody = table.querySelector("tbody");
    var trs = tbody ? tbody.querySelectorAll("tr") : table.querySelectorAll("tr");
    Array.prototype.forEach.call(trs, function (tr, i) {
      if (!thead && i === 0) return;
      var c = cellsOf(tr); if (c.length) lines.push("| " + c.join(" | ") + " |");
    });
    return lines.join("\n");
  }
  function blockMd(el) {
    var tag = el.tagName ? el.tagName.toLowerCase() : "";
    switch (tag) {
      case "h1": case "h2": return "## " + inlineMd(el).trim();
      case "h3": case "h4": return "### " + inlineMd(el).trim();
      case "blockquote": return inlineMd(el).trim().split("\n").map(function (l) { return "> " + l; }).join("\n");
      case "ul": return Array.prototype.map.call(el.children, function (li) { return "- " + inlineMd(li).trim(); }).join("\n");
      case "ol": return Array.prototype.map.call(el.children, function (li, i) { return (i + 1) + ". " + inlineMd(li).trim(); }).join("\n");
      case "table": return tableMd(el);
      case "hr": return "---";
      case "pre": return "~~~\n" + el.textContent.replace(/\n$/, "") + "\n~~~";
      case "img": return "![" + (el.getAttribute("alt") || "") + "](" + (el.getAttribute("src") || "") + ")";
      case "figure": { var im = el.querySelector("img"); return im ? "![" + (im.getAttribute("alt") || "") + "](" + (im.getAttribute("src") || "") + ")" : inlineMd(el).trim(); }
      default: return inlineMd(el).trim();
    }
  }
  function htmlToMarkdown(root) {
    var blocks = [];
    root.childNodes.forEach(function (n) {
      if (n.nodeType === 3) { var t = n.nodeValue.trim(); if (t) blocks.push(t); return; }
      if (n.nodeType !== 1) return;
      var md = blockMd(n);
      if (md !== "") blocks.push(md);
    });
    return blocks.join("\n\n").replace(/\n{3,}/g, "\n\n").trim() + "\n";
  }
})();

// 服务器健康度：站点管理页顶部的负载 / 内存 / 磁盘小指示，轮询刷新，隐藏标签页时暂停。
(function () {
  var el = document.querySelector("[data-server-health]");
  if (!el) return;
  var endpoint = el.getAttribute("data-endpoint") || "/admin/server-health";
  var loadEl = el.querySelector("[data-sh-load]");
  var memEl = el.querySelector("[data-sh-mem]");
  var diskEl = el.querySelector("[data-sh-disk]");
  var tipTpl = el.getAttribute("data-sh-tip-tpl") || "";
  var timer = null;

  function fixed2(n) { return (typeof n === "number" ? n : 0).toFixed(2); }

  function apply(d) {
    if (!d || !d.available) {
      el.classList.add("is-na");
      el.setAttribute("data-status", "na");
      return;
    }
    el.classList.remove("is-na");
    el.setAttribute("data-status", d.status || "ok");
    if (loadEl) loadEl.textContent = fixed2(d.load1);
    if (memEl) memEl.textContent = (d.mem_pct || 0) + "%";
    if (diskEl) diskEl.textContent = (d.disk_pct || 0) + "%";
    if (tipTpl) {
      el.setAttribute("data-tooltip", tipTpl
        .replace("{1}", fixed2(d.load1))
        .replace("{5}", fixed2(d.load5))
        .replace("{15}", fixed2(d.load15))
        .replace("{n}", d.num_cpu || 1));
    }
  }

  function poll() {
    fetch(endpoint, { headers: { "Accept": "application/json" }, credentials: "same-origin" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(apply)
      .catch(function () {});
  }

  function start() { if (!timer) { poll(); timer = setInterval(poll, 5000); } }
  function stop() { if (timer) { clearInterval(timer); timer = null; } }

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) stop(); else start();
  });
  start();
})();

/* ---------- 绑定访问域名向导（stepper + 探测/验证） ---------- */
(function () {
  var forms = document.querySelectorAll('form[data-domain-wizard]');
  if (!forms.length) return;

  function jget(url) {
    return fetch(url, { headers: { "Accept": "application/json", "X-Requested-With": "XMLHttpRequest" }, credentials: "same-origin" })
      .then(function (r) { return r.ok ? r.json() : null; }).catch(function () { return null; });
  }
  function jpost(url, params) {
    return fetch(url, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8", "X-Requested-With": "XMLHttpRequest" }, body: new URLSearchParams(params).toString() })
      .then(function (r) { return r.ok ? r.json().catch(function () { return null; }) : null; }).catch(function () { return null; });
  }

  forms.forEach(function (form) {
    var id = form.getAttribute("data-site-id");
    var steps = form.querySelector("[data-wizard-steps]");
    var hostInput = form.querySelector("[data-wizard-host]");
    if (!steps || !hostInput) return;
    var panels = form.querySelectorAll(".dw-panel");
    var stepBtns = steps.querySelectorAll(".dw-step");
    var prev = form.querySelector("[data-wizard-prev]");
    var next = form.querySelector("[data-wizard-next]");
    var finish = form.querySelector("[data-wizard-finish]");
    var statusEl = form.querySelector("[data-wizard-status]");
    var csrf = (form.querySelector('input[name="_csrf"]') || {}).value || "";
    var msgChecking = form.getAttribute("data-msg-checking") || "检测中…";
    var msgCopied = form.getAttribute("data-msg-copied") || "已复制";
    var msgDomainReq = form.getAttribute("data-msg-domain-required") || "请先填写主域名";
    var cur = 1, proxyDone = false, dnsHost = "";

    function host() { return (hostInput.value || "").trim().replace(/^https?:\/\//i, "").replace(/\/.*$/, "").toLowerCase(); }
    function setState(n, st) { var b = stepBtns[n - 1]; if (b) b.setAttribute("data-step-state", st); }
    function unlockAll() { stepBtns.forEach(function (b) { b.removeAttribute("data-locked"); }); }

    function setStep(n) {
      cur = n;
      steps.setAttribute("data-active-step", String(n));
      panels.forEach(function (p) { p.classList.toggle("is-active", p.getAttribute("data-step") === String(n)); });
      if (prev) prev.hidden = n <= 1;
      if (next) next.hidden = n >= 4;
      if (finish) finish.hidden = n < 4;
      if (n === 3 && !proxyDone) runProxy();
      if (n === 4) runVerify();
    }

    function fillTokens(ip, target) {
      var h = host() || "你的域名";
      form.querySelectorAll("[data-tok-host]").forEach(function (e) { e.textContent = h; });
      if (ip) form.querySelectorAll("[data-tok-ip]").forEach(function (e) { e.textContent = ip; });
      if (target) form.querySelectorAll("[data-tok-target]").forEach(function (e) { e.textContent = target; });
    }

    function runProxy() {
      proxyDone = true; setState(3, "running");
      jget("/admin/sites/" + id + "/wizard/proxy").then(function (d) {
        if (!d) { setState(3, "pending"); return; }
        var p = d.proxy || {};
        var kind = p.kind === "caddy" ? (p.on_demand ? "caddy-auto" : "caddy") : (p.kind === "nginx" ? "nginx" : "other");
        form.querySelectorAll("[data-proxy-guide] .dw-proxy-variant").forEach(function (v) { v.hidden = v.getAttribute("data-proxy") !== kind; });
        fillTokens((d.server_ip && d.server_ip.ipv4) || "", d.target || "127.0.0.1:8080");
        setState(3, "done");
      });
    }

    function runDNS() {
      var h = host();
      if (!h || h.indexOf(".") < 0 || h === dnsHost) return;
      dnsHost = h; setState(2, "running"); if (statusEl) statusEl.textContent = msgChecking;
      jpost("/admin/sites/" + id + "/wizard/dns", { host: h, _csrf: csrf }).then(function (d) {
        if (statusEl) statusEl.textContent = "";
        var cf = form.querySelector("[data-dns-cloudflare]"), manual = form.querySelector("[data-dns-manual]");
        var cfDns = form.querySelector('input[name="cf_dns"]');
        if (d && d.provider === "cloudflare") { if (cf) cf.hidden = false; if (manual) manual.hidden = true; if (cfDns) cfDns.checked = true; }
        else if (d && d.provider === "other") { if (cf) cf.hidden = true; if (manual) manual.hidden = false; if (cfDns) cfDns.checked = false; fillTokens("", ""); }
        else { if (cf) cf.hidden = false; if (manual) manual.hidden = false; }
        setState(2, "done");
      });
    }

    function runVerify() {
      var h = host(); if (!h) return;
      var box = form.querySelector("[data-verify-box]"), out = form.querySelector("[data-verify-result]");
      if (box) box.setAttribute("data-step-state", "running");
      setState(4, "running"); if (out) out.hidden = true; if (statusEl) statusEl.textContent = msgChecking;
      jpost("/admin/sites/" + id + "/wizard/verify", { host: h, _csrf: csrf }).then(function (d) {
        if (statusEl) statusEl.textContent = "";
        if (out) out.hidden = false;
        if (d && d.ok) { if (box) box.setAttribute("data-step-state", "done"); setState(4, "done"); if (out) out.textContent = "验证通过：" + h + " 已可正常访问。"; }
        else { if (box) box.setAttribute("data-step-state", "failed"); setState(4, "failed"); if (out) out.textContent = verifyMsg(d, h); }
      });
    }
    function verifyMsg(d, h) {
      if (!d) return "验证失败：无法完成检测，可稍后再试或直接保存。";
      if (d.reason === "unreachable") return "还无法访问 " + h + "（DNS 可能在传播、或证书还没签发）。可稍等重试，或直接保存。";
      if (d.reason === "not_gcms") return h + " 能打开，但看起来不是本站在服务（可能是缓存 / 其它代理）。可直接保存。";
      if (d.reason === "bad_status") return h + " 返回了 " + (d.status || "异常") + "。检查反代与证书后重试，或直接保存。";
      return "验证未通过，可稍后重试或直接保存。";
    }

    if (next) next.addEventListener("click", function () {
      if (cur === 1 && !host()) { setState(1, "failed"); if (statusEl) statusEl.textContent = msgDomainReq; hostInput.focus(); return; }
      if (cur === 1) { setState(1, "done"); if (statusEl) statusEl.textContent = ""; unlockAll(); runDNS(); }
      setStep(Math.min(4, cur + 1));
    });
    if (prev) prev.addEventListener("click", function () { setStep(Math.max(1, cur - 1)); });
    stepBtns.forEach(function (b, i) { b.addEventListener("click", function () { if (!b.hasAttribute("data-locked")) setStep(i + 1); }); });
    hostInput.addEventListener("blur", function () {
      if (host() !== dnsHost) setState(4, "pending");
      if (host()) runDNS();
    });

    form.querySelectorAll(".dw-copy").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var t = form.querySelector(btn.getAttribute("data-copy-target")); if (!t) return;
        var txt = t.textContent, orig = btn.textContent;
        var done = function () { btn.textContent = msgCopied; setTimeout(function () { btn.textContent = orig; }, 1200); };
        if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(txt).then(done, done); else done();
      });
    });

    var modal = form.closest(".site-domain-modal");
    function onOpen() {
      if (!modal || location.hash !== "#" + modal.id) return;
      if (!proxyDone) runProxy();
      if (host()) runDNS();
    }
    window.addEventListener("hashchange", onOpen);

    setStep(1);
    if (host()) { setState(1, "done"); unlockAll(); }
    onOpen();
  });
})();

/* ---------- 站点卡片：绑定状态（DNS + 可达性，异步逐个填充） ---------- */
(function () {
  var els = document.querySelectorAll("[data-domain-status]");
  if (!els.length) return;
  function check(el) {
    var url = el.getAttribute("data-status-url");
    var txt = el.querySelector("[data-status-text]");
    var orig = txt ? txt.textContent : "";
    var lbl = function (k) { return el.getAttribute("data-s-" + k) || ""; };
    el.setAttribute("data-stage", "checking");
    if (txt && lbl("checking")) txt.textContent = lbl("checking");
    return fetch(url, { headers: { "Accept": "application/json", "X-Requested-With": "XMLHttpRequest" }, credentials: "same-origin" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (d) {
        var stage = (d && d.stage) || "";
        if (stage === "ok" || stage === "pending" || stage === "dns") {
          el.setAttribute("data-stage", stage);
          if (txt) txt.textContent = lbl(stage) || orig;
        } else {
          el.removeAttribute("data-stage");
          if (txt) txt.textContent = orig;
        }
      })
      .catch(function () { el.removeAttribute("data-stage"); if (txt) txt.textContent = orig; });
  }
  var list = Array.prototype.slice.call(els), i = 0, active = 0, LIMIT = 3;
  function pump() {
    while (active < LIMIT && i < list.length) {
      active++;
      check(list[i++]).then(function () { active--; pump(); }, function () { active--; pump(); });
    }
  }
  pump();
})();

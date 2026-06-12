// 后台交互（原生 JS，无依赖）：上传(含前端转 WebP)、自定义下拉、主题微调(按主题)、
// Markdown ⇄ Medium 式富文本（气泡工具栏 / 链接弹层 / 加号菜单：图片·表格·分割线）。
(function () {
  "use strict";
  var CSRF = (document.body && document.body.dataset.csrf) || "";

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

  /* ---------- 自定义下拉（替代 <select>） ---------- */
  var openDD = null;
  function closeDD() {
    if (openDD) { openDD.classList.remove("open"); openDD.querySelector(".dd-toggle").setAttribute("aria-expanded", "false"); openDD = null; }
  }
  document.querySelectorAll(".dropdown").forEach(function (dd) {
    var toggle = dd.querySelector(".dd-toggle");
    var label = dd.querySelector(".dd-label");
    var hidden = dd.querySelector('input[type="hidden"]');
    var items = Array.prototype.slice.call(dd.querySelectorAll(".dd-menu li"));
    function select(li) {
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
  });
  document.addEventListener("click", closeDD);

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

  /* 社交链接行：增 / 删 */
  (function () {
    var box = document.querySelector("[data-social-rows]");
    var addBtn = document.querySelector("[data-social-add]");
    if (!box || !addBtn) return;
    addBtn.addEventListener("click", function () {
      var row = document.createElement("div");
      row.className = "social-row";
      row.innerHTML = '<input name="social_url" placeholder="https://github.com/you 或 mailto:you@x.com" inputmode="url"><input name="social_label" placeholder="名称（可选）"><button type="button" class="social-del" data-social-del title="删除" aria-label="删除"><svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/><path d="M10 11v6M14 11v6"/></svg></button>';
      box.appendChild(row);
      var inp = row.querySelector("input"); if (inp) inp.focus();
    });
    box.addEventListener("click", function (e) {
      var del = e.target.closest("[data-social-del]");
      if (del) { var r = del.closest(".social-row"); if (r) r.remove(); }
    });
  })();

  /* 导航菜单：增 / 删 / 拖动排序（仅手柄可拖，避免影响输入框） */
  (function () {
    var box = document.querySelector("[data-menu-rows]");
    var addBtn = document.querySelector("[data-menu-add]");
    if (!box) return;
    var langs = (box.getAttribute("data-langs") || "").split(",").filter(Boolean);
    var names = (box.getAttribute("data-lang-names") || "").split(",");
    var trash = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/><path d="M10 11v6M14 11v6"/></svg>';
    function labelsHTML() {
      return langs.map(function (code, i) {
        return '<label class="menu-label"><span class="ml-lang">' + (names[i] || code) + '</span><input name="nav_label_' + code + '" placeholder="名称"></label>';
      }).join("");
    }
    if (addBtn) addBtn.addEventListener("click", function () {
      var row = document.createElement("div");
      row.className = "menu-row";
      row.innerHTML = '<span class="drag-handle" aria-hidden="true">⠿</span><div class="menu-fields"><input class="menu-url" name="nav_url" placeholder="/ 或 /category/eng 或 https://…" inputmode="url"><div class="menu-labels">' + labelsHTML() + '</div></div><button type="button" class="menu-del" data-menu-del title="删除" aria-label="删除">' + trash + '</button>';
      box.appendChild(row);
      var inp = row.querySelector(".menu-url"); if (inp) inp.focus();
    });
    box.addEventListener("click", function (e) {
      var del = e.target.closest("[data-menu-del]");
      if (del) { var r = del.closest(".menu-row"); if (r) r.remove(); }
    });
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

  /* ---------- Markdown ⇄ 富文本编辑器 ---------- */
  initEditor();
  function initEditor() {
    var form = document.getElementById("post-form");
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

  /* ---------- 统一确认弹层（替代系统 confirm；拦截带 data-confirm 的表单） ---------- */
  (function () {
    var modal = document.getElementById("confirm-modal");
    if (!modal) return;
    var msgEl = modal.querySelector("[data-confirm-msg]");
    var okBtn = modal.querySelector("[data-confirm-ok]");
    var titleEl = modal.querySelector("#confirm-title");
    var pendingForm = null;
    function close() { modal.hidden = true; pendingForm = null; }
    function open(form) {
      pendingForm = form;
      if (msgEl) msgEl.textContent = form.getAttribute("data-confirm") || "确定执行此操作？";
      if (okBtn) okBtn.textContent = form.getAttribute("data-confirm-ok") || "删除";
      if (titleEl) titleEl.textContent = form.getAttribute("data-confirm-title") || "确认删除";
      modal.hidden = false;
      if (okBtn) setTimeout(function () { okBtn.focus(); }, 0);
    }
    // 捕获阶段优先拦截，未确认前阻断其它提交监听器（如防重复点击）
    document.addEventListener("submit", function (e) {
      var form = e.target;
      if (!form || !form.matches || !form.matches("[data-confirm]")) return;
      if (form.dataset.confirmed === "1") { delete form.dataset.confirmed; return; }
      e.preventDefault();
      e.stopImmediatePropagation();
      open(form);
    }, true);
    if (okBtn) okBtn.addEventListener("click", function () {
      var f = pendingForm; close();
      if (!f) return;
      f.dataset.confirmed = "1";
      if (typeof f.requestSubmit === "function") f.requestSubmit(); else f.submit();
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
      if (e.defaultPrevented) return; // 被 confirm 取消则不锁
      var btn = submitBtnsFor(form)[0];
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
      if (form.matches("[data-confirm]")) return;
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

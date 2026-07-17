package web

// ext_ops_test.go 工厂站运营管线：商品复制 / 置顶 / 定时发布 / 列表搜索分页 /
// 修订历史抽屉 / 扩展集合置顶 API / 商品质量门（全链路）/ 商品发布 Telegram 推送。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
)

// newTestFactoryServer 启用 product 的公开测试服务（每个测试独立 scratch 库）。
func newTestFactoryServer(t *testing.T) *Server {
	t.Helper()
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	return s
}

func seedProduct(t *testing.T, s *Server, slug, title, status string, extra string) int64 {
	t.Helper()
	p := &store.Post{
		Type: "product", Lang: s.defaultLang(), Slug: slug, Title: title, Status: status,
		Excerpt: "摘要", MetaDesc: "描述", Content: "正文", CoverImage: "/u/c.webp",
		EditorMode: "markdown", Extra: extra,
	}
	if status == "published" {
		p.PublishedAt = time.Now().Add(-time.Hour)
	}
	id, err := s.store.CreatePost(p)
	if err != nil {
		t.Fatalf("seed product %s: %v", slug, err)
	}
	return id
}

// TestAdminExtDuplicate 复制商品：整条（Extra 含图集/规格、封面、SEO）复制为草稿，
// 标题加「副本」、slug 去重、trans_group 独立，落到新草稿编辑页。
func TestAdminExtDuplicate(t *testing.T) {
	s := newTestFactoryServer(t)
	h := s.Handler()
	extra := `{"price":"US$ 12.5/pc","gallery":["/u/a.webp"],"specs":[{"k":"型号","v":"A-1"}]}`
	id := seedProduct(t, s, "cnc-part", "精密加工件", "published", extra)

	req, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/ext/product/%d/duplicate", id), nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("duplicate status = %d, body = %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/ext/product/") || !strings.HasSuffix(loc, "/edit?duplicated=1") {
		t.Fatalf("duplicate 落点 = %q, want ext 编辑页", loc)
	}

	copyPost, _ := s.store.GetTypedBySlug("product", s.defaultLang(), "cnc-part-2", true)
	if copyPost == nil {
		t.Fatalf("副本未按 uniqueSlug 落库（want slug cnc-part-2）")
	}
	src, _ := s.store.GetPostByID(id)
	if copyPost.Title != "精密加工件（副本）" {
		t.Fatalf("副本标题 = %q, want 精密加工件（副本）", copyPost.Title)
	}
	if copyPost.Status != "draft" {
		t.Fatalf("副本状态 = %q, want draft", copyPost.Status)
	}
	if copyPost.Extra != extra || copyPost.CoverImage != src.CoverImage || copyPost.Excerpt != src.Excerpt || copyPost.MetaDesc != src.MetaDesc {
		t.Fatalf("副本没有整条复制：extra=%s cover=%s", copyPost.Extra, copyPost.CoverImage)
	}
	if copyPost.TransGroup == src.TransGroup {
		t.Fatalf("副本不该进原互译组（%q）", src.TransGroup)
	}

	// 英文内容的副本后缀用 (copy)
	if got := duplicateTitle("Widget", "en"); got != "Widget (copy)" {
		t.Fatalf("duplicateTitle en = %q", got)
	}
	// 类型不匹配 404：拿商品 ID 打 doc 的 duplicate。
	_ = s.store.SetSetting(enabledContentTypesKey, "product,doc")
	reqBad, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/ext/doc/%d/duplicate", id), nil)
	wBad := httptest.NewRecorder()
	h.ServeHTTP(wBad, reqBad)
	if wBad.Code != http.StatusNotFound {
		t.Fatalf("跨类型 duplicate 应 404, got %d", wBad.Code)
	}
}

// TestAdminExtPin 置顶开关：featured 写库并让 ListPublishedByType 把置顶排最前
// （工厂首页「精选商品」名实相符）；类型不匹配 404。
func TestAdminExtPin(t *testing.T) {
	s := newTestFactoryServer(t)
	h := s.Handler()
	older := seedProduct(t, s, "p-old", "老商品", "published", "")
	// 让另一条发布时间更新（默认排前）
	newer := seedProduct(t, s, "p-new", "新商品", "published", "")
	if p, _ := s.store.GetPostByID(newer); p != nil {
		p.PublishedAt = time.Now().Add(-time.Minute)
		_ = s.store.UpdatePost(p)
	}

	pin := func(id int64, on string) *httptest.ResponseRecorder {
		req, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/ext/product/%d/pin", id), url.Values{"on": {on}, "status": {"published"}, "q": {"商品"}})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := pin(older, "1")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("pin status = %d", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "/admin/ext/product?") || !strings.Contains(loc, "status=published") || !strings.Contains(loc, "q=%E5%95%86%E5%93%81") {
		t.Fatalf("pin 后重定向没带回筛选现场：%q", loc)
	}
	if p, _ := s.store.GetPostByID(older); p == nil || !p.Featured {
		t.Fatalf("置顶未写库")
	}
	list, _ := s.store.ListPublishedByType("product", s.defaultLang(), 0, 0, 10)
	if len(list) != 2 || list[0].ID != older {
		t.Fatalf("置顶商品应排最前（featured DESC），got 首位 %v", list[0].ID)
	}
	// 取消置顶
	if w := pin(older, "0"); w.Code != http.StatusSeeOther {
		t.Fatalf("unpin status = %d", w.Code)
	}
	if p, _ := s.store.GetPostByID(older); p.Featured {
		t.Fatalf("取消置顶未写库")
	}
}

// TestAdminExtScheduled 定时发布全链：编辑表单 status=scheduled+publish_at 落库、
// 前台 404、列表快捷切换 published 生效；scheduled 但无未来时间 → 跳 ext 编辑页。
func TestAdminExtScheduled(t *testing.T) {
	s := newTestFactoryServer(t)
	h := s.Handler()
	id := seedProduct(t, s, "sched-prod", "定时商品", "draft", "")
	lang := s.defaultLang()

	future := time.Now().Add(2 * time.Hour)
	form := url.Values{
		"title": {"定时商品"}, "slug": {"sched-prod"}, "lang": {lang},
		"status": {"scheduled"}, "publish_at": {future.Format("2006-01-02T15:04")},
		"content": {"正文"},
	}
	req, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/ext/product/%d", id), form)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, body = %s", w.Code, w.Body.String())
	}
	p, _ := s.store.GetPostByID(id)
	if p.Status != "scheduled" || p.PublishedAt.IsZero() || !p.PublishedAt.After(time.Now()) {
		t.Fatalf("定时未落库：status=%q publish_at=%v", p.Status, p.PublishedAt)
	}
	// 定时中前台不可见
	wPub := httptest.NewRecorder()
	h.ServeHTTP(wPub, httptest.NewRequest(http.MethodGet, "/"+lang+"/products/sched-prod", nil))
	if wPub.Code != http.StatusNotFound {
		t.Fatalf("scheduled 商品前台应 404, got %d", wPub.Code)
	}
	// 编辑页回填 scheduled 选项与时间控件
	reqE, _ := authedAdminRequest(t, s, http.MethodGet, fmt.Sprintf("/admin/ext/product/%d/edit", id), nil)
	wE := httptest.NewRecorder()
	h.ServeHTTP(wE, reqE)
	if eb := wE.Body.String(); !strings.Contains(eb, `name="publish_at"`) || !strings.Contains(eb, `data-value="scheduled" aria-selected="true"`) {
		t.Fatalf("ext 编辑页缺定时发布控件/回填")
	}

	// 列表快捷切换 → published（发布时间过去化）
	reqS, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/ext/product/%d/status", id), url.Values{"target_status": {"published"}})
	wS := httptest.NewRecorder()
	h.ServeHTTP(wS, reqS)
	if wS.Code != http.StatusSeeOther {
		t.Fatalf("quick publish status = %d", wS.Code)
	}
	p, _ = s.store.GetPostByID(id)
	if p.Status != "published" || p.PublishedAt.After(time.Now()) {
		t.Fatalf("快捷发布未生效：status=%q publish_at=%v", p.Status, p.PublishedAt)
	}
	// 快捷切换回 scheduled 但没有未来时间 → 跳 ext 编辑页去补时间
	reqS2, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/ext/product/%d/status", id), url.Values{"target_status": {"scheduled"}})
	wS2 := httptest.NewRecorder()
	h.ServeHTTP(wS2, reqS2)
	if wS2.Code != http.StatusSeeOther || wS2.Header().Get("Location") != fmt.Sprintf("/admin/ext/product/%d/edit", id) {
		t.Fatalf("无未来时间的 scheduled 应跳 ext 编辑页, got %d %q", wS2.Code, wS2.Header().Get("Location"))
	}
}

// TestAdminExtListSearchFilterPagination 商品列表：标题搜索、状态筛选、分页与价格列。
func TestAdminExtListSearchFilterPagination(t *testing.T) {
	s := newTestFactoryServer(t)
	h := s.Handler()
	for i := 0; i < 25; i++ {
		seedProduct(t, s, fmt.Sprintf("bulk-%02d", i), fmt.Sprintf("批量商品 %02d", i), "published", "")
	}
	seedProduct(t, s, "priced", "带价商品", "draft", `{"price":"US$ 9.9/pc"}`)

	get := func(target string) string {
		req, _ := authedAdminRequest(t, s, http.MethodGet, target, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, w.Code)
		}
		return w.Body.String()
	}

	// 分页：26 条 → 第 1 页 20 行、第 2 页 6 行（同秒创建导致同页排序不稳定，只断言行数与导航）。
	rows := func(body string) int { return strings.Count(body, `class="t" href="/admin/ext/product/`) }
	body := get("/admin/ext/product")
	if !strings.Contains(body, "admin-pagination") || !strings.Contains(body, "page=2") {
		t.Fatalf("商品列表缺分页导航")
	}
	if n := rows(body); n != 20 {
		t.Fatalf("第 1 页 %d 行, want 20", n)
	}
	if !strings.Contains(body, "共 26 条") {
		t.Fatalf("总数徽标缺失")
	}
	body2 := get("/admin/ext/product?page=2")
	if n := rows(body2); n != 6 {
		t.Fatalf("第 2 页 %d 行, want 6", n)
	}

	// 标题搜索
	bodyQ := get("/admin/ext/product?q=" + url.QueryEscape("带价"))
	if !strings.Contains(bodyQ, "带价商品") || strings.Contains(bodyQ, "批量商品 24") {
		t.Fatalf("标题搜索未生效")
	}
	// 价格列：搜索结果里带价商品显示价格文本（自由文本原样输出）
	if !strings.Contains(bodyQ, "US$ 9.9/pc") {
		t.Fatalf("价格列未显示文本价格")
	}

	// 状态筛选
	bodyD := get("/admin/ext/product?status=draft")
	if !strings.Contains(bodyD, "带价商品") || strings.Contains(bodyD, "批量商品 24") {
		t.Fatalf("状态筛选未生效")
	}
	// 无命中给筛选空态
	bodyN := get("/admin/ext/product?q=" + url.QueryEscape("不存在"))
	if !strings.Contains(bodyN, "没有符合筛选条件的内容") {
		t.Fatalf("筛选空态文案缺失")
	}
}

// TestExtEditRevisionsDrawer 商品编辑页「历史版本」抽屉：保存留底、抽屉可见、
// 恢复端点回跳 ext 编辑页并还原字段。
func TestExtEditRevisionsDrawer(t *testing.T) {
	s := newTestFactoryServer(t)
	h := s.Handler()
	id := seedProduct(t, s, "rev-prod", "初版标题", "draft", "")
	lang := s.defaultLang()

	upd := url.Values{"title": {"改版标题"}, "slug": {"rev-prod"}, "lang": {lang}, "status": {"draft"}, "content": {"正文 v2"}}
	reqU, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/ext/product/%d", id), upd)
	wU := httptest.NewRecorder()
	h.ServeHTTP(wU, reqU)
	if wU.Code != http.StatusSeeOther {
		t.Fatalf("update = %d", wU.Code)
	}

	reqE, _ := authedAdminRequest(t, s, http.MethodGet, fmt.Sprintf("/admin/ext/product/%d/edit", id), nil)
	wE := httptest.NewRecorder()
	h.ServeHTTP(wE, reqE)
	eb := wE.Body.String()
	if !strings.Contains(eb, "历史版本") || !strings.Contains(eb, "/admin/revisions/") {
		t.Fatalf("ext 编辑页缺「历史版本」抽屉")
	}
	if !strings.Contains(eb, "初版标题") {
		t.Fatalf("抽屉里缺旧版本标题")
	}

	revs, _ := s.store.PostRevisions(id)
	if len(revs) == 0 {
		t.Fatalf("没有修订快照")
	}
	reqR, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/revisions/%d/restore", revs[0].ID), nil)
	wR := httptest.NewRecorder()
	h.ServeHTTP(wR, reqR)
	if wR.Code != http.StatusSeeOther {
		t.Fatalf("restore = %d", wR.Code)
	}
	if loc := wR.Header().Get("Location"); loc != fmt.Sprintf("/admin/ext/product/%d/edit?restored=1", id) {
		t.Fatalf("恢复后落点 = %q, want ext 编辑页", loc)
	}
	if p, _ := s.store.GetPostByID(id); p == nil || p.Title != "初版标题" {
		t.Fatalf("恢复未还原标题：%v", p)
	}
}

// TestAPIExtFeatured 扩展集合置顶 API：PATCH /api/admin/v1/products/featured/{id}
// 走 products:write（无 pin scope 枚举）；缺 scope 403；pages 404。
func TestAPIExtFeatured(t *testing.T) {
	s := newTestFactoryServer(t)
	h := s.Handler()
	id := seedProduct(t, s, "feat-prod", "待置顶商品", "published", "")

	token, prefix := newAutomationToken()
	if _, err := s.store.CreateAutomationKey("t", token, prefix, "products:read,products:write"); err != nil {
		t.Fatalf("create key: %v", err)
	}
	patch := func(path string, tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, path, bytes.NewReader([]byte(`{"featured":true}`)))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	w := patch("/api/admin/v1/products/featured/"+strconv.FormatInt(id, 10), token)
	if w.Code != http.StatusOK {
		t.Fatalf("ext featured = %d, body = %s", w.Code, w.Body.String())
	}
	var out struct {
		Item struct {
			Featured bool `json:"featured"`
		} `json:"item"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if !out.Item.Featured {
		t.Fatalf("featured 未生效：%s", w.Body.String())
	}
	if p, _ := s.store.GetPostByID(id); !p.Featured {
		t.Fatalf("featured 未写库")
	}

	// 只读 scope 403
	roToken, roPrefix := newAutomationToken()
	if _, err := s.store.CreateAutomationKey("ro", roToken, roPrefix, "products:read"); err != nil {
		t.Fatalf("create ro key: %v", err)
	}
	if w := patch("/api/admin/v1/products/featured/"+strconv.FormatInt(id, 10), roToken); w.Code != http.StatusForbidden {
		t.Fatalf("只读 scope 应 403, got %d body %s", w.Code, w.Body.String())
	}
	// pages 无置顶语义
	if w := patch("/api/admin/v1/pages/featured/1", token); w.Code != http.StatusNotFound {
		t.Fatalf("pages featured 应 404, got %d", w.Code)
	}
}

// TestAPIProductQualityGate 商品经自动化 API 直发要过商品规则集：
// 缺图/缺规格/正文过短 → 422 + 对应 failures；补齐后 201；文章阈值不受影响。
func TestAPIProductQualityGate(t *testing.T) {
	s := newTestFactoryServer(t)
	h := s.Handler()
	token, prefix := newAutomationToken()
	if _, err := s.store.CreateAutomationKey("t", token, prefix, "products:read,products:write,products:publish"); err != nil {
		t.Fatalf("create key: %v", err)
	}
	post := func(body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/admin/v1/products", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	// 一句话正文、无封面、无规格、无 meta_desc → 422，failures 指名道姓
	w := post(map[string]any{
		"title": "不合格商品直发标题", "lang": "zh", "status": "published",
		"content": "一句话。", "excerpt": "有摘要",
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("裸商品直发 = %d, want 422; body = %s", w.Code, w.Body.String())
	}
	var out struct {
		Error    string   `json:"error"`
		Failures []string `json:"failures"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Error != "quality_gate" {
		t.Fatalf("error = %q", out.Error)
	}
	joined := strings.Join(out.Failures, "|")
	for _, want := range []string{"body_too_short", "meta_desc_missing", "cover_or_gallery_missing", "specs_too_few (0/3)"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("failures 缺 %q：%v", want, out.Failures)
		}
	}

	// 补齐（正文 100+ 词、封面、specs 3 行）→ 201；正文远低于文章 400 阈值也能过（阈值分型）
	good := map[string]any{
		"title": "合格商品直发标题", "lang": "zh", "status": "published", "slug": "gate-ok",
		"content": strings.Repeat("规格与工艺说明。", 20), "excerpt": "摘要", "meta_desc": "描述",
		"cover_image": "/u/c.webp",
		"fields": map[string]any{
			"specs": []map[string]string{{"k": "型号", "v": "A"}, {"k": "材质", "v": "B"}, {"k": "起订量", "v": "C"}},
		},
	}
	if w := post(good); w.Code != http.StatusCreated {
		t.Fatalf("合格商品直发 = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	// 草稿不拦
	if w := post(map[string]any{"title": "草稿商品标题够长", "lang": "zh", "status": "draft", "slug": "gate-draft", "content": "短"}); w.Code != http.StatusCreated {
		t.Fatalf("商品草稿 = %d, want 201; body = %s", w.Code, w.Body.String())
	}
}

// TestAdminExtPublishTriggersTelegramPush 后台发布商品走 firePublishHooks 全链：
// 假 Bot API 收到 sendMessage，消息含标题与商品 URL + UTM；台账去重（重复保存不再推）。
func TestAdminExtPublishTriggersTelegramPush(t *testing.T) {
	s := newTestFactoryServer(t)
	fake := newFakeBotAPI(t)
	_ = s.store.SetSetting(telegramAutoPushSetting, "1")
	_ = s.store.SetSetting(telegramBotTokenSetting, "123:tok")
	_ = s.store.SetSetting(telegramChannelSetting, "@factory_news")
	h := s.Handler()
	lang := s.defaultLang()

	form := url.Values{
		"title": {"新品上架：精密轴承"}, "slug": {"tg-prod"}, "lang": {lang},
		"status": {"published"}, "content": {"正文"}, "excerpt": {"不锈钢深沟球轴承"},
	}
	req, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/ext/product", form)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create = %d, body = %s", w.Code, w.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	for fake.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fake.count() != 1 {
		t.Fatalf("商品发布未触发 Telegram 推送（收到 %d 次）", fake.count())
	}
	path, body := fake.call(0)
	if !strings.HasSuffix(path, "/sendMessage") {
		t.Fatalf("path = %q", path)
	}
	text, _ := body["text"].(string)
	if !strings.Contains(text, "新品上架：精密轴承") || !strings.Contains(text, "/"+lang+"/products/tg-prod/?utm_source=telegram&utm_medium=social") {
		t.Fatalf("商品推送消息不对：%q", text)
	}

	// 已发布内容再保存：台账去重，不再重复推送。
	p, _ := s.store.GetTypedBySlug("product", lang, "tg-prod", true)
	upd := url.Values{
		"title": {"新品上架：精密轴承"}, "slug": {"tg-prod"}, "lang": {lang},
		"status": {"published"}, "content": {"正文 v2"}, "excerpt": {"不锈钢深沟球轴承"},
	}
	reqU, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/ext/product/%d", p.ID), upd)
	wU := httptest.NewRecorder()
	h.ServeHTTP(wU, reqU)
	if wU.Code != http.StatusSeeOther {
		t.Fatalf("update = %d", wU.Code)
	}
	time.Sleep(80 * time.Millisecond)
	if fake.count() != 1 {
		t.Fatalf("重复保存不该再推，收到 %d 次", fake.count())
	}
}

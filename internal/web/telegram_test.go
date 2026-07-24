package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
)

// fakeBotAPI 假 Bot API：记录每次请求的路径与 JSON 体，按预设脚本回应。
// 推送在 goroutine 内发生，读写都加锁（-race 下测试轮询计数）。
type fakeBotAPI struct {
	srv    *httptest.Server
	mu     sync.Mutex
	paths  []string
	bodies []map[string]any
	// respond 为空时按方法默认成功回应；否则原样返回该 JSON。
	respond string
}

func newFakeBotAPI(t *testing.T) *fakeBotAPI {
	t.Helper()
	f := &fakeBotAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.bodies = append(f.bodies, body)
		respond := f.respond
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if respond != "" {
			_, _ = w.Write([]byte(respond))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/getChatMemberCount") {
			_, _ = w.Write([]byte(`{"ok":true,"result":1234}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	t.Cleanup(f.srv.Close)
	prev := telegramAPIBase
	telegramAPIBase = f.srv.URL
	t.Cleanup(func() { telegramAPIBase = prev })
	return f
}

func (f *fakeBotAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

func (f *fakeBotAPI) call(i int) (string, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paths[i], f.bodies[i]
}

func (f *fakeBotAPI) setRespond(body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respond = body
}

// TestTelegramMessageText 消息格式：HTML 转义、UTM、长摘要截断 300 字、空摘要省略中段。
func TestTelegramMessageText(t *testing.T) {
	pageURL := telegramArticleURL("https://example.com", "/zh/posts/hello/")
	if want := "https://example.com/zh/posts/hello/?utm_source=telegram&utm_medium=social"; pageURL != want {
		t.Fatalf("telegramArticleURL = %q, want %q", pageURL, want)
	}

	got := telegramMessageText(`Go <1.24> & "泛型"`, "摘要 <b>加粗</b> & 引用", pageURL)
	want := "<b>Go &lt;1.24&gt; &amp; &#34;泛型&#34;</b>\n\n摘要 &lt;b&gt;加粗&lt;/b&gt; &amp; 引用\n\n" + pageURL
	if got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}

	// 长摘要截断 ~300 字（rune 计），追加省略号。
	long := strings.Repeat("好", 350)
	got = telegramMessageText("标题", long, pageURL)
	middle := strings.Split(got, "\n\n")[1]
	if runes := []rune(middle); len(runes) != telegramExcerptMaxRunes+1 || !strings.HasSuffix(middle, "…") {
		t.Fatalf("长摘要未按 %d 字截断：len=%d suffix=%q", telegramExcerptMaxRunes, len(runes), middle[len(middle)-3:])
	}

	// 空摘要：省略中段，不留连续空行。
	got = telegramMessageText("标题", "   ", pageURL)
	if want := "<b>标题</b>\n\n" + pageURL; got != want {
		t.Fatalf("空摘要 message = %q, want %q", got, want)
	}
}

// TestDeriveTelegramChannelURL @频道名 → t.me 链接；数字 id / 非法名派生不出。
func TestDeriveTelegramChannelURL(t *testing.T) {
	cases := map[string]string{
		"@gcms_news":     "https://t.me/gcms_news",
		" @gcms_news ":   "https://t.me/gcms_news",
		"-1001234567890": "",
		"1234567":        "",
		"@":              "",
		"@有 空格":          "",
		"":               "",
	}
	for in, want := range cases {
		if got := deriveTelegramChannelURL(in); got != want {
			t.Fatalf("deriveTelegramChannelURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestValidTelegramChannel 频道标识校验：@名 / 数字 id 合法，其它拒绝。
func TestValidTelegramChannel(t *testing.T) {
	for _, ok := range []string{"", "@gcms_news", "@AB12", "-1001234567890", "1234567"} {
		if !validTelegramChannel(ok) {
			t.Fatalf("%q 应合法", ok)
		}
	}
	for _, bad := range []string{"@", "@a", "@有空格 x", "@na me", "-", "-12a3", "gcms_news", "https://t.me/x"} {
		if validTelegramChannel(bad) {
			t.Fatalf("%q 应非法", bad)
		}
	}
}

// TestTelegramPushLedger 台账去重：认领一次成功、二次失败；释放后可重试；成功记录不被释放。
func TestTelegramPushLedger(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	claimed, err := s.store.ClaimTelegramPush("post", 7)
	if err != nil || !claimed {
		t.Fatalf("首次认领 = (%v, %v), want (true, nil)", claimed, err)
	}
	claimed, err = s.store.ClaimTelegramPush("post", 7)
	if err != nil || claimed {
		t.Fatalf("重复认领 = (%v, %v), want (false, nil)", claimed, err)
	}
	// 失败释放 → 可再次认领。
	if err := s.store.ReleaseTelegramPush("post", 7); err != nil {
		t.Fatalf("release: %v", err)
	}
	if claimed, _ = s.store.ClaimTelegramPush("post", 7); !claimed {
		t.Fatalf("释放后应可重新认领")
	}
	// 成功回写 message_id 后，Release 不得删除（同一篇永远只推一次）。
	if err := s.store.SetTelegramPushMessageID("post", 7, 42); err != nil {
		t.Fatalf("set message id: %v", err)
	}
	_ = s.store.ReleaseTelegramPush("post", 7)
	if claimed, _ = s.store.ClaimTelegramPush("post", 7); claimed {
		t.Fatalf("已成功推送的记录不该被释放")
	}
}

// TestTelegramDeliver 假 Bot API 全链路：请求体断言、台账去重（同一篇只发一次）、
// 失败写最近错误键（不含 token）并释放认领。
func TestTelegramDeliver(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	fake := newFakeBotAPI(t)
	const token = "123456:SECRET-TOKEN"

	s.telegramDeliver(token, "@gcms_news", "post", 1, "<b>标题</b>\n\nhttps://example.com/zh/posts/a?utm_source=telegram&utm_medium=social")
	s.telegramDeliver(token, "@gcms_news", "post", 1, "再来一次不该发出") // 台账去重
	if fake.count() != 1 {
		t.Fatalf("Bot API 收到 %d 次请求, want 1（台账去重）", fake.count())
	}
	path, body := fake.call(0)
	if want := "/bot" + token + "/sendMessage"; path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if body["chat_id"] != "@gcms_news" || body["parse_mode"] != "HTML" {
		t.Fatalf("请求体 chat_id/parse_mode 不对：%v", body)
	}
	if text, _ := body["text"].(string); !strings.Contains(text, "<b>标题</b>") || !strings.Contains(text, "utm_source=telegram") {
		t.Fatalf("请求体 text 不对：%v", body["text"])
	}
	if got := s.store.Setting(telegramLastErrorSetting); got != "" {
		t.Fatalf("成功后最近错误键应为空，got %q", got)
	}

	// 失败：写最近错误键（原样 description、绝不含 token），释放认领允许下次重试。
	fake.setRespond(`{"ok":false,"description":"Bad Request: chat not found"}`)
	s.telegramDeliver(token, "@gcms_news", "post", 2, "x")
	lastErr := s.store.Setting(telegramLastErrorSetting)
	if !strings.Contains(lastErr, "Bad Request: chat not found") {
		t.Fatalf("最近错误键未记录 Bot API 错误：%q", lastErr)
	}
	if strings.Contains(lastErr, token) {
		t.Fatalf("最近错误键泄露了 token：%q", lastErr)
	}
	fake.setRespond("")
	s.telegramDeliver(token, "@gcms_news", "post", 2, "x")
	if fake.count() != 3 {
		t.Fatalf("失败释放认领后应可重试, got %d 次请求", fake.count())
	}
}

// TestTelegramPushPlan 推送前置判定：仅已发布 posts、开关开启、token/频道齐备、公开地址可派生。
func TestTelegramPushPlan(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	post := &store.Post{ID: 1, Type: "post", Status: "published", Lang: "zh", Slug: "hello", Title: "标题", Excerpt: "摘要"}

	if _, _, _, ok := s.telegramPushPlan(nil, post); ok {
		t.Fatalf("未配置时不该推")
	}
	_ = s.store.SetSetting(telegramAutoPushSetting, "1")
	_ = s.store.SetSetting(telegramBotTokenSetting, "123:tok")
	_ = s.store.SetSetting(telegramChannelSetting, "@gcms_news")

	// baseURL 是 localhost：派生不出公开地址 → 跳过（PublishDue 无请求上下文路径）。
	if _, _, _, ok := s.telegramPushPlan(nil, post); ok {
		t.Fatalf("本地 baseURL 不该推")
	}
	s.baseURL = "https://example.com"
	token, channel, text, ok := s.telegramPushPlan(nil, post)
	if !ok || token != "123:tok" || channel != "@gcms_news" {
		t.Fatalf("plan = (%q,%q,%v), want ok", token, channel, ok)
	}
	if !strings.Contains(text, "https://example.com/zh/posts/hello/?utm_source=telegram&utm_medium=social") {
		t.Fatalf("text 缺文章 URL + UTM：%q", text)
	}

	// 草稿 / page / 未启用扩展类型 / 开关关闭都不推。
	if _, _, _, ok := s.telegramPushPlan(nil, &store.Post{Type: "post", Status: "draft", Lang: "zh", Slug: "d"}); ok {
		t.Fatalf("草稿不该推")
	}
	if _, _, _, ok := s.telegramPushPlan(nil, &store.Post{Type: "page", Status: "published", Lang: "zh", Slug: "p"}); ok {
		t.Fatalf("page 不该推")
	}
	product := &store.Post{ID: 9, Type: "product", Status: "published", Lang: "zh", Slug: "widget", Title: "新品", Excerpt: "上架"}
	if _, _, _, ok := s.telegramPushPlan(nil, product); ok {
		t.Fatalf("未启用 product 时不该推")
	}
	// 启用 product 后：商品推送生效，消息同构（标题 + 摘要 + 商品 URL + UTM）。
	_ = s.store.SetSetting(enabledContentTypesKey, "product")
	_, _, text, ok = s.telegramPushPlan(nil, product)
	if !ok {
		t.Fatalf("已启用 product 应推")
	}
	if !strings.Contains(text, "<b>新品</b>") || !strings.Contains(text, "https://example.com/zh/products/widget/?utm_source=telegram&utm_medium=social") {
		t.Fatalf("商品推送消息不对：%q", text)
	}
	_ = s.store.SetSetting(telegramAutoPushSetting, "0")
	if _, _, _, ok := s.telegramPushPlan(nil, post); ok {
		t.Fatalf("开关关闭不该推")
	}
}

// TestRunScheduledPublish 定时翻发布路径：PublishDue 返回被翻文章、状态入库，
// 且经站点域名派生 URL 完成 Telegram 推送（无请求上下文）。
func TestRunScheduledPublish(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	fake := newFakeBotAPI(t)
	s.baseURL = "https://example.com"
	_ = s.store.SetSetting(telegramAutoPushSetting, "1")
	_ = s.store.SetSetting(telegramBotTokenSetting, "123:tok")
	_ = s.store.SetSetting(telegramChannelSetting, "@gcms_news")

	id, err := s.store.CreatePost(&store.Post{
		Type: "post", Slug: "scheduled-one", Title: "定时文章", Excerpt: "到点了",
		Status: "scheduled", Lang: "zh", PublishedAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	s.RunScheduledPublish()

	p, err := s.store.GetPostByID(id)
	if err != nil || p == nil || p.Status != "published" {
		t.Fatalf("翻发布后状态 = %v (err=%v), want published", p, err)
	}
	// 推送是异步 goroutine：轮询等待假 Bot API 收到请求。
	deadline := time.Now().Add(3 * time.Second)
	for fake.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fake.count() != 1 {
		t.Fatalf("定时翻发布未触发 Telegram 推送（收到 %d 次请求）", fake.count())
	}
	if _, body := fake.call(0); !strings.Contains(body["text"].(string), "https://example.com/zh/posts/scheduled-one/?utm_source=telegram") {
		t.Fatalf("推送 URL 未按站点域名派生：%v", body["text"])
	}
	// 再跑一轮：没有到点文章，也不重复推送。
	s.RunScheduledPublish()
	time.Sleep(50 * time.Millisecond)
	if fake.count() != 1 {
		t.Fatalf("重复翻发布不该再推，收到 %d 次请求", fake.count())
	}
}

func configureScheduledPublishCloudflareRealtime(t *testing.T, s *Server) {
	t.Helper()
	settings := map[string]string{
		cloudflareAPITokenKey:   "test-token",
		cloudflareDeployModeKey: cloudflareModeWorkerAssets,
		cloudflareWorkerNameKey: "gcms-scheduled-publish-test",
		cloudflareDomainsKey: encodeCloudflareDomains([]CloudflareDomain{{
			Host:    "scheduled.example.com",
			Primary: true,
		}}),
		cloudflareAutoSyncKey: "1",
		cloudflareSyncModeKey: cloudflareSyncModeRealtime,
	}
	for key, value := range settings {
		if err := s.store.SetSetting(key, value); err != nil {
			t.Fatalf("set Cloudflare setting %q: %v", key, err)
		}
	}
	t.Cleanup(s.stopCloudflareTimer)
}

func TestRunScheduledPublishSchedulesCloudflareRealtimeSync(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	configureScheduledPublishCloudflareRealtime(t, s)

	s.cacheMu.Lock()
	s.content["scheduled-post"] = contentCacheEntry{}
	s.endpoints = map[string]endpointCacheEntry{"feed": {}}
	s.pages = map[string]pageCacheEntry{"home": {}}
	s.cacheMu.Unlock()

	if _, err := s.store.CreatePost(&store.Post{
		Type: "post", Slug: "cloudflare-scheduled", Title: "Cloudflare 定时文章",
		Status: "scheduled", Lang: "zh", PublishedAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("create scheduled post: %v", err)
	}

	s.RunScheduledPublish()

	s.cacheMu.RLock()
	contentCount, endpointCount, pageCount := len(s.content), len(s.endpoints), len(s.pages)
	s.cacheMu.RUnlock()
	if contentCount != 0 || endpointCount != 0 || pageCount != 0 {
		t.Fatalf("定时发布后前台缓存未完整清理：content=%d endpoints=%d pages=%d",
			contentCount, endpointCount, pageCount)
	}
	if got := s.store.Setting(cloudflareSyncPendingKey); got != "1" {
		t.Fatalf("Cloudflare sync pending = %q, want 1", got)
	}
	nextAt := s.store.Setting(cloudflareSyncNextAtKey)
	if _, err := time.Parse(time.RFC3339, nextAt); err != nil {
		t.Fatalf("Cloudflare sync next_at = %q, want RFC3339 time: %v", nextAt, err)
	}
	s.cloudflareMu.Lock()
	timerArmed := s.cloudflareTimer != nil
	s.cloudflareMu.Unlock()
	if !timerArmed {
		t.Fatalf("定时发布未启动 Cloudflare 实时同步防抖计时器")
	}
}

func TestRunScheduledPublishWithoutDuePostsDoesNotScheduleCloudflareSync(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	configureScheduledPublishCloudflareRealtime(t, s)

	if _, err := s.store.CreatePost(&store.Post{
		Type: "post", Slug: "cloudflare-not-due", Title: "尚未到点",
		Status: "scheduled", Lang: "zh", PublishedAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create future scheduled post: %v", err)
	}

	s.RunScheduledPublish()

	if got := s.store.Setting(cloudflareSyncPendingKey); got != "" {
		t.Fatalf("没有到期文章时不应标记 Cloudflare 待同步，得到 %q", got)
	}
	if got := s.store.Setting(cloudflareSyncNextAtKey); got != "" {
		t.Fatalf("没有到期文章时不应安排 Cloudflare 同步时间，得到 %q", got)
	}
	s.cloudflareMu.Lock()
	timerArmed := s.cloudflareTimer != nil
	s.cloudflareMu.Unlock()
	if timerArmed {
		t.Fatalf("没有到期文章时不应启动 Cloudflare 同步计时器")
	}
}

// TestAPIStatsTelegram /stats/telegram：未配置 → 400 telegram_not_configured；
// 配置后返回 {ok,members} 并缓存 1 小时（第二次不再打 Bot API）。
func TestAPIStatsTelegram(t *testing.T) {
	s, token := newTestAutomationServer(t, "stats:read")
	fake := newFakeBotAPI(t)

	get := func() (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/v1/stats/telegram", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.apiStatsTelegram(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}

	w, out := get()
	if w.Code != http.StatusBadRequest || out["error"] != "telegram_not_configured" {
		t.Fatalf("未配置 = %d %v, want 400 telegram_not_configured", w.Code, out)
	}

	_ = s.store.SetSetting(telegramBotTokenSetting, "123:tok")
	_ = s.store.SetSetting(telegramChannelSetting, "@gcms_news")
	w, out = get()
	if w.Code != http.StatusOK || out["ok"] != true || out["members"] != float64(1234) {
		t.Fatalf("订阅数 = %d %v, want members=1234", w.Code, out)
	}
	if path, _ := fake.call(0); fake.count() != 1 || !strings.HasSuffix(path, "/getChatMemberCount") {
		t.Fatalf("应打一次 getChatMemberCount：%q", path)
	}
	// 第二次命中 1h 缓存：Bot API 不再被调用。
	if w, out = get(); w.Code != http.StatusOK || out["members"] != float64(1234) || fake.count() != 1 {
		t.Fatalf("缓存未生效：%d %v（%d 次请求）", w.Code, out, fake.count())
	}

	// 缺 scope → 403。
	s2, token2 := newTestAutomationServer(t, "posts:read")
	req := httptest.NewRequest(http.MethodGet, "/api/admin/v1/stats/telegram", nil)
	req.Header.Set("Authorization", "Bearer "+token2)
	w2 := httptest.NewRecorder()
	s2.apiStatsTelegram(w2, req)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("无 stats:read = %d, want 403", w2.Code)
	}
}

// TestAdminTelegramSettings 后台保存 + 测试发送：@频道名自动派生订阅链接、token 留空保持不变、
// 测试端点实测发送并把 Bot API 错误原样回显。
func TestAdminTelegramSettings(t *testing.T) {
	srv := newTestPublicServer(t, "")
	h := srv.Handler()
	fake := newFakeBotAPI(t)

	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		req, _ := authedAdminRequest(t, srv, http.MethodPost, path, form)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// 保存：@频道名 + 空链接 → 自动派生 t.me；auto_push 开启。
	rec := post("/admin/settings/telegram", url.Values{
		"telegram_bot_token": {"123456:SECRET"},
		"telegram_channel":   {"@gcms_news"},
		"telegram_auto_push": {"1"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := srv.store.Setting(telegramChannelURLSetting); got != "https://t.me/gcms_news" {
		t.Fatalf("channel_url 未自动派生：%q", got)
	}
	if srv.store.Setting(telegramAutoPushSetting) != "1" || srv.store.Setting(telegramBotTokenSetting) != "123456:SECRET" {
		t.Fatalf("设置未落库")
	}

	// token 留空再存：保持已保存值（密文性质，不回显也不覆盖）。
	rec = post("/admin/settings/telegram", url.Values{
		"telegram_channel":     {"@gcms_news"},
		"telegram_channel_url": {"https://t.me/gcms_news"},
	})
	if rec.Code != http.StatusSeeOther || srv.store.Setting(telegramBotTokenSetting) != "123456:SECRET" {
		t.Fatalf("token 留空不该被清空：%d %q", rec.Code, srv.store.Setting(telegramBotTokenSetting))
	}

	// 数字 id 频道：派生不出链接 → channel_url 留空（须手填）。
	rec = post("/admin/settings/telegram", url.Values{
		"telegram_channel":     {"-1001234567890"},
		"telegram_channel_url": {""},
	})
	if rec.Code != http.StatusSeeOther || srv.store.Setting(telegramChannelURLSetting) != "" {
		t.Fatalf("数字 id 不该派生链接：%q", srv.store.Setting(telegramChannelURLSetting))
	}

	// 非法频道格式 → 表单错误（400 页面）。
	rec = post("/admin/settings/telegram", url.Values{"telegram_channel": {"not a channel"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法频道 = %d, want 400", rec.Code)
	}

	// 测试发送：实测调 Bot API，成功返回 {ok,message_id}。
	rec = post("/admin/settings/telegram/test", url.Values{})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("test send = %d %s", rec.Code, rec.Body.String())
	}
	if path, _ := fake.call(fake.count() - 1); fake.count() != 1 || !strings.HasSuffix(path, "/sendMessage") {
		t.Fatalf("测试发送未打 sendMessage：%q", path)
	}

	// 测试发送失败：Bot API 错误原样回显（且不含 token）。
	fake.setRespond(`{"ok":false,"description":"Unauthorized"}`)
	rec = post("/admin/settings/telegram/test", url.Values{})
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "Unauthorized") {
		t.Fatalf("test send fail = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "123456:SECRET") {
		t.Fatalf("测试发送错误回显泄露 token")
	}
}

// TestTelegramFrontendEntries 前台入口：telegram.channel_url 非空时 footer 出现订阅链接、
// 文章详情页正文末尾出现轻量 CTA；未配置时两处都不渲染。
func TestTelegramFrontendEntries(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()

	get := func(path string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	if body := get("/zh/"); strings.Contains(body, "t.me/") {
		t.Fatalf("未配置时 footer 不该有 Telegram 入口")
	}

	_ = s.store.SetSetting(telegramChannelURLSetting, "https://t.me/gcms_news")
	s.clearGeneratedCaches()
	if body := get("/zh/"); !strings.Contains(body, `href="https://t.me/gcms_news"`) || !strings.Contains(body, "Telegram 频道") {
		t.Fatalf("footer 缺 Telegram 订阅入口")
	}

	if _, err := s.store.CreatePost(&store.Post{
		Type: "post", Slug: "tg-cta-post", Title: "CTA 测试", Excerpt: "摘要",
		Content: "正文", Status: "published", Lang: "zh",
	}); err != nil {
		t.Fatalf("create post: %v", err)
	}
	body := get("/zh/posts/tg-cta-post/")
	if !strings.Contains(body, `class="tg-cta"`) || !strings.Contains(body, `href="https://t.me/gcms_news"`) {
		t.Fatalf("文章页缺 Telegram CTA")
	}
}

// TestSitesPageTelegramBadge 平台站点卡片的 Telegram 徽标三态：未绑定 → is-missing 灰；
// 已绑定推送关 → is-dim 半透明；已绑定推送开 → is-on 正常色。点击跳该站设置页 Telegram 分区。
func TestSitesPageTelegramBadge(t *testing.T) {
	srv, h, ps, defaultSite, blogSite := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)

	getSites := func() string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites", nil)
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /admin/sites = %d", rec.Code)
		}
		return rec.Body.String()
	}

	// 未绑定：默认灰态（无 is-on / is-dim / is-warn），tooltip 引导配置，点击打开就地配置弹窗。
	body := getSites()
	if !strings.Contains(body, `href="#site-telegram-modal-`+strconv.FormatInt(defaultSite.ID, 10)+`"`) ||
		!strings.Contains(body, `href="#site-telegram-modal-`+strconv.FormatInt(blogSite.ID, 10)+`"`) {
		t.Fatalf("站点卡片徽标应打开就地配置弹窗")
	}
	// 弹窗自带保存/测试端点与「完整设置」兜底链接；默认站点也要有弹窗（别掉进域名弹窗的 not .IsDefault 守卫）。
	for _, id := range []string{strconv.FormatInt(defaultSite.ID, 10), strconv.FormatInt(blogSite.ID, 10)} {
		if !strings.Contains(body, `id="site-telegram-modal-`+id+`"`) ||
			!strings.Contains(body, `action="/admin/sites/`+id+`/telegram"`) ||
			!strings.Contains(body, `data-test-url="/admin/sites/`+id+`/telegram/test"`) ||
			!strings.Contains(body, `href="/admin/sites/`+id+`/settings/telegram"`) {
			t.Fatalf("站点 %s 的 Telegram 弹窗缺失或缺保存/测试端点/完整设置链接", id)
		}
	}
	if !strings.Contains(body, "未绑定 Telegram 频道，点击去配置") {
		t.Fatalf("未绑定态 tooltip 缺失")
	}
	if strings.Contains(body, `site-tg-badge is-`) {
		t.Fatalf("未绑定时不该出现 is-on / is-dim / is-warn")
	}
	if !strings.Contains(body, `telegram-connect is-missing`) {
		t.Fatalf("平台未配 token 时顶部入口应为 is-missing")
	}

	// 频道已填但两级 token 都没有 → is-warn 半灰。
	_ = srv.store.SetSetting(telegramChannelSetting, "@gcms_news")
	body = getSites()
	if !strings.Contains(body, `site-tg-badge is-warn`) || !strings.Contains(body, "已填频道但无可用 bot token") {
		t.Fatalf("无可用 token 应为 is-warn")
	}

	// 平台级 token 配好（站点 token 仍空）→ 已绑定 + 推送关 → is-dim；顶部入口转 is-configured。
	if err := ps.SetSetting(platformTelegramBotTokenKey, "888:PTOK"); err != nil {
		t.Fatalf("set platform token: %v", err)
	}
	body = getSites()
	if !strings.Contains(body, `site-tg-badge is-dim`) || !strings.Contains(body, "Telegram 频道 · 自动推送未开") {
		t.Fatalf("平台 token 回退下已绑定未开推送应为 is-dim")
	}
	if !strings.Contains(body, `telegram-connect is-configured`) {
		t.Fatalf("平台 token 配好后顶部入口应为 is-configured")
	}

	// 已绑定 + 推送开 → is-on。
	_ = srv.store.SetSetting(telegramAutoPushSetting, "1")
	body = getSites()
	if !strings.Contains(body, `site-tg-badge is-on`) || !strings.Contains(body, "Telegram 频道 · 自动推送已开") {
		t.Fatalf("已绑定已开推送应为 is-on")
	}

	// 「完整设置 →」兜底链接路由可用：/admin/sites/{id}/settings/telegram → 303 落到该站设置页 Telegram 分区。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/settings/telegram", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/settings/telegram" {
		t.Fatalf("完整设置跳转 = %d %q, want 303 /admin/settings/telegram", rec.Code, rec.Header().Get("Location"))
	}
}

// TestSiteTelegramCardEndpoints 站点卡片弹窗端点：AJAX 保存写入目标站点库（不落设置页）、
// 表单错误就地回显、普通表单回退 303 回站点页、未知站点 404、测试发送按站点生效。
func TestSiteTelegramCardEndpoints(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)
	fake := newFakeBotAPI(t)
	sid := strconv.FormatInt(blogSite.ID, 10)

	post := func(path string, form url.Values, ajax bool) *httptest.ResponseRecorder {
		form.Set("_csrf", "csrf")
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "https://platform.test"+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if ajax {
			req.Header.Set("Accept", "application/json")
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
		}
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		return rec
	}

	// AJAX 保存：写入的是 blog 站点自己的库（订阅链接自动派生）。
	rec := post("/admin/sites/"+sid+"/telegram", url.Values{
		"telegram_channel":   {"@cardchan"},
		"telegram_auto_push": {"1"},
		"telegram_bot_token": {"222:CARDTOK"},
	}, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("card save = %d %s", rec.Code, rec.Body.String())
	}
	rt, ok := srv.runtimePool().runtimeByID(blogSite.ID)
	if !ok || rt == nil || rt.Store == nil {
		t.Fatalf("blog runtime missing")
	}
	if rt.Store.Setting(telegramChannelSetting) != "@cardchan" || rt.Store.Setting(telegramChannelURLSetting) != "https://t.me/cardchan" || rt.Store.Setting(telegramAutoPushSetting) != "1" {
		t.Fatalf("blog 站点设置未落库：%q %q", rt.Store.Setting(telegramChannelSetting), rt.Store.Setting(telegramChannelURLSetting))
	}
	if got := srv.store.Setting(telegramChannelSetting); got == "@cardchan" {
		t.Fatalf("默认站库被误写")
	}

	// AJAX 表单错误 → 400 {ok:false} 就地回显。
	rec = post("/admin/sites/"+sid+"/telegram", url.Values{"telegram_channel": {"not a channel"}}, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("card save invalid = %d %s", rec.Code, rec.Body.String())
	}

	// 非 AJAX 回退：303 回站点页（绝不落设置页）。
	rec = post("/admin/sites/"+sid+"/telegram", url.Values{"telegram_channel": {"@cardchan"}}, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("card save 非 AJAX = %d %q, want 303 /admin/sites", rec.Code, rec.Header().Get("Location"))
	}

	// 未知站点 → 404。
	rec = post("/admin/sites/9999/telegram", url.Values{"telegram_channel": {"@x_chan"}}, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown site = %d", rec.Code)
	}

	// 测试发送：按 blog 站点已存的频道 + token 实际打假 Bot API。
	rec = post("/admin/sites/"+sid+"/telegram/test", url.Values{}, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("card test send = %d %s", rec.Code, rec.Body.String())
	}
	if path, body := fake.call(fake.count() - 1); !strings.HasSuffix(path, "/sendMessage") || body["chat_id"] != "@cardchan" {
		t.Fatalf("card test send 未按站点频道发送：%q %v", path, body["chat_id"])
	}
}

// TestTelegramEffectiveBotToken 回退链取值：站点有用站点的；站点空回退平台级；都无为空。
// 并端到端验证：站点只填频道时，推送用平台共用 token 发出。
func TestTelegramEffectiveBotToken(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	ps, err := platform.Open(filepath.Join(t.TempDir(), "system.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	s.platform = ps

	if got := s.telegramEffectiveBotToken(); got != "" {
		t.Fatalf("都无 token 应为空，got %q", got)
	}
	if err := ps.SetSetting(platformTelegramBotTokenKey, "888:PTOK"); err != nil {
		t.Fatalf("set platform token: %v", err)
	}
	if got := s.telegramEffectiveBotToken(); got != "888:PTOK" {
		t.Fatalf("站点空应回退平台 token，got %q", got)
	}
	_ = s.store.SetSetting(telegramBotTokenSetting, "111:SITE")
	if got := s.telegramEffectiveBotToken(); got != "111:SITE" {
		t.Fatalf("站点 token 应优先，got %q", got)
	}

	// 端到端：站点 token 清空、只填频道 → 推送经平台 token 走 /bot888:PTOK/sendMessage。
	_ = s.store.SetSetting(telegramBotTokenSetting, "")
	_ = s.store.SetSetting(telegramChannelSetting, "@gcms_news")
	_ = s.store.SetSetting(telegramAutoPushSetting, "1")
	s.baseURL = "https://example.com"
	fake := newFakeBotAPI(t)
	s.fireTelegramPush(nil, &store.Post{ID: 9, Type: "post", Status: "published", Lang: "zh", Slug: "p-tok", Title: "平台 bot 推送"})
	deadline := time.Now().Add(3 * time.Second)
	for fake.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fake.count() != 1 {
		t.Fatalf("平台 token 回退未触发推送")
	}
	if path, _ := fake.call(0); path != "/bot888:PTOK/sendMessage" {
		t.Fatalf("推送未使用平台 token：%q", path)
	}
}

// TestPlatformTelegramBotHandlers 顶部入口的保存与 getMe 验证：token 留空保持、显式清除、
// getMe 返回 bot 用户名回执、Bot API 错误原样回显且不含 token。
func TestPlatformTelegramBotHandlers(t *testing.T) {
	srv, h, ps, _, _ := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)
	fake := newFakeBotAPI(t)
	_ = srv

	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		form.Set("_csrf", "csrf")
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "https://platform.test"+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		return rec
	}

	// 保存平台 token。
	rec := post("/admin/sites/telegram-bot", url.Values{"platform_telegram_bot_token": {"888:PTOK"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("save = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := ps.Setting(platformTelegramBotTokenKey); got != "888:PTOK" {
		t.Fatalf("平台 token 未落库：%q", got)
	}
	// 留空再存 = 保持不变。
	rec = post("/admin/sites/telegram-bot", url.Values{})
	if rec.Code != http.StatusSeeOther || ps.Setting(platformTelegramBotTokenKey) != "888:PTOK" {
		t.Fatalf("留空不该清空平台 token")
	}

	// getMe 验证：返回 bot 用户名回执。
	fake.setRespond(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"GCMS Bot","username":"gcms_bot"}}`)
	rec = post("/admin/sites/telegram-bot/test", url.Values{})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "@gcms_bot") {
		t.Fatalf("getMe 验证 = %d %s", rec.Code, rec.Body.String())
	}
	if path, _ := fake.call(fake.count() - 1); !strings.HasSuffix(path, "/getMe") {
		t.Fatalf("验证未走 getMe：%q", path)
	}
	// getMe 失败：错误原样回显且不含 token。
	fake.setRespond(`{"ok":false,"description":"Unauthorized"}`)
	rec = post("/admin/sites/telegram-bot/test", url.Values{})
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "Unauthorized") || strings.Contains(rec.Body.String(), "888:PTOK") {
		t.Fatalf("getMe 失败回显 = %d %s", rec.Code, rec.Body.String())
	}

	// 显式清除。
	rec = post("/admin/sites/telegram-bot", url.Values{"platform_telegram_bot_token_clear": {"1"}})
	if rec.Code != http.StatusSeeOther || ps.Setting(platformTelegramBotTokenKey) != "" {
		t.Fatalf("显式清除失败：%q", ps.Setting(platformTelegramBotTokenKey))
	}
}

// TestRunScheduledPublishCoversEverySite 定时发布必须覆盖**每一个站**，不只是默认站。
//
// 病根：平台是一个进程按域名伺候所有站，每个非默认站有自己的库（runtimeForSite 里
// store.Open(site.DBPath)）；而 main.go 的分钟定时器调的是 srv.RunScheduledPublish()，
// 它只做 s.store.PublishDue() —— s.store 是**默认站**那一个库。
// 于是非默认站的定时文章到点了也永远翻不成已发布（用户实测：排到 2026-07-15 的文章，
// 07-17 了还挂着「定时」）。
func TestRunScheduledPublishCoversEverySite(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")

	// 再开一个「非默认站」的库，挂进运行时池——复刻 runtimeForSite 干的事
	other, err := store.Open(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatalf("open other store: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })

	past := time.Now().Add(-48 * time.Hour)
	mk := func(st *store.Store, slug string) int64 {
		id, err := st.CreatePost(&store.Post{Type: "post", Lang: "zh", Slug: slug, Title: slug, Status: "scheduled", PublishedAt: past})
		if err != nil {
			t.Fatalf("create %s: %v", slug, err)
		}
		return id
	}
	defID := mk(s.store, "on-default")
	otherID := mk(other, "on-other-site")

	otherSrv := &Server{store: other, rootServer: s}
	s.setRuntimePool(&SiteRuntimePool{byID: map[int64]*SiteRuntime{
		1: {Site: &platform.Site{ID: 1, Slug: "default", IsDefault: true}, Store: s.store, server: s},
		2: {Site: &platform.Site{ID: 2, Slug: "other"}, Store: other, server: otherSrv},
	}})

	s.RunScheduledPublish()

	if p, err := s.store.GetPostByID(defID); err != nil || p.Status != "published" {
		t.Fatalf("默认站该被翻成已发布，得到 %v (err=%v)", p.Status, err)
	}
	p, err := other.GetPostByID(otherID)
	if err != nil {
		t.Fatalf("other GetPostByID: %v", err)
	}
	if p.Status != "published" {
		t.Fatalf("★ 非默认站到点了也没发出去（status=%q）—— 定时器只覆盖了默认站，"+
			"平台上其余每个站的定时发布都是死的", p.Status)
	}
}

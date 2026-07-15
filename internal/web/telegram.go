package web

// telegram.go 「Telegram 频道」迷你版：把站点读者转化为频道订阅者、新文章自动推送。
//  - 站点设置（settings 表，站点级）：bot token（密文性质，绝不写日志/回显页面）、频道 ID、
//    公开订阅链接（@频道名可自动派生 t.me/…）、自动推送开关、最近一次推送错误。
//  - 发布自动推送：posts 首次置 published 时（firePublishHooks 同层，admin / 自动化 API /
//    定时翻发布三路都过）异步调 Bot API sendMessage；tg_pushed 台账唯一索引保证同一篇永远只推一次。
//  - 订阅数观测：GET /stats/telegram（stats:read，1h 缓存）→ getChatMemberCount。
// Bot API 基址可注入（telegramAPIBase），测试用 httptest 假服务。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/store"
)

const (
	telegramBotTokenSetting   = "telegram.bot_token"   // Bot token（密文性质：不回显、不写日志）
	telegramChannelSetting    = "telegram.channel"     // @频道名 或 -100 开头的数字 id
	telegramChannelURLSetting = "telegram.channel_url" // 公开订阅链接（前台入口用）
	telegramAutoPushSetting   = "telegram.auto_push"   // "1" 开启发布自动推送
	telegramLastErrorSetting  = "telegram.last_error"  // 最近一次推送错误（时间 + 原样错误）

	// 平台级共用 bot token（平台 settings 命名空间，与 Google OAuth 凭据同级）。
	// 站点级 telegram.bot_token 非空优先；为空回退这里（一次配好全平台共用）。
	platformTelegramBotTokenKey = "telegram.bot_token"

	telegramExcerptMaxRunes = 300 // 推送消息里摘要的最大长度（超出截断加 …）
	telegramPushContentType = "post"
)

// platformTelegramBotToken 平台级共用 token（单站部署无平台层，返回空）。
func (s *Server) platformTelegramBotToken() string {
	if s.platform == nil {
		return ""
	}
	return strings.TrimSpace(s.platform.Setting(platformTelegramBotTokenKey))
}

// telegramEffectiveBotToken 唯一取值助手（推送 / 测试发送 / 订阅数统计共用）：
// 站点级 telegram.bot_token 非空用站点的，空则回退平台级。
func (s *Server) telegramEffectiveBotToken() string {
	if t := strings.TrimSpace(s.store.Setting(telegramBotTokenSetting)); t != "" {
		return t
	}
	return s.platformTelegramBotToken()
}

// telegramAPIBase Bot API 基址；生产恒为官方地址，测试注入 httptest 假服务。
var telegramAPIBase = "https://api.telegram.org"

// telegramHTTPClient 推送用小超时客户端（异步 goroutine 内使用）。
var telegramHTTPClient = &http.Client{Timeout: 10 * time.Second}

// ---------- 纯函数：频道 / 消息组装 ----------

// validTelegramChannel 校验频道标识：@频道名（字母数字下划线）或数字 id（含 -100 前缀）。空串视为合法（未配置）。
func validTelegramChannel(channel string) bool {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return true
	}
	if strings.HasPrefix(channel, "@") {
		name := strings.TrimPrefix(channel, "@")
		if len(name) < 2 {
			return false
		}
		for _, r := range name {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
				return false
			}
		}
		return true
	}
	rest := strings.TrimPrefix(channel, "-")
	if rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// deriveTelegramChannelURL @频道名 → https://t.me/频道名；数字 id 派生不出公开链接，返回空（须手填）。
func deriveTelegramChannelURL(channel string) string {
	channel = strings.TrimSpace(channel)
	if strings.HasPrefix(channel, "@") && len(channel) > 1 && validTelegramChannel(channel) {
		return "https://t.me/" + strings.TrimPrefix(channel, "@")
	}
	return ""
}

// telegramArticleURL 文章绝对 URL + 渠道 UTM。
func telegramArticleURL(base, path string) string {
	u := absWithBase(base, path)
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "utm_source=telegram&utm_medium=social"
}

// telegramTruncate 摘要截断（按 rune，超出加 …）。
func telegramTruncate(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}

// telegramMessageText 推送文本（parse_mode=HTML）：「<b>标题</b>\n\n摘要\n\n链接」。
// 标题 / 摘要做 HTML 转义；摘要为空时省略中段。
func telegramMessageText(title, excerpt, pageURL string) string {
	parts := []string{"<b>" + html.EscapeString(strings.TrimSpace(title)) + "</b>"}
	if e := telegramTruncate(excerpt, telegramExcerptMaxRunes); e != "" {
		parts = append(parts, html.EscapeString(e))
	}
	parts = append(parts, pageURL)
	return strings.Join(parts, "\n\n")
}

// sanitizeTelegramError 错误信息脱敏：Bot API 的 URL 带 token，任何错误文本先抹掉 token 再返回 / 记日志。
func sanitizeTelegramError(msg, token string) string {
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "***")
	}
	return msg
}

// ---------- Bot API 客户端 ----------

// telegramBotCall 调 Bot API 一个方法（POST JSON），解析 {ok,description,result} 包封。
// 返回的 error 保证不含 token；Bot API 的 description 原样透出（供后台回显）。
func telegramBotCall(apiBase, token, method string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := telegramHTTPClient.Post(strings.TrimRight(apiBase, "/")+"/bot"+token+"/"+method, "application/json", bytes.NewReader(body))
	if err != nil {
		return errors.New(sanitizeTelegramError(err.Error(), token))
	}
	defer resp.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("telegram bot api: HTTP %d，响应解析失败", resp.StatusCode)
	}
	if !envelope.OK {
		desc := strings.TrimSpace(envelope.Description)
		if desc == "" {
			desc = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return errors.New(sanitizeTelegramError(desc, token))
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("telegram bot api: result 解析失败: %v", err)
		}
	}
	return nil
}

// telegramSendMessage sendMessage（parse_mode=HTML），返回 message_id。
func telegramSendMessage(apiBase, token, chatID, text string) (int64, error) {
	var result struct {
		MessageID int64 `json:"message_id"`
	}
	err := telegramBotCall(apiBase, token, "sendMessage", map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}, &result)
	return result.MessageID, err
}

// telegramGetChatMemberCount getChatMemberCount，返回频道成员数（订阅数）。
func telegramGetChatMemberCount(apiBase, token, chatID string) (int, error) {
	var n int
	err := telegramBotCall(apiBase, token, "getChatMemberCount", map[string]any{"chat_id": chatID}, &n)
	return n, err
}

// telegramGetMe getMe：只验证 token 本身可用（不需要频道），返回 bot 用户名回执。
func telegramGetMe(apiBase, token string) (string, error) {
	var result struct {
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
	}
	if err := telegramBotCall(apiBase, token, "getMe", map[string]any{}, &result); err != nil {
		return "", err
	}
	if result.Username != "" {
		return "@" + result.Username, nil
	}
	return result.FirstName, nil
}

// ---------- 站点卡片状态徽标 ----------

// SiteTelegramStatus 平台站点卡片上 Telegram 频道徽标 + 就地配置弹窗的数据。
// 只读 settings（站点键 + 平台 token 键），绝不调 Bot API（不查订阅数，保持站点管理页快）。
type SiteTelegramStatus struct {
	Channel        bool // telegram.channel 非空
	TokenAvailable bool // 站点级 token 非空，或平台级共用 token 非空（回退链）
	AutoPush       bool // telegram.auto_push == "1"
	// 弹窗回填值（token 本身绝不进视图，只给「是否已配置」）。
	ChannelValue string
	ChannelURL   string
	SiteTokenSet bool
}

// Bound 已绑定 = 频道非空且有可用 token（站点级或平台级）。
func (t SiteTelegramStatus) Bound() bool { return t.Channel && t.TokenAvailable }

// siteTelegramStatusFor 按站点 store 读取徽标状态（站点管理页渲染卡片时逐站调用）。
func (s *Server) siteTelegramStatusFor(st *store.Store) SiteTelegramStatus {
	if st == nil {
		return SiteTelegramStatus{}
	}
	channel := strings.TrimSpace(st.Setting(telegramChannelSetting))
	siteToken := strings.TrimSpace(st.Setting(telegramBotTokenSetting))
	return SiteTelegramStatus{
		Channel:        channel != "",
		TokenAvailable: siteToken != "" || s.platformTelegramBotToken() != "",
		AutoPush:       st.Setting(telegramAutoPushSetting) == "1",
		ChannelValue:   channel,
		ChannelURL:     strings.TrimSpace(st.Setting(telegramChannelURLSetting)),
		SiteTokenSet:   siteToken != "",
	}
}

// ---------- 发布自动推送 ----------

// fireTelegramPush 发布钩子的 Telegram 分支：仅 posts；开关开启、token / 频道齐备才推。
// 网络发送在 goroutine 内进行，失败仅记日志 + 写最近错误键，绝不阻塞发布。
func (s *Server) fireTelegramPush(r *http.Request, p *store.Post) {
	token, channel, text, ok := s.telegramPushPlan(r, p)
	if !ok {
		return
	}
	go s.telegramDeliver(token, channel, p.ID, text)
}

// telegramPushPlan 同步的推送前置判定与消息组装（可测的确定性部分）。
// r 可为 nil（定时翻发布 PublishDue 路径）：URL 一律经 publicBaseURL 派生——请求存在用请求域名，
// 无请求上下文时回退站点域名配置（子站 baseURL 由 siteBaseURL 按主域名链派生，参考 media_cleanup
// 的 Domain 回退）；派生不出公开地址（本地 baseURL）就跳过并记日志。
func (s *Server) telegramPushPlan(r *http.Request, p *store.Post) (token, channel, text string, ok bool) {
	if p == nil || p.Type != telegramPushContentType || p.Status != "published" {
		return "", "", "", false
	}
	if s.store.Setting(telegramAutoPushSetting) != "1" {
		return "", "", "", false
	}
	token = s.telegramEffectiveBotToken() // 站点级优先，空则回退平台级共用 bot
	channel = strings.TrimSpace(s.store.Setting(telegramChannelSetting))
	if token == "" || channel == "" {
		return "", "", "", false
	}
	base := s.publicBaseURL(r)
	if isLocalBaseURL(base) {
		log.Printf("telegram: 派生不出站点公开地址（%s），跳过推送 post %d", base, p.ID)
		return "", "", "", false
	}
	text = telegramMessageText(p.Title, p.Excerpt, telegramArticleURL(base, s.apiContentURL(p)))
	return token, channel, text, true
}

// telegramDeliver 实际推送（goroutine 内执行）：先经台账认领去重（同一篇永远只推一次），
// 成功回写 message_id 并清空最近错误键；失败记日志 + 写最近错误键并释放认领（下次发布事件可重试）。
func (s *Server) telegramDeliver(token, channel string, postID int64, text string) {
	claimed, err := s.store.ClaimTelegramPush(telegramPushContentType, postID)
	if err != nil {
		log.Printf("telegram: 推送台账写入失败（post %d）: %v", postID, err)
		return
	}
	if !claimed {
		return // 已推过（或正被并发推送）：台账唯一索引去重
	}
	msgID, err := telegramSendMessage(telegramAPIBase, token, channel, text)
	if err != nil {
		log.Printf("telegram: 推送 post %d 失败: %v", postID, err)
		_ = s.store.ReleaseTelegramPush(telegramPushContentType, postID)
		_ = s.store.SetSetting(telegramLastErrorSetting, time.Now().Format("2006-01-02 15:04:05")+" "+err.Error())
		return
	}
	_ = s.store.SetTelegramPushMessageID(telegramPushContentType, postID, msgID)
	_ = s.store.SetSetting(telegramLastErrorSetting, "")
}

// RunScheduledPublish 由 main 的分钟定时器调用：把到点的「定时发布」翻为已发布，
// 失效 sitemap 端点缓存并触发 Telegram 推送。维持既有决策：该路径不提交 IndexNow
// （见 indexnow.go；定时翻发布与修订恢复两路均不提交）。
func (s *Server) RunScheduledPublish() {
	posts, err := s.store.PublishDue()
	if err != nil {
		log.Printf("定时发布: 翻发布失败: %v", err)
		return
	}
	if len(posts) == 0 {
		return
	}
	s.invalidateSitemapCache()
	for _, p := range posts {
		s.fireTelegramPush(nil, p)
	}
}

// ---------- 后台设置 ----------

// applyTelegramSettingsForm 校验并落库站点级 Telegram 设置（站点设置页与站点卡片弹窗共用）。
// token 留空 = 保持已保存值（telegram_bot_token_clear=1 显式清除）；@频道名在订阅链接留空时自动派生。
// 返回非空字符串 = 用户可读的表单错误（此时不写库）。
func (s *Server) applyTelegramSettingsForm(r *http.Request) string {
	channel := strings.TrimSpace(r.FormValue("telegram_channel"))
	if !validTelegramChannel(channel) {
		return "频道 ID 格式不正确：请填写 @频道名（字母、数字、下划线）或 -100 开头的数字 ID。"
	}
	token := strings.TrimSpace(r.FormValue("telegram_bot_token"))
	if token == "" && r.FormValue("telegram_bot_token_clear") != "1" {
		token = strings.TrimSpace(s.store.Setting(telegramBotTokenSetting)) // 留空保持不变
	}
	channelURL := strings.TrimSpace(r.FormValue("telegram_channel_url"))
	if channelURL == "" {
		channelURL = deriveTelegramChannelURL(channel) // @频道名自动派生；数字 id 派生不出须手填
	}
	if channelURL != "" && !strings.HasPrefix(channelURL, "http://") && !strings.HasPrefix(channelURL, "https://") {
		return "订阅链接必须是 http(s) 绝对地址，例如 https://t.me/yourchannel。"
	}
	autoPush := "0"
	if r.FormValue("telegram_auto_push") == "1" {
		autoPush = "1"
	}
	effectiveToken := token
	if effectiveToken == "" {
		effectiveToken = s.platformTelegramBotToken() // 站点留空可回退平台级共用 bot
	}
	if autoPush == "1" && (effectiveToken == "" || channel == "") {
		return "开启自动推送前，需要先填写频道 ID，且本站或平台至少配置一个 Bot Token。"
	}
	_ = s.store.SetSetting(telegramBotTokenSetting, token)
	_ = s.store.SetSetting(telegramChannelSetting, channel)
	_ = s.store.SetSetting(telegramChannelURLSetting, channelURL)
	_ = s.store.SetSetting(telegramAutoPushSetting, autoPush)
	s.clearGeneratedCaches()
	return ""
}

// adminSaveTelegram POST /admin/settings/telegram：站点设置页保存 Telegram 频道设置。
func (s *Server) adminSaveTelegram(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if msg := s.applyTelegramSettingsForm(r); msg != "" {
		s.showSettings(w, r, "telegram", "", msg)
		return
	}
	s.redirectSettings(w, r, "telegram", "Telegram 频道设置已保存。")
}

// telegramTestSendPayload 测试发送的共用核心（设置页与站点卡片弹窗）：表单里未保存的
// token / 频道优先生效（先测后存）；错误原样回显（已脱敏，不含 token）。
func (s *Server) telegramTestSendPayload(r *http.Request) (int, map[string]any) {
	token := strings.TrimSpace(r.FormValue("telegram_bot_token"))
	if token == "" {
		token = s.telegramEffectiveBotToken() // 站点级优先，空则回退平台级共用 bot
	}
	channel := strings.TrimSpace(r.FormValue("telegram_channel"))
	if channel == "" {
		channel = strings.TrimSpace(s.store.Setting(telegramChannelSetting))
	}
	if token == "" || channel == "" {
		return http.StatusBadRequest, map[string]any{"ok": false, "error": "telegram_not_configured", "message": "请先填写 Bot Token 和频道 ID。"}
	}
	siteName := s.site(s.defaultLang()).Name
	text := telegramMessageText("GCMS 测试消息", "来自「"+siteName+"」的连接测试：看到这条消息说明频道推送已就绪。", "")
	text = strings.TrimRight(text, "\n")
	msgID, err := telegramSendMessage(telegramAPIBase, token, channel, text)
	if err != nil {
		return http.StatusBadGateway, map[string]any{"ok": false, "error": "telegram_api_error", "message": err.Error()}
	}
	return http.StatusOK, map[string]any{"ok": true, "message_id": msgID}
}

// adminTelegramTest POST /admin/settings/telegram/test：站点设置页的测试发送（JSON）。
func (s *Server) adminTelegramTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	status, payload := s.telegramTestSendPayload(r)
	writeJSON(w, status, payload)
}

// ---------- 站点卡片「Telegram 频道」弹窗（站点管理页就地配置） ----------

// siteServerByID 站点卡片弹窗端点：按 {id} 解析目标站点的 server（平台经运行时池；
// 单站部署没有池，站点管理页只有 ID 1 的当前站点）。
func (s *Server) siteServerByID(raw string) (*Server, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return nil, false
	}
	if pool := s.runtimePool(); pool != nil {
		if rt, ok := pool.runtimeByID(id); ok && rt != nil && rt.server != nil {
			return rt.server, true
		}
		return nil, false
	}
	if id == 1 {
		return s, true
	}
	return nil, false
}

// adminSiteTelegramSave POST /admin/sites/{id}/telegram：站点卡片弹窗保存（不跳设置页）。
// AJAX（Accept JSON）返回 {ok}/{ok:false,message} 供就地回显；普通表单回退 flash + 303 回站点页。
func (s *Server) adminSiteTelegramSave(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	fail := func(status int, code, msg string) {
		if wantsJSON(r) {
			writeJSON(w, status, map[string]any{"ok": false, "error": code, "message": msg})
			return
		}
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: msg})
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
	}
	siteServer, ok := s.siteServerByID(r.PathValue("id"))
	if !ok {
		fail(http.StatusNotFound, "site_not_found", "站点不存在或未启用。")
		return
	}
	if msg := siteServer.applyTelegramSettingsForm(r); msg != "" {
		fail(http.StatusBadRequest, "invalid_form", msg)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Telegram 频道设置已保存。"})
		return
	}
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: "Telegram 频道设置已保存。"})
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

// adminSiteTelegramTest POST /admin/sites/{id}/telegram/test：站点卡片弹窗的测试发送（JSON）。
func (s *Server) adminSiteTelegramTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	siteServer, ok := s.siteServerByID(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "site_not_found", "message": "站点不存在或未启用。"})
		return
	}
	status, payload := siteServer.telegramTestSendPayload(r)
	writeJSON(w, status, payload)
}

// ---------- 平台级共用 bot（站点管理页顶部入口） ----------

// adminSavePlatformTelegramBot POST /admin/sites/telegram-bot：保存平台级共用 bot token。
// token 留空 = 保持已保存值（platform_telegram_bot_token_clear=1 显式清除），与站点级纪律一致。
func (s *Server) adminSavePlatformTelegramBot(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	token := strings.TrimSpace(r.FormValue("platform_telegram_bot_token"))
	if token == "" && r.FormValue("platform_telegram_bot_token_clear") != "1" {
		token = s.platformTelegramBotToken() // 留空保持不变
	}
	if err := s.platform.SetSetting(platformTelegramBotTokenKey, token); err != nil {
		s.serverError(w, err)
		return
	}
	msg := "平台 Telegram bot 已保存，站点未填 token 时将共用它。"
	if token == "" {
		msg = "平台 Telegram bot token 已清除。"
	}
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: msg})
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

// adminPlatformTelegramBotTest POST /admin/sites/telegram-bot/test：getMe 验证 token 本身可用（JSON），
// 成功返回 bot 用户名回执；错误原样回显（已脱敏，不含 token）。
func (s *Server) adminPlatformTelegramBotTest(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	token := strings.TrimSpace(r.FormValue("platform_telegram_bot_token"))
	if token == "" {
		token = s.platformTelegramBotToken()
	}
	if token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "telegram_not_configured", "message": "请先填写 Bot Token。"})
		return
	}
	bot, err := telegramGetMe(telegramAPIBase, token)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "telegram_api_error", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bot": bot})
}

// ---------- 订阅数观测（自动化 API） ----------

// apiStatsTelegram GET /stats/telegram（stats:read）：Bot API getChatMemberCount 的订阅数。
// 响应 {ok,members}，缓存 1 小时；未配置返回 400 telegram_not_configured。
func (s *Server) apiStatsTelegram(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScopeStatsRead); !ok {
		return
	}
	token := s.telegramEffectiveBotToken() // 站点级优先，空则回退平台级共用 bot
	channel := strings.TrimSpace(s.store.Setting(telegramChannelSetting))
	if token == "" || channel == "" {
		apiError(w, http.StatusBadRequest, "telegram_not_configured", "该站点尚未配置 Telegram 频道（需要 Bot Token 与频道 ID），无法读取订阅数。")
		return
	}
	cacheKey := "telegram|" + channel
	if payload, ok := s.statsCacheGet(cacheKey); ok {
		writeJSON(w, http.StatusOK, payload)
		return
	}
	members, err := telegramGetChatMemberCount(telegramAPIBase, token, channel)
	if err != nil {
		apiError(w, http.StatusBadGateway, "telegram_api_error", err.Error())
		return
	}
	payload := map[string]any{"ok": true, "members": members}
	s.statsCachePut(cacheKey, payload)
	writeJSON(w, http.StatusOK, payload)
}

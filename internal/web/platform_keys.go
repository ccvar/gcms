package web

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/platform"
)

// ===================== 平台自动化密钥后台（多站 AI 管理，v2 P3） =====================
//
// UI 在站点管理页（/admin/sites，{{if .PlatformMode}}）；这些 handler 全部在 /admin/sites/automation/keys*
// 下，天然被 platformOnlyPath 的 /admin/sites/ 前缀覆盖。密钥、成员范围、跨站审计都存平台库（非站库）。

// newPlatformToken 造一把平台密钥明文 + 前缀（gcmsp_ 前缀，与站点 gcms_ 区分）。
func newPlatformToken() (token, prefix string) {
	token = "gcmsp_" + randToken()
	prefix = token
	if len(prefix) > 13 {
		prefix = prefix[:13]
	}
	return token, prefix
}

// platformMembershipFromForm 读成员模式（all|allowlist）与白名单站点 ID。
func platformMembershipFromForm(r *http.Request) (mode string, siteIDs []int64) {
	_ = r.ParseForm()
	mode = strings.TrimSpace(r.FormValue("membership"))
	if mode != platform.KeyMembershipAll {
		mode = platform.KeyMembershipAllowlist
	}
	for _, v := range r.Form["site_ids"] {
		if id := atoi64(v); id > 0 {
			siteIDs = append(siteIDs, id)
		}
	}
	return mode, siteIDs
}

// adminPlatformAutomation 渲染独立的「平台 AI 接入」页（/admin/automation），承载平台密钥管理。
func (s *Server) adminPlatformAutomation(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.Redirect(w, r, "/admin/settings/automation", http.StatusSeeOther)
		return
	}
	sess, _ := s.currentSession(r)
	v := s.adminView(r, "平台 AI 接入")
	s.platformAuthed(v, sess)
	if sites, err := s.platform.Sites(); err == nil {
		v.PlatformSites = sites // 成员范围勾选用
	}
	var newPlatformSecret []string
	if f, ok := s.sess.takeSettingsFlash(sessionToken(r)); ok {
		v.Flash = f.Flash
		newPlatformSecret = f.NewPlatformSecret
	}
	s.populatePlatformKeys(v, r, newPlatformSecret)
	s.rnd.Admin(w, "platform_automation", http.StatusOK, v)
}

func (s *Server) redirectPlatformKeys(w http.ResponseWriter, r *http.Request, flash string, newSecret ...string) {
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: flash, NewPlatformSecret: newSecret})
	http.Redirect(w, r, "/admin/automation", http.StatusSeeOther)
}

func (s *Server) platformKeysError(w http.ResponseWriter, r *http.Request, msg string) {
	// 用顶部 Flash 提示（页面顶部横幅），错误信息随重定向回 /admin/automation 展示。
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: msg})
	http.Redirect(w, r, "/admin/automation", http.StatusSeeOther)
}

// populatePlatformKeys 把密钥列表、跨站审计、以及创建/重生成后一次性明文（来自 flash）填进 View。
func (s *Server) populatePlatformKeys(v *View, r *http.Request, newSecret []string) {
	if s == nil || v == nil || s.platform == nil {
		return
	}
	v.PlatformKeys, _ = s.platform.ListPlatformKeys()
	v.PlatformKeyLogs, _ = s.platform.ListPlatformAutomationLogs(30)
	v.PlatformSkillURL = "/admin/automation/platform-skill.zip"
	if len(newSecret) == 0 {
		return
	}
	apiBase := s.absForPlatformRequest(r, "/api/platform/v1")
	v.NewPlatformSecret = newSecret[0]
	scopesCSV := ""
	if len(newSecret) > 1 {
		scopesCSV = newSecret[1]
		v.NewPlatformScopes = scopesCSV
	}
	if len(newSecret) > 2 {
		v.NewPlatformName = newSecret[2]
	}
	if len(newSecret) > 3 {
		v.NewPlatformKeyID = atoi64(newSecret[3])
	}
	if len(newSecret) > 4 {
		v.NewPlatformMembership = newSecret[4]
	}
	v.NewPlatformBrief = platformAssistantBriefMarkdown(automationSkillOptions{apiBase: apiBase, token: newSecret[0], name: v.NewPlatformName, scopes: scopesCSV})
}

func (s *Server) adminCreatePlatformKey(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.platformKeysError(w, r, "用途名称不能为空。")
		return
	}
	scopes := automationScopesFromForm(r)
	mode, siteIDs := platformMembershipFromForm(r)
	token, prefix := newPlatformToken()
	id, err := s.platform.CreatePlatformKey(name, token, prefix, mode, strings.Join(scopes, ","), siteIDs, time.Time{})
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectPlatformKeys(w, r, "平台访问密钥已创建，请在列表中点“查看”复制密钥。", token, strings.Join(scopes, ","), name, strconv.FormatInt(id, 10), mode)
}

func (s *Server) adminUpdatePlatformKey(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	id := atoi64(r.FormValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))
	if id <= 0 {
		s.platformKeysError(w, r, "访问权限不存在。")
		return
	}
	if name == "" {
		s.platformKeysError(w, r, "用途名称不能为空。")
		return
	}
	scopes := automationScopesFromFormRequired(r)
	if len(scopes) == 0 {
		s.platformKeysError(w, r, "至少选择一项权限。")
		return
	}
	mode, siteIDs := platformMembershipFromForm(r)
	if err := s.platform.UpdatePlatformKey(id, name, mode, strings.Join(scopes, ","), siteIDs, time.Time{}); err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectPlatformKeys(w, r, "平台访问密钥已更新。")
}

func (s *Server) adminRegeneratePlatformKey(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	id := atoi64(r.FormValue("id"))
	key, ok, err := s.platform.GetPlatformKey(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		s.platformKeysError(w, r, "访问权限不存在。")
		return
	}
	if !key.RevokedAt.IsZero() {
		s.platformKeysError(w, r, "这条访问权限已吊销，不能重新生成密钥。")
		return
	}
	token, prefix := newPlatformToken()
	if err := s.platform.RotatePlatformKeyToken(id, token, prefix); err != nil {
		if err == sql.ErrNoRows {
			s.platformKeysError(w, r, "这条访问权限已失效，不能重新生成密钥。")
			return
		}
		s.serverError(w, err)
		return
	}
	s.redirectPlatformKeys(w, r, "平台访问密钥已重新生成，请在列表中点“查看”复制新密钥。", token, key.Scopes, key.Name, strconv.FormatInt(id, 10), key.MembershipMode)
}

func (s *Server) adminRevokePlatformKey(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	key, ok, err := s.platform.GetPlatformKey(atoi64(r.FormValue("id")))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		s.platformKeysError(w, r, "访问权限不存在。")
		return
	}
	if !key.RevokedAt.IsZero() {
		s.platformKeysError(w, r, "这条访问权限已吊销。")
		return
	}
	if err := s.platform.RevokePlatformKey(key.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectPlatformKeys(w, r, "平台访问密钥已吊销。")
}

func (s *Server) adminDeletePlatformKey(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	key, ok, err := s.platform.GetPlatformKey(atoi64(r.FormValue("id")))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		s.platformKeysError(w, r, "访问权限不存在。")
		return
	}
	if key.RevokedAt.IsZero() {
		s.platformKeysError(w, r, "只能删除已吊销的访问权限。")
		return
	}
	if err := s.platform.DeletePlatformKey(key.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectPlatformKeys(w, r, "已删除这条平台访问权限记录。")
}

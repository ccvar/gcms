package web

// 技能包的 API 下载/版本端点（密钥鉴权，供 gcms Pilot 等客户端「就地升级」已导入的技能包）：
//   - 单站：GET /api/admin/v1/skill-pack(.../version)，平台命名空间同样注册（/api/platform/v1/sites/{id}/...）。
//   - 平台薄包：GET /api/platform/v1/skill-pack(.../version)，仅平台密钥（在 servePlatformAPI 里先于 siteID 解析拦截）。
// 下发的都是**不含密钥**的原始包（.env.example）——客户端的密钥在自己的钥匙串里，升级只换脚本与文档。
// 版本 = 服务端构建版本（包内容随服务端代码生成，服务端升级 ≡ 包升级）。

import (
	"net/http"
	"strings"

	"cms.ccvar.com/internal/version"
)

const packVersionHeader = "X-GCMS-Version"

// packAPIBase 从请求路径推导包内 .env.example 应写的 API base：
// 客户端从哪个 base 下载，包里就指回哪个 base（去掉 /skill-pack 后缀即是）。
func (s *Server) packAPIBase(r *http.Request) string {
	p := strings.TrimSuffix(strings.TrimSuffix(r.URL.Path, "/version"), "/skill-pack")
	if strings.HasPrefix(p, "/api/platform/") {
		return s.absForPlatformRequest(r, p)
	}
	return s.absForRequest(r, p)
}

// apiSkillPackVersion GET .../skill-pack/version → {"version":"v1.3.x"}。任意有效密钥可查。
func (s *Server) apiSkillPackVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationToken(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": version.Version})
}

// apiDownloadSkillPack GET .../skill-pack → 单站原始技能包 zip（无密钥）。任意有效密钥可下。
func (s *Server) apiDownloadSkillPack(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationToken(w, r); !ok {
		return
	}
	w.Header().Set(packVersionHeader, version.Version)
	s.writeAutomationSkillZip(w, automationSkillOptions{apiBase: s.packAPIBase(r)})
}

// servePlatformSkillPack 处理平台级 /api/platform/v1/skill-pack(.../version)。
// 鉴权与 servePlatformDiscovery 同款：仅平台密钥（站点密钥不发平台薄包）。
func (s *Server) servePlatformSkillPack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET。")
		return
	}
	if s.platform == nil {
		apiError(w, http.StatusNotFound, "platform_api_disabled", "未启用平台模式。")
		return
	}
	token := apiTokenFromRequest(r)
	if !s.checkAPIRateLimit(w, r, token) {
		return
	}
	if token == "" {
		apiError(w, http.StatusUnauthorized, "missing_token", "缺少访问密钥。")
		return
	}
	key, isPlat, err := s.platform.GetPlatformKeyByToken(token)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "auth_error", err.Error())
		return
	}
	if !isPlat || key == nil {
		apiError(w, http.StatusUnauthorized, "invalid_token", "访问密钥无效或不是平台密钥。")
		return
	}
	if s.platformAutomationKilled() {
		apiError(w, http.StatusForbidden, "platform_automation_disabled", "平台自动化已被全局关闭。")
		return
	}
	_ = s.platform.TouchPlatformKey(key.ID)
	if strings.HasSuffix(r.URL.Path, "/version") {
		writeJSON(w, http.StatusOK, map[string]string{"version": version.Version})
		return
	}
	w.Header().Set(packVersionHeader, version.Version)
	s.writePlatformSkillZip(w, automationSkillOptions{apiBase: s.absForPlatformRequest(r, "/api/platform/v1")})
}

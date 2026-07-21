package web

// PilotAssistantAutomationScopes 返回 Pilot 运营助手使用的平台密钥权限。
//
// 这不是后台管理员权限：它只覆盖公开给自动化 API 的内容、站点资料、媒体、
// 导航、语种和统计能力。平台密钥使用“全部站点”成员模式后，新建站点也会自动可见。
func PilotAssistantAutomationScopes() []string {
	scopes := []string{
		apiScopeLanguagesRead,
		apiScopeLanguagesWrite,
		apiScopeLanguagesEnable,
		apiScopeLanguagesDefault,
		apiScopeLanguagesCatalog,
		apiScopeMediaWrite,
		apiScopeSiteRead,
		apiScopeSiteWrite,
		apiScopeBrandAssetsWrite,
		apiScopeNavigationRead,
		apiScopeNavigationWrite,
		apiScopeStatsRead,
		// 扩展内容类型无法预先枚举；content:* 是它们的读写发布通配权限。
		apiScopeContentRead,
		apiScopeContentWrite,
		apiScopeContentPublish,
	}
	seen := make(map[string]bool, len(scopes)+16)
	for _, scope := range scopes {
		seen[scope] = true
	}
	// 内置集合还有分类、置顶等非通配动作，必须逐项加入。
	for _, collection := range automationCollections {
		for _, action := range automationScopeActions(collection.path) {
			scope := apiScope(collection.path, action)
			if !seen[scope] {
				scopes = append(scopes, scope)
				seen[scope] = true
			}
		}
	}
	return scopes
}

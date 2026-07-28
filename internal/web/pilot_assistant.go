package web

// PilotAssistantAutomationScopes 返回 Pilot 运营助手使用的平台密钥权限。
//
// 这不是无条件的后台管理员权限：内容能力仍受细粒度 scope 约束，
// 删站、域名和安全设置等高风险操作还要求用户在 Pilot UI 中用后台密码签发
// 短时、按操作绑定的授权。平台密钥使用“全部站点”成员模式后，新建站点也会自动可见。
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
		// 新页面工程使用独立权限族，不能依赖旧 content:* 隐式继承。
		apiScopePagesRead,
		apiScopePageProjectsRead,
		apiScopePageProjectsWrite,
		apiScopePageProjectsBuild,
		apiScopePageAssetsWrite,
		apiScopePageAppsWrite,
		apiScopePagePreviewRead,
		apiScopePagesPublish,
		apiScopePageCapabilitiesRequest,
		// Grant remains a distinct high-risk scope. The built-in Pilot may reach
		// its endpoint only so the server can issue a target-bound native
		// password challenge; old content:* keys never inherit this scope.
		apiScopePageCapabilitiesGrant,
	}
	// Pilot 运营助手密钥可发现统一控制契约。这些 scope 只是第一道门；
	// 标记 requires_unlock 的操作没有后台密码仍然无法执行。初始密码没有
	// HTTP 写 scope 或控制操作，只能由 Pilot 经 SSH stdin 调 GCMS 专用 CLI。
	for _, scope := range platformControlScopes() {
		scopes = append(scopes, scope)
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

package web

import (
	"net/http"
	"strings"
)

// platformControlOpenAPISpec is the stable, platform-level contract used by
// Pilot and the generated skill pack. Site-content OpenAPI remains separate.
func platformControlOpenAPISpec(apiBase string) map[string]any {
	mutationParams := func(unlock bool) []any {
		out := []any{
			map[string]any{"name": "dry_run", "in": "query", "schema": map[string]any{"type": "boolean"}, "description": "Validate and report impact without changing data."},
			map[string]any{"name": controlConfirmHeader, "in": "header", "schema": map[string]any{"type": "string"}, "description": "For execution, exact operation id from capabilities."},
			map[string]any{"name": controlIdempotencyHeader, "in": "header", "schema": map[string]any{"type": "string", "minLength": 8, "maxLength": 128}, "description": "Stable id reused only when retrying the identical operation."},
		}
		if unlock {
			out = append(out, map[string]any{"name": controlUnlockHeader, "in": "header", "schema": map[string]any{"type": "string"}, "description": "Short-lived operation-bound token issued after Pilot UI password verification."})
		}
		return out
	}
	pilotUIParam := map[string]any{
		"name": controlUIRequestHeader, "in": "header", "required": true,
		"schema":      map[string]any{"type": "string", "const": controlUIPilotValue},
		"description": "Required. Password input must come from Pilot's native UI, never the skill CLI.",
	}
	jsonBody := func(ref string) map[string]any {
		return map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/" + ref}}}}
	}
	response := map[string]any{"200": map[string]any{"description": "Success"}, "400": map[string]any{"description": "Invalid input"}, "403": map[string]any{"description": "Missing scope, confirmation, membership, or unlock"}, "409": map[string]any{"description": "Conflict or idempotency collision"}, "422": map[string]any{"description": "Validation failed"}}
	paths := map[string]any{
		"/control/capabilities": map[string]any{"get": map[string]any{"operationId": "control.capabilities", "summary": "Inspect live operations, scopes and risk", "responses": response}},
		"/control/openapi.json": map[string]any{"get": map[string]any{"operationId": "control.openapi", "summary": "Read this control API contract", "responses": response}},
		"/control/sites": map[string]any{
			"get":  map[string]any{"operationId": "sites.list", "summary": "List manageable sites including disabled members", "responses": response},
			"post": map[string]any{"operationId": "sites.create", "summary": "Validate or create a real child site", "parameters": mutationParams(false), "requestBody": jsonBody("SiteCreateInput"), "responses": response},
		},
		"/control/sites/{siteId}": map[string]any{
			"parameters": []any{map[string]any{"name": "siteId", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int64"}}},
			"get":        map[string]any{"operationId": "sites.get", "summary": "Read one manageable site", "responses": response},
			"patch":      map[string]any{"operationId": "sites.update", "summary": "Validate or update site metadata/status", "parameters": mutationParams(false), "requestBody": jsonBody("SiteUpdateInput"), "responses": response},
			"delete":     map[string]any{"operationId": "sites.delete", "summary": "Validate or archive-delete a disabled non-default site", "parameters": mutationParams(true), "responses": response},
		},
		"/control/sites/{siteId}/categories/{collection}/{categoryId}": map[string]any{
			"parameters": []any{
				map[string]any{"name": "siteId", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int64"}},
				map[string]any{"name": "collection", "in": "path", "required": true, "schema": map[string]any{"type": "string"}, "description": "Collection name such as posts, links, or products."},
				map[string]any{"name": "categoryId", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int64"}},
				map[string]any{"name": "remove_navigation", "in": "query", "schema": map[string]any{"type": "boolean", "default": false}, "description": "Also remove exact navigation references reported by dry-run."},
				map[string]any{"name": "expected_revision", "in": "query", "schema": map[string]any{"type": "string"}, "description": "Required for execution; copy impact_revision from the matching dry-run."},
			},
			"delete": map[string]any{"operationId": "categories.delete", "summary": "Inspect impact or delete one category; associated content becomes uncategorized", "parameters": mutationParams(true), "responses": response},
		},
		"/control/sites/{siteId}/navigation/{index}": map[string]any{
			"parameters": []any{
				map[string]any{"name": "siteId", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int64"}},
				map[string]any{"name": "index", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "minimum": 0}, "description": "Zero-based index from the latest navigation response."},
				map[string]any{"name": "expected_url", "in": "query", "schema": map[string]any{"type": "string"}, "description": "Required for execution; guards against deleting an item after the list changed."},
				map[string]any{"name": "expected_revision", "in": "query", "schema": map[string]any{"type": "string"}, "description": "Required for execution; copy impact_revision from the matching dry-run."},
			},
			"delete": map[string]any{"operationId": "navigation.delete", "summary": "Inspect or delete one navigation item", "parameters": mutationParams(true), "responses": response},
		},
		"/control/themes": map[string]any{"get": map[string]any{"operationId": "themes.list", "summary": "List structured theme choices for AI recommendation", "responses": response}},
		"/control/themes/{themeId}": map[string]any{
			"parameters": []any{map[string]any{"name": "themeId", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}},
			"get":        map[string]any{"operationId": "themes.get", "summary": "Read one theme", "responses": response},
		},
		"/control/sites/{siteId}/theme": map[string]any{
			"parameters": []any{map[string]any{"name": "siteId", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int64"}}},
			"get":        map[string]any{"operationId": "themes.current", "summary": "Read selected and rollback themes", "responses": response},
			"put":        map[string]any{"operationId": "themes.apply", "summary": "Validate, apply, or rollback a theme", "parameters": mutationParams(false), "requestBody": jsonBody("ThemeApplyInput"), "responses": response},
		},
		"/control/sites/{siteId}/domains": map[string]any{
			"parameters": []any{map[string]any{"name": "siteId", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int64"}}},
			"get":        map[string]any{"operationId": "domains.read", "summary": "Read GCMS internal primary and redirect domains", "responses": response},
			"put":        map[string]any{"operationId": "domains.apply", "summary": "Validate or replace GCMS internal domain records", "parameters": mutationParams(true), "requestBody": jsonBody("DomainApplyInput"), "responses": response},
		},
		"/control/sites/{siteId}/public-access": map[string]any{
			"parameters": []any{map[string]any{"name": "siteId", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int64"}}},
			"get":        map[string]any{"operationId": "public_access.read", "summary": "Read GCMS-owned DNS, Caddy and HTTPS status", "responses": response},
			"post":       map[string]any{"operationId": "public_access.apply", "summary": "Configure public access through GCMS-owned integrations", "parameters": mutationParams(true), "requestBody": jsonBody("PublicAccessInput"), "responses": response},
		},
		"/control/security": map[string]any{"get": map[string]any{"operationId": "security.status", "summary": "Read initial-password status; password writes use the server-local GCMS CLI", "responses": response}},
		"/control/unlock": map[string]any{
			"post":   map[string]any{"operationId": "control.unlock", "summary": "Pilot-UI-only short-lived unlock", "parameters": []any{pilotUIParam}, "requestBody": jsonBody("UnlockInput"), "responses": response},
			"delete": map[string]any{"operationId": "control.unlock.revoke", "summary": "Revoke a short-lived unlock", "parameters": []any{map[string]any{"name": controlUnlockHeader, "in": "header", "required": true, "schema": map[string]any{"type": "string"}}}, "responses": response},
		},
	}
	return map[string]any{
		"openapi":  "3.1.0",
		"info":     map[string]any{"title": "GCMS Platform Control API", "version": "v1", "description": "Additive management API. It exposes password status but never accepts an initial-password write; Pilot uses the server-local GCMS CLI for that transition."},
		"servers":  []any{map[string]any{"url": strings.TrimRight(apiBase, "/")}},
		"security": []any{map[string]any{"bearerAuth": []any{}}},
		"paths":    paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer"}},
			"schemas": map[string]any{
				"SiteCreateInput": map[string]any{"type": "object", "required": []string{"slug"}, "properties": map[string]any{
					"slug":                          map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,62}$"},
					"name":                          map[string]any{"type": "string"},
					"seed_mode":                     map[string]any{"type": "string", "enum": []string{"empty", "demo"}, "default": "empty"},
					"site_kind":                     map[string]any{"type": "string", "enum": []string{"content", "factory", "dtc"}, "default": "content"},
					"management_automation_enabled": map[string]any{"type": "boolean", "default": true, "description": "Keep true so Pilot can continue building the new site without requiring an admin login."},
				}},
				"SiteUpdateInput":  map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"enabled", "disabled"}}, "management_automation_enabled": map[string]any{"type": "boolean"}}},
				"ThemeApplyInput":  map[string]any{"type": "object", "properties": map[string]any{"theme_id": map[string]any{"type": "string"}, "rollback": map[string]any{"type": "boolean"}}},
				"DomainApplyInput": map[string]any{"type": "object", "properties": map[string]any{"primary_domain": map[string]any{"type": "string"}, "redirect_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}},
				"PublicAccessInput": map[string]any{"type": "object", "required": []string{"primary_domain"}, "properties": map[string]any{
					"primary_domain":   map[string]any{"type": "string", "description": "主域名，不含协议、端口或路径"},
					"redirect_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "可选别名域名，GCMS 会配置 301 到主域名"},
					"auto_dns":         map[string]any{"type": "boolean", "default": true, "description": "使用 GCMS 自己保存的 Cloudflare 配置自动创建或更新 DNS"},
					"cloudflare_proxy": map[string]any{"type": "boolean", "description": "可选；为 true 时先以灰云完成 DNS 与源站 HTTPS，验证通过后再自动开启橙云代理"},
				}},
				"UnlockInput": map[string]any{"type": "object", "writeOnly": true, "required": []string{"password", "operations"}, "properties": map[string]any{"password": map[string]any{"type": "string", "format": "password", "writeOnly": true}, "operations": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}},
			},
		},
	}
}

func (s *Server) servePlatformControlOpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET。")
		return
	}
	if _, ok := s.requirePlatformControlKey(w, r, apiScopeControlRead); !ok {
		return
	}
	writeJSON(w, http.StatusOK, platformControlOpenAPISpec(s.platformBaseURL+"/api/platform/v1"))
}

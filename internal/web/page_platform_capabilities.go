package web

import (
	"net/http"
)

const (
	pagePlatformAPIVersion      = "v1"
	pagePlatformContractVersion = "1"
	pagePlatformPhase           = "page-platform-phase-1"

	pagePlatformIdempotencyHeader = "Idempotency-Key"
	pagePlatformConcurrencyHeader = "If-Match"
)

// 页面平台 scopes 是独立权限族。尤其不能经 automationScopeAllowed 的
// content:{action} 兼容通配获得，否则升级前的内容密钥会在升级后自动拥有
// 应用源码、资源或运行时能力批准权限。
const (
	apiScopePageProjectsRead        = "page_projects:read"
	apiScopePageProjectsWrite       = "page_projects:write"
	apiScopePageProjectsBuild       = "page_projects:build"
	apiScopePageAssetsWrite         = "page_assets:write"
	apiScopePageAppsWrite           = "page_apps:write"
	apiScopePagePreviewRead         = "page_preview:read"
	apiScopePagesRead               = "pages:read"
	apiScopePagesPublish            = "pages:publish"
	apiScopePageCapabilitiesRequest = "page_capabilities:request"
	apiScopePageCapabilitiesGrant   = "page_capabilities:grant"
)

const (
	pagePlatformRiskRead        = "read"
	pagePlatformRiskWrite       = "write"
	pagePlatformRiskSensitive   = "sensitive"
	pagePlatformRiskDestructive = "destructive"

	pagePlatformConfirmationNone              = "none"
	pagePlatformConfirmationExplicit          = "explicit"
	pagePlatformConfirmationApprovalToken     = "approval_token"
	pagePlatformConfirmationImpactAndExplicit = "impact_and_explicit"

	pagePlatformConcurrencyNone                    = "none"
	pagePlatformConcurrencyIfMatch                 = "if_match"
	pagePlatformConcurrencyContentRevisionBound    = "content_revision_bound"
	pagePlatformConcurrencyBaseRevisionAndIfMatch  = "base_revision_and_if_match"
	pagePlatformConcurrencyApprovalRevisionAndETag = "approval_revision_and_if_match"
)

// pagePlatformModeCapability 区分“协议认识这种模式”和“当前版本已能创建、
// 编辑、预览并发布这种模式”。三种模式始终在能力响应中出现，Pilot 不得
// 仅根据版本号猜测 composition/app 已经可用。
type pagePlatformModeCapability struct {
	ID                string `json:"id"`
	Label             string `json:"label"`
	Available         bool   `json:"available"`
	ManifestVersions  []int  `json:"manifest_versions"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// pagePlatformOperation 沿用 control capability 的“操作即契约”结构，并
// 补充页面修订所需的幂等与乐观并发语义。
//
// Granted 只表示当前密钥拥有 RequiredScope；Available 只表示服务端已经
// 接线。两者故意独立，避免“有权限但旧服务器不支持”和“服务器支持但密钥
// 没权限”被客户端混为一谈。
type pagePlatformOperation struct {
	ID                     string `json:"id"`
	Label                  string `json:"label"`
	RequiredScope          string `json:"required_scope,omitempty"`
	Risk                   string `json:"risk"`
	Method                 string `json:"method"`
	Endpoint               string `json:"endpoint"`
	RequiresConfirmation   bool   `json:"requires_confirmation"`
	Confirmation           string `json:"confirmation"`
	RequiresUnlock         bool   `json:"requires_unlock"`
	SupportsDryRun         bool   `json:"supports_dry_run"`
	RequiresIdempotencyKey bool   `json:"requires_idempotency_key"`
	Concurrency            string `json:"concurrency"`
	Available              bool   `json:"available"`
	Granted                bool   `json:"granted"`
	UnavailableReason      string `json:"unavailable_reason,omitempty"`
}

type pagePlatformFeatures struct {
	RevisionConflict              bool `json:"revision_conflict"`
	PrivatePreview                bool `json:"private_preview"`
	StandardPagePrivatePreview    bool `json:"standard_page_private_preview"`
	ProjectRevisionPrivatePreview bool `json:"project_revision_private_preview"`
	PilotDesignContext            bool `json:"pilot_design_context"`
	StaticExport                  bool `json:"static_export"`
	CapabilityBridge              bool `json:"capability_bridge"`
	PublishApprovalToken          bool `json:"publish_approval_token"`
}

// pagePlatformLimits 是页面平台的单一默认限制来源。后续接入配置项时，只
// 应在 pagePlatformServerLimits 汇总覆盖值，不能把大小限制散落到 handler、
// 后台和 Pilot。
type pagePlatformLimits struct {
	MaxManifestBytes          int64 `json:"max_manifest_bytes"`
	MaxSections               int   `json:"max_sections"`
	MaxNestingDepth           int   `json:"max_nesting_depth"`
	MaxBindingQueries         int   `json:"max_binding_queries"`
	MaxBindingResultsPerQuery int   `json:"max_binding_results_per_query"`
	MaxAssets                 int   `json:"max_assets"`
	MaxAssetBytes             int64 `json:"max_asset_bytes"`
	MaxProjectAssetBytes      int64 `json:"max_project_asset_bytes"`
	MaxAppPackageBytes        int64 `json:"max_app_package_bytes"`
	MaxAppTextFileBytes       int64 `json:"max_app_text_file_bytes"`
	MaxAppFiles               int   `json:"max_app_files"`
	MaxAppUnpackedBytes       int64 `json:"max_app_unpacked_bytes"`
	MaxAppCompressionRatio    int   `json:"max_app_compression_ratio"`
	MaxBridgeCallsPerMinute   int   `json:"max_bridge_calls_per_minute"`
	MaxBridgeRequestBytes     int64 `json:"max_bridge_request_bytes"`
	PrivatePreviewTTLSeconds  int   `json:"private_preview_ttl_seconds"`
}

type pagePlatformDescriptor struct {
	Version            string                       `json:"version"`
	Modes              []pagePlatformModeCapability `json:"modes"`
	ManifestVersions   map[string][]int             `json:"manifest_versions"`
	BindingUpdateModes []string                     `json:"binding_update_modes"`
	Features           pagePlatformFeatures         `json:"features"`
	Limits             pagePlatformLimits           `json:"limits"`
}

type pagePlatformMutationProtocol struct {
	IdempotencyHeader     string `json:"idempotency_header"`
	ConcurrencyHeader     string `json:"concurrency_header"`
	ETagRequiredOnWrites  bool   `json:"etag_required_on_revision_writes"`
	ApprovalRevisionBound bool   `json:"approval_token_revision_bound"`
}

type pagePlatformCapabilitiesResponse struct {
	APIVersion       string                       `json:"api_version"`
	Phase            string                       `json:"phase"`
	PagePlatform     pagePlatformDescriptor       `json:"page_platform"`
	Operations       []pagePlatformOperation      `json:"operations"`
	MutationProtocol pagePlatformMutationProtocol `json:"mutation_protocol"`
}

func pagePlatformModes() []pagePlatformModeCapability {
	return []pagePlatformModeCapability{
		{
			ID:               "standard",
			Label:            "标准页面",
			Available:        true,
			ManifestVersions: []int{},
		},
		{
			ID:               "composition",
			Label:            "自由编排页面",
			Available:        true,
			ManifestVersions: []int{1},
		},
		{
			ID:               "app",
			Label:            "互动应用",
			Available:        true,
			ManifestVersions: []int{1},
		},
	}
}

func pagePlatformServerLimits() pagePlatformLimits {
	const (
		oneMiB = int64(1 << 20)
	)
	return pagePlatformLimits{
		MaxManifestBytes:          int64(CompositionLimits.MaxManifestBytes),
		MaxSections:               CompositionLimits.MaxSections,
		MaxNestingDepth:           CompositionLimits.MaxDepth,
		MaxBindingQueries:         CompositionLimits.MaxBindings,
		MaxBindingResultsPerQuery: CompositionLimits.MaxBindingLimit,
		MaxAssets:                 200,
		MaxAssetBytes:             20 * oneMiB,
		MaxProjectAssetBytes:      100 * oneMiB,
		MaxAppPackageBytes:        20 * oneMiB,
		MaxAppTextFileBytes:       pageAppTextEditMaxBytes,
		MaxAppFiles:               500,
		MaxAppUnpackedBytes:       100 * oneMiB,
		MaxAppCompressionRatio:    100,
		MaxBridgeCallsPerMinute:   60,
		MaxBridgeRequestBytes:     256 << 10,
		PrivatePreviewTTLSeconds:  int(frontendPreviewTTL.Seconds()),
	}
}

func pagePlatformScopes() []string {
	return []string{
		apiScopePageProjectsRead,
		apiScopePageProjectsWrite,
		apiScopePageProjectsBuild,
		apiScopePageAssetsWrite,
		apiScopePageAppsWrite,
		apiScopePagePreviewRead,
		apiScopePageCapabilitiesRequest,
		apiScopePageCapabilitiesGrant,
	}
}

// pagePlatformScopeAllowed 只对已有 pages:read/pages:publish 保留内容 API
// 的历史通配语义。其他页面平台 scope 均要求密钥中显式存在，不能经
// content:read/write/publish 继承。
func pagePlatformScopeAllowed(scopes map[string]bool, required string) bool {
	if required == "" {
		return true
	}
	switch required {
	case apiScopePagesRead, apiScopePagesPublish:
		return automationScopeAllowed(scopes, required)
	}
	return scopes[required]
}

func pagePlatformOperationImplemented(id string) bool {
	switch id {
	case "page_platform.capabilities",
		"page_design_context.get",
		"page_projects.list",
		"page_projects.create",
		"page_projects.get",
		"pages.convert_plan",
		"pages.convert",
		"page_revisions.list",
		"page_revisions.get",
		"page_revisions.create",
		"page_revisions.restore",
		"page_components.list",
		"page_data_sources.list",
		"page_bindings.preview",
		"page_assets.list",
		"page_assets.upload",
		"page_apps.upload",
		"page_apps.source.read",
		"page_apps.source.edit",
		"page_projects.validate",
		"page_builds.create",
		"page_builds.get",
		"standard_pages.preview",
		"pages.preview",
		"pages.publish_plan",
		"pages.publish",
		"page_publications.list",
		"pages.rollback_plan",
		"pages.rollback",
		"page_capabilities.list",
		"page_capabilities.request",
		"page_capabilities.grant",
		"page_capabilities.revoke":
		return true
	default:
		return false
	}
}

func pagePlatformOperationCatalog() []pagePlatformOperation {
	operations := []pagePlatformOperation{
		{
			ID: "page_platform.capabilities", Label: "读取页面平台能力",
			Risk: pagePlatformRiskRead, Method: http.MethodGet,
			Endpoint:     "/page-platform/capabilities",
			Confirmation: pagePlatformConfirmationNone,
			Concurrency:  pagePlatformConcurrencyNone,
		},
		{
			ID: "page_design_context.get", Label: "读取 Pilot 页面设计上下文",
			RequiredScope: apiScopePageProjectsRead, Risk: pagePlatformRiskRead,
			Method: http.MethodGet, Endpoint: "/page-design-context",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "page_projects.list", Label: "发现页面工程",
			RequiredScope: apiScopePageProjectsRead, Risk: pagePlatformRiskRead,
			Method: http.MethodGet, Endpoint: "/page-projects",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "page_projects.create", Label: "创建自由页面工程",
			RequiredScope: apiScopePageProjectsWrite, Risk: pagePlatformRiskWrite,
			Method: http.MethodPost, Endpoint: "/page-projects",
			Confirmation:           pagePlatformConfirmationNone,
			RequiresIdempotencyKey: true, Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "page_projects.get", Label: "读取自由页面工程",
			RequiredScope: apiScopePageProjectsRead, Risk: pagePlatformRiskRead,
			Method: http.MethodGet, Endpoint: "/page-projects/{project_id}",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "page_projects.update", Label: "修改自由页面工程",
			RequiredScope: apiScopePageProjectsWrite, Risk: pagePlatformRiskWrite,
			Method: http.MethodPatch, Endpoint: "/page-projects/{project_id}",
			Confirmation:           pagePlatformConfirmationNone,
			RequiresIdempotencyKey: true, Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "page_projects.delete", Label: "删除自由页面工程",
			RequiredScope: apiScopePageProjectsWrite, Risk: pagePlatformRiskDestructive,
			Method: http.MethodDelete, Endpoint: "/page-projects/{project_id}",
			RequiresConfirmation: true, Confirmation: pagePlatformConfirmationImpactAndExplicit,
			RequiresUnlock: true, SupportsDryRun: true, RequiresIdempotencyKey: true,
			Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "pages.convert_plan", Label: "预检标准页面转换",
			RequiredScope: apiScopePageProjectsWrite, Risk: pagePlatformRiskRead,
			Method: http.MethodPost, Endpoint: "/pages/{page_id}/convert-plan",
			Confirmation: pagePlatformConfirmationNone, SupportsDryRun: true,
			Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "pages.convert", Label: "转换标准页面副本",
			RequiredScope: apiScopePageProjectsWrite, Risk: pagePlatformRiskWrite,
			Method: http.MethodPost, Endpoint: "/pages/{page_id}/convert",
			RequiresConfirmation: true, Confirmation: pagePlatformConfirmationExplicit,
			RequiresIdempotencyKey: true, Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "page_revisions.list", Label: "读取页面修订列表",
			RequiredScope: apiScopePageProjectsRead, Risk: pagePlatformRiskRead,
			Method: http.MethodGet, Endpoint: "/page-projects/{project_id}/revisions",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "page_revisions.get", Label: "读取页面修订",
			RequiredScope: apiScopePageProjectsRead, Risk: pagePlatformRiskRead,
			Method: http.MethodGet, Endpoint: "/page-projects/{project_id}/revisions/{revision_id}",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "page_revisions.create", Label: "创建页面修订",
			RequiredScope: apiScopePageProjectsWrite, Risk: pagePlatformRiskWrite,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/revisions",
			Confirmation:           pagePlatformConfirmationNone,
			RequiresIdempotencyKey: true, Concurrency: pagePlatformConcurrencyBaseRevisionAndIfMatch,
		},
		{
			ID: "page_revisions.restore", Label: "恢复页面修订",
			RequiredScope: apiScopePageProjectsWrite, Risk: pagePlatformRiskWrite,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/restore",
			RequiresConfirmation: true, Confirmation: pagePlatformConfirmationExplicit,
			RequiresIdempotencyKey: true, Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "page_components.list", Label: "读取页面组件",
			RequiredScope: apiScopePageProjectsRead, Risk: pagePlatformRiskRead,
			Method: http.MethodGet, Endpoint: "/page-components",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "page_data_sources.list", Label: "读取页面数据源",
			RequiredScope: apiScopePageProjectsRead, Risk: pagePlatformRiskRead,
			Method: http.MethodGet, Endpoint: "/page-data-sources",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "page_bindings.preview", Label: "预览页面数据绑定",
			RequiredScope: apiScopePageProjectsRead, Risk: pagePlatformRiskRead,
			Method: http.MethodPost, Endpoint: "/page-bindings/preview",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "page_assets.list", Label: "读取页面资源",
			RequiredScope: apiScopePageProjectsRead, Risk: pagePlatformRiskRead,
			Method: http.MethodGet, Endpoint: "/page-projects/{project_id}/assets",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "page_assets.upload", Label: "上传页面资源",
			RequiredScope: apiScopePageAssetsWrite, Risk: pagePlatformRiskWrite,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/assets",
			Confirmation:           pagePlatformConfirmationNone,
			RequiresIdempotencyKey: true, Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "page_assets.delete", Label: "删除页面资源",
			RequiredScope: apiScopePageAssetsWrite, Risk: pagePlatformRiskDestructive,
			Method: http.MethodDelete, Endpoint: "/page-projects/{project_id}/assets/{asset_id}",
			RequiresConfirmation: true, Confirmation: pagePlatformConfirmationImpactAndExplicit,
			RequiresUnlock: true, SupportsDryRun: true, RequiresIdempotencyKey: true,
			Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "page_apps.upload", Label: "上传互动应用包",
			RequiredScope: apiScopePageAppsWrite, Risk: pagePlatformRiskSensitive,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/app-package",
			RequiresConfirmation: true, Confirmation: pagePlatformConfirmationExplicit,
			RequiresIdempotencyKey: true, Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "page_apps.source.read", Label: "读取互动应用文本源码",
			RequiredScope: apiScopePageAppsWrite, Risk: pagePlatformRiskRead,
			Method: http.MethodGet, Endpoint: "/page-projects/{project_id}/app-files/{file_path}",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "page_apps.source.edit", Label: "编辑互动应用文本源码",
			RequiredScope: apiScopePageAppsWrite, Risk: pagePlatformRiskSensitive,
			Method: http.MethodPut, Endpoint: "/page-projects/{project_id}/app-files/{file_path}",
			RequiresConfirmation: true, Confirmation: pagePlatformConfirmationExplicit,
			RequiresIdempotencyKey: true, Concurrency: pagePlatformConcurrencyBaseRevisionAndIfMatch,
		},
		{
			ID: "page_projects.validate", Label: "校验页面工程",
			RequiredScope: apiScopePageProjectsBuild, Risk: pagePlatformRiskRead,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/validate",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "page_builds.create", Label: "生成页面构建",
			RequiredScope: apiScopePageProjectsBuild, Risk: pagePlatformRiskWrite,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/builds",
			Confirmation:           pagePlatformConfirmationNone,
			RequiresIdempotencyKey: true, Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "page_builds.get", Label: "读取页面构建",
			RequiredScope: apiScopePageProjectsRead, Risk: pagePlatformRiskRead,
			Method: http.MethodGet, Endpoint: "/page-projects/{project_id}/builds/{build_id}",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "standard_pages.preview", Label: "创建标准页面私有预览",
			RequiredScope: apiScopePagesRead, Risk: pagePlatformRiskRead,
			Method: http.MethodPost, Endpoint: "/pages/{page_id}/preview-url",
			Confirmation: pagePlatformConfirmationNone,
			Concurrency:  pagePlatformConcurrencyContentRevisionBound,
		},
		{
			ID: "pages.preview", Label: "创建指定修订的私有预览",
			RequiredScope: apiScopePagePreviewRead, Risk: pagePlatformRiskRead,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/preview-url",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "pages.publish_plan", Label: "预检页面发布",
			RequiredScope: apiScopePagesPublish, Risk: pagePlatformRiskRead,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/publish-plan",
			Confirmation: pagePlatformConfirmationNone, SupportsDryRun: true,
			Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "pages.publish", Label: "发布页面修订",
			RequiredScope: apiScopePagesPublish, Risk: pagePlatformRiskSensitive,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/publish",
			RequiresConfirmation: true, Confirmation: pagePlatformConfirmationApprovalToken,
			RequiresUnlock: true, RequiresIdempotencyKey: true,
			Concurrency: pagePlatformConcurrencyApprovalRevisionAndETag,
		},
		{
			ID: "page_publications.list", Label: "读取页面发布记录",
			RequiredScope: apiScopePageProjectsRead, Risk: pagePlatformRiskRead,
			Method: http.MethodGet, Endpoint: "/page-projects/{project_id}/publications",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "pages.rollback_plan", Label: "预检页面回滚",
			RequiredScope: apiScopePagesPublish, Risk: pagePlatformRiskRead,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/rollback-plan",
			Confirmation: pagePlatformConfirmationNone, SupportsDryRun: true,
			Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "pages.rollback", Label: "回滚线上页面",
			RequiredScope: apiScopePagesPublish, Risk: pagePlatformRiskSensitive,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/rollback",
			RequiresConfirmation: true, Confirmation: pagePlatformConfirmationApprovalToken,
			RequiresUnlock: true, RequiresIdempotencyKey: true,
			Concurrency: pagePlatformConcurrencyApprovalRevisionAndETag,
		},
		{
			ID: "page_capabilities.list", Label: "读取互动应用能力",
			RequiredScope: apiScopePageProjectsRead, Risk: pagePlatformRiskRead,
			Method: http.MethodGet, Endpoint: "/page-projects/{project_id}/capabilities",
			Confirmation: pagePlatformConfirmationNone, Concurrency: pagePlatformConcurrencyNone,
		},
		{
			ID: "page_capabilities.request", Label: "请求互动应用能力",
			RequiredScope: apiScopePageCapabilitiesRequest, Risk: pagePlatformRiskSensitive,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/capabilities/request",
			RequiresConfirmation: true, Confirmation: pagePlatformConfirmationExplicit,
			RequiresIdempotencyKey: true, Concurrency: pagePlatformConcurrencyIfMatch,
		},
		{
			ID: "page_capabilities.grant", Label: "批准互动应用能力",
			RequiredScope: apiScopePageCapabilitiesGrant, Risk: pagePlatformRiskSensitive,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/capabilities/apply",
			RequiresConfirmation: true, Confirmation: pagePlatformConfirmationApprovalToken,
			RequiresUnlock: true, RequiresIdempotencyKey: true,
			Concurrency: pagePlatformConcurrencyApprovalRevisionAndETag,
		},
		{
			ID: "page_capabilities.revoke", Label: "撤销互动应用能力",
			RequiredScope: apiScopePageCapabilitiesGrant, Risk: pagePlatformRiskSensitive,
			Method: http.MethodPost, Endpoint: "/page-projects/{project_id}/capabilities/revoke",
			RequiresConfirmation: true, Confirmation: pagePlatformConfirmationExplicit,
			RequiresIdempotencyKey: true, Concurrency: pagePlatformConcurrencyIfMatch,
		},
	}

	for i := range operations {
		operations[i].Available = pagePlatformOperationImplemented(operations[i].ID)
		if !operations[i].Available {
			operations[i].UnavailableReason = "phase_not_implemented"
		}
	}
	return operations
}

func pagePlatformCapabilities(scopes map[string]bool) pagePlatformCapabilitiesResponse {
	operations := pagePlatformOperationCatalog()
	for i := range operations {
		operations[i].Granted = pagePlatformScopeAllowed(scopes, operations[i].RequiredScope)
	}
	return pagePlatformCapabilitiesResponse{
		APIVersion: pagePlatformAPIVersion,
		Phase:      pagePlatformPhase,
		PagePlatform: pagePlatformDescriptor{
			Version: pagePlatformContractVersion,
			Modes:   pagePlatformModes(),
			ManifestVersions: map[string][]int{
				"composition": {1},
				"app":         {1},
			},
			BindingUpdateModes: []string{"live"},
			Features: pagePlatformFeatures{
				RevisionConflict:              true,
				PrivatePreview:                true,
				StandardPagePrivatePreview:    true,
				ProjectRevisionPrivatePreview: true,
				PilotDesignContext:            true,
				StaticExport:                  true,
				CapabilityBridge:              true,
				PublishApprovalToken:          true,
			},
			Limits: pagePlatformServerLimits(),
		},
		Operations: operations,
		MutationProtocol: pagePlatformMutationProtocol{
			IdempotencyHeader:     pagePlatformIdempotencyHeader,
			ConcurrencyHeader:     pagePlatformConcurrencyHeader,
			ETagRequiredOnWrites:  true,
			ApprovalRevisionBound: true,
		},
	}
}

// servePagePlatformCapabilities 只要求一把有效 automation token。能力发现的
// 目的正是让老 Key 看见服务端已经支持新协议，同时通过 operation.granted
// 明确得知自己没有任何新权限。
func (s *Server) servePagePlatformCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET。")
		return
	}
	auth, ok := s.requireAutomationToken(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, pagePlatformCapabilities(auth.scopes))
}

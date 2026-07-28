package web

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPagePlatformSkillScriptsExposeSafeDialogueWorkflow(t *testing.T) {
	files := []string{
		filepath.Join("skillsrc", "gcms_single.js"),
		filepath.Join("skillsrc", "gcms_platform.js"),
		filepath.Join("..", "..", "skills", "gcms-content-assistant", "scripts", "gcms.js"),
	}
	commands := []string{
		"page-context", "page-capabilities", "page-projects", "page-get", "page-create", "page-update",
		"page-revisions", "page-revision", "page-restore",
		"page-components", "page-data-sources", "page-binding-preview",
		"page-assets", "page-asset-upload", "page-app-upload",
		"page-app-source-read", "page-app-source-edit", "page-capability-list",
		"page-capability-request", "page-capability-grant", "page-capability-revoke",
		"page-validate", "page-build", "page-build-get",
		"page-preview", "page-publish-plan", "page-publish",
		"page-publications", "page-rollback-plan", "page-rollback",
	}
	for _, filename := range files {
		raw, err := os.ReadFile(filename)
		if err != nil {
			t.Fatalf("read %s: %v", filename, err)
		}
		script := string(raw)
		for _, command := range commands {
			if !strings.Contains(script, `"`+command+`"`) {
				t.Errorf("%s missing %s", filename, command)
			}
		}
		for _, required := range []string{
			`"If-Match": etag`,
			`"Idempotency-Key": requestID`,
			`process.env.GCMS_CONTROL_UNLOCK_TOKEN`,
			`safe_to_overwrite = false`,
			`unlock_challenge`,
			`pageETagOptions(rest)`,
			`pagePathSegments(filePath)`,
			`"/capabilities/apply"`,
			`"page_capabilities.grant"`,
			`pageMutationOptions(rest.slice(1), { confirm: true });`,
			`headers["X-GCMS-Control-Confirm"] = "page_apps.upload"`,
		} {
			if !strings.Contains(script, required) {
				t.Errorf("%s missing protocol fragment %q", filename, required)
			}
		}
		for _, forbidden := range []string{
			"GCMS_ADMIN_PASSWORD",
			"GCMS_PAGE_APPROVAL_TOKEN",
			`body.approval_token`,
			`body.password`,
		} {
			if strings.Contains(script, forbidden) {
				t.Errorf("%s exposes forbidden page confirmation input %q", filename, forbidden)
			}
		}
	}
}

func TestPagePlatformSkillDescriptionsCoverFullDialogueSurface(t *testing.T) {
	mirror, err := os.ReadFile(filepath.Join("..", "..", "skills", "gcms-content-assistant", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	documents := map[string]string{
		"generated single-site SKILL.md": automationSkillMarkdown("https://site.test/api/admin/v1"),
		"generated platform SKILL.md":    platformSkillMarkdown("https://platform.test/api/platform/v1"),
		"repository SKILL.md":            string(mirror),
	}
	for name, document := range documents {
		for _, command := range []string{
			"page-context", "page-projects", "page-revisions", "page-revision", "page-restore",
			"page-components", "page-data-sources", "page-binding-preview",
			"page-assets", "page-build-get", "page-publications",
			"page-app-source-read", "page-app-source-edit",
			"page-capability-list", "page-capability-request",
			"page-capability-grant", "page-capability-revoke",
		} {
			if !strings.Contains(document, command) {
				t.Errorf("%s missing %s", name, command)
			}
		}
		for _, boundary := range []string{"If-Match", "request-id", "Pilot"} {
			if !strings.Contains(document, boundary) {
				t.Errorf("%s missing security boundary %s", name, boundary)
			}
		}
	}
}

func TestPagePlatformSkillNeverInventsProjectETags(t *testing.T) {
	for name, content := range map[string]string{
		"single skill":   automationSkillMarkdown(""),
		"platform skill": platformSkillMarkdown(""),
	} {
		if strings.Contains(content, "page-project-42-rev-") {
			t.Fatalf("%s contains a fabricated ETag example", name)
		}
		if !strings.Contains(content, "copy _protocol.etag verbatim") {
			t.Fatalf("%s must tell Pilot to copy the live ETag verbatim", name)
		}
	}
}

func TestPagePlatformOpenAPIDocumentsDialogueOperations(t *testing.T) {
	spec := automationOpenAPISpec("https://site.test/api/admin/v1")
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatal("openapi paths missing")
	}
	for _, endpoint := range []string{
		"/page-platform/capabilities",
		"/page-design-context",
		"/page-projects",
		"/page-projects/{project_id}",
		"/page-projects/{project_id}/revisions",
		"/page-projects/{project_id}/revisions/{revision_id}",
		"/page-projects/{project_id}/restore",
		"/page-components",
		"/page-data-sources",
		"/page-bindings/preview",
		"/page-projects/{project_id}/assets",
		"/page-projects/{project_id}/app-package",
		"/page-projects/{project_id}/app-files/{file_path}",
		"/page-projects/{project_id}/validate",
		"/page-projects/{project_id}/builds",
		"/page-projects/{project_id}/builds/{build_id}",
		"/page-projects/{project_id}/preview-url",
		"/page-projects/{project_id}/publish-plan",
		"/page-projects/{project_id}/publish",
		"/page-projects/{project_id}/publications",
		"/page-projects/{project_id}/rollback-plan",
		"/page-projects/{project_id}/rollback",
		"/page-projects/{project_id}/capabilities",
		"/page-projects/{project_id}/capabilities/request",
		"/page-projects/{project_id}/capabilities/apply",
		"/page-projects/{project_id}/capabilities/revoke",
		"/control/unlock",
	} {
		if _, exists := paths[endpoint]; !exists {
			t.Errorf("openapi missing %s", endpoint)
		}
	}
	for endpoint := range paths {
		if strings.Contains(endpoint, "{projectId}") {
			t.Errorf("OpenAPI contains ambiguous legacy path parameter spelling: %s", endpoint)
		}
	}

	operation := func(endpoint, method string) map[string]any {
		t.Helper()
		pathItem, ok := paths[endpoint].(map[string]any)
		if !ok {
			t.Fatalf("OpenAPI path %s = %#v", endpoint, paths[endpoint])
		}
		op, ok := pathItem[method].(map[string]any)
		if !ok {
			t.Fatalf("OpenAPI operation %s %s = %#v", method, endpoint, pathItem[method])
		}
		return op
	}
	requiredHeader := func(op map[string]any, name string) bool {
		t.Helper()
		parameters, _ := op["parameters"].([]map[string]any)
		for _, parameter := range parameters {
			if parameter["in"] == "header" && parameter["name"] == name {
				required, _ := parameter["required"].(bool)
				return required
			}
		}
		return false
	}
	hasHeader := func(op map[string]any, name string) bool {
		t.Helper()
		parameters, _ := op["parameters"].([]map[string]any)
		for _, parameter := range parameters {
			if parameter["in"] == "header" && parameter["name"] == name {
				return true
			}
		}
		return false
	}
	for _, item := range []struct {
		endpoint string
		method   string
	}{
		{"/page-design-context", "get"},
		{"/page-projects", "get"},
		{"/page-projects/{project_id}/revisions", "get"},
		{"/page-projects/{project_id}/revisions/{revision_id}", "get"},
		{"/page-projects/{project_id}/restore", "post"},
		{"/page-components", "get"},
		{"/page-data-sources", "get"},
		{"/page-bindings/preview", "post"},
		{"/page-projects/{project_id}/assets", "get"},
		{"/page-projects/{project_id}/builds/{build_id}", "get"},
		{"/page-projects/{project_id}/publications", "get"},
		{"/page-projects/{project_id}/app-files/{file_path}", "get"},
		{"/page-projects/{project_id}/app-files/{file_path}", "put"},
		{"/page-projects/{project_id}/capabilities", "get"},
		{"/page-projects/{project_id}/capabilities/request", "post"},
		{"/page-projects/{project_id}/capabilities/apply", "post"},
		{"/page-projects/{project_id}/capabilities/revoke", "post"},
	} {
		operation(item.endpoint, item.method)
	}
	for _, endpoint := range []string{
		"/page-projects/{project_id}/validate",
		"/page-projects/{project_id}/preview-url",
		"/page-projects/{project_id}/publish-plan",
		"/page-projects/{project_id}/rollback-plan",
	} {
		op := operation(endpoint, "post")
		if !requiredHeader(op, pagePlatformConcurrencyHeader) {
			t.Errorf("%s must require If-Match", endpoint)
		}
		if requiredHeader(op, pagePlatformIdempotencyHeader) {
			t.Errorf("%s must not require Idempotency-Key", endpoint)
		}
	}
	for _, endpoint := range []string{
		"/page-projects",
		"/page-projects/{project_id}/revisions",
		"/page-projects/{project_id}/restore",
		"/page-projects/{project_id}/assets",
		"/page-projects/{project_id}/builds",
		"/page-projects/{project_id}/publish",
		"/page-projects/{project_id}/rollback",
	} {
		op := operation(endpoint, "post")
		if !requiredHeader(op, pagePlatformConcurrencyHeader) ||
			!requiredHeader(op, pagePlatformIdempotencyHeader) {
			t.Errorf("%s mutation must require If-Match and Idempotency-Key", endpoint)
		}
	}
	var createCatalog pagePlatformOperation
	for _, candidate := range pagePlatformOperationCatalog() {
		if candidate.ID == "page_projects.create" {
			createCatalog = candidate
			break
		}
	}
	if createCatalog.Concurrency != pagePlatformConcurrencyIfMatch ||
		!createCatalog.RequiresIdempotencyKey {
		t.Fatalf("page project create catalog does not match the handler: %#v", createCatalog)
	}
	capabilityJSON := toJSONForTest(pagePlatformCapabilities(nil))
	for _, unsupportedClaim := range []string{
		"bridge_timeout_seconds",
		"max_page_projects_per_site",
	} {
		if strings.Contains(capabilityJSON, unsupportedClaim) {
			t.Errorf("capability catalog must not advertise unenforced limit %s", unsupportedClaim)
		}
	}

	apply := operation("/page-projects/{project_id}/capabilities/apply", "post")
	body, _ := apply["requestBody"].(map[string]any)
	bodyJSON := strings.ToLower(strings.TrimSpace(toJSONForTest(body)))
	if strings.Contains(bodyJSON, "approval_token") {
		t.Fatal("capability grant OpenAPI must not accept approval_token in the JSON body")
	}
	if !strings.Contains(bodyJSON, `"required":["capability","decision"]`) {
		t.Fatalf("capability grant OpenAPI must require capability and decision: %s", bodyJSON)
	}
	if !strings.Contains(bodyJSON, `"additionalproperties":false`) {
		t.Fatalf("capability grant OpenAPI must reject undeclared password/token fields: %s", bodyJSON)
	}
	if !requiredHeader(apply, pagePlatformConcurrencyHeader) ||
		!requiredHeader(apply, pagePlatformIdempotencyHeader) {
		t.Fatal("capability grant must require If-Match and Idempotency-Key")
	}
	if !hasHeader(apply, controlUnlockHeader) {
		t.Fatal("capability grant must document the Pilot-native unlock header")
	}
	for _, endpoint := range []string{
		"/page-projects/{project_id}/publish",
		"/page-projects/{project_id}/rollback",
	} {
		if !hasHeader(operation(endpoint, "post"), controlUnlockHeader) {
			t.Errorf("%s must document the Pilot-native unlock header", endpoint)
		}
	}

	legacyPageUpdate := operation("/pages/{id}", "patch")
	if requiredHeader(legacyPageUpdate, pagePlatformConcurrencyHeader) ||
		!hasHeader(legacyPageUpdate, pagePlatformConcurrencyHeader) {
		t.Fatal("legacy page update must document optional If-Match compatibility")
	}
	legacyResponses, _ := legacyPageUpdate["responses"].(map[string]any)
	legacySuccess, _ := legacyResponses["200"].(map[string]any)
	legacyHeaders, _ := legacySuccess["headers"].(map[string]any)
	if _, ok := legacyHeaders["ETag"]; !ok {
		t.Fatal("legacy page update must document the returned strong ETag")
	}
}

func TestBundledPagePlatformOpenAPITracksGeneratedContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "skills", "gcms-content-assistant", "references", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("decode bundled OpenAPI: %v", err)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatal("bundled OpenAPI paths missing")
	}
	for _, endpoint := range []string{
		"/page-design-context",
		"/page-projects/{project_id}/revisions",
		"/page-projects/{project_id}/revisions/{revision_id}",
		"/page-projects/{project_id}/restore",
		"/page-components",
		"/page-data-sources",
		"/page-bindings/preview",
		"/page-projects/{project_id}/assets",
		"/page-projects/{project_id}/app-files/{file_path}",
		"/page-projects/{project_id}/capabilities",
		"/page-projects/{project_id}/capabilities/request",
		"/page-projects/{project_id}/capabilities/apply",
		"/page-projects/{project_id}/capabilities/revoke",
		"/page-projects/{project_id}/builds/{build_id}",
		"/page-projects/{project_id}/publications",
	} {
		if _, exists := paths[endpoint]; !exists {
			t.Errorf("bundled OpenAPI missing %s", endpoint)
		}
	}
	if strings.Contains(string(raw), "{projectId}") {
		t.Fatal("bundled OpenAPI still uses the legacy projectId path spelling")
	}
	if strings.Contains(strings.ToLower(string(raw)), "approval_token") {
		t.Fatal("bundled OpenAPI exposes an internal page approval token")
	}

	operation := func(endpoint, method string) map[string]any {
		t.Helper()
		pathItem, ok := paths[endpoint].(map[string]any)
		if !ok {
			t.Fatalf("bundled OpenAPI path %s = %#v", endpoint, paths[endpoint])
		}
		op, ok := pathItem[method].(map[string]any)
		if !ok {
			t.Fatalf("bundled OpenAPI operation %s %s = %#v", method, endpoint, pathItem[method])
		}
		return op
	}
	hasParameterRef := func(op map[string]any, ref string) bool {
		t.Helper()
		parameters, _ := op["parameters"].([]any)
		for _, value := range parameters {
			parameter, _ := value.(map[string]any)
			if parameter["$ref"] == ref {
				return true
			}
		}
		return false
	}
	ifMatchRef := "#/components/parameters/PageIfMatch"
	idempotencyRef := "#/components/parameters/PageIdempotencyKey"
	unlockRef := "#/components/parameters/PageControlUnlock"
	for _, endpoint := range []string{
		"/page-projects/{project_id}/validate",
		"/page-projects/{project_id}/preview-url",
		"/page-projects/{project_id}/publish-plan",
		"/page-projects/{project_id}/rollback-plan",
	} {
		op := operation(endpoint, "post")
		if !hasParameterRef(op, ifMatchRef) || hasParameterRef(op, idempotencyRef) {
			t.Errorf("%s must require If-Match and omit Idempotency-Key", endpoint)
		}
	}
	for _, endpoint := range []string{
		"/page-projects",
		"/page-projects/{project_id}/restore",
		"/page-projects/{project_id}/capabilities/request",
		"/page-projects/{project_id}/capabilities/apply",
		"/page-projects/{project_id}/capabilities/revoke",
	} {
		op := operation(endpoint, "post")
		if !hasParameterRef(op, ifMatchRef) || !hasParameterRef(op, idempotencyRef) {
			t.Errorf("%s mutation must require If-Match and Idempotency-Key", endpoint)
		}
	}
	for _, endpoint := range []string{
		"/page-projects/{project_id}/publish",
		"/page-projects/{project_id}/rollback",
		"/page-projects/{project_id}/capabilities/apply",
	} {
		if !hasParameterRef(operation(endpoint, "post"), unlockRef) {
			t.Errorf("%s must document the Pilot-native unlock header", endpoint)
		}
	}
	components, _ := spec["components"].(map[string]any)
	pathItems, _ := components["pathItems"].(map[string]any)
	legacyPageUpdate, _ := pathItems["UpdatePages"].(map[string]any)
	if !hasParameterRef(legacyPageUpdate, "#/components/parameters/ContentIfMatch") {
		t.Fatal("bundled legacy page update must document optional strong ETag concurrency")
	}
	if !strings.Contains(string(raw), "build_stale") {
		t.Fatal("bundled OpenAPI must document build_stale publication conflicts")
	}
	parameters, _ := components["parameters"].(map[string]any)
	projectID, _ := parameters["PageProjectId"].(map[string]any)
	if projectID["name"] != "project_id" {
		t.Fatalf("bundled PageProjectId name = %v", projectID["name"])
	}
	requestBodies, _ := components["requestBodies"].(map[string]any)
	decisionBody, _ := requestBodies["PageCapabilityDecision"].(map[string]any)
	decisionJSON := toJSONForTest(decisionBody)
	if !strings.Contains(decisionJSON, `"required":["capability","decision"]`) {
		t.Fatalf("bundled capability decision must require capability and decision: %s", decisionJSON)
	}
	if !strings.Contains(decisionJSON, `"additionalProperties":false`) {
		t.Fatalf("bundled capability decision must reject undeclared password/token fields: %s", decisionJSON)
	}
}

func TestPlatformControlOpenAPIDocumentsNativePageCapabilityApproval(t *testing.T) {
	spec := platformControlOpenAPISpec("https://platform.test/api/platform/v1")
	components, _ := spec["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	unlock, _ := schemas["UnlockInput"].(map[string]any)
	description, _ := unlock["description"].(string)
	if !strings.Contains(description, pageCapabilityGrant) ||
		!strings.Contains(description, "page_challenge") {
		t.Fatalf("platform unlock contract omits target-bound capability approval: %q", description)
	}
}

func toJSONForTest(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestPagePlatformSkillCLIRoutesAndHeaders(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	mockPath := filepath.Join(dir, "mock-fetch.js")
	mock := `
global.fetch = async (rawURL, init = {}) => {
  const url = new URL(rawURL);
  const jsonHeaders = {"Content-Type":"application/json","ETag":"\"page-project-42-rev-5\""};
  if (url.pathname === "/api/platform/v1/sites") {
    return new Response(JSON.stringify({items:[{id:12,slug:"blog",name:"Blog"}]}), {status:200,headers:jsonHeaders});
  }
  if (process.env.GCMS_MOCK_OLD === "1") {
    return new Response(JSON.stringify({error:"not_found"}), {status:404,headers:jsonHeaders});
  }
  if ((init.method || "GET") === "GET" && /\/pages\/9$/.test(url.pathname)) {
    const status = process.env.GCMS_MOCK_PUBLISHED === "1" ? "published" : "draft";
    return new Response(JSON.stringify({item:{id:9,type:"page",status}}), {
      status:200,
      headers:{"Content-Type":"application/json","ETag":"\"content-9-current\""}
    });
  }
  let body = null;
  if (typeof init.body === "string" && init.body) {
    try { body = JSON.parse(init.body); } catch { body = init.body; }
  }
  const headers = Object.fromEntries(new Headers(init.headers || {}).entries());
  if (process.env.GCMS_MOCK_CHALLENGE === "1" && url.pathname.endsWith("/capabilities/apply")) {
    return new Response(JSON.stringify({
      error:"capability_confirmation_required",unlock_required:true,
      operation:"page_capabilities.grant",unlock_challenge:"challenge-123",
      project_id:42,revision_id:5,capability:"content.read",
      etag:"\"page-project-42-rev-5\"",request_id:"cap-grant-001",
      admin_path:"/admin/pages/7/project"
    }), {status:409,headers:jsonHeaders});
  }
  return new Response(JSON.stringify({
    echo:{method:init.method || "GET",path:url.pathname+url.search,headers,body}
  }), {status:200,headers:jsonHeaders});
};`
	if err := os.WriteFile(mockPath, []byte(mock), 0o600); err != nil {
		t.Fatalf("write fetch mock: %v", err)
	}
	type cliVariant struct {
		name   string
		script string
		base   string
		token  string
		site   []string
		prefix string
	}
	variants := []cliVariant{
		{
			name: "single", script: filepath.Join("skillsrc", "gcms_single.js"),
			base: "http://mock/api/admin/v1", token: "gcms_test",
			prefix: "/api/admin/v1",
		},
		{
			name: "platform", script: filepath.Join("skillsrc", "gcms_platform.js"),
			base: "http://mock/api/platform/v1", token: "gcmsp_test",
			site: []string{"--site", "blog"}, prefix: "/api/platform/v1/sites/12",
		},
	}
	run := func(t *testing.T, variant cliVariant, extraEnv []string, args ...string) (map[string]any, string, error) {
		t.Helper()
		argv := append([]string{variant.script}, args...)
		if len(variant.site) > 0 {
			argv = append(argv[:2], append(variant.site, argv[2:]...)...)
		}
		command := exec.Command(node, argv...)
		command.Env = append(os.Environ(),
			"NODE_OPTIONS=--require="+mockPath,
			"GCMS_API_BASE="+variant.base,
			"GCMS_API_KEY="+variant.token,
			"GCMS_CONTROL_UNLOCK_TOKEN= ",
		)
		command.Env = append(command.Env, extraEnv...)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		runErr := command.Run()
		var output map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatalf("%s JSON output: %v\nstdout=%s\nstderr=%s", variant.name, err, stdout.String(), stderr.String())
		}
		return output, stderr.String(), runErr
	}
	echo := func(t *testing.T, output map[string]any) map[string]any {
		t.Helper()
		item, ok := output["echo"].(map[string]any)
		if !ok {
			t.Fatalf("missing echo: %#v", output)
		}
		return item
	}

	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			output, stderr, runErr := run(t, variant, nil,
				"update", "pages", "9", `{"title":"Safe draft edit"}`,
			)
			if runErr != nil {
				t.Fatalf("standard page draft update: %v %s", runErr, stderr)
			}
			contentEcho := echo(t, output)
			if contentEcho["path"] != variant.prefix+"/pages/9" ||
				contentEcho["method"] != "PATCH" {
				t.Fatalf("standard page update route = %#v", contentEcho)
			}
			contentHeaders := contentEcho["headers"].(map[string]any)
			if contentHeaders["if-match"] != `"content-9-current"` {
				t.Fatalf("standard page update omitted ETag: %#v", contentHeaders)
			}
			blockedOutput, stderr, runErr := run(t, variant, []string{"GCMS_MOCK_PUBLISHED=1"},
				"update", "pages", "9", `{"title":"Unsafe live edit"}`,
			)
			if runErr == nil || blockedOutput["error"] != "legacy_standard_page_protected" ||
				blockedOutput["safe_to_overwrite"] != false {
				t.Fatalf("published standard page update was not blocked: output=%#v err=%v stderr=%s",
					blockedOutput, runErr, stderr)
			}

			output, stderr, runErr = run(t, variant, nil, "page-components")
			if runErr != nil {
				t.Fatalf("page-components: %v %s", runErr, stderr)
			}
			if got := echo(t, output)["path"]; got != variant.prefix+"/page-components" {
				t.Fatalf("components path = %v", got)
			}

			output, stderr, runErr = run(t, variant, nil, "page-context", "--lang", "zh")
			if runErr != nil {
				t.Fatalf("page-context: %v %s", runErr, stderr)
			}
			if got := echo(t, output)["path"]; got != variant.prefix+"/page-design-context?lang=zh" {
				t.Fatalf("page context path = %v", got)
			}

			output, stderr, runErr = run(t, variant, nil,
				"page-projects", "--lang", "zh", "--slug", "campaign-2026", "--mode", "composition",
			)
			if runErr != nil {
				t.Fatalf("page-projects: %v %s", runErr, stderr)
			}
			if got := echo(t, output)["path"]; got != variant.prefix+
				"/page-projects?lang=zh&slug=campaign-2026&mode=composition" {
				t.Fatalf("page projects path = %v", got)
			}

			output, stderr, runErr = run(t, variant, nil,
				"page-publish-plan", "42", "--revision-id", "5", "--etag", `"page-project-42-rev-5"`,
				"--request-id", "legacy-plan-id",
			)
			if runErr != nil {
				t.Fatalf("publish plan: %v %s", runErr, stderr)
			}
			headers := echo(t, output)["headers"].(map[string]any)
			if headers["if-match"] == nil {
				t.Fatal("publish plan omitted If-Match")
			}
			if headers["idempotency-key"] != nil {
				t.Fatalf("publish plan must not send Idempotency-Key: %#v", headers)
			}

			output, stderr, runErr = run(t, variant, nil,
				"page-build", "42", "--revision-id", "5", "--etag", `"page-project-42-rev-5"`,
				"--request-id", "build-revision-005",
			)
			if runErr != nil {
				t.Fatalf("build: %v %s", runErr, stderr)
			}
			headers = echo(t, output)["headers"].(map[string]any)
			if headers["if-match"] == nil || headers["idempotency-key"] != "build-revision-005" {
				t.Fatalf("build mutation headers = %#v", headers)
			}

			appPath := filepath.Join(dir, variant.name+"-app.zip")
			if err := os.WriteFile(appPath, []byte("mock app bundle"), 0o600); err != nil {
				t.Fatal(err)
			}
			output, stderr, runErr = run(t, variant, nil,
				"page-app-upload", "42", appPath,
				"--base-revision-id", "5", "--etag", `"page-project-42-rev-5"`,
				"--request-id", "app-upload-005", "--confirm", "true",
			)
			if runErr != nil {
				t.Fatalf("app upload: %v %s", runErr, stderr)
			}
			appEcho := echo(t, output)
			if appEcho["path"] != variant.prefix+"/page-projects/42/app-package" {
				t.Fatalf("app upload path = %v", appEcho["path"])
			}
			appHeaders := appEcho["headers"].(map[string]any)
			if appHeaders["x-gcms-control-confirm"] != "page_apps.upload" {
				t.Fatalf("app upload confirmation headers = %#v", appHeaders)
			}

			sourcePath := filepath.Join(dir, variant.name+"-app.js")
			if err := os.WriteFile(sourcePath, []byte(`document.body.textContent = "ok";`), 0o600); err != nil {
				t.Fatal(err)
			}
			output, stderr, runErr = run(t, variant, nil,
				"page-app-source-edit", "42", "src/app.js", "@"+sourcePath,
				"--base-revision-id", "5", "--etag", `"page-project-42-rev-5"`,
				"--request-id", "source-revision-006", "--confirm", "true",
			)
			if runErr != nil {
				t.Fatalf("source edit: %v %s", runErr, stderr)
			}
			sourceEcho := echo(t, output)
			if sourceEcho["path"] != variant.prefix+"/page-projects/42/app-files/src/app.js" {
				t.Fatalf("source edit path = %v", sourceEcho["path"])
			}
			sourceBody := sourceEcho["body"].(map[string]any)
			if sourceBody["base_revision_id"] != float64(5) ||
				!strings.Contains(sourceBody["content"].(string), "textContent") {
				t.Fatalf("source edit body = %#v", sourceBody)
			}

			output, stderr, runErr = run(t, variant, nil,
				"page-capability-grant", "42", "content.read",
				"--etag", `"page-project-42-rev-5"`, "--request-id", "cap-grant-001",
				"--confirm", "true",
			)
			if runErr != nil {
				t.Fatalf("capability grant request: %v %s", runErr, stderr)
			}
			grantEcho := echo(t, output)
			grantBody := grantEcho["body"].(map[string]any)
			if grantBody["capability"] != "content.read" || grantBody["decision"] != "approve" {
				t.Fatalf("grant body = %#v", grantBody)
			}
			if _, exists := grantBody["approval_token"]; exists {
				t.Fatalf("grant body exposed approval_token: %#v", grantBody)
			}
			grantHeaders := grantEcho["headers"].(map[string]any)
			if grantHeaders["x-gcms-control-confirm"] != "page_capabilities.grant" ||
				grantHeaders["x-gcms-control-unlock"] != nil {
				t.Fatalf("initial grant headers = %#v", grantHeaders)
			}

			output, stderr, runErr = run(t, variant, []string{"GCMS_CONTROL_UNLOCK_TOKEN=native-unlock-123"},
				"page-capability-grant", "42", "content.read",
				"--etag", `"page-project-42-rev-5"`, "--request-id", "cap-grant-001",
				"--confirm", "true",
			)
			if runErr != nil {
				t.Fatalf("capability grant retry: %v %s", runErr, stderr)
			}
			grantHeaders = echo(t, output)["headers"].(map[string]any)
			if grantHeaders["x-gcms-control-unlock"] != "native-unlock-123" {
				t.Fatalf("grant retry omitted native unlock: %#v", grantHeaders)
			}

			output, _, runErr = run(t, variant, []string{"GCMS_MOCK_CHALLENGE=1"},
				"page-capability-grant", "42", "content.read",
				"--etag", `"page-project-42-rev-5"`, "--request-id", "cap-grant-001",
				"--confirm", "true",
			)
			if runErr == nil {
				t.Fatal("grant challenge must pause with a non-zero status")
			}
			if output["unlock_required"] != true || output["unlock_challenge"] != "challenge-123" ||
				output["operation"] != "page_capabilities.grant" {
				t.Fatalf("grant challenge not preserved: %#v", output)
			}

			output, stderr, runErr = run(t, variant, []string{"GCMS_MOCK_OLD=1"}, "page-components")
			if runErr != nil {
				t.Fatalf("old-server fallback: %v %s", runErr, stderr)
			}
			if output["available"] != false || output["error"] != "page_platform_unavailable" {
				t.Fatalf("old-server fallback = %#v", output)
			}
		})
	}
}

---
name: gcms-content-assistant
description: "Use this skill when operating a GCMS site through its automation API: manage standard content and, when live capabilities allow it, create, revise, build, preview, publish, or roll back composition pages and sandboxed interactive page apps."
---

# GCMS Content Operations Assistant

You are a GCMS content operations assistant. Use this skill to safely inspect, draft, and improve GCMS posts, pages, and links through the automation API.

## Setup

- Read `GCMS_API_BASE` and `GCMS_API_KEY` from the environment or a local `.env` file.
- The script loads `.env` from the current working directory and from this skill folder.
- Use `.env.example` as the local configuration template; never commit a real `GCMS_API_KEY`.
- Prefer `node scripts/gcms.js ...` for API calls when Node.js 18+ is available.
- Read `references/openapi.json` only when endpoint or schema details are needed.
- Read `references/seo-checklist.md` for audits or SEO work.
- Read `references/content-rules.md` for drafting, editing, and final reporting rules.
- Read `references/brand-voice.md` when creating or rewriting user-facing copy.

## Allowed Work

- Run diagnostics for API connectivity, OpenAPI shape, category reads, and media permission.
- List enabled languages.
- List post and link categories.
- Upload images and use the returned URL for `cover_image` or Markdown image embeds.
- List and read posts, pages, and links.
- Preview post and link drafts before publishing.
- Create drafts for posts, pages, and links.
- Update drafts or, with publish permission, update published content.
- Improve titles, excerpts, content, SEO descriptions, keywords, slugs, categories, and link URLs.
- Produce audits and recommendations without changing content.
- Read the live Pilot page-design context and page-platform contract before using page projects.
- Create or revise composition-page manifests without hard-coding business data that belongs in GCMS data bindings.
- Upload validated page images or sandboxed app ZIP packages, build immutable revisions, and create private previews.
- Read the current site profile and, when the user explicitly requests it, adjust the global front-end Logo scale with `brand:assets:write`.
- Publish or roll back a page only through the Pilot-native, target-bound confirmation flow.

## Task Modes

- `doctor`: verify configuration, OpenAPI, read permissions, and media permission before operational work.
- `audit`: inspect content and report issues without changing anything.
- `draft`: create new content as `status: "draft"`.
- `update`: patch existing content only after finding the exact ID.
- `media`: upload approved files and reuse the returned URL in `cover_image` or Markdown.
- `multilingual`: inspect languages and `trans_group`, then handle each language's own item separately.
- `publish-review`: check readiness for publishing; publish only when explicitly asked and permitted.
- `preview`: inspect rendered post or link drafts, including HTML, TOC, public URL, and a short-lived front-end preview URL, before publishing.
- `page-project`: create/revise a themed composition page or interactive app from the Pilot conversation against an exact ETag and immutable base revision.
- `page-release`: validate, build, preview, show the impact plan, then wait for Pilot-native publish/rollback confirmation.
- `logo-display`: read `site-profile`, then update only the top-level `logo_scale` when the user explicitly asks to resize the front-end Logo.

## Hard Boundaries

- Do not delete content.
- Do not change site settings, navigation, security, users, system updates, or category definitions, except the explicitly requested top-level `logo_scale` operation described above.
- Do not publish unless the user explicitly asks and the key has the matching publish scope.
- Do not overwrite one language with another language's body unless the user explicitly asks.
- Do not guess an ID from a title. Search first, then use the exact ID.
- If multiple similar items match, ask the user to confirm before modifying.
- Never ask for, read, print, or save the GCMS backend password.
- Never accept an approval token from conversation text or add `approval_token` to a page request. Pilot native UI owns password verification; the server converts its target-bound unlock internally.
- Never retry a page 409 by overwriting the latest state. Read the new ETag/revision, compare, and create an explicit merged revision.
- Never change the target, ETag, revision, request-id, or payload after an `unlock_required` response. A changed target requires a fresh plan and native confirmation.
- Treat app capability approval as the same native boundary: capability, normalized config, decision, revision, ETag, request-id, site/project/page, and API-key subject are part of the target. Never substitute a broader grant after confirmation.
- Single-item legacy content reads expose `_protocol.etag` when the server supports strong content validators. Send it on updates so concurrent human changes return `revision_conflict`; old clients may omit it only for compatibility.
- For a legacy standard page, `update pages` first reads the strong ETag and is limited to drafts. Do not use the legacy command to modify an already published page or set `status` to `published`/`scheduled`; keep the normal path inside Pilot by reading the conversion plan and converting it to a protected page project. Offer the GCMS backend only when the user explicitly asks for governance or emergency manual takeover.

## Standard Workflow

1. Classify the task: audit, draft, update, publish, multilingual, or category assignment.
2. For a new environment or after permission changes, run `doctor` first.
3. Inspect first. Use `languages`, category lookup, list, upload, or get commands before editing.
4. For updates, find the exact content ID with `q`, `slug`, or `trans_group`.
5. For broad or risky changes, summarize the intended edits before applying them.
6. Default to `status: "draft"` for new content.
7. After writing, read back the item when possible.
8. Before publishing a post or link, use the preview endpoint to inspect rendered HTML and TOC; generate a front-end preview URL when browser review is useful.
9. Report changed IDs, language, status, fields changed, and review points.

For a composition page or app, use this exact sequence:

1. Stay in the Pilot conversation. Run `page-context --lang <code>` first, then `page-capabilities`; require both `available=true` and `granted=true` for every needed operation.
2. Use `page-context.manifest_default`: keep `theme.inherit=true`, theme tokens empty, and `shell.mode=site` unless the user explicitly asks for a standalone canvas. Use its live Hero, navigation, component registry and data sources instead of guessing or copying demo data.
3. For a new page, create a standard `pages` draft first, read its conversion plan, then create the sidecar project. For an existing page, run `page-projects --lang <code> --slug <slug>` to discover its real project ID and latest ETag; if it has no project yet, read the exact standard page and conversion plan. Old standard-page data must remain untouched.
4. Run `page-get` and keep `_protocol.etag` plus `working_revision.ID`.
5. Create a new immutable revision (or upload the app package) with that ETag, base revision, and a stable request-id. Real posts/products/custom content must use bindings and `page-binding-preview`, never hard-coded business values.
6. After every meaningful user-requested change, run `page-validate`, `page-build`, and `page-preview` against the exact revision. Pass the ready `build.id` returned by `page-build` into `page-preview`, so the preview, publish plan and formal publish all target the same verified artifact and real-data snapshot. Return the private preview URL plus a short change summary; do not send the user to the web editor.
7. Check the required desktop, tablet and mobile sizes returned by `page-context.quality`. Treat clipping, horizontal overflow, unreadable content and missing required previews as blockers.
8. Only when the user explicitly asks to publish, run `page-publish-plan`, show the impact and preview, and wait for native approval. A generic “looks good” is not publish authorization.
9. Run the formal command once. If it returns `unlock_required`, preserve the complete structured response—including `operation`, `unlock_challenge`, target IDs, ETag, request-id, and `admin_path`—so Pilot can show its native password UI.
10. After native confirmation, retry only that formal command with byte-equivalent arguments and the same request-id. Do not repeat earlier writes.

## Useful Commands

```bash
node scripts/gcms.js doctor
node scripts/gcms.js languages
node scripts/gcms.js site-profile
node scripts/gcms.js site-profile-update '{"logo_scale":0.8}'
node scripts/gcms.js upload ./cover.webp
node scripts/gcms.js upload ./cover-1.webp ./cover-2.webp ./cover-3.webp
node scripts/gcms.js categories posts --lang zh
node scripts/gcms.js categories links --lang zh
node scripts/gcms.js list posts --lang zh --q keyword
node scripts/gcms.js list posts --lang all --trans_group group
node scripts/gcms.js get posts 123
node scripts/gcms.js similar posts --title "Planned title" --lang zh
node scripts/gcms.js preview posts 123
node scripts/gcms.js preview-url posts 123
node scripts/gcms.js preview links 123
node scripts/gcms.js create posts '{"title":"Title","content":"Body","lang":"zh","status":"draft"}'
node scripts/gcms.js update posts 123 '{"meta_desc":"Updated SEO description"}'
node scripts/gcms.js update pages 321 '{"meta_desc":"Draft page SEO"}' --etag '"content-321-…"'
node scripts/gcms.js update posts 123 '{}' --robots "noindex, follow" --canonical https://example.com/original
node scripts/gcms.js audit posts --lang zh --limit 50
node scripts/gcms.js audit pages --lang zh --limit 20 --deep true
node scripts/gcms.js search-stats --days 28 --limit 100 --group query
node scripts/gcms.js search-stats --days 28 --group page --compare
node scripts/gcms.js search-stats --days 28 --group date
node scripts/gcms.js traffic-stats --days 7
node scripts/gcms.js page-stats --days 7 --limit 50
node scripts/gcms.js analytics-stats --days 30 --limit 200 --group sources
node scripts/gcms.js analytics-stats --days 30 --limit 200 --group geography
node scripts/gcms.js analytics-stats --days 30 --limit 200 --group devices
node scripts/gcms.js analytics-stats --days 30 --limit 100 --group trend
node scripts/gcms.js tg-stats
node scripts/gcms.js page-context --lang zh
node scripts/gcms.js page-capabilities
node scripts/gcms.js page-projects --lang zh --slug campaign-2026
node scripts/gcms.js page-get 42
node scripts/gcms.js page-create @project.json --etag '"content-…"' --request-id pilot-project-001
node scripts/gcms.js page-update 42 @revision.json --etag '<copy _protocol.etag verbatim>' --request-id pilot-revision-004
node scripts/gcms.js page-revisions 42 --limit 100
node scripts/gcms.js page-revision 42 5
node scripts/gcms.js page-restore 42 --revision-id 3 --etag '<copy _protocol.etag verbatim>' --request-id pilot-restore-003 --confirm true
node scripts/gcms.js page-components
node scripts/gcms.js page-data-sources --lang zh
node scripts/gcms.js page-binding-preview @binding-preview.json
node scripts/gcms.js page-assets 42
node scripts/gcms.js page-asset-upload 42 ./hero.webp --logical-key hero --etag '<copy _protocol.etag verbatim>' --request-id pilot-asset-001
node scripts/gcms.js page-app-upload 42 ./app.zip --base-revision-id 4 --etag '<copy _protocol.etag verbatim>' --request-id pilot-app-001 --confirm true
node scripts/gcms.js page-app-source-read 42 src/app.js --revision-id 5
node scripts/gcms.js page-app-source-edit 42 src/app.js @app.js --base-revision-id 5 --etag '<copy _protocol.etag verbatim>' --request-id pilot-source-006 --confirm true
node scripts/gcms.js page-capability-list 42
node scripts/gcms.js page-capability-request 42 content.read --config '{"types":["post"],"max_items":10}' --etag '<copy _protocol.etag verbatim>' --request-id pilot-cap-request-001 --confirm true
node scripts/gcms.js page-capability-grant 42 content.read --etag '<copy _protocol.etag verbatim>' --request-id pilot-cap-grant-001 --confirm true
node scripts/gcms.js page-capability-revoke 42 content.read --etag '<copy _protocol.etag verbatim>' --request-id pilot-cap-revoke-001 --confirm true
node scripts/gcms.js page-build 42 --revision-id 5 --etag '<copy _protocol.etag verbatim>' --request-id pilot-build-005
node scripts/gcms.js page-build-get 42 7
node scripts/gcms.js page-preview 42 --revision-id 5 --build-id 7 --etag '<copy _protocol.etag verbatim>'
node scripts/gcms.js page-publish-plan 42 --revision-id 5 --build-id 7 --etag '<copy _protocol.etag verbatim>'
node scripts/gcms.js page-publish 42 --revision-id 5 --build-id 7 --etag '<copy _protocol.etag verbatim>' --request-id pilot-publish-005 --confirm true
node scripts/gcms.js page-publications 42 --limit 100
```

## Page Project Protocol

- The three modes are independent: `standard`, `composition`, and `app`. If capabilities say a mode or operation is unavailable, keep using the standard page path and report that the server needs an upgrade.
- `page-context` is the first call for page creation. It is a versioned, live design contract: use its theme inheritance defaults, site identity, Hero, navigation, components, real data-source schemas, recipes and required preview sizes.
- Use `page-projects` to rediscover an existing composition/app project by `page_id`, language, slug or mode. Never guess a project ID or scrape it from the backend UI.
- Every page-project response carries `_protocol.etag`. Mutation commands send it as `If-Match` and require `--request-id`; revision-bound reads (`page-validate`, `page-preview`, `page-publish-plan`, `page-rollback-plan`) require only `If-Match`/`--etag` and never consume an idempotency key. A retry may reuse a request-id only for the identical mutation.
- `page-update` creates an immutable revision and requires `base_revision_id` in its JSON body. It never performs an in-place project overwrite.
- Prefer the component schemas and live data sources returned by `page-context`; use `page-binding-preview` instead of hard-coding GCMS business data into props.
- Page asset upload is multipart and server-managed; never invent `storage_ref`. App upload accepts a ZIP only and the server rejects unsafe paths, remote code, undeclared capabilities, and oversized packages.
- App source edits always clone and revalidate the complete private source bundle into a new immutable revision. Capability grant never accepts a backend password or approval token: preserve the `unlock_required` challenge for Pilot native UI, then retry the byte-equivalent request.
- If validation, a publish plan, or publication returns `build_stale`, live bound data changed after the immutable build. Revalidate and create a new build; never publish or relabel the old artifact.
- A conflict result is structured with `safe_to_overwrite:false`. Stop and merge; do not silently replace human edits.
- Publish and rollback are separate from deployment. Report publication and delivery status independently.
- `_links.admin_path` is a governance and emergency fallback. Keep ordinary creation and iteration in Pilot; offer the backend only for manual takeover, version review, publication governance or recovery.

## Duplicate Check Before Drafting (similar)

- Before drafting a new post, run `similar [<collection>] --title "..."` (collection defaults to `posts`; needs only the collection's read scope). It matches the title against existing content (published and drafts) via the site's FTS index and returns `{ok, rows:[{id,title,slug,status,lang,score}]}` with `score` normalized to 0..1 (1 = most similar).
- Example: `{"ok":true,"rows":[{"id":42,"title":"GCMS guide","slug":"gcms-guide","status":"published","lang":"en","score":0.87}]}`.
- If a row scores high (roughly >= 0.6), update that existing item instead of creating a near-duplicate.

## Publish Quality Gate (posts only)

- Setting a post's `status` to `published` through the automation API (create-as-published or an update that sets the status) runs a hard server-side check: effective body length >= 400 words (markdown stripped; CJK counts per character, Latin per word), non-empty `excerpt`, non-empty `meta_desc`, and title length 8-120 characters.
- Failing requests get HTTP 422: `{"error":"quality_gate","failures":["body_too_short (380/400)","excerpt_missing"]}`. Fix each listed failure and retry, or keep the content as a draft (drafts are never gated).

## Per-Item SEO Overrides

- `update` accepts `robots_override` (e.g. `"noindex, follow"`) and `canonical_override` in the JSON body; the CLI flags `--robots` / `--canonical` pass them through.
- `canonical_override` must be a valid absolute http(s) URL, otherwise the API returns 422 `invalid_canonical`. Send an empty string to clear an override.
- Typical uses: point canonical at the original source for syndicated content; temporarily noindex a campaign page.

## Statistics (stats:read)

- `search-stats` returns Search Console performance with clicks, impressions, CTR and average position for the last `--days` days (clamped 1..90, default 28; `--limit` clamped 1..1000, default 100). `--group` accepts `query_page` (default), `query`, `page`, `date`, or `total`; use `--fresh` only when an uncached read is necessary.
- `search-stats --compare` additionally fetches the immediately preceding window of equal length and merges it by the selected group: each row gains `prev_clicks`, `prev_impressions`, `prev_ctr`, `prev_position` (null when the key had no data before). `date` does not support compare.
- `traffic-stats` returns GA active users, sessions, engagement rate and average session duration for the last `--days` days (default 7).
- `page-stats` returns GA per-page traffic rows `{path, active_users, sessions, engagement_rate, average_session_duration}` (default `--days 7`, `--limit 50`, sorted by active users desc). Combine with `search-stats` to pick which old page to improve.
- `analytics-stats` returns GA breakdown rows with the same four quality metrics. `--group` accepts `sources` (default channel + source/medium), `geography` (country + region), `devices` (device + OS + browser), or `trend` (daily rows). Use these dimensions to explain where useful traffic comes from instead of inferring from totals.
- Responses are cached server-side for 1 hour; if the site has no Search Console / GA integration the API returns `search_console_not_connected` / `analytics_not_connected` — ask the user to connect Google in the platform admin first.
- `tg-stats` returns the Telegram channel subscriber count `{ok, members}` via `GET /stats/telegram` (also cached 1 hour). Use it to track reader-to-subscriber conversion. If the site has no Telegram channel configured the API returns `telegram_not_configured` — ask the user to configure it in the site admin (Settings → Telegram) first. On an older server without this command the request returns 404; that is not a failure, just skip it.

## Multilingual Rules

- Before multilingual work, run `languages`.
- Use `trans_group` to find sibling versions.
- Update each language's own ID separately.
- Preserve local language style, terminology, and intent.
- If a translation is missing, draft a new version instead of overwriting another language.

## Category Rules

- Before setting `category_id`, run the matching category command.
- Use post categories only for posts and link categories only for links.
- Category language must match content language.
- If no suitable category exists, leave uncategorized and mention it in the report.

## Media Rules

- Before setting `cover_image`, upload the local file with `node scripts/gcms.js upload <file>`.
- When several media files are ready, pass all of them to one `upload` command. The client uploads with bounded concurrency, retries transient network failures, and returns one batch result. Do not write a model-driven shell loop or retry successful files one by one.
- Use the returned `url` unchanged in `cover_image` or Markdown image syntax.
- Do not upload unrelated or unverified media just to fill a field; mention missing assets when no suitable file exists.

## Publishing Rules

- Treat publishing as a separate, explicit action.
- Confirm that the user requested publishing in the current conversation.
- Confirm the content status, language, and ID before publishing.
- For posts and links, run `preview` and check rendered HTML, TOC, and public URL before publishing. Use `preview-url` when someone needs to open the real front-end page.
- If the immediately preceding completed turn already audited or previewed an explicit set of IDs and the user now asks to publish that same set, do not repeat the full audit or manually prefetch every ETag. Run `update <collection> <id> '{"status":"published"}'` for each confirmed ID; the script reads the latest state and ETag before each PATCH. Re-review only an item whose state or ETag changed.
- Process bulk publishing in small, verifiable batches. Retry a transient read failure at most twice with a short backoff, then stop that item and report completed and unprocessed IDs. Never wait indefinitely or rewrite items already confirmed successful.
- If publish scope is missing, create or update a draft and say publishing was not available.

## Extension Principle

New capabilities should only be added when GCMS exposes a matching API permission, the safety boundary is clear, and the result can be verified and reported.

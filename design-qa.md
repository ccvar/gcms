# Pilot GCMS installer drawer design QA

- Source visual truth:
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-f42de616-bbd4-451c-b35b-be14453f801a.png`
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-8c7a04d1-5b8d-49cd-8fa5-d0597197ca38.png`
- Rendered implementation: `/private/tmp/pilot-gcms-access-link-desktop.png`
- Combined comparison: `/private/tmp/pilot-gcms-comparison-20260720.png`
- Viewport: Pilot window 820 × 560 logical pixels, captured at Retina resolution.
- State: GCMS installer drawer open with two detected installations; one has a public HTTPS address.

## Full-view comparison evidence

The former multi-line introduction card is removed, shortening the drawer before the server list. Its complete content now lives behind the small information icon beside `远程服务器`, matching the annotated target area without changing the drawer width, card hierarchy, or existing visual tokens.

## Focused region comparison evidence

- Header: the information icon sits on the subtitle baseline, keeps the title block compact, and uses the app-wide custom multiline Tooltip.
- Installed-server card: the installation directory remains attached to the folder icon Tooltip.
- Public address: `https://cms.clarcoin.com` is now a visible, truncated-safe button below the fact grid, with an external-open affordance. It no longer depends on non-interactive Tooltip content.
- DOM measurements place both fact cards and the address action fully inside the 400px drawer; no horizontal overflow occurs.

## Required fidelity surfaces

- Fonts and typography: existing application font stack and type hierarchy are unchanged; the new address uses the existing small-action weight.
- Spacing and layout rhythm: the removed intro card saves one full content block; header and card spacing remain aligned with existing 6–12px increments.
- Colors and visual tokens: existing neutral, accent, success, border and hover colors are reused.
- Image quality and asset fidelity: no raster assets are introduced; the implementation reuses Pilot's existing monochrome information and external-open icon language.
- Copy and content: the full installation explanation remains available in the Tooltip, while the actionable URL is visible in the card.

## Findings

No actionable P0, P1 or P2 visual or interaction differences remain in the captured state.

## Comparison history

- Initial state: a large introduction panel consumed vertical space; the public URL was only readable inside a Tooltip and therefore could not be clicked.
- Fixes: moved the explanation into the header information Tooltip and promoted the public URL to a visible button beneath the installed-server facts.
- Post-fix evidence: `/private/tmp/pilot-gcms-comparison-20260720.png`; no P0/P1/P2 findings.

## Follow-up polish

No P3 follow-up is required for this scoped change.

final result: passed

---

## GCMS Paper Current 文章详情阅读轨道修复 — 2026-07-24

### Source and implementation evidence

- Source visual truth: `run/design-audit/2026-07-24-paper-current-detail/02-detail-bottom.png`.
- Desktop implementation top: `run/design-qa/paper-current-detail/implementation-desktop-top.png`.
- Desktop implementation bottom: `run/design-qa/paper-current-detail/implementation-desktop-bottom.png`.
- Compact implementation: `run/design-qa/paper-current-detail/implementation-compact-top.png`.
- Same-frame before/after comparison: `run/design-qa/paper-current-detail/comparison-before-after.png`.
- Desktop viewport: 1366 × 866 CSS pixels.
- Compact breakpoint viewport: 900 × 900 CSS pixels.
- State: Paper Current article with seven real heading anchors, tags, previous-article navigation and related content.

### Full-view comparison evidence

- The article outline now belongs to the article reading row rather than the outer page grid. At desktop width it remains in the right reading rail while the article body occupies the 760px text column.
- The outline rail ends with the reading content. Tags and previous/next navigation render in subsequent full-width rows, so the outline no longer drops beside the end-of-article navigation.
- Related reading starts after the pager with a separate 64px section gap. The article column adds 72–96px of bottom breathing room before the 56px theme footer.
- At 900px the layout collapses to one column: the outline appears once above the article header, followed by the reading content, ending controls and related content. No second vertical rail or duplicate outline is created.

### Focused layout measurements

- Desktop article shell: 1022px.
- Reading column: 760px.
- Outline rail: 210px with a 52px column gap.
- Article reading row height: 3541.73px; the outline rail shares exactly that row height.
- Tags begin after the reading row; pager ends at 3798.63px, related content ends at 4030.22px and the footer begins at 4125.84px.
- Compact outline width: 760px; it ends at 349.95px and the article reading region starts at 375.55px.

### Findings and verification

- Initial P1: the outline was a sibling of the complete article column, so its grid/sticky behavior could place it beside the tags, pager and page footer. Fixed by grouping the outline with `.article-reading` inside the Paper Current article shell.
- Initial P2: the article ending and footer had insufficient separation. Fixed with family-scoped ending rows and responsive bottom spacing.
- Existing themes retain the original outer-grid outline path; the markup branch and CSS selectors are scoped to the Paper Current family.
- Focused and full Go tests passed.
- `npm run check` passed with 0 errors and 0 warnings.
- `cargo test` passed outside the listener-restricted sandbox (190 passed, 11 ignored).
- `git diff --check` passed.
- The browser-rendered desktop and compact captures show no remaining actionable P0, P1 or P2 issue.

final result: passed

---

## GCMS Poster 老主题响应式标题修复 — 2026-07-22

### Source and implementation evidence

- Source visual truth: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-b21858af-52b1-456a-830a-89c5572e9ad5.png` (1506 × 1910, approximately 753 × 955 CSS pixels at 2× density).
- Implementation at the matching CSS viewport: `run/poster-visual/poster-fixed-753x955.png` (753 × 955).
- Mobile implementation: `run/poster-visual/poster-fixed-390x844.png` (390 × 844).
- Desktop implementation: `run/poster-visual/poster-fixed-1280x720.jpg` (1265 × 720 raster from a 1280 × 720 CSS viewport with the scrollbar reserved).
- Full comparison: `run/poster-visual/poster-comparison.png`.
- Content-only normalized comparison: `run/poster-visual/poster-content-comparison.png`. The source browser chrome was removed before comparing the page surfaces at the same 753px CSS width.
- State: base `poster` skin with the real GCMS preview Hero title, description, featured cover, article teaser, navigation and CTA data.

### Full-view comparison evidence

- The former `22ch` parent / `12vw` child mismatch forced the Chinese Hero title into a one-character column and expanded the cover far beyond the intended fold.
- At 753 × 955 the repaired title renders as three balanced lines at 82.83px, within x=45.18–624.98 and y=489.43–713.05. The cover ends at y=955, so the title no longer invades the following section.
- The complete Hero content remains inside the cover: the CTA row ends at y=923. The redundant teaser stack and scroll cue are hidden at this width, preventing the former right-edge clipping and CTA/cue collision.
- At 390 × 844 the title remains three lines at 48px, within x=40–376 and y=472.46–602.05. The document reports `scrollWidth=375 <= innerWidth=390`; no horizontal overflow is present.
- At 1280 × 720 the title preserves the large Poster composition at 102.4px and three lines. The desktop teaser stack remains visible and the document reports `scrollWidth=1265 <= innerWidth=1280`.

### Focused region comparison evidence

- The content-only side-by-side comparison isolates the Hero region from Chrome UI and confirms that the title changed from an unreadable character column to the intended oversized lower-left poster block.
- No template copy, article data, cover image, navigation, color token, logo, button style or content source changed. The repair is limited to `:root[data-theme="poster"]` selectors.
- The mobile capture confirms the follow-up fix: the decorative down cue is removed below 900px and no longer touches the secondary CTA.

### Required fidelity surfaces

- Fonts and typography: the existing sans display family, weight, tracking and white treatment are preserved; only the responsive measure, clamp and emergency wrapping behavior changed.
- Spacing and layout rhythm: the original lower-left composition remains, with the headline, description and CTA fitting inside one cover fold at desktop, tablet and mobile widths.
- Colors and visual tokens: unchanged; the active Poster palette and cover scrim are reused exactly.
- Image quality and asset fidelity: unchanged; the preview uses the real featured cover and no new raster, SVG or placeholder asset.
- Copy and content: unchanged and data-driven; the existing Hero title line breaks are retained without introducing hardcoded words or new CMS fields.

### Findings and comparison history

- Initial P1: the Hero title collapsed into a one-character column and obscured the page. Fixed with a viewport-aware title measure, a bounded responsive font size and `break-word` fallback scoped to the base Poster skin.
- Initial P2: the 641–900px range retained the desktop teaser stack even though it competed with the Hero. Fixed by hiding the decorative teaser stack below 900px.
- Follow-up P2: the down cue touched the CTA row on the 390px capture. Fixed by hiding that decorative cue below 900px.
- Post-fix evidence: matching-width and mobile captures show no remaining actionable P0, P1 or P2 issue.

### Automated verification

- `go test ./...`: passed.
- `npm run check` in `desktop`: passed with 0 errors and 0 warnings.
- `cargo test` in `desktop/src-tauri`: passed outside the listener-restricted sandbox (172 passed, 11 ignored).
- `git diff --check`: passed.

final result: passed

---

## GCMS 三套 Web3 推广内容骨架主题 — 2026-07-22

### Source and implementation evidence

- Reference viewport: 1487 × 1058.
- Briefing Desk reference: `/Users/apple/.codex/generated_images/019f7e38-6f93-7692-afd6-daabd8142ff1/exec-89f8870f-456b-4c4e-a0f9-c1ee89d1a82d.png`
- Decision Wall reference: `/Users/apple/.codex/generated_images/019f7e38-6f93-7692-afd6-daabd8142ff1/exec-250448b9-529b-49bd-9a8a-0cf9258123ef.png`
- Route Atlas reference: `/Users/apple/.codex/generated_images/019f7e38-6f93-7692-afd6-daabd8142ff1/exec-20cabe06-d001-4b00-b444-e357b78eb324.png`
- Current desktop captures: `run/theme-previews/qa/{briefing-desk,decision-wall,route-atlas}-final.png`
- Same-frame comparisons: `run/theme-previews/qa/{briefing-desk,decision-wall,route-atlas}-comparison.png`
- Inner-page captures: `/private/tmp/{briefing,decision,route}-about.png`.
- Decision Wall logo follow-up source: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-d73f39b7-0d24-46b3-8526-53971546a764.png` (570 × 614).
- Decision Wall logo follow-up implementation: `run/theme-previews/qa/decision-wall-logo-implementation.png` (1083 × 712 CSS capture).
- Decision Wall logo focused comparison: `run/theme-previews/qa/decision-wall-logo-comparison.png`; the source card and implementation header are shown together with enlarged header crops.
- Route Atlas search follow-up source: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-4b36dca4-c445-475d-a0e0-f235164bf8f6.png` (2424 × 1500 pixels, browser chrome included).
- Route Atlas search follow-up implementation: `run/theme-previews/qa/route-atlas-search-implementation.png` (1095 × 720 raster from a 1280 × 720 CSS viewport at density 1).
- Route Atlas search focused comparison: `run/theme-previews/qa/route-atlas-search-comparison.png`; source and implementation are proportionally fitted in one frame, and the assessment is limited to the empty-search component because the source includes a different browser frame and viewport.

### Full-view comparison evidence

- Briefing Desk preserves the compact masthead and risk line, dominant editorial lead, right-side inspection checklist, two ruled guide rows and three-column latest-content rail.
- Decision Wall preserves the cobalt split hero, full-height featured card, black four-topic rail and the asymmetric guide/latest-content lower grid.
- Route Atlas preserves the 235/920/324 three-column shell, left route index, image-backed center hero, numbered stage rows and right latest-update rail.
- The implementation intentionally renders real preview titles, dates, categories, excerpts and media rather than copying the illustrative exchange copy from the references.

### Content-control and isolation verification

- All visible site copy, navigation, category labels, article titles, excerpts, dates, counts, links and images come from existing `Site`, `Menu`, `Categories`, `Posts`, `Featured`, `FeatLinks` and translation data.
- Numeric sequence markers are derived from template indexes; no fake registration count, reward value, exchange name, exchange logo or demo article is embedded in any of the three templates.
- `TestWeb3GuideTemplatesDoNotBakeInDemoContent` rejects the reference-only exchange names and illustrative copy if they reappear in source templates.
- The existing global homepage article-count setting limits the three families; multiple-featured regression coverage verifies that the limit is still honored.
- About and the other public content routes inherit family-scoped typography, spacing, surfaces, rules and responsive behavior without adding new CMS fields.
- Existing themes remain on their previous template paths and selectors; the shared header/footer add only explicit branches for these three new families.

### Palette and responsive verification

- Each family has four independent theme cards, for 12 new cards in total.
- `briefing-desk-white`, `decision-wall-white` and `route-atlas-white` compute to `body background-color: rgb(255, 255, 255)` with `--bg` and `--surface` both set to `#fff`.
- All three white variants report `scrollWidth <= innerWidth` at the checked 1105px viewport.
- No palette swatch strip or design-annotation artifact is rendered on public pages.

### Findings and comparison history

- Initial P1: the first pass inherited generic content proportions. Fixed by calibrating each family independently to the measured reference shell, hero, rails and content sections.
- Initial P1: Route Atlas's real light screenshot could disappear against its paper hero. Fixed with contained dynamic media, right alignment and a family-scoped blend treatment; no replacement image was hardcoded.
- Initial P2: stored hero line breaks could distort the references' intended headline wrapping. Fixed by rendering the existing dynamic title as normal collapsible whitespace only inside the three new layouts.
- Follow-up P2: Decision Wall bypassed the shared brand component and therefore rendered a text-only wordmark even when the site had a configured logo. Fixed by routing this family through `header_brand`; the captured logo resolves from `Site.Logo`, measures 70.31 × 30 CSS px, preserves its configured scale and introduces no horizontal overflow.
- Follow-up P2: Route Atlas applied its article-list left and bottom frame to an empty search-result container, visibly doubling the empty-state card's left and bottom edges. Fixed with a family- and state-scoped `:has(> .search-empty)` override; computed outer borders are now `0px none`, while the empty card retains one complete 1px border and populated result lists keep the normal Route Atlas frame.
- Search-state fidelity pass: typography, spacing, paper/accent tokens and copy remain unchanged; the stylesheet loaded, the captured page reported no runtime errors and no horizontal overflow. No additional focused crop was needed because both conflicting edges are clearly readable in the combined comparison.
- Post-fix combined comparisons show no remaining actionable P0, P1 or P2 fidelity issue.

### Automated verification

- Focused theme, palette, post-limit and no-hardcode tests: passed.
- `go test ./...`: passed.
- `git diff --check`: passed.

final result: passed

---

## Pilot 站点接入入口与站点卡片 — 2026-07-22

- Source visual truth:
  - GCMS 站点卡片：`/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-4d4d319a-622b-4f58-a546-bf74a5d856e2.png`
  - GCMS Google OAuth 浮层：`/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-71048d4e-8f87-49e5-b2cb-b92d2ab3d1bd.png`
  - Pilot 访问数据目标位置：`/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-fe71a55b-7a6a-4e5a-8ced-298b8ac10e44.png`
  - Pilot 紧凑卡片目标：`/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-c9cdd64b-d8a4-45ea-85b5-c81d43f3a3c5.png`
  - Canonical markup and tokens: `templates/admin/sites.html` and `assets/css/admin.css`.
- Implementation: `desktop/src/routes/+page.svelte` and the four Google assets under `desktop/static/icons/`.

### Source-to-implementation comparison

- The Pilot header now exposes exactly two independent 30px integration triggers, Google and Telegram, with GCMS's configured-color / missing-grayscale state treatment.
- Clicking Google opens the complete OAuth/data-range surface directly. The former intermediate summary card is not part of the interaction.
- The Google popover uses the GCMS 460px width, 9px radius, 30px segmented tabs, matching spacing, shadow, typography hierarchy, configured summary, account list and data-range form.
- Site cards keep three compact integration icons in the title row. Missing integrations are gray; configured integrations retain their product color. Enabled GA and GSC summaries now render directly below the icons as separate compact rows, using GCMS's labels and state colors without truncating both services into one crowded line.
- Site favicons, card padding, inter-card gaps and action spacing are reduced; grid items no longer stretch to match the tallest card in their row.
- The Sites content container expands to 1480px and switches from two to three columns when its own usable width reaches 1260px; standard widths keep two columns and narrow layouts keep one.
- A completed `6/6` readiness badge is hidden. The platform default site is also excluded because it is the existing primary entry, not a new-site launch checklist; incomplete or unchecked badges remain only for non-default sites.
- Pilot's Google, OAuth, Analytics and Search Console SVG files now byte-match the canonical GCMS assets.
- The redundant `进入后台` action was removed from Pilot's site cards; `运营站点` is now a borderless icon-and-label action matching the launcher, with `预览` as the secondary action.

### Verification

- `npm run check`: passed with 0 errors and 0 warnings.
- `cargo test`: passed outside the network-listener sandbox (168 passed, 11 ignored).
- `git diff --check`: passed.
- Runtime image capture of this exact connected Sites state is blocked: the Computer Use bridge timed out while reading the running Tauri process. The local Vite page was checked successfully, but without the Tauri connection it can only render the disconnected onboarding state, so no visual pass is claimed from that unrelated state.

final result: blocked (runtime screenshot unavailable; source and automated checks passed)

---

## GCMS 三套独立内容骨架主题 — 2026-07-21

### Source and implementation evidence

- Reference viewport: 1487 × 1058.
- Orbit Index reference: `/Users/apple/.codex/generated_images/019f7e38-6f93-7692-afd6-daabd8142ff1/exec-84501698-022d-4d40-ab81-2939e775561a.png`
- Column Stage reference: `/Users/apple/.codex/generated_images/019f7e38-6f93-7692-afd6-daabd8142ff1/exec-6822cbaf-ffa8-4e85-bb52-ecc7bd7c7d54.png`
- Type Cascade reference: `/Users/apple/.codex/generated_images/019f7e38-6f93-7692-afd6-daabd8142ff1/exec-3344e676-9840-4da4-be15-dee3502219aa.png`
- Current desktop captures: `run/theme-previews/qa/{orbit-index,column-stage,type-cascade}-impl-3.png`
- Same-frame comparisons: `run/theme-previews/qa/{orbit-index,column-stage,type-cascade}-comparison.png`
- Responsive captures: `run/theme-previews/qa/{orbit-index,column-stage,type-cascade}-mobile.png`
- About-page captures: `run/theme-previews/qa/{orbit-index,column-stage,type-cascade}-about.png`

### Full-view comparison evidence

- Orbit Index preserves the centered elliptical index, nested ruled orbits, category nodes, five dated notes, centered featured article, clipped real cover, reading queue and minimal footer. The calibrated desktop shell is exactly one 1058px viewport high.
- Column Stage preserves the 75px masthead, 88px manifesto strip, five full-height editorial columns, expanded magenta feature panel, distinct bone/cyan/ochre/navy panels and 99px footer. The stage occupies the reference y=163–959 interval.
- Type Cascade preserves the permanent 216px left rail, top-right navigation, oversized numbered feature, image spanning the number and content columns, right-side detail copy, six-step cascade and minimal footer.
- Differences in titles, dates, category names, counts, descriptions and screenshots are expected: the implementation uses the real GCMS preview dataset instead of copying illustrative text from the design images.

### Data, routes, and interaction verification

- All visible article/category/count/date/excerpt/cover values come from existing GCMS site, menu, category and post data. No theme-specific CMS field or hardcoded production statistic was added.
- The existing global homepage article-count setting controls all three families; regression coverage includes multiple featured articles and verifies that the configured limit is not exceeded.
- About and other public content routes inherit family-specific typography, spacing, rules, color tokens and responsive structure. The three About pages were rendered in the browser, not inferred from CSS alone.
- At 760 × 900 all three documents report `scrollWidth <= innerWidth`.
- At 760px the shared menu button is unique, toggles `aria-expanded` from `false` to `true`, adds `.nav-open`, and reveals the navigation as flex content for all three families.
- Column panels expose hover/focus expansion through family-scoped rules; links remain real content URLs.

### Isolation and palette verification

- Each family has four registered color cards, with shared family geometry and independent palette variables.
- The five-color strips in the exploration images are treated as design annotations: they remain represented by theme-library color cards and are not rendered in any public footer.
- New template dispatches, header/footer variants and CSS selectors are limited to the three new family prefixes. Existing theme registrations and old family CSS were not changed.
- No new raster placeholder, handcrafted SVG, fake metric, or unrelated backend parameter was introduced. Existing real content covers are used with the product fallback behavior.

### Findings and comparison history

- Initial P1: Orbit’s shell and footer did not fill the reference viewport. Fixed by calibrating the orbit shell, nested rings, feature card, queue and footer to the measured y coordinates.
- Initial P1: Column Stage used equal panel proportions and misplaced cover starts. Fixed with measured flex weights, a 796px stage and reference-aligned cover positions.
- Initial P1: Type Cascade’s feature image occupied only the content column and the navigation started too far right. Fixed with a three-column feature grid, a two-column image span and calibrated navigation gaps.
- Initial P2: long real titles could collide with Column covers or push Type Cascade’s feature below the intended fold. Fixed with family-specific display sizing and line clamping while preserving the real text.
- Post-fix side-by-side comparisons and browser geometry checks show no remaining actionable P0, P1 or P2 fidelity issue.

final result: passed

---

# Four content themes — controlled copy and cascade regression

Date: 2026-07-21

## Source and implementation evidence

- Reference viewport: 1487 × 1058.
- Field Ledger comparison: `run/design-audit/2026-07-21-theme-data-style/field-ledger-comparison.jpg`
- Signal Archive comparison: `run/design-audit/2026-07-21-theme-data-style/signal-archive-comparison.jpg`
- Night Watch comparison: `run/design-audit/2026-07-21-theme-data-style/night-watch-comparison.jpg`
- Paper Current comparison: `run/design-audit/2026-07-21-theme-data-style/paper-current-comparison.jpg`
- Current implementation captures: `run/design-audit/2026-07-21-theme-data-style/*-fixed.jpg`

## Content-control audit

- Field Ledger's masthead is now `site.hero_title`; its supporting line is `site.tagline`; its issue/index and section labels reuse the existing all-content, latest and featured controls.
- Signal Archive's eyebrow, featured label, latest heading, category heading and all-content label now reuse existing site/category controls and translations.
- Night Watch's board and dispatch headings now use `home.latest_title` and `home.featured_title`; table labels reuse category/date controls and translations.
- Paper Current's latest heading, directory heading and featured labels now reuse existing site/category controls and translations.
- No new database setting, API field or admin form parameter was added.
- Preview regression assertions reject the former fixed labels for all color variants of these four theme families.

## Visual fixes verified

- Signal Archive: `.sa-list-head` computes to `justify-content: flex-start`; category navigation begins 28px after the heading instead of being pushed to the far edge.
- Signal Archive: `.sa-topic-more` computes to `display: flex` and `justify-content: center`, overriding the older broad `.sa-topics a` rule.
- Paper Current: `.pc-all` computes to `display: inline-flex`, `grid-template-columns: none` and a 64px content width, so the action remains horizontal.
- All four desktop previews report document `scrollWidth <= innerWidth` at 1487px.
- All four previews report no document-level horizontal overflow at 680px.
- All fixes are scoped to `[data-theme^="field-ledger"]`, `[data-theme^="signal-archive"]`, `[data-theme^="paper-current"]` or `[data-theme^="night-watch"]`; legacy themes were not edited.

## Remaining expected differences

- Production copy, categories, counts, dates and article imagery intentionally come from real GCMS data, so they differ from the illustrative reference content.
- The selected color-card variants continue to share each family's layout while using their own palette tokens.

final result: passed

## GCMS HTTPS / 橙云维护入口 — 2026-07-21

- Source visual truth: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-ddca0dbc-9130-4a8b-8e40-d2bec7b09963.png`
- Rendered implementation screenshot: unavailable; the macOS Computer Use bridge timed out while reading the running Tauri window.
- Viewport: source capture 806 × 1044 pixels.
- State: migrated instance opens the existing-domain HTTPS / Cloudflare maintenance flow.

## Full-view comparison evidence

The supplied source confirms two P1 flow/copy problems: the maintenance entry was routed through the domain-change step, and the permission recovery actions used the ambiguous labels `更新此连接` and `已更新，重试`. The implementation now uses a dedicated `https_proxy` intent, routes a matched domain directly to step 3, and removes domain-edit navigation from that maintenance path.

## Focused region comparison evidence

The completion-state copy was changed to `网站已可访问，补充权限即可开启橙云`, `补充所需权限`, and `重新检测橙云`. A post-change focused screenshot could not be captured from the Tauri window, so typography, wrapping, spacing, colors, and final button geometry remain visually unverified.

## Findings and comparison history

- Earlier P1: `HTTPS / 橙云` opened `变更访问域名` and stopped at step 2. Fixed by introducing the dedicated maintenance intent and selecting step 3 after the existing domain and web-server check pass.
- Earlier P2: recovery copy mixed state and action (`已更新，重试`). Fixed with explicit next actions and a shorter explanation that permission repair does not interrupt the website.
- Remaining blocker: no accepted implementation screenshot is available for a same-state visual comparison. Functional source checks pass, but image-based QA cannot be marked passed.

final result: blocked

## GCMS 四套内容骨架主题 — 2026-07-21

- Source visual truth:
  - Field Ledger: `/Users/apple/.codex/generated_images/019f7e38-6f93-7692-afd6-daabd8142ff1/exec-9f1efece-e650-4849-8ea7-7f0cebce5ce7.png`
  - Signal Archive: `/Users/apple/.codex/generated_images/019f7e38-6f93-7692-afd6-daabd8142ff1/exec-1b18197c-fdd0-4630-8408-1c8c911923c7.png`
  - Paper Current: `/Users/apple/.codex/generated_images/019f7e38-6f93-7692-afd6-daabd8142ff1/exec-39ef6bee-61ca-4b74-b913-23dd1ca8fded.png`
  - Night Watch: `/Users/apple/.codex/generated_images/019f7e38-6f93-7692-afd6-daabd8142ff1/exec-847e0cd6-2924-4bc7-bc4e-e7e67808e958.png`
- Rendered implementation:
  - `/Users/apple/work/cms.ccvar.com/run/design-qa/field-ledger-final.png`
  - `/Users/apple/work/cms.ccvar.com/run/design-qa/signal-archive-final.png`
  - `/Users/apple/work/cms.ccvar.com/run/design-qa/paper-current-final.png`
  - `/Users/apple/work/cms.ccvar.com/run/design-qa/night-watch-final.png`
- Combined comparisons:
  - `/Users/apple/work/cms.ccvar.com/run/design-qa/field-ledger-final-comparison.png`
  - `/Users/apple/work/cms.ccvar.com/run/design-qa/signal-archive-final-comparison.png`
  - `/Users/apple/work/cms.ccvar.com/run/design-qa/paper-current-final-comparison.png`
  - `/Users/apple/work/cms.ccvar.com/run/design-qa/night-watch-final-comparison.png`
- Viewport: implementation rendered at 1120 × 797 logical pixels; the in-app browser panel captured a 1095 × 797 raster. Source visuals were proportionally fitted to the same comparison frame.
- State: each theme uses its default color card, real preview articles/categories/featured image, and the shared GCMS preview dataset.

## Full-view comparison evidence

- Field Ledger preserves the research-ledger masthead, top-aligned issue/about block, signal counters, ruled featured entry and subject index. The long real site description is clamped instead of changing the intended masthead proportions.
- Signal Archive preserves the centered navigation, oversized editorial headline, signal strip, timeline rhythm and asymmetric feature grid. Its cards use the real article count and featured image rather than generated metrics or collage art.
- Paper Current preserves the permanent left rail, restrained paper palette, issue-note hero and reading-index hierarchy. The rail collapses responsively without introducing horizontal overflow.
- Night Watch now matches the intended centered navigation and neon-accent editorial hierarchy, with the featured story, pulse strip and evidence board aligned to the reference rather than the former generic right-aligned header.

## Focused region comparison evidence

- Header and footer are selected by the four new content-theme families; no old theme markup or selectors were changed.
- Every visible count, category, date, article title, excerpt and image is derived from existing GCMS site/content data. No fabricated score, trend, issue number or research metric remains.
- About, article detail, category/list, search, links, pagination, related-content and not-found surfaces inherit the selected family's typography, rules, spacing, card treatment and color tokens through family-scoped selectors.
- Each family retains independent color-card variants while sharing the family layout.
- At a 680px viewport all four rendered documents reported `scrollWidth <= innerWidth`; no horizontal overflow was detected.

## Required fidelity surfaces

- Typography: editorial serif/display faces, monospace labels and UI sans-serif roles follow each source's hierarchy and weights.
- Layout: centered navigation, Field masthead columns, Signal timeline, Paper rail and Night evidence board follow the reference compositions.
- Spacing: the large-screen canvases, section rules, card padding and mobile collapse points were calibrated per family rather than inherited from the old generic max-width.
- Colors: all four families expose their own palette cards and reuse their active palette consistently on inner pages.
- Images: production renders use real featured/article media with safe fallbacks; generated reference imagery is not hardcoded into content.
- Content control: existing site copy, categories, posts, links and featured-content controls remain the sole content inputs; no new backend fields are required.

## Findings and comparison history

- Initial P1: the new themes still inherited the generic header/footer and narrow canvas; Night Watch navigation was right-aligned. Fixed with family-specific shared chrome and calibrated canvases.
- Initial P1: hardcoded scores, issue labels and placeholder trends made preview content unmanageable. Fixed by mapping every visible content datum to existing GCMS data.
- Initial P2: Field Ledger's masthead and long description drifted from the source. Fixed with top alignment and a four-line clamp.
- Initial P2: the four homepages were themed but About and other inner pages remained visually generic. Fixed with family-scoped inner-page systems covering all public content routes.
- Post-fix comparison: the four combined comparison images above show no remaining actionable P0, P1 or P2 visual difference. Remaining differences are expected content differences caused by real site text and real article screenshots.

final result: passed

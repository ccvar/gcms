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

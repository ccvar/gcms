# Design QA — Answer Desk / Portrait Journal / Casebook

## Comparison targets

- Answer Desk source: `/Users/apple/.codex/generated_images/019f9819-858c-7022-be40-dcad9b27ecd2/call_x1GK5YgrFrGMTm8SDZKgy98L.png`
- Portrait Journal source: `/Users/apple/.codex/generated_images/019f9819-858c-7022-be40-dcad9b27ecd2/call_PpguYoES05mJMuAnE5HAKRY5.png`
- Casebook source: `/Users/apple/.codex/generated_images/019f9819-858c-7022-be40-dcad9b27ecd2/call_PpeS9Mv4N2WenSrQiS8GjqAY.png`
- Final desktop captures:
  - `run/theme-previews/qa/answer-desk-final.png`
  - `run/theme-previews/qa/portrait-journal-final.png`
  - `run/theme-previews/qa/casebook-final.png`
- Mobile resilience captures:
  - `run/theme-previews/qa/answer-desk-mobile.png`
  - `run/theme-previews/qa/portrait-journal-mobile.png`
  - `run/theme-previews/qa/casebook-mobile.png`

## Normalization and state

- Source pixels: 1435 × 1096 for all three designs.
- Desktop implementation viewport: 1435 × 1096 CSS px, device scale factor 1.
- Desktop implementation captures: 1435 × 1096 px.
- Mobile implementation viewport: 390 × 844 CSS px; browser content width is 375 px because of the visible scrollbar.
- State: Chinese locale, populated theme-preview data, first viewport at page top.
- Density normalization: none required; source and desktop implementation use equal pixel dimensions.
- Full-view evidence: each source and its implementation capture were opened together at native size during the final comparison.
- Focused-region evidence: a separate crop was not needed. At native 1435 px width, header, Hero, feature area, list typography, borders, image crops, and footer were all legible in the paired full-view comparisons. DOM box measurements were additionally checked for the principal Hero/lead regions.

## Required fidelity surfaces

- Fonts and typography: serif/sans roles, heading hierarchy, weights, tracking, line height, wrapping, and long CMS-title behavior match the intended editorial character. Long dynamic titles no longer enlarge the lead regions beyond the source proportions.
- Spacing and layout rhythm: the three desktop compositions retain their source grids, hairline dividers, square corners, section order, and dense below-the-fold rhythm. Mobile layouts stack without horizontal overflow.
- Colors and tokens: blue, forest green, and vermilion accents are implemented as theme tokens on warm neutral backgrounds. No gradients or generic elevated cards were introduced.
- Image quality and asset fidelity: all visible article and Hero imagery comes from the CMS/site profile. Images were complete at capture time and retain real intrinsic widths; no placeholder drawings, custom SVG art, CSS art, or fake photography is used.
- Copy and content: theme templates contain no mockup article names or descriptions. Site, Hero, menu, category, featured-post, post-list, dates, counts, excerpts, and covers are all data-bound.
- Interactions and accessibility: the Answer Desk search is a semantic GET search form with `q`; the mobile navigation control exposes a named button and changes the navigation from hidden to visible; images carry CMS alt text; desktop and mobile have no horizontal overflow. Browser console produced no warnings or errors.

## Comparison history

### Pass 1 — blocked

- [P1] Answer Desk Hero type was oversized for real CMS copy, pushing the featured-answer band far below the source fold.
- [P1] Portrait Journal's long dynamic Hero and featured title expanded the lead from the intended editorial proportion and caused the image caption to dominate the photograph.
- [P2] Casebook's proposition heading used too large a scale for longer backend copy, making the lead substantially taller than the reference.

Fixes:

- Reduced and capped Answer Desk's display scale while preserving the serif hierarchy.
- Reduced Portrait Journal lead scale and clamped the photograph caption to two lines.
- Reduced Casebook proposition scale and tightened tracking to preserve the source's compact left column.

### Pass 2 — passed

- Re-captured all three implementations at 1435 × 1096 and compared each with its source in the same visual input.
- Re-captured all three at 390 × 844; reported horizontal overflow is false for every theme.
- Verified loaded image state: all captured images are complete with non-zero intrinsic widths.
- Verified no browser console warnings or errors.
- Remaining visible differences are expected CMS-data differences: the source mockups use art-directed example copy and portraits/architecture, while the implementation deliberately renders the current site's real Hero text, categories, excerpts, covers, and counts. Structure, hierarchy, tokens, and image treatment remain faithful.

### Pass 3 — responsive shell correction, passed

- User evidence: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-57851a2d-9b01-4474-979b-eed32e88ba33.png` (2102 × 714 px).
- Final implementation evidence: `run/theme-previews/qa/answer-desk-nav-boxed-final.png` (2102 × 714 px).
- [P2] The active navigation item rendered both the editorial-collection border and the global navigation `::after`, producing two blue rules. The collection header now suppresses the global pseudo-element and retains one active rule.
- [P2] The three new themes filled very wide preview canvases. Header, page body, and footer now use a fluid shell capped at 1280 px: widths from 1080 through 1240 shrink naturally, while 1360 and 1440 center the 1280 px shell.
- Browser measurements covered 1080, 1200, 1240, 1360, and 1440 for all three themes. Every result had `overflow: false`; active-navigation `::after` was `display: none`.
- Additional 820, 768, 760, and 390 checks confirmed no horizontal overflow. The existing 760 px mobile breakpoint still collapses the navigation, while wider tablet states retain the desktop navigation.
- Console warnings/errors: none.

### Pass 4 — content-page breathing room, passed

- User evidence: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-3fba9b75-7165-4693-9a18-fe15d4372afc.png` (2764 × 818 px).
- Final implementation evidence: `run/theme-previews/qa/answer-desk-about-breathing.png` (2764 × 818 px).
- [P2] The final paragraph, divider, and footer read as one compressed block. The three new themes now reserve a fluid 72–112 px content-page closing space before the footer, reduced to 56 px at the mobile breakpoint.
- At 1440 px, the measured final-content-to-footer distance is 144 px for all three themes; at 760 px and 390 px it is 88 px. All checked states report no horizontal overflow and no console warnings/errors.
- The rule is scoped to Answer Desk, Portrait Journal, and Casebook content pages, so existing themes and homepage density are unchanged.

### Pass 5 — all inner-page breathing room, passed

- User evidence: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-f4fc0cba-66d6-4588-97f0-9bbdb15de42d.png` (2290 × 902 px).
- Final implementation evidence: `run/theme-previews/qa/answer-desk-article-bottom-breathing.png`.
- [P2] The previous correction only targeted `.content-page`, so article detail and other inner-page templates could still place their last section directly against the footer. The spacing contract now lives on the three themes' inner `<main>` elements and explicitly excludes their homepage shells.
- Article pages for all three themes were measured at 1440, 760, and 390 px. Desktop closing space is 112 px; tablet and mobile closing space is 56 px. Every checked state reports no horizontal overflow.
- Scope checks confirm all three homepages retain `padding-bottom: 0`, while representative About and article pages receive the shared inner-page spacing. This covers article, category, search, links, generic list/detail, documentation, product, and not-found templates without changing existing themes.

### Pass 6 — pure-white and dark palette skins, passed

- Source visual truth:
  - `/Users/apple/.codex/generated_images/019f9819-858c-7022-be40-dcad9b27ecd2/call_x1GK5YgrFrGMTm8SDZKgy98L.png`
  - `/Users/apple/.codex/generated_images/019f9819-858c-7022-be40-dcad9b27ecd2/call_PpguYoES05mJMuAnE5HAKRY5.png`
  - `/Users/apple/.codex/generated_images/019f9819-858c-7022-be40-dcad9b27ecd2/call_PpeS9Mv4N2WenSrQiS8GjqAY.png`
- Combined comparison evidence, ordered source / pure white / dark:
  - `run/theme-previews/qa/answer-desk-palette-comparison.jpg`
  - `run/theme-previews/qa/portrait-journal-palette-comparison.jpg`
  - `run/theme-previews/qa/casebook-palette-comparison.jpg`
- Implementation viewport captures:
  - `run/theme-previews/qa/{answer-desk,portrait-journal,casebook}-{white,dark}-viewport.jpg`
  - `run/theme-previews/qa/{answer-desk,portrait-journal,casebook}-{white,dark}-mobile.jpg`
- Source and desktop implementation are 1435 × 1096 px at device scale factor 1. Mobile checks use a 390 × 844 CSS-pixel viewport. No density normalization was required.
- State: Chinese locale, populated CMS preview data, homepage at page top. Copy and imagery intentionally differ from the visual studies because every palette skin continues to consume the current site's real Hero, categories, featured content, post lists, dates, counts, excerpts, and covers.
- The three compositions, typography roles, image treatment, straight-rule system, responsive breakpoints, and data-bound templates remain unchanged. Only palette tokens vary.
- Each family now exposes exactly three skins on one theme card: original, pure white, and dark. Pure-white skins compute `--bg: #ffffff`; dark skins compute near-black family-specific backgrounds and raised surfaces.
- Desktop and mobile captures for all six new skins report no horizontal overflow; every image is complete with a non-zero intrinsic width.
- WCAG contrast measurements against each skin background:
  - primary text: 17.35:1–18.93:1
  - accent text: 4.81:1–9.64:1
- Answer Desk's dark search action uses an explicit `--on-accent` token so its button label remains readable without introducing component-specific hardcoded content.
- Focused-region evidence was not required beyond the combined full-view comparisons: the change is token-only, and the relevant line work, type, controls, images, and section boundaries are readable at the normalized desktop size.

### Pass 7 — header top-rule removal, passed

- User evidence: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-5439df4e-f911-47dd-ad76-feb880b6af3a.png` (2048 × 186 px).
- Final implementation evidence: `run/theme-previews/qa/answer-desk-no-top-line.jpg`.
- Focused comparison evidence: `run/theme-previews/qa/answer-desk-top-line-comparison.jpg` (user browser capture above, revised implementation header below).
- [P2] The editorial-collection header carried a 2 px accent-colored top border. At wide widths it read as an unintended page-wide rule above the navigation. The top border is now removed; the single active-navigation underline and the neutral header-bottom divider remain.
- All nine skins across Answer Desk, Portrait Journal, and Casebook report `border-top-width: 0`, a 59 px header, one 1 px bottom divider, hidden global active-link pseudo-element, and no horizontal overflow.
- Typography, palette tokens, page-shell width, navigation positions, CMS content, imagery, and responsive behavior are otherwise unchanged.

### Pass 8 — sticky navigation, passed

- Source visual truth: `run/theme-previews/qa/answer-desk-no-top-line.jpg` (the accepted header appearance after Pass 7).
- Desktop implementation evidence: `run/theme-previews/qa/answer-desk-sticky-header.jpg` at a scrolled article state.
- Mobile interaction evidence: `run/theme-previews/qa/answer-desk-dark-sticky-mobile-menu.jpg` at 390 × 844 with the sticky header menu open after scrolling.
- [P2] The shared editorial-collection header previously used normal document flow, so navigation disappeared on long articles and case pages. It now uses `position: sticky; top: 0` with the existing opaque theme background and a contained stacking level.
- Original and dark skins from all three families were scrolled and measured: the document scroll position changed while every header retained `getBoundingClientRect().top === 0`, `position: sticky`, and no horizontal overflow. The same shared selector covers the three pure-white skins.
- On mobile, the dark Answer Desk page was scrolled beyond 1200 px and the real menu button was activated. The header remained at top 0, `aria-expanded` changed to `true`, the menu opened directly below the 59 px header, and no horizontal overflow appeared.
- No shadow, blur, new top rule, or layout offset was introduced. The active navigation underline and neutral bottom divider remain the only persistent header rules.

### Pass 9 — six additional content skeleton families, passed

- Visual sources:
  - Shelf Index: `/Users/apple/.codex/generated_images/019f986c-a7c7-71f0-9b09-ea435acec882/call_PeBVpTixXQ0p2chF5xbYeYpJ.png`
  - Tradeoff Sheet: `/Users/apple/.codex/generated_images/019f986c-a7c7-71f0-9b09-ea435acec882/call_4edW5gUlWuGWtKWvARf905Py.png`
  - Progress Bulletin: `/Users/apple/.codex/generated_images/019f986c-a7c7-71f0-9b09-ea435acec882/call_5vOP6F0NACOdrKU5baC0LOOj.png`
  - Margin Reading Room: `/Users/apple/.codex/generated_images/019f986c-a7c7-71f0-9b09-ea435acec882/call_19xwzTGnS8KFhV7Cg9HomWen.png`
  - Light Table: `/Users/apple/.codex/generated_images/019f986c-a7c7-71f0-9b09-ea435acec882/call_bg80cNH95ZiEkkJQedh6r6j3.png`
  - Counterpoint: `/Users/apple/.codex/generated_images/019f986c-a7c7-71f0-9b09-ea435acec882/call_fXjAWCEwioVy7GF0K4DyQku7.png`
- Same-viewport comparison evidence:
  - `run/design-qa/{shelf-index,tradeoff-sheet,progress-bulletin,margin-reading-room,light-table,counterpoint}-comparison-viewport.png`
  - Each combined image places the design source on the left and the rendered implementation on the right. Sources were width-normalized to 1435 CSS pixels and cropped at the top to 1435 × 1096; implementation captures use the same page-top state and 1435 × 1096 viewport.
- Dynamic-content fidelity: all six skeletons bind the current site logo, name, navigation, Hero eyebrow/title/description/media, category data, featured item, post collections, dates, excerpts, counts, links, locale and translated labels. No design-study article copy, mock URLs, category mapping or Hero asset was copied into production templates.
- Hero-media contract: site-profile image and SVG modes are honored first, followed by the featured post cover. All six use the same backend data contract; image slots crop responsively with `object-fit: cover` rather than assuming a fixed source aspect ratio.
- Layout fidelity: the six distinct source structures remain recognizable—shelved index, decision worksheet, bulletin ledger, annotated reading room, contact sheet, and paired counterpoint stream. Typography roles, square corners, hairline rules, section ordering, accents, dense editorial rhythm and 1280 px capped shell follow the accepted studies.
- Header contract: every family has a real CMS logo, `position: sticky; top: 0`, no top border, exactly one neutral bottom divider and one active-navigation underline. The implementation remains boxed rather than filling an ultrawide viewport.
- Palette contract: each family exposes original, pure-white and dark skins, for 18 selectable theme IDs. Browser-computed pure-white backgrounds are `rgb(255, 255, 255)`; dark backgrounds are family-specific near-black values. All 12 white/dark variants retain the logo, one header divider and no horizontal overflow.
- Responsive evidence: all six base themes were measured at 390 × 844, 768 × 900 and 1440 × 1000. Every state retained the sticky header and fluid capped shell with no horizontal overflow. The mobile compositions stack their Hero, feature, table and paired-stream structures at the shared 760 px breakpoint.
- Browser console warnings/errors: none.
- Automated verification: `go test ./internal/web -count=1`, `go test ./... -count=1`, theme-preview dump generation and `git diff --check` all pass.
- Expected visual differences are limited to live CMS data: the generated studies use art-directed example names, copy and architectural photography, while the production preview deliberately uses the site's actual logo, navigation, Hero text and screenshot media.

### Pass 10 — media-neutral Hero surfaces, passed

- User evidence: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-5e966413-d06f-4e82-b5ab-825e150f06ce.png`.
- Revised implementation evidence: `run/design-qa/light-table-hero-transparent-2102x1100.png`.
- Combined comparison evidence: `run/design-qa/light-table-hero-background-comparison.png`, with the user capture on the left and revised implementation on the right.
- [P2] Shelf Index, Tradeoff Sheet, Light Table and Counterpoint supplied theme-colored Hero-media container backgrounds. Those colors could show through transparent SVGs or images with alpha and incorrectly alter user-provided artwork. Progress Bulletin and Margin Reading Room did not visibly add a fill, but are now covered by the same explicit contract.
- Fix: all six Hero media wrappers now compute `background-color: rgba(0, 0, 0, 0)`. The image and SVG wrappers remain transparent; theme wash colors are retained only for true no-media fallback/empty states.
- Browser verification covered all six families and all three palette skins, for 18 rendered theme IDs. Every wrapper and current media child is transparent, and every page reports no horizontal overflow.
- The peach and blue regions still visible in the supplied Cloudflare screenshot are pixels inside that user-provided image, not CSS backgrounds added by the theme.
- Regression coverage: `TestThemePreviewsRender` now asserts the transparent Hero-media contract for all six selectors. Targeted Go tests and `git diff --check` pass.

### Pass 11 — three integrated-media skeleton families, passed

- Visual sources:
  - Seamless Canvas: `/Users/apple/.codex/generated_images/019f986c-a7c7-71f0-9b09-ea435acec882/call_l552Yv8ZAv0o2HcjyE4Al9Uu.png`
  - Night Corridor: `/Users/apple/.codex/generated_images/019f986c-a7c7-71f0-9b09-ea435acec882/call_HAwpavPKA0QOFsmGWb9SYINV.png`
  - Open Ascent: `/Users/apple/.codex/generated_images/019f986c-a7c7-71f0-9b09-ea435acec882/call_zlD8yppTkDLk13GfZoe42edG.png`
- Same-input comparison evidence:
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/seamless-canvas-comparison-final.jpg`
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/night-corridor-comparison-final.jpg`
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/open-ascent-comparison-final.jpg`
- Exact layout viewport verification used a 1487 × 1058 iframe matching the three source canvases. The in-app browser surface scaled that frame to 68% for inspection; DOM measurements retained the exact 1487 × 1058 CSS-pixel viewport. Direct rendered screenshots with the current CMS Hero image were paired with each source in the comparison files above.
- Structure fidelity: all three place the real CMS Logo and navigation over the same continuous Hero media surface. Seamless Canvas retains its left editorial statement and four-column bottom index; Night Corridor retains the darkened corridor treatment, right reading guide and five-column track; Open Ascent retains the left statement, three-row index and lower-right featured caption. Display-type scales were reduced after comparison so real long CMS titles preserve the source proportions.
- Media contract: image, SVG and featured-cover fallback all use the same dynamic slot and compute a transparent media-wrapper background. No family introduces a Hero background color, gradient or decorative replacement asset. Only a true `no-media` state receives a readable token-based fallback.
- Data contract: Hero copy/media, Logo, navigation, categories, Featured, articles, excerpts, dates, counts, links and translated labels are backend-bound. The templates contain no design-study business copy, URLs, dates, category mappings or asset paths. Short collections degrade by omission and never index beyond available data.
- Navigation contract: the integrated header is transparent and fixed at the top on the homepage, then uses the existing `data-fnav-hero` progressive enhancement to switch to an opaque token-based state after scroll. Inner pages keep the standard sticky editorial header. There is no top rule and only one active-navigation underline.
- Palette contract: each family exposes default, pure-white and deep-background skins, for nine registered theme IDs. Browser-computed `--bg` values and picker swatches agree; all nine keep the same skeleton and dynamic data.
- Responsive and runtime evidence: all nine skins render their Logo, transparent media wrapper and correct family shell with zero horizontal overflow. Desktop Hero height is capped rather than `100vh`; the 1100, 820, 760 and 480 px rules progressively reduce type, stack guides and indexes, and collapse category grids. Browser console warnings/errors: none.
- Automated verification: the new visual-contract test covers the nine token selectors, picker colors, transparent Hero wrappers, fixed transparent navigation, responsive selectors and real-home template output. Targeted tests, full `go test ./... -count=1`, preview generation and `git diff --check` pass.
- Expected visible differences are restricted to current CMS data. The studies use purpose-made architectural imagery and short sample copy, while the implementation intentionally shows the configured Cloudflare deployment screenshot and the site's actual Hero text.

### Pass 12 — Open Ascent full-bleed media and CTA rhythm, passed

- User evidence: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-3ab7e380-5399-4b66-9e5b-93b5994ddf47.png`.
- Final implementation evidence: `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/open-ascent-fullbleed-final.jpg`.
- Same-input before/after comparison: `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/open-ascent-gutter-cta-comparison.jpg`.
- [P2] The 1440 px page shell clipped the Hero media at wide preview widths, leaving two unrelated body-background gutters. Open Ascent now lets the Hero and media span the full available width while deriving its copy, article list and featured-caption gutters from the centered 1440 px content frame. At the reported 1736 px viewport, page/media width changed from 1440 px at x=140.5 to 1721 px at x=0, while the left content anchor remained x=230.5.
- [P2] The primary “阅读全文” action shared an underline treatment with other theme actions and collided with the article-list top rule. Open Ascent now uses a plain accent-text CTA, compacts each article index row, and preserves a measured 30.7 px gap between the CTA and list at the supplied desktop state.
- Responsive verification covered 1440 × 1000, 1240 × 900, 1080 × 900, 760 × 900 and 390 × 844. The Hero/media remains full bleed, CTA-to-list spacing stays positive (54–187 px at the explicit breakpoints), and every state reports zero horizontal overflow.
- The current Hero image completed with a non-zero intrinsic width. Its media wrapper remains fully transparent. No application console errors were reported; observed warnings originated only from an unrelated browser wallet extension.
- Regression verification: `TestMediaThemeVisualContracts`, `TestThemePreviewRendersAllThemes`, preview dump generation and `git diff --check` pass.

### Pass 13 — Seamless Canvas / Night Corridor full-bleed and live preview parity, passed

- User evidence: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-425b0e28-0e8e-4ca6-9086-830c5fddb6cc.png`.
- Final implementation evidence:
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/theme-cards-live-data-final.jpg`
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/seamless-canvas-fullbleed-final.jpg`
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/night-corridor-fullbleed-final.jpg`
- Combined evidence: `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/media-themes-preview-sync-comparison.jpg`, ordered user theme-card capture / revised Seamless Canvas / revised Night Corridor.
- [P2] Seamless Canvas and Night Corridor still inherited the 1440 px outer page cap, so their continuous Hero media stopped before the viewport edges. Both pages now use the same full-bleed contract as Open Ascent. At the 1736 × 920 browser viewport, each page and Hero occupies the complete 1721 px client width at x=0, while both H1 content anchors remain aligned to the centered 1440 px frame at x=230.5.
- The below-Hero Seamless feature/list and Night categories remain capped at 1440 px. The Hero copy widths were recalculated from the centered frame gutter so full-bleed does not shrink the readable text measure. Both pages report zero horizontal overflow.
- [P1] Theme-card thumbnails previously cleared `HeroVisual`, `HeroImage` and `HeroSVG`, selected their featured/article stream with a separate algorithm, injected fallback categories into partially populated sites, and used a non-production total for knowledge grouping. The card endpoint now preserves real Hero media and shares `populateHomeContent` with the actual homepage/browser route. Real FeaturedLinks are loaded even when there are no articles; synthetic categories and links are restricted to true empty-site fallback.
- Direct browser comparison for both families confirmed exact equality of Hero source, Hero title and featured content between `/admin/theme-preview/{theme}` and `/admin/theme-browse/{theme}/`. The configured Hero image loaded with non-zero intrinsic width in both representations.
- `TestThemeCardPreviewUsesLiveHeroAndHomeOrdering` locks the real Hero and multi-featured article ordering across thumbnail and opened preview. Full `go test ./internal/web -count=1`, visual-contract tests and `git diff --check` pass.

### Pass 14 — Night Corridor pure-white media without grey wash, passed

- User evidence: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-8b79f087-77b5-4d6d-8c3f-a074fdd79f8a.png`.
- Final implementation evidence: `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/night-corridor-white-no-mask-final.jpg`.
- Same-state comparison evidence: `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/night-corridor-white-mask-comparison.jpg`, with the supplied capture on the left and the revised page on the right.
- [P1] `night-corridor-white` inherited the base family's `brightness(.42) saturate(.82)` media filter. Although implemented as a filter rather than an overlay element, it visually produced the reported full-screen grey mask.
- The pure-white skin now computes `filter: none` for both uploaded images and Hero SVGs. Default Night Corridor and the deep-background skin retain their intended cinematic darkening.
- Because the original image is light, the pure-white skin also switches Hero/header/title/guide/index content to dark theme tokens, replaces white guide rules with token-derived hairlines, and uses an 88% translucent white index surface. No copy, image or business data is hardcoded.
- Browser verification at 1736 × 920 reports: media filter `none`; image loaded with non-zero intrinsic width; Hero and H1 color `rgb(21, 22, 23)`; zero horizontal overflow. The original CMS Hero pixels are now visible without a grey wash.
- Regression coverage in `TestMediaThemeVisualContracts` locks the filter removal, dark Hero token and light index surface. Targeted tests and `git diff --check` pass.

### Pass 15 — media-theme curation, duplicate removal and unfiltered Night Corridor, passed

- User evidence:
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-1cee56c7-1d9e-4039-b353-303ee256201f.png`
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-9cfde9c3-c5db-4fe4-ad8e-a4407b189c63.png`
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-74bff34c-384a-4938-b127-b7fd184ca211.png`
- Final evidence:
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/seamless-curation-final.jpg`
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/open-curation-final.jpg`
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/night-corridor-no-mask-readable-final.jpg`
- [P1] Night Corridor repeated the same category/count collection both inside the Hero rail and immediately below it. The duplicate lower category section is removed; the Hero rail remains the single category navigation.
- All three media families now expose a lower curation composition built from real `.FeaturedMore` and `.FeatLinks` values. The currently promoted `.Featured` item is intentionally excluded from the lower article collection, so Hero/main-feature content is not duplicated. Empty article or link collections omit their subsection, and an entirely empty curation result emits no wrapper.
- Seamless Canvas uses a compact editorial ledger, Night Corridor a two-column reading/resources rail, and Open Ascent a three-card exhibition row plus resource index. The data contract is shared, while each family retains its accepted typography, rules, spacing and responsive behavior.
- Night Corridor's saturated red accents were replaced with lower-chroma copper/brown tokens for its default, pure-white and dark color cards. Hero accents use a separate readable token so the restrained lower-page color does not disappear over media.
- [P1] The remaining default/deep Night Corridor `brightness(.42) saturate(.82)` filter was the reported full-screen dark mask. Images and SVGs now compute `filter: none` in every Night Corridor skin. The default card uses dark Hero typography and token-derived hairlines over the current light image, preserving legibility without adding a replacement overlay, gradient or background fill.
- Browser verification on all three current theme-browse pages found exactly one curation section, three dynamic selected articles, four dynamic selected links and zero horizontal overflow. Night Corridor reports zero `.nc-categories` duplicates and its media filter is `none`.
- Automated verification: full `go test ./... -count=1`, focused media visual-contract tests and `git diff --check` pass.

### Pass 16 — appearance save state, portal dropdown and persistent Hero animation, passed

- User evidence:
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-b3e407b6-8c59-473a-8a2d-5a925905600f.png`
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-342eabe1-9f98-4cb8-a6ca-9e090f319118.png`
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-1a50746a-5ad9-48c2-8faa-bfa1aa8f09e5.png`
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-b470ad01-01b4-48c6-9b83-bfe7671afd50.png`
- Final implementation evidence:
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/appearance-save-reset-final.jpg`
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/theme-options-dropdown-portal-final.jpg`
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/night-default-animation-final.jpg`
- Same-input comparison evidence:
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/dropdown-reference-final-comparison.jpg`
  - `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/hero-setting-reference-final-comparison.jpg`
- [P1] The AJAX appearance save completed and displayed its success flash, but the generic duplicate-submit guard could apply its delayed “处理中…” label after completion. Successful saves now cancel/clear the busy marker, restore every submit label, and rebase the dirty-state signature. Browser verification after a real save found the success flash, no open dialog, no `data-busy` or `aria-busy`, and the restored “保存外观设置” label.
- [P2] The custom Hero display menu lived inside the modal scroll container and was clipped at the footer boundary. Non-search dropdown menus now portal to `body` while open, use fixed viewport coordinates and a top-level z-index, then restore to their original DOM position when closed. The measured live menu is a direct `body` child with `.dd-portal`, fixed coordinates and a viewport-limited maximum height.
- [P1] Seamless Canvas, Night Corridor and Open Ascent used the featured article cover as an undeclared fallback when `hero.visual` selected a theme animation. Each family now branches strictly on the stored Hero visual mode: configured image, configured SVG, animation 1, or the theme-default animation. Browser checks after a complete service restart found one `.hero-anim2` and zero direct Hero images in all three families.
- [P1] The showcase normalization migration rewrote an intentionally empty `hero.visual` back to `image` on every process start. Empty is a valid persisted value meaning “theme default animation”, so that recurring rewrite was removed. A store reopen regression test preserves both the empty mode and the dormant uploaded image URL, and a real save → process restart → three-theme browser check confirmed the setting remains empty and the animations remain active.
- Night Corridor's animation was reduced and moved into the middle negative space so it no longer obscures the right reading guide. Seamless Canvas and Open Ascent retain their accepted animation scale.
- Regression verification: `TestShowcaseDefaultHeroAnimationSurvivesReopen`, the appearance interaction contract, all three media Hero modes, media visual contracts, theme preview rendering and theme-browse routing pass. The broader suite reaches unrelated network/listener tests that are blocked by the current sandbox, while all in-scope packages and targeted contracts pass.

### Pass 17 — six content skeleton Hero/article routing, passed

- Source visual truth: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-9f44eb3e-acdd-4153-8924-f6a31fc887d3.png` (1886 × 1160 px). This accepted theme-card capture defines the six skeletons and their relative Hero/article emphasis.
- Browser-rendered implementation evidence: `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/six-theme-hero-implementation/shelf-index-final.png` (3840 × 2160 px full-display capture) plus direct browser screenshots inspected for all six `/admin/theme-browse/{theme}/` routes.
- Comparison state: signed-in theme browse, default stored Hero visual, default palette, desktop. The page viewport measured 1736 × 864 CSS px at device pixel ratio 2; the display capture includes browser chrome and was judged against the source at fit-to-frame rather than pretending pixel equality. A same-input comparison opened the source card grid and the unobstructed Shelf Index implementation together.
- Full-view evidence: Shelf Index, Tradeoff Sheet and Light Table preserve their approved copy/media grid, typography, rules and below-Hero content composition while replacing only the media payload with the shared default animation. Progress Bulletin, Margin Reading Room and Counterpoint preserve the accepted article-led compositions and show the real selected article cover by default.
- Focused Hero evidence: direct DOM measurements found Shelf Index 712 × 556 px, Tradeoff Sheet 712 × 626 px, Light Table 679 × 567 px, Progress Bulletin 489 × 333 px, Margin Reading Room 305 × 463 px and Counterpoint 470 × 426 px. The first three contain exactly one shared animation wrapper and no dormant configured image; the latter three contain the selected article cover and no animation wrapper. Every route reports zero horizontal overflow and zero broken images.
- [P1] Shelf Index and Tradeoff Sheet rendered any stored `hero.image` even when `hero.visual` selected the theme default or animation 1. Light Table used the featured article cover as its implicit fallback, making its wide visual stage behave like an article thumbnail. All three now branch strictly on image, SVG, animation 1, or default animation 2. Dormant image/SVG values cannot leak into another mode.
- Article-first contract: Progress Bulletin, Margin Reading Room and Counterpoint intentionally keep the selected article cover for both empty/default and animation-1 values. An explicitly selected image or SVG still overrides the article cover. This preserves the six designs' different editorial purposes instead of forcing one global Hero behavior.
- Typography: the accepted serif/sans display hierarchy, text wrapping and small metadata weights are unchanged. The reusable animation adds no visible copy.
- Spacing/layout: the established grid tracks, padding, section rhythm and non-full-screen Hero heights are unchanged; the animation wrapper inherits each theme's measured media slot and scales within it.
- Colors/tokens: the shared animation consumes existing accent, line and background tokens, so default, pure-white and dark palette cards remain theme-driven without hardcoded colors.
- Image quality/assets: explicit user images and SVGs remain unmodified and are rendered only in their selected mode. Article-first themes use the real CMS cover at its existing crop. The default animation reuses the product's established vector animation partial rather than introducing a new approximate asset.
- Copy/content: Logo, navigation, Hero copy, featured article, covers, lists, links, dates and counts remain backend-bound. No business text, image URL, article title or category mapping was added to a template.
- Interaction and runtime checks: automated render tests cover all four modes for the three Hero-first themes and the featured-cover/image/SVG behavior for the three article-first themes. `go test ./internal/web -count=1` and `git diff --check` pass. Browser logs contain no application error; observed warnings are emitted by installed wallet extensions.
- Comparison history: the initial P1 mode-routing mismatch was fixed in the three Hero-first templates, followed by a fresh service build/restart, six-route browser inspection and the same-input source/implementation comparison. No actionable P0/P1/P2 difference remains.
- Residual test gap: the physical Chrome window was not resized through every appearance-editor width preset in this pass. Existing responsive CSS and the browser's zero-overflow checks remain covered by the established theme preview tests; this is non-blocking because the change does not alter grid breakpoints.

## Findings

No actionable P0, P1, or P2 differences remain.

## Follow-up polish

- [P3] Casebook's small industry icons from the visual study are omitted because the current category model has no icon field. Adding guessed icon mappings would hardcode content semantics, so the dynamic production version keeps number, category, description, count, and arrow only.

## Final result

final result: passed

### Pass 29 — Fixed-sidebar theme-card preview framing, passed

- Source visual truth: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-77804881-4761-4e81-b136-bebfad47e0a6.png` (1848 × 1168 px). It shows Catalogue and Atelier cards rendering only the left rail while their page content is pushed outside the thumbnail crop.
- Browser evidence: `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/sidebar-preview-audit/01-before-picker.png`, `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/sidebar-preview-audit/04-after-picker.png`, and the focused same-input comparison `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/sidebar-preview-audit/05-before-after-catalogue.png`.
- [P1] The preview-only `.site-header:not(.pc-site-header) { position: relative; }` rule had equal specificity to the four fixed-sidebar layout rules and loaded later. It therefore turned a `100vh` rail into an in-flow block, pushing the main skeleton below the fixed 760 px preview crop.
- The recovery selector is deliberately limited to `sidebar`, `factory-sidebar`, `dtc-catalogue`, and `dtc-atelier`. It covers all 16 registered skins in those families without changing ordinary top bars, Paper Current's dedicated rail, Type Cascade's independent rail, or Column's right-side index.
- Before the fix, Catalogue reported `position: relative` and `main.top = 1152`. After the fix its header reports `position: fixed`, `left = 0`, `top = 0`, and `main.top = 0`.
- Browser regression checks also pass for Sidebar (`main width 844`), Factory Sidebar (`main width 856`), and Atelier (`main top 26`, `main width 1024`); all four representative rails remain fixed at the left edge while useful content is visible in the first frame.
- The card's existing 16:10 crop, lazy loading, real-data preview endpoint, and 560% × `.18` scale are unchanged. No theme-specific content or preview image was hard-coded.
- Automated verification: `TestThemePreviewKeepsFixedSidebarLayoutsOutOfFlow`, `TestThemePreviewRendersAllThemes`, and the ThemePreview/ThemeBrowse/ThemePicker/DTC targeted web suite pass.

## Findings

No actionable P0, P1, or P2 difference remains for fixed-sidebar theme-card thumbnails.

## Final result

final result: passed

### Pass 28 — Responsive visibility controls and flat page filters, passed

- Source visual truth:
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-dad19284-7175-4cf5-aec9-dd9f81261d1c.png` (2538 × 504 px), showing the unwanted white card shell around the page-list filters.
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-a0a8e13e-9add-419c-982c-4527b98afc1a.png` (2864 × 1660 px), showing the unexplained oversized checkbox squares in the responsive inspector.
- Browser-rendered implementation evidence:
  - `/Users/apple/work/cms.ccvar.com/.qa/page-filters-flat-final.jpg` (1880 × 864 px, authenticated Chrome viewport at native capture density).
  - `/Users/apple/work/cms.ccvar.com/.qa/responsive-visibility-switches-bottom-final.jpg` (1721 × 912 px, authenticated Chrome viewport at native capture density).
  - `/Users/apple/work/cms.ccvar.com/.qa/responsive-visibility-switches-final.jpg` (1721 × 912 px, desktop switch focused state).
- Same-input focused comparison: `/Users/apple/work/cms.ccvar.com/.qa/visibility-filter-comparison.png` (2400 × 1400 px). The source and browser-rendered filter rows were cropped and normalized into equal 1100 × 145 px cells; the source and browser-rendered responsive inspectors were cropped and normalized into equal 1000 × 820 px cells. Source screenshots do not expose CSS viewport or DPR metadata; their stored density is 72 dpi. No density-specific visual finding was filed.
- State: authenticated page list and page 51 simple composition editor, the first Hero section selected, Style tab open, desktop canvas active, and all three device visibility values restored to their original visible state. No Save or Publish action was submitted.
- Full-view evidence: the page-list screenshot shows the beige page background flowing directly around the search and dropdown controls. The duplicate white surface, outer border, radius, and shadow are gone while each control retains its own visible boundary. The editor screenshot shows the complete desktop/tablet/mobile inspector without the former blank square controls.
- Focused evidence: the combined comparison makes both fixes legible in one image. The old unlabelled large squares are replaced by compact accent switches labelled “在桌面显示”, “在平板显示”, and “在手机显示”.
- [P1] The generic editor input rule applied full-width/min-height styling to the breakpoint `hidden` checkboxes, producing large empty squares with no clear meaning. The simple editor now presents positive display switches and inverts their checked value only at the existing `responsive.*.hidden` protocol boundary.
- [P2] The page-list filter form added a second white card, border, radius, and elevation around controls that already had their own borders. The outer form is now transparent, borderless, radius-free, and padding-free; its compact grid and individual control affordances remain intact.
- Fonts and typography: existing admin font families, weights, field labels, table copy, and hierarchy remain unchanged. Only the formerly missing visibility labels were added.
- Spacing and layout rhythm: the filter controls keep their existing compact 8 px grid gap without an extra padded shell. Visibility rows use a restrained divider and compact 34 × 20 px switch; the narrow-screen rule preserves a 44 px label hit area.
- Colors and visual tokens: controls continue to use the existing surface, line, muted, and accent tokens. The page background is now visible around the filter controls, and checked switches use the existing admin accent.
- Image quality and asset fidelity: no product image, icon, logo, or illustrative asset was added, replaced, or approximated. The change uses a standard form-control treatment only.
- Copy and content: all page data, filter choices, URLs, revisions, and publication state remain unchanged. The UI says what the control does; the stored `hidden` field and schema are unchanged.
- Primary interactions tested: the Style tab exposes exactly three named switches; all begin checked. Turning desktop display off creates an unsaved edit and unchecks the switch; turning it back on restores the saved manifest and disables Save. The same off/on restoration was verified for mobile. No revision or database write occurred.
- Runtime checks: both authenticated routes render after the rebuilt server restart, Chrome reports no application console errors on either tab, JavaScript syntax validation passes, targeted admin page/composition tests pass, the application builds, formatting is clean, and `git diff --check` passes.
- Comparison history: the source captures contained one P1 affordance failure and one P2 duplicate-surface issue. Both were fixed before the browser capture; the post-fix full and focused comparison found no remaining actionable P0/P1/P2 difference.

## Findings

No actionable P0, P1, or P2 difference remains. The filter row is visually lighter, and device visibility is now explicit without changing the stored page protocol.

## Final result

final result: passed

### Pass 25 — 普通用户自由编排编辑器，passed

- Product Design 选定稿：`/Users/apple/.codex/generated_images/019f9819-858c-7022-be40-dcad9b27ecd2/call_1TnF3r6xho7KZm8Tw9U4duJq.png`（1487 × 1058 px）。
- Chrome 实现截图：
  - `/Users/apple/work/cms.ccvar.com/.qa/simple-editor-final-top-1487x1058.png`（设计稿对应桌面状态）。
  - `/Users/apple/work/cms.ccvar.com/.qa/simple-editor-1080x900.png`（紧凑桌面）。
  - `/Users/apple/work/cms.ccvar.com/.qa/simple-editor-390x844.png`（手机）。
- 同图对照：`/Users/apple/work/cms.ccvar.com/.qa/simple-editor-comparison.png`。参考稿与同尺寸实现并排检查了页面层级、三栏比例、工具条、选区、右栏表单、边框、圆角、间距和字体；实际预览继续使用当前站点主题与真实后端数据，没有把设计稿示例内容或装饰图硬编码进渲染器。
- 自由编排页默认进入简单模式；左侧只显示中文业务区块，中央为真实修订预览，右侧为内容、样式、数据三类设置。高级组件标识和原工作台仅在“高级编排”中显示；互动应用继续使用原高级工作台。
- Chrome 真实交互通过：iframe/结构树点选同步、选区浮层、编辑/浏览往返、内容/样式/数据切换、中文添加区块目录、桌面/平板/手机切换、通用宽度下拉、修改后保存启用、发布禁用、撤销后恢复原草稿、高级/简单模式往返。未执行保存或发布，数据库未产生测试修订。
- 浏览器验收发现并修复了三项 P1：初始选中区块未显示右栏字段、树中定位区块会带动外层页面滚动、浏览模式隐藏了返回编辑入口。修正后没有剩余可执行 P0/P1/P2。
- 1487、1080 与 390 CSS px 均无横向溢出；1080 时检查器下移，390 时工具条与三段工作区按单列排列。应用自身 warning/error 为 0；仅观察到用户浏览器扩展产生的独立 warning。
- 自动验证：`node --check assets/js/admin.js`、页面平台定向测试、完整 `go test ./internal/web -count=1`、`go build -o bin/cms .` 与 `git diff --check` 均通过。

## Findings

No actionable P0, P1, or P2 visual or interaction difference remains in this scope.

## Final result

final result: passed

### Pass 24 — Pilot mobile spacing rhythm, passed

- Source visual truth: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-c532579e-0624-42ab-9c35-9d8bc4574bdf.png` (958 × 1322 px), showing the reported mobile state with cards and composer touching the white content frame.
- Browser-rendered implementation evidence: `/private/tmp/pilot-mobile-breathing-final.png` at 390 × 844 CSS px and `/private/tmp/pilot-mobile-breathing-480.png` at 480 × 844 CSS px.
- Same-input comparison evidence: `/private/tmp/pilot-mobile-breathing-comparison.png`. The source was normalized to the 465 px browser-capture width and placed beside the 480 CSS px implementation state.
- State: mobile new-conversation screen, site-operations mode selected, no available site or Pilot.
- [P2] The previous mobile override removed all horizontal padding from `.pc-main`, making the mode cards, composer and safety note share the exact content-frame edge. The corrected layout adds 16 px horizontal and 26/34 px vertical main padding.
- [P2] The welcome copy, mode cards and composer had nearly uniform, compressed spacing. The welcome group now uses 16/28 px spacing, its heading has a 12 px text gap, card rows use a 12 px gap, and the transition into the composer uses 24 px.
- Typography: the existing type families and sizes are preserved; the welcome paragraph now uses a 1.65 line height and a 320 px readable measure.
- Spacing and layout rhythm: all interactive blocks align to a consistent inset, while the surrounding off-white frame remains visible. The safety note gains a 14 px separation and side inset.
- Colors and tokens: no palette or state-color changes were made.
- Image and icon fidelity: the existing Pilot-aligned icons remain unchanged.
- Copy and content: all text and real empty-device state remain unchanged.
- Responsive behavior: both 390 and 480 CSS px states retain the compact top controls, single-column launcher, visible composer actions and no horizontal overflow. Browser console errors: none.

## Findings

No actionable P0, P1, or P2 mobile spacing issue remains.

## Final result

final result: passed

### Pass 23 — Pilot header device controls density, passed

- Source visual truth: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-43f72dac-4968-4a7b-83dc-7a1cd7c5b726.png` (2634 × 1136 px), with the device selector, connection summary and refresh action marked as oversized.
- Browser-rendered implementation evidence: `/private/tmp/pilot-top-controls-compact-desktop-final.png` at 1280 × 720 CSS px and `/private/tmp/pilot-top-controls-compact-mobile-final.png` at 390 × 844 CSS px.
- Focused same-input comparison evidence: `/private/tmp/pilot-top-controls-compact-comparison.png`. Source and corrected toolbar crops were normalized to 700 px width for direct density comparison.
- State: no bound Pilot, `0 在线 · 0 设备`, refresh action available.
- [P2] The original header used a 260 px device selector, 34 px refresh button, larger status padding and 8 px inter-control gaps. The corrected group uses a 196 px selector, 30 px control height, a 30 px refresh button, 6 px gaps and smaller 10–11 px utility type.
- [P2] At 390 px, the earlier available width caused the refresh action to wrap beneath the selector and the page title to break awkwardly. The mobile header now uses a 148 px selector, 28 px controls, 4 px gaps, nowrap behavior and a 24 px single-line title.
- Typography and spacing: utility labels retain the existing admin font and optical weight while using compact control sizing. Desktop and mobile alignment is vertically centered and the title/content hierarchy is unchanged.
- Colors and visual tokens: the existing white surface, border, muted icon and online/offline status tokens are unchanged.
- Image and icon fidelity: the existing refresh icon asset and status indicator are preserved; no new approximation was introduced.
- Copy and content: device selection and real online/device counts are unchanged.
- Interaction: the refresh button was activated successfully after the change and the page produced no browser console errors.

## Findings

No actionable P0, P1, or P2 difference remains for the header device-control density.

## Final result

final result: passed

### Pass 22 — Pilot composer dropdown density, passed

- Source visual truth: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-3a85bdce-2dd6-4d88-a819-9f899a86c291.png` (1532 × 776 px), showing the reported oversized default model trigger and open model menu.
- Browser-rendered implementation evidence: `/private/tmp/pilot-dropdown-compact-desktop-open.png` at 1280 × 720 CSS px and `/private/tmp/pilot-dropdown-compact-open.png` at 390 × 844 CSS px.
- Focused same-input comparison evidence: `/private/tmp/pilot-dropdown-compact-comparison.png`. The source and corrected dropdown regions were normalized to 430 px width and stacked for direct density comparison.
- State: new conversation, site-operations mode, no available bound device, model dropdown open with its inherited default selected.
- [P2] The original trigger preserved long “Pilot 默认” phrases and the global menu inherited large type, padding and row height. The composer-scoped trigger is now 26 px high with 10.5 px type; menu rows use 11 px type, 26 px minimum height and 5 × 7 px padding.
- [P2] Default labels now read `Codex · 默认`, `Claude Code · 默认`, `Grok · 默认` and `默认强度`, removing redundant wording while preserving the source of the default.
- Layout rhythm: model width is 132 px, effort width is 92 px, site width is capped at 160 px, control gaps are 2–6 px, and the menu remains aligned to its trigger.
- Colors and tokens: existing GCMS dropdown surface, line, accent, hover and selected colors remain unchanged. Only density and copy were adjusted.
- Typography: compact UI sizes and weights remain legible at both desktop and 390 px mobile widths; no option wraps.
- Image and icon fidelity: no visible asset was added or approximated in this pass.
- Interaction and accessibility: the shared dropdown behavior, native select synchronization, selected check, keyboard semantics and ARIA-expanded state are retained. The open model menu exposed all three options and produced no browser console errors.

## Findings

No actionable P0, P1, or P2 difference remains for the composer dropdown density.

## Final result

final result: passed

### Pass 21 — GCMS Pilot 新对话与 Pilot 客户端视觉对齐，passed

- Source visual truth: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-3bef011d-01e0-447e-bd85-e0ee08e8f1d1.png` and the production Pilot launcher/composer implementation in `desktop/src/routes/+page.svelte`.
- Browser-rendered implementation evidence: `/private/tmp/pilot-console-full-final.png` at the default desktop viewport and `/private/tmp/pilot-console-mobile.png` at 390 × 844 CSS px.
- Same-input comparison evidence: `/private/tmp/pilot-console-qa-comparison.png`. The Pilot reference crop and browser implementation crop were normalized to 716 px and stacked in one image for direct typography, spacing, card and control comparison.
- State: “新对话” selected, default “站点运营” mode, no bound device available, disabled site selection and send action. The empty-device state is real and is not visually presented as successful or online.
- [P1] The former GCMS form used oversized workflow cards, a separate three-column settings row, a large standalone textarea and a square blue send button. It did not read like the Pilot composer. The form now follows Pilot's actual launcher/composer structure: compact three-mode cards, integrated context/model/effort controls, restrained textarea height and a circular arrow send control.
- [P1] Text glyph placeholders were replaced with the same external SVG icon assets used by Pilot's launcher semantics for site operations, site building, free conversation and send.
- [P2] Mode context now behaves like Pilot: site operations exposes the site picker, site building shows a new-site context label, and free conversation no longer requires a selected site.
- Typography and spacing: launcher card labels, secondary descriptions, icon tiles, 12 px radii, compact gaps and selected inset border follow Pilot's component measurements. The GCMS page shell intentionally retains its established serif heading and off-white admin frame.
- Controls and interaction: the model and effort controls use the shared admin dropdown component. The model dropdown opened with three real options and the selected state; switching among all three modes updated the visible context without a page refresh.
- Responsive QA: at 390 × 844 CSS px the mode cards stack, composer controls reflow without horizontal overflow, the circular send control stays visible and the mobile bottom navigation remains available.
- Runtime QA: the final desktop and mobile states produced no browser console errors.

## Findings

No actionable P0, P1, or P2 visual difference remains in the new-conversation surface.

## Final result

final result: passed

### Pass 24 — 页面列表筛选与高级页面工作台收口，passed

- 参考截图：
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-3a037806-4ba7-4965-95ea-d7eb7c3403be.png`（2362 × 1362 px，页面列表）。
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-4f625c0e-ad38-4dc7-91cb-f8319859ab7d.png`（3448 × 1874 px，自由编排工作台）。
- Chrome 实现截图：
  - `/private/tmp/gcms-pages-list-after.png`（1736 × 864 px）。
  - `/private/tmp/gcms-composer-after.png`（1721 × 857 px，未选组件）。
  - `/private/tmp/gcms-composer-inspector-after.png`（1721 × 857 px，已选组件与右侧属性面板）。
- 同图对照：
  - `/private/tmp/gcms-pages-list-compare.png`。
  - `/private/tmp/gcms-composer-compare.png`。
- 页面列表筛选区由原来的大尺寸表单收紧为 52 px 整体高度；搜索、四个筛选下拉和提交按钮均为 34 px 控件高度。四个下拉复用后台现有 `dropdown[data-select-dropdown]` 交互，实际展开、选择“自由编排”和按钮联动均通过。
- 自由编排页面始终提供工作修订预览；互动应用在安全构建通过后提供工作修订预览，未构建状态显示不可用预览提示。两类高级页面的未发布草稿均提供删除入口。
- 高级页面删除使用独立端点并要求 CSRF、当前 ETag、草稿状态、从未发布且无历史发布记录；已发布或存在发布历史的项目不会显示可执行删除。视觉验收未触发实际删除。
- 工作台取消超宽负边距与页面级横向滚动，改为受视口约束的三栏容器。左侧结构/组件库和右侧设置面板各自独立滚动，滚动条统一为 7 px 细轨道；中间画布与预览框限制在可用列宽内。
- 画布宽度下拉和动态组件属性单选均复用通用下拉。设备切换、画布下拉展开、结构树选中、右侧属性渲染与预览 iframe 均在真实 8080 登录态通过。
- Chrome 控制台过滤掉浏览器扩展日志后，应用自身 warning/error 数为 0。
- 自动验证：`go test ./internal/web -count=1`、相关高级页面定向测试、`node --check assets/js/admin.js`、`go build -o bin/cms .` 与 `git diff --check` 均通过。

## Findings

No actionable P0, P1, or P2 visual or interaction difference remains in this scope.

## Final result

final result: passed

### Pass 23 — Advanced page creation forms, passed

- User-reported source evidence: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-a5adf6a3-a7f8-4774-a887-a7150c2094c5.png` and `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-19ddc47f-37e0-4964-ba90-587a9933f6f3.png`.
- Browser-rendered evidence: `/private/tmp/gcms-page-form-composition-final.png` at the normal desktop viewport and `/private/tmp/gcms-page-form-app-mobile-final.png` at 390 × 844 CSS px.
- Same-artifact comparison: `/private/tmp/gcms-page-form-before-after-final.png`.
- [P1] The composition and app creation forms used undefined `.form-grid` and `.form-actions` classes, so native inline controls produced the reported arbitrary layout. Both modes now share one structured GCMS admin form with a mode header, three semantic field groups, explicit labels, help associations, and a dedicated action footer.
- [P1] The form inherited the generic content-card hover lift and used the wrong `.form-error` class. It now has a static form surface and uses the existing styled `.form-err` alert.
- [P1] Page Platform styles referenced an undefined `--card` token. All platform surfaces now use the established `--surface` token.
- Desktop QA measured an 860 px form, six contained fields, one required field, and 44 px primary action height. Mobile QA measured a 327 px form within a 390 px viewport, no horizontal overflow, full-width contained controls, valid label/control associations, 44 px actions, and matching visual/keyboard action order.
- Composition and app keep the same backend contract and field names. Their labels, guidance and CTA text vary by mode; no site, customer or campaign data is hard-coded.
- Native required-field validation remains authoritative; after it passes, the submit action becomes busy and locked so a double click cannot create duplicate drafts.
- Automated verification: targeted create-form rendering and validation-error preservation tests pass, the complete `internal/web` package passes, and `git diff --check` passes.

## Findings

No actionable P0, P1, or P2 visual difference remains.

## Final result

final result: passed

### Pass 22 — Editorial trio responsive and release closure, passed

- The three themes in this delivery are `answer-desk`, `portrait-journal`, and `casebook`; `field-ledger` was the pre-existing theme visible in the first browse screenshot and is not part of this trio.
- User-reported navigation reference: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-5439df4e-f911-47dd-ad76-feb880b6af3a.png`.
- Browser-rendered implementation evidence: `/private/tmp/gcms-theme-answer-desk-viewport.png`.
- Same-artifact before/after comparison: `/private/tmp/gcms-answer-desk-nav-comparison.png`. The user screenshot's page-header region and the current browser render were normalized into adjacent panels and visually inspected together. The current header has a measured `0px` top border, one `2px` active-navigation underline, no generated duplicate underline, and remains sticky at `top: 0`.
- Palette evidence: `/private/tmp/gcms-editorial-trio-palettes.png`. It contains the browser-rendered white and dark cards for all three skeletons in one inspected matrix: Answer Desk white/deep-night, Portrait Journal white/deep-forest, and Casebook white/deep-charcoal.
- Desktop browser matrix covered 1080, 1200, 1240, 1360, and 1440 CSS px for all three themes. Every result reported zero horizontal overflow. The content shell follows the available width through 1240 and caps at a centered 1280 px at 1360/1440, so the themes no longer behave as full-screen canvases.
- Mobile browser QA at 390 × 844 covered all three themes: the effective 375 px content viewport had zero horizontal overflow, the page shell measured 375 px, and the menu trigger replaced the desktop navigation.
- The same three standard pages and article pages measured 112 px between their final content and footer at 1440 px. This closes the reported footer crowding on both ordinary pages and related-reading article pages.
- All nine palette IDs render the same backend-bound family skeleton. The added regression test supplies CMS Hero values and requires each base/white/dark theme to consume them, preventing a palette card from drifting into hard-coded demo content.
- The advanced-page workbench was also browser-verified at its exact 1080/1200/1240/1360/1440 presets, plus 768 px tablet and 390 px phone modes. Composition data binding, immutable revisions, app build/preview/public interaction, trusted-shell/untrusted-app sandbox separation, and the corrected responsive grid were exercised against disposable QA data.

## Findings

No actionable P0, P1, or P2 visual difference remains in the three-theme scope or the page-platform workbench.

## Final result

final result: passed

### Pass 21 — Pilot header action and mobile menu alignment, passed

- Source screenshots: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-b70c133a-c230-4aff-bc40-79ce625d02e0.png` and `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-ec0edab3-6899-4da9-858a-29da3e076bd5.png`.
- Browser evidence: `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/pilot-audit/11-mobile-header-fixed.jpg` and `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/pilot-audit/13-mobile-menu-final.jpg`.
- The ambiguous global “View all” header CTA is no longer rendered for the Pilot family; other content-theme families retain their existing behavior.
- At 375 CSS px the brand remains left aligned, search and menu form a right-aligned tool group, and the menu button no longer occupies the header center.
- The opened navigation is anchored directly below the 54 px header and spans the available 343 px content width with 16 px outer gutters.
- Runtime checks report no horizontal overflow (`scrollWidth === clientWidth`) and no Pilot header CTA. Targeted Pilot tests, build, and `git diff --check` pass.

## Findings

No actionable P0, P1, or P2 differences remain.

## Final result

final result: passed

### Pass 20 — Pilot Flight Deck reference-fidelity rebuild, passed

- Visual source of truth: `/Users/apple/.codex/generated_images/019f986c-a7c7-71f0-9b09-ea435acec882/call_WjSa8mULZFQ2VLXxy4tSXOgJ.png` (864 × 1821 px).
- Browser evidence: `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/pilot-audit/08-final-864.jpg`, `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/pilot-audit/09-final-comparison.png`, `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/pilot-audit/05-mobile.jpg`, and `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/pilot-audit/06-final-desktop.jpg`.
- [P1] The former 1000 px breakpoint collapsed the reference's desktop composition at its native 864 px width. Desktop grids now remain intact through 721 px and stack only on phone-sized viewports.
- [P1] The page previously read as a generic content theme with oversized section rhythm. Hero, workflow strip, product demo, capability matrix, trust block, gallery, resources, download strip, and footer now use the reference's compact launch-page hierarchy and proportions.
- [P1] The workflow is now a continuous numbered band; resources render as a horizontal card rail; the former full-width dark trust panel is replaced by the reference's light split composition.
- The Hero visual remains fully data-driven and transparent. Uploaded images, SVG files, pasted SVG, and the configured default animation continue to use the existing backend slot without a hard-coded Pilot screenshot or decorative mask.
- Workflow, release, downloads, trust, gallery, featured articles, categories, and links remain backend-bound. New/empty sites receive a four-step localized workflow default; existing saved site values are intentionally preserved.
- Responsive QA at 375 CSS px reports `scrollWidth === clientWidth`; desktop QA at 1721 CSS px reports the same. No broken images, empty anchors, or lost sticky navigation were found.
- Automated verification: targeted Pilot/theme rendering tests pass, the application builds, and `git diff --check` passes. The broad web suite still reaches unrelated IndexNow network calls and an `httptest` listener blocked by the current sandbox.

## Findings

No actionable P0, P1, or P2 visual difference remains within the backend-driven content contract.

## Final result

final result: passed

### Pass 18 — Pilot Flight Deck visual fidelity and live configuration schema, passed

- Source visual truth: `/Users/apple/.codex/generated_images/019f986c-a7c7-71f0-9b09-ea435acec882/call_WjSa8mULZFQ2VLXxy4tSXOgJ.png` (864 × 1821 px).
- User-reported implementation evidence:
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-cdf13eb4-0220-45e6-b56c-8245a1df7f49.png` (2492 × 1418 px).
  - `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-f9d05009-89a1-437b-aa22-af23d9fed9b8.png` (1746 × 986 px).
- Browser-rendered implementation evidence: `/private/tmp/pilot-flight-deck-final.png` (1280 × 720 px).
- Same-input comparison evidence: `/private/tmp/pilot-flight-deck-comparison.png` (2584 × 768 px). The reference Hero crop (864 × 486) was normalized to 1280 × 720 and placed beside the 1280 × 720 browser capture. Browser viewport was 1280 × 720 CSS px at the in-app browser's native density.
- State: default Pilot palette, real site logo/navigation/Hero copy, no Pilot workflow/release/download/trust/gallery records configured, and the theme-default Hero animation selected. The reference's Pilot client screenshot, CTA, release metadata and platform labels are data-dependent states rather than template defaults.
- Full-view comparison: the corrected implementation now uses the reference's approximately 35/65 copy/media division, compact product-launch typography, two-line desktop title, restrained navigation height, matching content anchor, and transparent Hero media region. The configured image/SVG slot remains responsible for the reference client screenshot; no replacement image, background panel, or product data was hardcoded.
- Focused comparison: the pre-fix title occupied 400 × 149 px over three forced lines at 1280 px. After switching the Pilot heading to the product sans stack, reducing its scale and allowing theme-controlled wrapping, it occupies 400 × 85 px over two lines. The Hero grid is 1201 px wide and the header is 65 px tall.
- [P1] Pilot inherited the global serif heading family and content-theme display scale (`6vw`, capped at `6.9rem`), producing the reported oversized four-line title. Fixed with Pilot-scoped sans typography, `clamp(2.35rem, 3.2vw, 3.5rem)`, 1.04 line-height, tighter tracking, and a 4.15/7.85 grid.
- [P1] After an AJAX theme change, the page kept the configuration schema rendered for the previous theme. The saved Pilot theme therefore opened a stale Hero-only modal even though its five site slots were registered. Successful saves now reload only when the selected theme ID changes, forcing the server to reassemble the correct schema. A new server-render regression test requires all five Pilot slot markers and editors.
- Fonts and typography: Pilot headings now use `var(--sans)` at product-launch weights; body, kicker and navigation retain the existing design system's optical hierarchy. Desktop wrapping matches the target composition and mobile wraps naturally without horizontal overflow.
- Spacing and layout rhythm: Hero padding is 44/54 px, the grid gap is 44 px, and media no longer has a forced 540 px minimum height. At 390 × 844, the page reports no horizontal overflow and the media stacks below copy.
- Colors and visual tokens: the accepted cool off-white, blue accent, hairline and three palette-card variables are unchanged. The Hero media wrapper stays transparent.
- Image quality and asset fidelity: real uploaded image/SVG content is rendered with `object-fit: contain`, no background fill, crop, filter or mask. The empty state continues to use the established theme animation; the reference client screenshot is not approximated.
- Copy and content: logo, navigation, Hero copy, workflow, release, downloads, trust points, gallery, articles, categories and links remain backend-bound. The five Pilot configuration values are normalized and validated before storage.
- Primary runtime checks: desktop render, 390 × 844 responsive render, sticky navigation, no horizontal overflow, and empty console error/warning log. Targeted Pilot/theme-option tests and build pass.
- Broader suite note: the complete `internal/web` package reaches unrelated IndexNow network calls and an `httptest` listener that the current sandbox disallows. The in-scope targeted tests pass; the failure is not in Pilot rendering or configuration.
- Comparison history: initial P1 typography/grid mismatch and P1 stale-schema modal were fixed, rebuilt and restarted. The post-fix same-input comparison and responsive browser capture found no remaining actionable P0/P1/P2 issue. Differences in client screenshot, CTA and version/platform labels are expected empty-data state and are now editable through the restored site slots.

## Findings

No actionable P0, P1, or P2 differences remain.

## Follow-up polish

- [P3] The Pilot structured slots currently expose normalized JSON editors. A future editor could provide repeatable visual rows without changing the stored schema.

## Final result

final result: passed

### Pass 19 — Pilot structured slot editor, passed

- Source visual truth: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-1b6ea5fc-a8fd-42d3-ae66-e658bbacdc93.png` (1464 × 1236 px). The reported state exposed long JSON examples beside undersized raw textareas.
- Browser-rendered implementation evidence: `/Users/apple/.codex/visualizations/2026/07/25/019f986c-a7c7-71f0-9b09-ea435acec882/theme-qa/pilot-visual-fields-final.png`.
- [P1] Workflow, release, downloads, trust and gallery were technically configurable but required users to author valid JSON. Each slot now renders semantic fields and numbered repeatable rows while retaining the existing normalized JSON storage contract.
- The dialog grows from 760 px to a measured 980 px at a 1721 px viewport. Intro fields use a clear two-column hierarchy; repeat rows have labels, compact numbering, consistent field height, and responsive single-column behavior.
- Raw JSON examples are removed from the UI. Invalid or incomplete URLs are still discarded by the existing sanitizers, empty rows do not serialize, and localized/global storage scope is unchanged.
- Default values are centralized in the locale catalogs and assembled as structured Pilot data, rather than embedded in the template. The live form reports `1.0.0`, `下载 macOS 版`, `每一步都在掌控中`, and the configured real Hero image as the gallery seed when the corresponding slot is semantically empty.
- Download URLs remain intentionally unset until the site supplies a real destination; no broken package URL is invented. Gallery fallback reuses the configured Hero asset and never generates a fake client screenshot.
- A browser QA pass found the expected 980 px dialog width, visible defaults, no horizontal overflow in the inspected state, and no application panic after reload. A nil preview-state regression discovered during QA was fixed by resolving the Hero image from localized settings instead of the admin-only Settings view.
- Automated verification: Pilot appearance rendering, structured save/normalization, configured data rendering, localized default rendering and template no-hardcoding tests pass. `git diff --check` passes.

## Findings

No actionable P0, P1, or P2 differences remain.

## Final result

final result: passed

### Pass 26 — Simple editor compact operation chrome, passed

- Source visual truth: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-e3fc1870-947f-4c13-9138-770929788a68.png` (3264 × 1668 px, annotated current state).
- Browser-rendered implementation evidence: `/Users/apple/work/cms.ccvar.com/.qa/simple-editor-compact-desktop.jpg` (1736 × 920 px at a 1736 × 920 CSS viewport) and `/Users/apple/work/cms.ccvar.com/.qa/simple-editor-compact-mobile.jpg` (390 × 844 px).
- Full-view comparison evidence: `/Users/apple/work/cms.ccvar.com/.qa/simple-editor-compact-comparison.png`. The annotated source and final browser render were normalized to equal 1000 px widths and placed side by side in one inspected image.
- Focused controls did not require a separate comparison image because the source explicitly boxes all four operation regions and the equal-width full comparison keeps every affected control legible.
- Spacing and layout rhythm: top actions reduced from 42 px to 34 px; toolbar from 58 px to 46 px; left rail from 240 px to 212 px; right inspector from 352 px to 300 px; section rows from 46 px to 38 px; form inputs from 42 px to 36 px. The center canvas increased from 886 px to 966 px at the same 1480 px workbench width.
- Typography: compact desktop controls use 11–12 px UI type with the existing font stack and weights. Titles and preview content are unchanged. No tested action or section label clips.
- Colors and visual tokens: existing surface, accent, line, muted, success and danger tokens are unchanged; the pass changes density only.
- Image quality and asset fidelity: no image or icon substitution was needed. Existing icon templates and preview assets remain unchanged.
- Copy and content: all labels and backend-bound page data remain unchanged; no business data was added or hard-coded.
- Responsive behavior: a 390 × 844 rendered DOM/CSS pass reports zero horizontal overflow. Desktop controls compact to 34–36 px, while the mobile media query restores 40–42 px touch targets for actions, tabs, section rows and form fields.
- Primary interactions tested in the authenticated Chrome page: Edit/Browse, Content/Style/Data, selected-section editing, and restoration to the Content tab. Browse mode hid both side panels and returned without layout overflow.
- Runtime checks: targeted admin page/composition tests, JavaScript syntax validation, application build and CSS diff hygiene pass. The application emitted no local-origin console warnings or errors; observed warnings came only from installed wallet extensions.

## Findings

No actionable P0, P1, or P2 differences remain. The intentionally denser desktop chrome preserves readable labels and gives the preview canvas materially more room.

## Final result

final result: passed

### Pass 27 — CTA meaning and button proportion, passed

- Source visual truth: `/var/folders/hv/v_cz9tgs4b74bg3qdvssct_h0000gn/T/codex-clipboard-0c2c2a99-e139-4acf-91a8-da6d3fb70fa2.png` (2574 × 1320 px). It shows the selected CTA with its front-end link stretched to almost the full 238 px section height.
- Browser-rendered implementation evidence: `/Users/apple/work/cms.ccvar.com/.qa/cta-button-fixed-desktop.png` (1721 × 912 px browser viewport at native density) and `/Users/apple/work/cms.ccvar.com/.qa/cta-button-fixed-mobile.png` (same editor viewport with the embedded preview switched to its 375 px mobile canvas).
- Focused same-input comparison: `/Users/apple/work/cms.ccvar.com/.qa/cta-button-fix-comparison.png` (1912 × 236 px). The 1904 × 472 source CTA crop was normalized to 944 × 236 and placed beside the 944 × 236 implementation crop. This keeps the affected text, button, selection outline, and editing overlay legible in one visual inspection.
- State: authenticated simple editor, page 51, `cta.banner` selected, saved revision unchanged, desktop `wide 1240` canvas for the primary comparison. The CTA link remains `/zh/` and no editor field was submitted.
- [P1] The CTA action flex container inherited grid-track stretch. Its single link therefore rendered at about 214 px tall and looked like a large vertical panel instead of a normal call-to-action. The CTA action container now opts out of stretch; in the desktop two-column layout it measures 46.8 px tall and is vertically aligned beside the copy.
- [P2] “行动按钮区” and the inspector heading “按钮” described implementation jargon, not user intent. The catalog and client fallback now call the component “下一步引导”, its inspector calls the field “跳转按钮”, and the description explains that the title, supporting text, and link guide visitors to an important page or next step.
- Responsive evidence: the default desktop/tablet two-column CTA uses the compact centered action. Stack or one-column configurations retain a 1rem text-to-action gap. In the 375 px mobile canvas the CTA becomes one column, the action measures 45.2 px tall with a 16 px top gap, and `scrollWidth === clientWidth` (no horizontal overflow). The rules are breakpoint-, layout-, and column-aware so custom row/stack settings do not inherit the wrong spacing.
- Fonts and typography: existing display and UI fonts, sizes, weights, wrapping, and CTA label text remain intact. The button is content-sized rather than height-stretched.
- Spacing and layout rhythm: the copy and action now share a balanced two-column row; the button no longer dictates the section height. Mobile keeps a clear vertical gap before the action.
- Colors and visual tokens: the existing blue accent, warm-red editor selection state, borders, radii, and surfaces are unchanged.
- Image quality and asset fidelity: this component contains no image asset and no replacement or approximation was introduced.
- Copy and content: only the editor-facing component label, field label, and explanation changed. The page title, CTA text, destination, manifest identity, revision data, and publication behavior remain unchanged.
- Runtime checks: the targeted composition-render and component-catalog tests pass, `admin.js` parses, the application builds, `git diff --check` passes, desktop/mobile browser measurements pass, the inspector shows “下一步引导”, and Chrome reports no application console errors.
- Comparison history: the initial source exposed a stretched 214 px action. The first scoped fix reduced it to normal content height. A follow-up audit found that a global zero-margin rule could crowd custom one-column layouts, so the final rule was narrowed by breakpoint, layout, and column count. Post-fix desktop and mobile evidence shows no remaining actionable P0/P1/P2 issue.

## Findings

No actionable P0, P1, or P2 difference remains. The button now reads as a normal next-step link instead of an unexplained blue panel, while its saved content and destination remain intact.

## Final result

final result: passed

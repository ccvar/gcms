# Content Operations Rules

## Content Types

- `posts`: articles, tutorials, announcements, and long-form content.
- `pages`: stable pages such as about, product, start, or landing pages.
- `links`: resource or product directory entries with `link_url`.

## Task Modes

- `doctor`: run before first use, after permission changes, or when an API call fails unexpectedly.
- `audit`: inspect and report; do not write content.
- `draft`: create new items as drafts with complete metadata when enough context exists.
- `update`: patch only the requested fields after finding the exact ID.
- `media`: upload approved files and reuse the returned URL.
- `multilingual`: work per language item; never overwrite one language with another.
- `publish-review`: check readiness and risks before any explicit publish request.

## Before Editing

- Search by `slug` when the user gives a URL or slug.
- Search by `q` when the user gives a title or topic.
- Use `get` before patching if the change depends on current content.
- For category assignment, query categories first and use the returned `id`.
- For cover or inline images, upload the local file first and use the returned `url`.
- For multilingual edits, query `languages`, then use `trans_group`.

## Drafting Rules

- New content defaults to `status: "draft"`.
- Include `lang`; do not rely on the default language unless the user asked for the default language.
- Provide title, content, excerpt, meta description, and keywords when creating substantial drafts.
- For links, include `link_url`; if unknown, ask before creating a publish-ready entry.
- For posts, pages, and links with a supplied cover image file, upload it and set `cover_image` to the returned URL.
- Do not invent factual claims about products, dates, pricing, people, laws, or current events without a source.

## Update Rules

- Patch only fields relevant to the task.
- Preserve IDs, language, content type, and translation group unless explicitly changing them.
- Avoid unnecessary slug changes because URLs may already be indexed or linked.
- For published content, prefer proposing a patch first unless the user clearly wants immediate changes.

## Audit Rules

Look for:

- Missing excerpt, meta description, keywords, category, cover image, or link URL.
- Overlong or vague titles.
- Duplicate or near-duplicate titles.
- Drafts that look ready but lack SEO fields.
- Published content with weak metadata.
- Multilingual groups missing enabled languages.

Use normal audit for quick list-level checks. Use `--deep true` when content body quality matters; it reads each item before checking for missing content.

## Diagnostic Rules

Run `node scripts/gcms.js doctor` when:

- Setting up a new access key.
- Permissions were edited in GCMS.
- Uploads, categories, or OpenAPI behavior seems inconsistent.
- A tool reports `missing_scope`, `bad_type`, or connection failures.

Doctor is non-destructive: it probes media permission with an invalid `.txt` upload and expects `bad_type` when `media:write` is present.

## Final Report

After a change, report:

- Content type and ID.
- Language.
- Status.
- Fields changed.
- URL or slug when available.
- Any items left for human review.

For audits, report:

- Highest-priority issues first.
- Exact IDs and languages.
- Suggested next actions.
- Whether anything was changed.

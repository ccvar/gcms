# SEO Checklist

Use this checklist for audits, drafts, and metadata improvements.

## Required Fields

- `title`: clear, specific, and useful to humans.
- `excerpt`: short summary for list pages.
- `meta_desc`: search result description; usually 50-160 characters is a useful target, not a hard rule.
- `keywords`: 3-8 focused terms when appropriate.
- `slug`: readable, stable, and language-appropriate.
- `category_id`: use when a matching category exists.
- `cover_image`: recommended for posts, pages, and links when visual presentation matters; upload the file first and use the returned URL.

## Title

- Put the main topic early.
- Avoid generic titles such as "Update", "News", or "Introduction".
- Do not stuff keywords.
- Keep one clear promise.

## Excerpt

- Summarize what the reader will get.
- Avoid repeating the title verbatim.
- Mention the concrete use case when possible.

## Meta Description

- Write a natural sentence or two.
- Include the main topic and benefit.
- Avoid clickbait.
- Do not include unsupported claims.

## Keywords

- Use terms the page actually covers.
- Include product, problem, and category terms.
- Avoid very broad one-word keywords unless they are central.

## Slug

- Prefer lowercase Latin slugs for stable URLs unless the existing site convention differs.
- Avoid date-only or meaningless slugs.
- Do not change published slugs unless the user asks.

## Multilingual SEO

- Keep each language natural.
- Do not directly machine-copy metadata across languages without localization.
- Check that translated versions share the same `trans_group`.
- If a language version is missing, report it instead of overwriting another language.

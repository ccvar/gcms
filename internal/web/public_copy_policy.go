package web

import _ "embed"

// Shared with Pilot at compile time so client prompts and downloaded kits use
// the same public-copy boundary. This is editorial guidance, not a word blacklist.
//
//go:embed public_copy_policy.md
var publicCopyPolicy string

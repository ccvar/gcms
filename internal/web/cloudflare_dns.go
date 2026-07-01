package web

import (
	"context"
	"strings"
)

// Platform-level settings shared across all sites for Cloudflare DNS auto-config.
const (
	platformCFDNSTokenKey = "cloudflare.dns_token"
	platformServerIPv4Key = "cloudflare.server_ipv4"
	platformServerIPv6Key = "cloudflare.server_ipv6"
	// platformCFProxiedKey remembers the "橙云代理" toggle: unset or "1" = write orange-cloud
	// (proxied) records — the default (checked); "0" = grey-cloud (DNS-only). Orange-cloud
	// only works when the origin serves a cert Cloudflare accepts (e.g. Caddy `tls internal`
	// + CF SSL mode = Full), which is the operator's responsibility.
	platformCFProxiedKey = "cloudflare.dns_proxied"
)

// cloudflareDNSResult reports one host's DNS auto-config outcome.
type cloudflareDNSResult struct {
	Host string
	OK   bool
	Msg  string
}

// dnsPlan reconciles a host's records of one type against the desired state so the host
// ends up with exactly one record pointing at the right content and proxy mode.
type dnsPlan struct {
	DeleteIDs []string // conflicting CNAME(s) + duplicate/stale same-type records to remove
	UpdateID  string   // non-empty: update this record to the desired content/proxy
	Create    bool     // true: no usable record exists, create a fresh one
}

// cloudflareRecordProxied reports whether rec is orange-cloud (proxied).
func cloudflareRecordProxied(rec cloudflareDNSRecord) bool {
	return rec.Proxied != nil && *rec.Proxied
}

// planCloudflareRecord decides how to make host resolve to a single recordType record
// pointing at content with the given proxy mode. It removes a conflicting CNAME (CF
// error 81054) and collapses duplicate/stale records of the same type down to one,
// updating that record in place when its content or proxy mode differs. This keeps a
// re-bind idempotent and prevents a leftover record from a previous deployment (e.g. an
// old Cloudflare Pages A record) from making the host resolve to two different IPs.
func planCloudflareRecord(existing []cloudflareDNSRecord, recordType, content string, proxied bool) dnsPlan {
	recordType = strings.ToUpper(strings.TrimSpace(recordType))
	content = strings.TrimSpace(content)
	var plan dnsPlan
	var sameType []cloudflareDNSRecord
	for _, rec := range existing {
		rt := strings.ToUpper(strings.TrimSpace(rec.Type))
		if rt == recordType {
			sameType = append(sameType, rec)
			continue
		}
		// A CNAME can't coexist with A/AAAA of the same name; drop it either way.
		if rt == "CNAME" || recordType == "CNAME" {
			plan.DeleteIDs = append(plan.DeleteIDs, rec.ID)
		}
	}
	if len(sameType) == 0 {
		plan.Create = true
		return plan
	}
	// Keep the first record; delete the rest so the host resolves to a single IP.
	keep := sameType[0]
	for _, dup := range sameType[1:] {
		plan.DeleteIDs = append(plan.DeleteIDs, dup.ID)
	}
	if strings.EqualFold(strings.TrimSpace(keep.Content), content) && cloudflareRecordProxied(keep) == proxied {
		return plan // already correct; only duplicates (if any) get removed
	}
	plan.UpdateID = keep.ID
	return plan
}

// upsertCloudflareRecord makes host's recordType (A/AAAA) resolve to exactly one record
// pointing at content with the given proxy mode, removing conflicting/duplicate records.
func upsertCloudflareRecord(ctx context.Context, cfg CloudflareConfig, recordType, host, content string, proxied bool) error {
	recordType = strings.ToUpper(strings.TrimSpace(recordType))
	existing, err := listCloudflareDNSRecords(ctx, cfg, host)
	if err != nil {
		return err
	}
	plan := planCloudflareRecord(existing, recordType, content, proxied)
	for _, id := range plan.DeleteIDs {
		if err := deleteCloudflareDNSRecord(ctx, cfg, id); err != nil {
			return err
		}
	}
	switch {
	case plan.UpdateID != "":
		return putCloudflareDNSRecord(ctx, cfg, plan.UpdateID, recordType, host, content, proxied)
	case plan.Create:
		return createCloudflareDNSRecord(ctx, cfg, recordType, host, content, proxied)
	default:
		return nil
	}
}

// applyCloudflareDNS upserts A/AAAA records pointing every host at the server IP(s) using
// the platform Cloudflare token. proxied selects orange-cloud (CDN/proxy) vs grey-cloud
// (DNS-only). Hosts whose zone is not in the account are reported as skipped rather than
// failing the batch.
func applyCloudflareDNS(ctx context.Context, token, ipv4, ipv6 string, hosts []string, proxied bool) ([]cloudflareDNSResult, error) {
	zoneCache := map[string]cloudflareZone{}
	seen := map[string]bool{}
	var results []cloudflareDNSResult
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		zone, err := findCloudflareZoneForHost(ctx, token, host, zoneCache)
		if err != nil {
			return nil, err
		}
		if zone.ID == "" {
			results = append(results, cloudflareDNSResult{Host: host, Msg: "未找到对应 Cloudflare Zone（确认 Token 有 Zone:Read 权限并覆盖该域名）"})
			continue
		}
		cfg := CloudflareConfig{APIToken: token, ZoneID: zone.ID}
		var errs []string
		if ipv4 != "" {
			if err := upsertCloudflareRecord(ctx, cfg, "A", host, ipv4, proxied); err != nil {
				errs = append(errs, "A "+err.Error())
			}
		}
		if ipv6 != "" {
			if err := upsertCloudflareRecord(ctx, cfg, "AAAA", host, ipv6, proxied); err != nil {
				errs = append(errs, "AAAA "+err.Error())
			}
		}
		if len(errs) > 0 {
			results = append(results, cloudflareDNSResult{Host: host, Msg: strings.Join(errs, "; ")})
		} else {
			results = append(results, cloudflareDNSResult{Host: host, OK: true, Msg: zone.Name})
		}
	}
	return results, nil
}

// findCloudflareZoneForHost resolves the Cloudflare zone for host by querying each
// candidate zone name (host, then each parent domain) via /zones?name=X. This is an
// exact per-zone lookup — unaffected by account pagination or zone-scoped tokens,
// unlike listing all zones. Results are cached per candidate name across hosts.
// Empty zone ID (no error) = host's zone is not in the token's account.
func findCloudflareZoneForHost(ctx context.Context, token, host string, cache map[string]cloudflareZone) (cloudflareZone, error) {
	for _, cand := range cloudflareZoneNameCandidates(host) {
		if z, ok := cache[cand]; ok {
			if z.ID != "" {
				return z, nil
			}
			continue
		}
		zones, err := listCloudflareZonesByName(ctx, token, cand)
		if err != nil {
			return cloudflareZone{}, err
		}
		var found cloudflareZone
		for _, z := range zones {
			if sameCloudflareDNSName(z.Name, cand) {
				found = z
				break
			}
		}
		cache[cand] = found
		if found.ID != "" {
			return found, nil
		}
	}
	return cloudflareZone{}, nil
}

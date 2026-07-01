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
)

// cloudflareDNSResult reports one host's DNS auto-config outcome.
type cloudflareDNSResult struct {
	Host string
	OK   bool
	Msg  string
}

// dnsAction is how a desired record reconciles against the existing ones.
type dnsAction struct {
	Op       string // "create" | "update" | "skip"
	RecordID string
}

// chooseDNSAction decides whether to create, update, or skip a record of the given
// type so that it points at content.
func chooseDNSAction(existing []cloudflareDNSRecord, recordType, content string) dnsAction {
	recordType = strings.ToUpper(strings.TrimSpace(recordType))
	content = strings.TrimSpace(content)
	for _, rec := range existing {
		if strings.ToUpper(strings.TrimSpace(rec.Type)) != recordType {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(rec.Content), content) {
			return dnsAction{Op: "skip", RecordID: rec.ID}
		}
		return dnsAction{Op: "update", RecordID: rec.ID}
	}
	return dnsAction{Op: "create"}
}

// upsertCloudflareRecord points host's recordType (A/AAAA) at content, grey-cloud.
func upsertCloudflareRecord(ctx context.Context, cfg CloudflareConfig, recordType, host, content string) error {
	existing, err := listCloudflareDNSRecords(ctx, cfg, host)
	if err != nil {
		return err
	}
	switch act := chooseDNSAction(existing, recordType, content); act.Op {
	case "skip":
		return nil
	case "update":
		return putCloudflareDNSRecord(ctx, cfg, act.RecordID, recordType, host, content, false)
	default:
		return createCloudflareDNSRecord(ctx, cfg, recordType, host, content, false)
	}
}

// applyCloudflareDNS upserts grey-cloud (DNS-only) A/AAAA records pointing every host
// at the server IP(s) using the platform Cloudflare token. Hosts whose zone is not in
// the account are reported as skipped rather than failing the batch.
func applyCloudflareDNS(ctx context.Context, token, ipv4, ipv6 string, hosts []string) ([]cloudflareDNSResult, error) {
	zones, err := listCloudflareZones(ctx, token)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var results []cloudflareDNSResult
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		zone := matchCloudflareZone(host, zones)
		if zone.ID == "" {
			results = append(results, cloudflareDNSResult{Host: host, Msg: "不在已授权的 Cloudflare 账号，请手动配置 DNS"})
			continue
		}
		cfg := CloudflareConfig{APIToken: token, ZoneID: zone.ID}
		var errs []string
		if ipv4 != "" {
			if err := upsertCloudflareRecord(ctx, cfg, "A", host, ipv4); err != nil {
				errs = append(errs, "A "+err.Error())
			}
		}
		if ipv6 != "" {
			if err := upsertCloudflareRecord(ctx, cfg, "AAAA", host, ipv6); err != nil {
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

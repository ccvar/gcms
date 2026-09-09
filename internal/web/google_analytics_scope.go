package web

import (
	"errors"
	"strings"

	"cms.ccvar.com/internal/platform"
)

// A binding identifies a GA data stream independently of a site's deployment host.
// Older manually configured bindings have no stream, so retain their own hostname
// filter. Never broaden a malformed binding or an empty result to the whole property.
type googleAnalyticsReportScope struct {
	DataStream string
	Hostnames  []string
}

func (s *Server) googleAnalyticsScopeForSite(in *platform.SiteGoogleIntegration) googleAnalyticsReportScope {
	if in == nil {
		return googleAnalyticsReportScope{}
	}
	return googleAnalyticsReportScope{
		DataStream: strings.TrimSpace(in.DataStream),
		Hostnames:  s.googleAnalyticsHostnamesForSite(in.SiteID),
	}
}

func (scope googleAnalyticsReportScope) streamID(property string) string {
	parts := strings.Split(strings.TrimSpace(scope.DataStream), "/")
	if len(parts) != 4 || parts[0]+"/"+parts[1] != normalizeGoogleAnalyticsPropertyName(property) || parts[2] != "dataStreams" || parts[3] == "" {
		return ""
	}
	for _, ch := range parts[3] {
		if ch < '0' || ch > '9' {
			return ""
		}
	}
	if strings.Trim(parts[3], "0") == "" {
		return ""
	}
	return parts[3]
}

func (scope googleAnalyticsReportScope) kind() string {
	if scope.DataStream != "" {
		return "stream"
	}
	return "host"
}

func (scope googleAnalyticsReportScope) host() string {
	return firstGoogleAnalyticsHostname(scope.Hostnames)
}

func (scope googleAnalyticsReportScope) cacheKey() string {
	if scope.DataStream != "" {
		return "v2:stream:" + scope.DataStream
	}
	return "v2:host:" + strings.Join(scope.Hostnames, ",")
}

func (scope googleAnalyticsReportScope) matches(in *platform.SiteGoogleIntegration, summary *platform.SiteGoogleAnalyticsSummary) bool {
	return in != nil && summary != nil && summary.ScopeKey == scope.cacheKey() &&
		summary.Property == in.Property && summary.MeasurementID == in.MeasurementID
}

func (scope googleAnalyticsReportScope) apply(body map[string]any, property string) error {
	if scope.DataStream != "" {
		id := scope.streamID(property)
		if id == "" {
			return errors.New("GA4 数据流与属性不匹配或格式无效，请重新选择该站点的数据流")
		}
		body["dimensionFilter"] = map[string]any{
			"filter": map[string]any{
				"fieldName":    "streamId",
				"stringFilter": map[string]any{"matchType": "EXACT", "value": id},
			},
		}
		return nil
	}
	applyGoogleAnalyticsHostnameFilter(body, scope.Hostnames)
	if _, ok := body["dimensionFilter"]; !ok {
		return errors.New("GA4 未绑定数据流，且站点没有正式域名，无法确定统计范围")
	}
	return nil
}

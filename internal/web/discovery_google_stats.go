package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"cms.ccvar.com/internal/platform"
)

const discoveryGoogleSummaryWorkers = 6

// 发现接口默认只读取已缓存的摘要；Pilot 在用户主动保存数据范围或点击刷新时，
// 会显式请求重新抓取。这样普通的站点发现和后台任务不会反复消耗 Google 配额。
type discoveryStatsRefreshReport struct {
	Requested bool `json:"requested"`
	Refreshed int  `json:"refreshed"`
	Failed    int  `json:"failed"`
}

type discoveryGoogleSummaryJob struct {
	integration platform.SiteGoogleIntegration
	token       string
	tokenErr    error
}

type discoveryGoogleSummaryResult struct {
	job       discoveryGoogleSummaryJob
	analytics *googleAnalyticsSummaryMetrics
	search    *googleSearchConsoleSummaryMetrics
	err       error
	fetchedAt time.Time
}

type discoveryGoogleTokenResult struct {
	token string
	err   error
}

// 独立变量让发现接口的刷新路径可在测试中替换为确定性数据，而不访问 Google。
var (
	discoveryGoogleAnalyticsSummaryFetch = googleAnalyticsSummary
	discoveryGoogleSearchSummaryFetch    = googleSearchConsoleSummary
)

// refreshDiscoveryGoogleSummaries 按当前全局数据范围刷新当前密钥可管理站点的
// GA/GSC 摘要。Google 请求并发执行，SQLite 写入集中串行完成，避免多站点刷新过慢，
// 也避免并发写摘要时出现锁竞争。
func (s *Server) refreshDiscoveryGoogleSummaries(ctx context.Context, r *http.Request, manageableIDs map[int64]bool) discoveryStatsRefreshReport {
	report := discoveryStatsRefreshReport{Requested: true}
	if s == nil || s.platform == nil {
		return report
	}
	integrations, err := s.platform.SiteGoogleIntegrations()
	if err != nil {
		report.Failed = 1
		return report
	}

	jobs := make([]discoveryGoogleSummaryJob, 0, len(integrations)*2)
	for siteID, services := range integrations {
		if !manageableIDs[siteID] {
			continue
		}
		for _, service := range []string{platform.GoogleServiceAnalytics, platform.GoogleServiceSearchConsole} {
			integration := services[service]
			if integration == nil || !integration.Enabled {
				continue
			}
			jobs = append(jobs, discoveryGoogleSummaryJob{integration: *integration})
		}
	}
	if len(jobs) == 0 {
		return report
	}

	// 同一授权账号通常被多个站点共用。访问令牌只解析/刷新一次，再分发给各摘要任务。
	tokens := make(map[string]discoveryGoogleTokenResult)
	for i := range jobs {
		job := &jobs[i]
		in := &job.integration
		accountID := strings.TrimSpace(in.GoogleAccountID)
		key := in.Service + "\x00" + accountID
		if cached, ok := tokens[key]; ok {
			job.token, job.tokenErr = cached.token, cached.err
			continue
		}
		result := discoveryGoogleTokenResult{}
		if accountID == "" {
			result.err = errors.New("未选择 Google 授权账号")
		} else if account, ok, accountErr := s.platform.GoogleAccount(in.Service, accountID); accountErr != nil {
			result.err = accountErr
		} else if !ok || account == nil {
			result.err = errors.New("没有找到 Google 授权账号")
		} else {
			result.token, result.err = s.googleAccessToken(ctx, r, account)
		}
		tokens[key] = result
		job.token, job.tokenErr = result.token, result.err
	}

	dataRange := s.googleDataRange()
	jobsCh := make(chan discoveryGoogleSummaryJob)
	resultsCh := make(chan discoveryGoogleSummaryResult, len(jobs))
	workers := discoveryGoogleSummaryWorkers
	if len(jobs) < workers {
		workers = len(jobs)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobsCh {
				result := discoveryGoogleSummaryResult{job: job, fetchedAt: time.Now()}
				in := &job.integration
				if job.tokenErr != nil {
					result.err = job.tokenErr
				} else {
					switch in.Service {
					case platform.GoogleServiceAnalytics:
						if strings.TrimSpace(in.Property) == "" || strings.TrimSpace(in.MeasurementID) == "" {
							result.err = errors.New("Google Analytics 配置不完整")
						} else {
							metrics, fetchErr := discoveryGoogleAnalyticsSummaryFetch(ctx, job.token, in.Property, dataRange)
							result.analytics, result.err = &metrics, fetchErr
						}
					case platform.GoogleServiceSearchConsole:
						if strings.TrimSpace(in.Property) == "" {
							result.err = errors.New("Google Search Console 配置不完整")
						} else {
							metrics, fetchErr := discoveryGoogleSearchSummaryFetch(ctx, job.token, in.Property, dataRange)
							result.search, result.err = &metrics, fetchErr
						}
					default:
						result.err = errors.New("不支持的 Google 服务")
					}
				}
				resultsCh <- result
			}
		}()
	}
	go func() {
		for _, job := range jobs {
			jobsCh <- job
		}
		close(jobsCh)
		wg.Wait()
		close(resultsCh)
	}()

	rangeKey := googleDataRangeKeyValue(dataRange)
	for result := range resultsCh {
		in := &result.job.integration
		message := ""
		if result.err != nil {
			message = strings.TrimSpace(result.err.Error())
		}
		var storeErr error
		switch in.Service {
		case platform.GoogleServiceAnalytics:
			summary := &platform.SiteGoogleAnalyticsSummary{
				SiteID: in.SiteID, Property: in.Property, MeasurementID: in.MeasurementID,
				RangeKey: rangeKey, Status: platform.GoogleAnalyticsSummaryStatusError,
				ErrorMessage: message, FetchedAt: result.fetchedAt,
			}
			if result.err == nil && result.analytics != nil {
				summary.ActiveUsers7D = result.analytics.ActiveUsers7D
				summary.Sessions7D = result.analytics.Sessions7D
				summary.ActiveUsers = result.analytics.ActiveUsers7D
				summary.Sessions = result.analytics.Sessions7D
				summary.Status = platform.GoogleAnalyticsSummaryStatusOK
			}
			storeErr = s.platform.UpsertSiteGoogleAnalyticsSummary(summary)
		case platform.GoogleServiceSearchConsole:
			summary := &platform.SiteGoogleSearchConsoleSummary{
				SiteID: in.SiteID, Property: in.Property, RangeKey: rangeKey,
				Status:       platform.GoogleSearchConsoleSummaryStatusError,
				ErrorMessage: message, FetchedAt: result.fetchedAt,
			}
			if result.err == nil && result.search != nil {
				summary.Clicks7D = result.search.Clicks7D
				summary.Impressions7D = result.search.Impressions7D
				summary.CTR7D = result.search.CTR7D
				summary.Position7D = result.search.Position7D
				summary.Clicks = result.search.Clicks7D
				summary.Impressions = result.search.Impressions7D
				summary.CTR = result.search.CTR7D
				summary.Position = result.search.Position7D
				summary.Status = platform.GoogleSearchConsoleSummaryStatusOK
			}
			storeErr = s.platform.UpsertSiteGoogleSearchConsoleSummary(summary)
		}
		if result.err != nil || storeErr != nil {
			report.Failed++
		} else {
			report.Refreshed++
		}
	}
	return report
}

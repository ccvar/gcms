package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/version"
)

type UpdateInfo struct {
	Current         version.Info
	Channel         string
	LatestTag       string
	LatestName      string
	LatestURL       string
	LatestBody      string
	PublishedAt     string
	ManifestURL     string
	AssetName       string
	AssetURL        string
	ChecksumURL     string
	SHA256          string
	AssetSize       int64
	UpdateAvailable bool
	CheckedAt       time.Time
	Error           string
}

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	HTMLURL     string        `json:"html_url"`
	Body        string        `json:"body"`
	PublishedAt time.Time     `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type releaseManifest struct {
	Schema      int             `json:"schema"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	ReleaseRepo string          `json:"release_repo"`
	ReleaseURL  string          `json:"release_url"`
	PublishedAt string          `json:"published_at"`
	Notes       string          `json:"notes"`
	ChecksumURL string          `json:"checksum_url"`
	Assets      []manifestAsset `json:"assets"`
}

type manifestAsset struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func checkLatestRelease(ctx context.Context) *UpdateInfo {
	return checkLatestReleaseForChannel(ctx, updateChannelStable)
}

func checkLatestReleaseForChannel(ctx context.Context, channel string) *UpdateInfo {
	channel = normalizeUpdateChannel(channel)
	info := currentUpdateInfoForChannel(channel)
	cur := info.Current
	manifestURL := updateManifestURLForChannel(cur, channel)
	if err := fillUpdateFromManifest(ctx, info, manifestURL); err == nil {
		return info
	} else {
		info.Error = "更新清单：" + err.Error()
	}
	// Preview 必须故障关闭：私有/预览清单不可达时不能回退到 Stable，
	// 否则页面展示与真正升级会跨通道，甚至把预览实例指向旧稳定版本。
	if channel == updateChannelPreview {
		return info
	}
	if err := fillUpdateFromGitHub(ctx, info); err != nil {
		info.Error = info.Error + "；GitHub API：" + err.Error()
		return info
	}
	info.Error = ""
	return info
}

func currentUpdateInfo() *UpdateInfo {
	return currentUpdateInfoForChannel(updateChannelStable)
}

func currentUpdateInfoForChannel(channel string) *UpdateInfo {
	return &UpdateInfo{
		Current:   version.Current(),
		Channel:   normalizeUpdateChannel(channel),
		CheckedAt: time.Now(),
	}
}

func updateManifestURL(cur version.Info) string {
	return updateManifestURLForChannel(cur, updateChannelStable)
}

func updateManifestURLForChannel(cur version.Info, channel string) string {
	if normalizeUpdateChannel(channel) == updateChannelPreview {
		if v := strings.TrimSpace(os.Getenv("GCMS_PREVIEW_UPDATE_URL")); v != "" {
			return v
		}
		repo := strings.TrimSpace(os.Getenv("GCMS_PREVIEW_RELEASE_REPO"))
		if repo == "" {
			repo = strings.TrimSpace(version.PreviewRepo)
		}
		if repo == "" {
			repo = "ccvar/gcms-preview-releases"
		}
		return "https://github.com/" + repo + "/releases/latest/download/manifest.json"
	}
	if v := strings.TrimSpace(os.Getenv("GCMS_UPDATE_URL")); v != "" {
		return v
	}
	return "https://github.com/" + cur.Repo + "/releases/latest/download/manifest.json"
}

func fillUpdateFromManifest(ctx context.Context, info *UpdateInfo, manifestURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return err
	}
	setUpdateHeaders(req, info.Current.Version)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var mf releaseManifest
	if err := json.NewDecoder(resp.Body).Decode(&mf); err != nil {
		return err
	}
	if strings.TrimSpace(mf.Version) == "" {
		return fmt.Errorf("manifest 缺少 version")
	}
	releaseRepo := strings.TrimSpace(mf.ReleaseRepo)
	if releaseRepo == "" {
		releaseRepo = info.Current.Repo
	}

	info.ManifestURL = resp.Request.URL.String()
	info.LatestTag = mf.Version
	info.LatestName = mf.Name
	info.LatestURL = mf.ReleaseURL
	if info.LatestURL == "" {
		info.LatestURL = releaseTagURL(releaseRepo, mf.Version)
	}
	info.LatestBody = strings.TrimSpace(mf.Notes)
	info.PublishedAt = formatManifestTime(mf.PublishedAt)
	info.ChecksumURL = mf.ChecksumURL
	if info.ChecksumURL == "" {
		info.ChecksumURL = releaseDownloadURL(releaseRepo, mf.Version, "checksums.txt")
	}
	info.UpdateAvailable = versionGreater(mf.Version, info.Current.Version)

	for _, a := range mf.Assets {
		if a.OS != info.Current.GOOS || a.Arch != info.Current.GOARCH {
			continue
		}
		info.AssetName = a.Name
		info.AssetURL = a.URL
		if info.AssetURL == "" {
			info.AssetURL = releaseDownloadURL(releaseRepo, mf.Version, a.Name)
		}
		info.SHA256 = a.SHA256
		info.AssetSize = a.Size
		break
	}
	return nil
}

func fillUpdateFromGitHub(ctx context.Context, info *UpdateInfo) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+info.Current.Repo+"/releases/latest", nil)
	if err != nil {
		return err
	}
	setUpdateHeaders(req, info.Current.Version)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return err
	}
	info.LatestTag = rel.TagName
	info.LatestName = rel.Name
	info.LatestURL = rel.HTMLURL
	info.LatestBody = strings.TrimSpace(rel.Body)
	if rel.PublishedAt.IsZero() {
		info.PublishedAt = ""
	} else {
		info.PublishedAt = rel.PublishedAt.Local().Format("2006-01-02 15:04")
	}
	info.UpdateAvailable = versionGreater(rel.TagName, info.Current.Version)

	target := "-" + info.Current.GOOS + "-" + info.Current.GOARCH
	suffix := version.AssetSuffix()
	for _, a := range rel.Assets {
		name := strings.ToLower(a.Name)
		switch {
		case name == "manifest.json":
			info.ManifestURL = a.BrowserDownloadURL
		case strings.Contains(name, "checksum") || strings.Contains(name, "sha256"):
			info.ChecksumURL = a.BrowserDownloadURL
		case strings.Contains(name, target) && strings.HasSuffix(name, suffix):
			info.AssetName = a.Name
			info.AssetURL = a.BrowserDownloadURL
		}
	}
	return nil
}

func setUpdateHeaders(req *http.Request, currentVersion string) {
	req.Header.Set("Accept", "application/json, application/vnd.github+json")
	req.Header.Set("User-Agent", "gcms-update-checker/"+currentVersion)
	if tok := strings.TrimSpace(os.Getenv("GCMS_UPDATE_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	} else if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

func releaseTagURL(repo, tag string) string {
	return "https://github.com/" + repo + "/releases/tag/" + url.PathEscape(tag)
}

func releaseDownloadURL(repo, tag, name string) string {
	return "https://github.com/" + repo + "/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(name)
}

func formatManifestTime(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.Local().Format("2006-01-02 15:04")
	}
	return v
}

func versionGreater(latest, current string) bool {
	if current == "" || current == "dev" {
		return false
	}
	la, lok := parseSemVersion(latest)
	ca, cok := parseSemVersion(current)
	if !lok || !cok {
		return latest != current
	}
	return compareSemVersion(la, ca) > 0
}

type semVersion struct {
	core       [3]int
	prerelease []string
}

func parseSemVersion(raw string) (semVersion, bool) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	raw = strings.SplitN(raw, "+", 2)[0]
	main, pre, _ := strings.Cut(raw, "-")
	parts := strings.Split(main, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return semVersion{}, false
	}
	var out semVersion
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semVersion{}, false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return semVersion{}, false
		}
		out.core[i] = n
	}
	if pre == "" {
		return out, true
	}
	out.prerelease = strings.Split(pre, ".")
	for _, identifier := range out.prerelease {
		if identifier == "" {
			return semVersion{}, false
		}
		for _, r := range identifier {
			if !(r >= 'a' && r <= 'z') &&
				!(r >= 'A' && r <= 'Z') &&
				!(r >= '0' && r <= '9') &&
				r != '-' {
				return semVersion{}, false
			}
		}
		if _, err := strconv.Atoi(identifier); err == nil &&
			len(identifier) > 1 && identifier[0] == '0' {
			return semVersion{}, false
		}
	}
	return out, true
}

func compareSemVersion(a, b semVersion) int {
	for i := range a.core {
		if a.core[i] < b.core[i] {
			return -1
		}
		if a.core[i] > b.core[i] {
			return 1
		}
	}
	if len(a.prerelease) == 0 && len(b.prerelease) == 0 {
		return 0
	}
	if len(a.prerelease) == 0 {
		return 1
	}
	if len(b.prerelease) == 0 {
		return -1
	}
	for i := 0; i < len(a.prerelease) && i < len(b.prerelease); i++ {
		left, right := a.prerelease[i], b.prerelease[i]
		if left == right {
			continue
		}
		ln, lerr := strconv.Atoi(left)
		rn, rerr := strconv.Atoi(right)
		switch {
		case lerr == nil && rerr == nil:
			if ln < rn {
				return -1
			}
			return 1
		case lerr == nil:
			return -1
		case rerr == nil:
			return 1
		case left < right:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(a.prerelease) < len(b.prerelease):
		return -1
	case len(a.prerelease) > len(b.prerelease):
		return 1
	default:
		return 0
	}
}

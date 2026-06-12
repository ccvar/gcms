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
	"unicode"

	"cms.ccvar.com/internal/version"
)

type UpdateInfo struct {
	Current         version.Info
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
	cur := version.Current()
	info := &UpdateInfo{Current: cur, CheckedAt: time.Now()}
	manifestURL := updateManifestURL(cur)
	if err := fillUpdateFromManifest(ctx, info, manifestURL); err == nil {
		return info
	} else {
		info.Error = "更新清单：" + err.Error()
	}
	if err := fillUpdateFromGitHub(ctx, info); err != nil {
		info.Error = info.Error + "；GitHub API：" + err.Error()
		return info
	}
	info.Error = ""
	return info
}

func updateManifestURL(cur version.Info) string {
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

	info.ManifestURL = resp.Request.URL.String()
	info.LatestTag = mf.Version
	info.LatestName = mf.Name
	info.LatestURL = mf.ReleaseURL
	if info.LatestURL == "" {
		info.LatestURL = releaseTagURL(info.Current.Repo, mf.Version)
	}
	info.LatestBody = strings.TrimSpace(mf.Notes)
	info.PublishedAt = formatManifestTime(mf.PublishedAt)
	info.ChecksumURL = mf.ChecksumURL
	if info.ChecksumURL == "" {
		info.ChecksumURL = releaseDownloadURL(info.Current.Repo, mf.Version, "checksums.txt")
	}
	info.UpdateAvailable = versionGreater(mf.Version, info.Current.Version)

	for _, a := range mf.Assets {
		if a.OS != info.Current.GOOS || a.Arch != info.Current.GOARCH {
			continue
		}
		info.AssetName = a.Name
		info.AssetURL = a.URL
		if info.AssetURL == "" {
			info.AssetURL = releaseDownloadURL(info.Current.Repo, mf.Version, a.Name)
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
	la, lok := versionParts(latest)
	ca, cok := versionParts(current)
	if !lok || !cok {
		return latest != current
	}
	for i := 0; i < len(la) || i < len(ca); i++ {
		var l, c int
		if i < len(la) {
			l = la[i]
		}
		if i < len(ca) {
			c = ca[i]
		}
		if l != c {
			return l > c
		}
	}
	return false
}

func versionParts(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var parts []int
	for _, s := range strings.FieldsFunc(v, func(r rune) bool {
		return r == '.' || r == '-' || r == '_' || unicode.IsSpace(r)
	}) {
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			break
		}
		parts = append(parts, n)
	}
	return parts, len(parts) > 0
}

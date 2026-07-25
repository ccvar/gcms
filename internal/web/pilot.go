package web

import (
	"encoding/json"
	"strings"
)

const (
	pilotWorkflowSettingKey  = "pilot.workflow"
	pilotReleaseSettingKey   = "pilot.release"
	pilotDownloadsSettingKey = "pilot.downloads"
	pilotTrustSettingKey     = "pilot.trust"
	pilotGallerySettingKey   = "pilot.gallery"
)

const (
	maxPilotWorkflow  = 6
	maxPilotDownloads = 8
	maxPilotTrust     = 6
	maxPilotGallery   = 6
)

type PilotStep struct {
	Title string `json:"title"`
	Note  string `json:"note"`
}

type PilotRelease struct {
	Version string `json:"version"`
	Date    string `json:"date"`
	Channel string `json:"channel"`
	URL     string `json:"url"`
}

type PilotDownload struct {
	Label    string `json:"label"`
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	URL      string `json:"url"`
	Meta     string `json:"meta"`
}

type PilotTrustPoint struct {
	Title string `json:"title"`
	Note  string `json:"note"`
}

type PilotSection[T any] struct {
	Title string `json:"title"`
	Note  string `json:"note"`
	Items []T    `json:"items"`
}

// Pilot 内置文案只在站点未配置对应数据槽时使用。所有默认值都经过 i18n，
// 模板只消费结构化数据，不承载产品文案或平台信息。
func defaultPilotWorkflow(t func(string) string) PilotSection[PilotStep] {
	return PilotSection[PilotStep]{
		Title: t("pilot.workflow.title"),
		Note:  t("pilot.workflow.note"),
		Items: []PilotStep{
			{Title: t("pilot.workflow.step_1.title"), Note: t("pilot.workflow.step_1.note")},
			{Title: t("pilot.workflow.step_2.title"), Note: t("pilot.workflow.step_2.note")},
			{Title: t("pilot.workflow.step_3.title"), Note: t("pilot.workflow.step_3.note")},
			{Title: t("pilot.workflow.step_4.title"), Note: t("pilot.workflow.step_4.note")},
		},
	}
}

func defaultPilotRelease(t func(string) string) PilotRelease {
	return PilotRelease{
		Version: t("pilot.release.version"),
		Channel: t("pilot.release.channel"),
	}
}

func defaultPilotDownloads(t func(string) string) PilotSection[PilotDownload] {
	return PilotSection[PilotDownload]{
		Title: t("pilot.downloads.title"),
		Note:  t("pilot.downloads.note"),
		Items: []PilotDownload{
			{Label: t("pilot.downloads.macos.label"), Platform: t("pilot.downloads.macos.platform"), Arch: t("pilot.downloads.macos.arch")},
			{Label: t("pilot.downloads.windows.label"), Platform: t("pilot.downloads.windows.platform"), Arch: t("pilot.downloads.windows.arch")},
		},
	}
}

func defaultPilotTrust(t func(string) string) PilotSection[PilotTrustPoint] {
	return PilotSection[PilotTrustPoint]{
		Title: t("pilot.trust.title"),
		Note:  t("pilot.trust.note"),
		Items: []PilotTrustPoint{
			{Title: t("pilot.trust.item_1.title"), Note: t("pilot.trust.item_1.note")},
			{Title: t("pilot.trust.item_2.title"), Note: t("pilot.trust.item_2.note")},
			{Title: t("pilot.trust.item_3.title"), Note: t("pilot.trust.item_3.note")},
		},
	}
}

func cleanPilotURL(v string) string {
	v = strings.TrimSpace(v)
	lower := strings.ToLower(v)
	if strings.HasPrefix(v, "/") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		return v
	}
	return ""
}

func parsePilotWorkflow(raw string) PilotSection[PilotStep] {
	var v PilotSection[PilotStep]
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &v) != nil {
		return PilotSection[PilotStep]{}
	}
	v.Title, v.Note = strings.TrimSpace(v.Title), strings.TrimSpace(v.Note)
	out := make([]PilotStep, 0, maxPilotWorkflow)
	for _, item := range v.Items {
		item.Title, item.Note = strings.TrimSpace(item.Title), strings.TrimSpace(item.Note)
		if item.Title == "" {
			continue
		}
		out = append(out, item)
		if len(out) == maxPilotWorkflow {
			break
		}
	}
	v.Items = out
	return v
}

func parsePilotRelease(raw string) PilotRelease {
	var v PilotRelease
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &v) != nil {
		return PilotRelease{}
	}
	v.Version = strings.TrimSpace(v.Version)
	v.Date = strings.TrimSpace(v.Date)
	v.Channel = strings.TrimSpace(v.Channel)
	v.URL = cleanPilotURL(v.URL)
	return v
}

func parsePilotDownloads(raw string) PilotSection[PilotDownload] {
	var v PilotSection[PilotDownload]
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &v) != nil {
		return PilotSection[PilotDownload]{}
	}
	v.Title, v.Note = strings.TrimSpace(v.Title), strings.TrimSpace(v.Note)
	out := make([]PilotDownload, 0, maxPilotDownloads)
	for _, item := range v.Items {
		item.Label = strings.TrimSpace(item.Label)
		item.Platform = strings.TrimSpace(item.Platform)
		item.Arch = strings.TrimSpace(item.Arch)
		item.Meta = strings.TrimSpace(item.Meta)
		item.URL = cleanPilotURL(item.URL)
		if item.Label == "" || item.URL == "" {
			continue
		}
		out = append(out, item)
		if len(out) == maxPilotDownloads {
			break
		}
	}
	v.Items = out
	return v
}

func parsePilotTrust(raw string) PilotSection[PilotTrustPoint] {
	var v PilotSection[PilotTrustPoint]
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &v) != nil {
		return PilotSection[PilotTrustPoint]{}
	}
	v.Title, v.Note = strings.TrimSpace(v.Title), strings.TrimSpace(v.Note)
	out := make([]PilotTrustPoint, 0, maxPilotTrust)
	for _, item := range v.Items {
		item.Title, item.Note = strings.TrimSpace(item.Title), strings.TrimSpace(item.Note)
		if item.Title == "" {
			continue
		}
		out = append(out, item)
		if len(out) == maxPilotTrust {
			break
		}
	}
	v.Items = out
	return v
}

func parsePilotGallery(raw string) []string {
	var values []string
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &values) != nil {
		return nil
	}
	out := make([]string, 0, maxPilotGallery)
	for _, value := range values {
		if value = cleanPilotURL(value); value != "" {
			out = append(out, value)
		}
		if len(out) == maxPilotGallery {
			break
		}
	}
	return out
}

func (s *Server) fillPilotHome(v *View, lang string) {
	if v.Layout != "pilot-flight-deck" {
		return
	}
	tr := s.i18n.Tr(lang, s.defaultLang())
	v.PilotWorkflow = parsePilotWorkflow(s.localizedSetting(pilotWorkflowSettingKey, lang, ""))
	v.PilotRelease = parsePilotRelease(s.localizedSetting(pilotReleaseSettingKey, lang, ""))
	v.PilotDownloads = parsePilotDownloads(s.localizedSetting(pilotDownloadsSettingKey, lang, ""))
	v.PilotTrust = parsePilotTrust(s.localizedSetting(pilotTrustSettingKey, lang, ""))
	v.PilotGallery = parsePilotGallery(s.store.Setting(pilotGallerySettingKey))
	if v.PilotWorkflow.Title == "" && len(v.PilotWorkflow.Items) == 0 {
		v.PilotWorkflow = defaultPilotWorkflow(tr.T)
	}
	if v.PilotRelease.Version == "" && v.PilotRelease.Date == "" && v.PilotRelease.Channel == "" {
		v.PilotRelease = defaultPilotRelease(tr.T)
	}
	if v.PilotDownloads.Title == "" && len(v.PilotDownloads.Items) == 0 {
		// 下载地址必须由站点填写，默认数据只提供标题与设备文案，避免生成失效链接。
		def := defaultPilotDownloads(tr.T)
		v.PilotDownloads.Title, v.PilotDownloads.Note = def.Title, def.Note
	}
	if v.PilotTrust.Title == "" && len(v.PilotTrust.Items) == 0 {
		v.PilotTrust = defaultPilotTrust(tr.T)
	}
	if len(v.PilotGallery) == 0 {
		// 图集没有虚构截图；若站点已配置 Hero 图片，则复用该真实素材作为首张默认图。
		// 直接读语种化设置，兼容主题卡片预览（该预览不会装配后台 Settings 视图）。
		if hero := cleanPilotURL(s.localizedSetting("hero.image", lang, "")); hero != "" {
			v.PilotGallery = []string{hero}
		}
	}
}

func normalizePilotOption(key, raw string) string {
	var v any
	switch key {
	case pilotWorkflowSettingKey:
		v = parsePilotWorkflow(raw)
	case pilotReleaseSettingKey:
		v = parsePilotRelease(raw)
	case pilotDownloadsSettingKey:
		v = parsePilotDownloads(raw)
	case pilotTrustSettingKey:
		v = parsePilotTrust(raw)
	case pilotGallerySettingKey:
		gallery := parsePilotGallery(raw)
		if len(gallery) == 0 {
			return ""
		}
		v = gallery
	default:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

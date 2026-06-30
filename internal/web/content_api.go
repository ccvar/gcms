package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"cms.ccvar.com/internal/store"
)

// 「扩展」内容类型的自动化 API 支撑：集合解析、自定义字段读写、以及给 AI 的类型自省接口。

// applyExtraFields 把 API 请求体里的 fields 合并进 Post.Extra：仅接受该类型 schema 中的键，
// 支持部分更新（未提供的键保留，显式 null 删除）。值的类型信任调用方（数字/数组/字符串）。
func (s *Server) applyExtraFields(p *store.Post, kind string, fields map[string]any) {
	ct := s.lookupType(kind)
	if ct == nil || ct.Builtin || len(ct.Fields) == 0 || fields == nil {
		return
	}
	m := map[string]any{}
	if strings.TrimSpace(p.Extra) != "" {
		_ = json.Unmarshal([]byte(p.Extra), &m)
	}
	for _, f := range ct.Fields {
		v, ok := fields[f.Key]
		if !ok {
			continue
		}
		if v == nil {
			delete(m, f.Key)
			continue
		}
		m[f.Key] = v
	}
	if len(m) == 0 {
		p.Extra = ""
		return
	}
	if b, err := json.Marshal(m); err == nil {
		p.Extra = string(b)
	}
}

// extraToAPIMap 把 Post.Extra 反解为 API 响应里的 fields 对象（仅含该类型 schema 的键）。
func (s *Server) extraToAPIMap(kind, extra string) map[string]any {
	ct := s.lookupType(kind)
	if ct == nil || len(ct.Fields) == 0 {
		return nil
	}
	extra = strings.TrimSpace(extra)
	if extra == "" || extra == "{}" {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(extra), &raw); err != nil {
		return nil
	}
	out := map[string]any{}
	for _, f := range ct.Fields {
		if v, ok := raw[f.Key]; ok && v != nil {
			out[f.Key] = v
		}
	}
	return out
}

type apiTypeFieldDef struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Type      string   `json:"type"`
	Required  bool     `json:"required"`
	Localized bool     `json:"localized"`
	Options   []string `json:"options,omitempty"`
	Help      string   `json:"help,omitempty"`
}

type apiTypeDef struct {
	Key         string            `json:"key"`
	Name        string            `json:"name"`
	Collection  string            `json:"collection"`
	URLPrefix   string            `json:"url_prefix"`
	Searchable  bool              `json:"searchable"`
	HasCategory bool              `json:"has_category"`
	Fields      []apiTypeFieldDef `json:"fields"`
}

// apiContentTypes 是给 AI/自动化的内容类型自省接口：返回本站「已启用」的扩展内容类型
// 及其字段 schema。这份 schema 就是 AI 操作这些类型（创建/更新自定义字段）的契约。
func (s *Server) apiContentTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationToken(w, r); !ok {
		return
	}
	lang := s.defaultLang()
	out := make([]apiTypeDef, 0)
	for _, ct := range s.activeExtContentTypes() {
		def := apiTypeDef{
			Key: ct.Key, Name: ct.Name(lang), Collection: ct.URLPrefix, URLPrefix: ct.URLPrefix,
			Searchable: ct.Searchable, HasCategory: ct.HasCategory,
		}
		for _, f := range ct.Fields {
			fd := apiTypeFieldDef{
				Key: f.Key, Label: f.Label(lang), Type: string(f.Type),
				Required: f.Required, Localized: f.Localized, Help: pickLabel(f.Help, lang, ""),
			}
			for _, o := range f.Options {
				fd.Options = append(fd.Options, o.Value)
			}
			def.Fields = append(def.Fields, fd)
		}
		out = append(out, def)
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": out})
}

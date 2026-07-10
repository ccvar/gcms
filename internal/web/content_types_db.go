package web

import (
	"encoding/json"
	"strings"

	"cms.ccvar.com/internal/store"
)

// Phase 3：数据库驱动的自定义内容类型（每站独立）。
// 这里把 content_types 表里的定义解析成与代码内置类型完全相同的 *ContentType，
// 再与代码注册表合并——于是路由、后台 CRUD、搜索、导出、API 全都"免费"支持自定义类型。

// fieldJSON 是字段定义在数据库 / 表单里的 JSON 形态。
type fieldJSON struct {
	Key        string            `json:"key"`
	Label      map[string]string `json:"label"`
	Type       string            `json:"type"`
	Required   bool              `json:"required"`
	Localized  bool              `json:"localized"`
	Structural bool              `json:"structural"`
	Options    []string          `json:"options"`
	Help       map[string]string `json:"help"`
}

func parseLabelJSON(s, fallback string) map[string]string {
	s = strings.TrimSpace(s)
	if s == "" {
		return map[string]string{"zh": fallback}
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err == nil && len(m) > 0 {
		return m
	}
	// 非 JSON：当作中文名
	return map[string]string{"zh": s}
}

func parseFieldsJSON(s string) []Field {
	s = strings.TrimSpace(s)
	if s == "" || s == "[]" {
		return nil
	}
	var raw []fieldJSON
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil
	}
	out := make([]Field, 0, len(raw))
	for _, fj := range raw {
		key := strings.TrimSpace(fj.Key)
		if key == "" {
			continue
		}
		f := Field{
			Key: key, Labels: fj.Label, Type: FieldType(strings.TrimSpace(fj.Type)),
			Required: fj.Required, Localized: fj.Localized, Structural: fj.Structural, Help: fj.Help,
		}
		if f.Type == "" {
			f.Type = FieldText
		}
		for _, o := range fj.Options {
			if o = strings.TrimSpace(o); o != "" {
				f.Options = append(f.Options, FieldOption{Value: o, Labels: map[string]string{"zh": o}})
			}
		}
		out = append(out, f)
	}
	return out
}

// dbTypeToContentType 把一条数据库类型定义转成运行期 *ContentType（前缀恒等于 key）。
func dbTypeToContentType(row *store.ContentTypeRow) *ContentType {
	ct := &ContentType{
		Key:          row.Key,
		Names:        parseLabelJSON(row.Name, row.Key),
		Icon:         row.Icon,
		URLPrefix:    row.Key, // 自定义类型：URL 前缀恒等于 key，免去运行期前缀解析的歧义
		Fields:       parseFieldsJSON(row.Fields),
		HasCategory:  row.HasCategory,
		Multilingual: true,
		Searchable:   row.Searchable,
		Hierarchical: row.Hierarchical,
		Builtin:      false,
		DefaultOn:    false,
	}
	// 层级类型必须有 parent/order 结构字段（公开渲染隐藏、树构建依赖）；
	// 设计器/API 的字段输入都不含它们——在合并层统一注入，缺哪个补哪个。
	if ct.Hierarchical {
		has := map[string]bool{}
		for _, f := range ct.Fields {
			has[f.Key] = true
		}
		if !has["parent"] {
			ct.Fields = append(ct.Fields, Field{Key: "parent", Labels: map[string]string{"zh": "上级", "en": "Parent"}, Type: FieldRelation, Structural: true})
		}
		if !has["order"] {
			ct.Fields = append(ct.Fields, Field{Key: "order", Labels: map[string]string{"zh": "排序", "en": "Order"}, Type: FieldNumber, Structural: true})
		}
	}
	return ct
}

// dbExtTypes 返回本站数据库里定义的全部自定义类型（每站独立）。
func (s *Server) dbExtTypes() []*ContentType {
	rows, err := s.store.ListContentTypes()
	if err != nil || len(rows) == 0 {
		return nil
	}
	out := make([]*ContentType, 0, len(rows))
	for _, r := range rows {
		// 自定义类型的 key 不能与代码内置类型冲突；冲突时以代码为准、忽略 DB。
		if contentTypeByKey(r.Key) != nil {
			continue
		}
		out = append(out, dbTypeToContentType(r))
	}
	return out
}

// allExtTypes 返回本站「全部扩展类型」：代码内置扩展类型 + 数据库自定义类型。
func (s *Server) allExtTypes() []*ContentType {
	out := append([]*ContentType{}, extContentTypes()...)
	out = append(out, s.dbExtTypes()...)
	return out
}

// lookupType 在「代码注册表 + 本站数据库类型」中按 key 查类型。
func (s *Server) lookupType(key string) *ContentType {
	key = strings.TrimSpace(key)
	if ct := contentTypeByKey(key); ct != nil {
		return ct
	}
	for _, ct := range s.dbExtTypes() {
		if ct.Key == key {
			return ct
		}
	}
	return nil
}

// extTypeByPrefix 在代码 + 数据库扩展类型中按 URL 前缀查找。
func (s *Server) extTypeByPrefix(prefix string) *ContentType {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return nil
	}
	for _, ct := range s.allExtTypes() {
		if ct.URLPrefix == prefix {
			return ct
		}
	}
	return nil
}

// activeExtTypeByPrefix 返回「按前缀匹配且对本站启用」的扩展类型，用于动态路由分发。
func (s *Server) activeExtTypeByPrefix(prefix string) *ContentType {
	ct := s.extTypeByPrefix(prefix)
	if ct == nil || !s.contentTypeActive(ct.Key) {
		return nil
	}
	return ct
}

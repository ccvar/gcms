package store

import "time"

// ContentTypeRow 是「扩展」可视化设计器在数据库里保存的一条自定义内容类型定义（每站独立）。
// name 与 fields 以 JSON 文本保存，由上层 web 解析为运行期类型。
type ContentTypeRow struct {
	ID           int64
	Key          string
	Name         string // JSON {zh:..,en:..}（或纯文本）
	Icon         string
	URLPrefix    string
	Fields       string // JSON 数组，字段定义
	HasCategory  bool
	Searchable   bool
	Hierarchical bool
	Position     int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func scanContentType(sc interface{ Scan(...any) error }) (*ContentTypeRow, error) {
	var r ContentTypeRow
	var hasCat, searchable, hier int
	var created, updated string
	if err := sc.Scan(&r.ID, &r.Key, &r.Name, &r.Icon, &r.URLPrefix, &r.Fields,
		&hasCat, &searchable, &hier, &r.Position, &created, &updated); err != nil {
		return nil, err
	}
	r.HasCategory = hasCat != 0
	r.Searchable = searchable != 0
	r.Hierarchical = hier != 0
	r.CreatedAt, _ = time.Parse(time.RFC3339, created)
	r.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return &r, nil
}

const contentTypeCols = `id,key,name,icon,url_prefix,fields,has_category,searchable,hierarchical,position,created_at,updated_at`

// ListContentTypes 返回本站全部自定义类型定义（按 position 再按 key）。
func (s *Store) ListContentTypes() ([]*ContentTypeRow, error) {
	rows, err := s.db.Query(`SELECT ` + contentTypeCols + ` FROM content_types ORDER BY position, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ContentTypeRow
	for rows.Next() {
		r, err := scanContentType(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetContentType 按 key 取一条自定义类型定义，未找到返回 (nil, nil)。
func (s *Store) GetContentType(key string) (*ContentTypeRow, error) {
	row := s.db.QueryRow(`SELECT `+contentTypeCols+` FROM content_types WHERE key=?`, key)
	r, err := scanContentType(row)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// SaveContentType 按 key 新增或更新一条自定义类型定义。
func (s *Store) SaveContentType(r *ContentTypeRow) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Exec(`INSERT INTO content_types
		(key,name,icon,url_prefix,fields,has_category,searchable,hierarchical,position,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET
			name=excluded.name, icon=excluded.icon, url_prefix=excluded.url_prefix, fields=excluded.fields,
			has_category=excluded.has_category, searchable=excluded.searchable, hierarchical=excluded.hierarchical,
			position=excluded.position, updated_at=excluded.updated_at`,
		r.Key, r.Name, r.Icon, r.URLPrefix, r.Fields,
		boolInt(r.HasCategory), boolInt(r.Searchable), boolInt(r.Hierarchical), r.Position, now, now)
	return err
}

// DeleteContentType 删除一条自定义类型定义（不删除其下内容）。
func (s *Store) DeleteContentType(key string) error {
	_, err := s.db.Exec(`DELETE FROM content_types WHERE key=?`, key)
	return err
}

// Package store 封装 SQLite 数据访问：模型、迁移与查询。
// 使用纯 Go 驱动 modernc.org/sqlite，无需 CGO，便于交叉编译为单一二进制。
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ---------- 模型 ----------

type Category struct {
	ID          int64
	Slug        string
	Name        string
	Description string
	Position    int
	Lang        string // 语种码，如 zh / en
	TransGroup  string // 互译分组键：同一逻辑分类的各语种版本共享
	Kind        string // post | link：分类归属（文章分类 / 链接分类，相互独立）
	Count       int    // 该分类下已发布条目数（列表时填充）
}

type Post struct {
	ID         int64
	Type       string // post | page | link
	Slug       string
	Title      string
	Excerpt    string
	Content    string // Markdown 源文
	ContentLen int    // 正文字符数；列表查询不取正文时用于估算阅读时长
	MetaDesc   string
	Keywords   string
	CoverImage string
	Author     string
	Status     string // draft | published | scheduled
	Featured   bool   // 置顶（首页精选优先）
	EditorMode string // markdown | rich（记住上次编辑方式）
	Lang       string // 语种码，如 zh / en
	TransGroup string // 互译分组键：同一逻辑文章的各语种版本共享
	CategoryID sql.NullInt64
	Category   *Category
	LinkURL    string // 仅 type=link：指向的目标网址

	PublishedAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ReadingTime 估算阅读时长（分钟）。中文按约 350 字/分钟。
func (p *Post) ReadingTime() int {
	n := p.ContentLen
	if n == 0 && p.Content != "" {
		n = len([]rune(p.Content))
	}
	m := n / 350
	if m < 1 {
		m = 1
	}
	return m
}

func (p *Post) IsPublished() bool { return p.Status == "published" }

// KeywordList 把逗号分隔的关键词拆成切片，供模板渲染标签。
func (p *Post) KeywordList() []string {
	var out []string
	for _, k := range strings.Split(p.Keywords, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

type Setting struct {
	Key   string
	Value string
}

// ---------- 连接与迁移 ----------

type Store struct {
	db *sql.DB
	// Seeded 表示本次 Open 触发了空库播种（首次启动），供上层提示默认账号。
	Seeded bool
	// 默认密码校验结果缓存（bcrypt 较慢，仅当 hash 变化时重算）。
	pwMu        sync.Mutex
	pwHash      string
	pwIsDefault bool
	// 设置项读多写少，启动后缓存在内存中；后台保存设置时同步更新。
	settingsMu     sync.RWMutex
	settings       map[string]string
	settingsLoaded bool
}

func Open(path string) (*Store, error) {
	// 通过 DSN 设置 WAL、忙等待与外键约束。
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// WAL 下允许多个读连接并发；写入仍由 SQLite 串行化，连接数保持保守。
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// 新库建表：slug 不再全局唯一，而是 (lang, slug) 复合唯一，
// 以支持各语种使用各自的 slug（如 /zh/about 与 /en/about 并存）。
const schema = `
CREATE TABLE IF NOT EXISTS categories (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  slug        TEXT NOT NULL,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  position    INTEGER NOT NULL DEFAULT 0,
  lang        TEXT NOT NULL DEFAULT 'zh',
  trans_group TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL DEFAULT 'post',
  UNIQUE(lang, slug)
);

CREATE TABLE IF NOT EXISTS posts (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  type         TEXT NOT NULL DEFAULT 'post',
  slug         TEXT NOT NULL,
  title        TEXT NOT NULL,
  excerpt      TEXT NOT NULL DEFAULT '',
  content      TEXT NOT NULL DEFAULT '',
  meta_desc    TEXT NOT NULL DEFAULT '',
  keywords     TEXT NOT NULL DEFAULT '',
  cover_image  TEXT NOT NULL DEFAULT '',
  author       TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'draft',
  featured     INTEGER NOT NULL DEFAULT 0,
  editor_mode  TEXT NOT NULL DEFAULT 'markdown',
  lang         TEXT NOT NULL DEFAULT 'zh',
  trans_group  TEXT NOT NULL DEFAULT '',
  link_url     TEXT NOT NULL DEFAULT '',
  category_id  INTEGER REFERENCES categories(id) ON DELETE SET NULL,
  published_at TEXT,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  UNIQUE(lang, slug)
);

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS admin_sessions (
  token_hash   TEXT PRIMARY KEY,
  user         TEXT NOT NULL,
  csrf         TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  pw_dismissed INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("迁移失败: %w", err)
	}
	// 旧库补列（幂等）——先补简单列，再做多语种结构迁移。
	s.addColumnIfMissing("posts", "featured", "INTEGER NOT NULL DEFAULT 0")
	s.addColumnIfMissing("posts", "editor_mode", "TEXT NOT NULL DEFAULT 'markdown'")
	s.addColumnIfMissing("categories", "position", "INTEGER NOT NULL DEFAULT 0")
	// 旧库（slug 全局唯一、无 lang 列）整体重建为多语种结构。
	if err := s.rebuildForI18n(); err != nil {
		return fmt.Errorf("多语种迁移失败: %w", err)
	}
	// 「链接」内容类型新增列（幂等，须在多语种重建之后补，确保重建后的表也带上）。
	s.addColumnIfMissing("posts", "link_url", "TEXT NOT NULL DEFAULT ''")
	s.addColumnIfMissing("categories", "kind", "TEXT NOT NULL DEFAULT 'post'")
	// 索引在表结构（含 lang/trans_group）就绪后统一创建，兼容新旧库。
	if err := s.createIndexes(); err != nil {
		return fmt.Errorf("索引创建失败: %w", err)
	}
	if err := s.createSearchIndex(); err != nil {
		return fmt.Errorf("搜索索引创建失败: %w", err)
	}
	return s.seedIfEmpty()
}

// createIndexes 在 posts 表确定具备 lang/trans_group 列后统一建立索引。
func (s *Store) createIndexes() error {
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_posts_list ON posts(lang, type, status, published_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_category ON posts(category_id)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_category_list ON posts(category_id, type, status, published_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_featured ON posts(lang, type, status, featured, published_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_group ON posts(trans_group)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_due ON posts(status, published_at)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) createSearchIndex() error {
	stmts := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS post_search USING fts5(
			title, excerpt, content,
			lang UNINDEXED, type UNINDEXED, status UNINDEXED, published_at UNINDEXED,
			tokenize='trigram'
		)`,
		`CREATE TRIGGER IF NOT EXISTS posts_search_ai AFTER INSERT ON posts BEGIN
			INSERT INTO post_search(rowid,title,excerpt,content,lang,type,status,published_at)
			VALUES(new.id,new.title,new.excerpt,new.content,new.lang,new.type,new.status,new.published_at);
		END`,
		`CREATE TRIGGER IF NOT EXISTS posts_search_au AFTER UPDATE ON posts BEGIN
			DELETE FROM post_search WHERE rowid=old.id;
			INSERT INTO post_search(rowid,title,excerpt,content,lang,type,status,published_at)
			VALUES(new.id,new.title,new.excerpt,new.content,new.lang,new.type,new.status,new.published_at);
		END`,
		`CREATE TRIGGER IF NOT EXISTS posts_search_ad AFTER DELETE ON posts BEGIN
			DELETE FROM post_search WHERE rowid=old.id;
		END`,
		`INSERT OR REPLACE INTO post_search(rowid,title,excerpt,content,lang,type,status,published_at)
			SELECT id,title,excerpt,content,lang,type,status,published_at FROM posts`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// hasColumn 判断某表是否已有某列。
func (s *Store) hasColumn(table, col string) bool {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err == nil && name == col {
			return true
		}
	}
	return false
}

// addColumnIfMissing 在列不存在时 ALTER 添加（用于已存在的旧数据库平滑升级）。
func (s *Store) addColumnIfMissing(table, col, def string) {
	if s.hasColumn(table, col) {
		return
	}
	_, _ = s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col + " " + def)
}

// rebuildForI18n 把「slug 全局唯一、无 lang」的旧表重建为「(lang,slug) 复合唯一 + lang/trans_group」。
// 仅当 posts 还没有 lang 列时执行一次。已有数据全部归入默认语种 zh，trans_group 取 'zh:'||slug。
func (s *Store) rebuildForI18n() error {
	if s.hasColumn("posts", "lang") {
		return nil
	}
	// 重建期间临时关闭外键（posts 引用 categories）。PRAGMA 不能在事务内生效。
	_, _ = s.db.Exec("PRAGMA foreign_keys=OFF")
	defer s.db.Exec("PRAGMA foreign_keys=ON")

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmts := []string{
		// 分类
		`CREATE TABLE categories_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slug TEXT NOT NULL, name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '', position INTEGER NOT NULL DEFAULT 0,
			lang TEXT NOT NULL DEFAULT 'zh', trans_group TEXT NOT NULL DEFAULT '',
			UNIQUE(lang, slug))`,
		`INSERT INTO categories_new(id,slug,name,description,position,lang,trans_group)
			SELECT id,slug,name,COALESCE(description,''),COALESCE(position,0),'zh','zh:'||slug FROM categories`,
		`DROP TABLE categories`,
		`ALTER TABLE categories_new RENAME TO categories`,
		// 文章
		`CREATE TABLE posts_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL DEFAULT 'post', slug TEXT NOT NULL, title TEXT NOT NULL,
			excerpt TEXT NOT NULL DEFAULT '', content TEXT NOT NULL DEFAULT '',
			meta_desc TEXT NOT NULL DEFAULT '', keywords TEXT NOT NULL DEFAULT '',
			cover_image TEXT NOT NULL DEFAULT '', author TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'draft', featured INTEGER NOT NULL DEFAULT 0,
			editor_mode TEXT NOT NULL DEFAULT 'markdown',
			lang TEXT NOT NULL DEFAULT 'zh', trans_group TEXT NOT NULL DEFAULT '',
			category_id INTEGER REFERENCES categories(id) ON DELETE SET NULL,
			published_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			UNIQUE(lang, slug))`,
		`INSERT INTO posts_new(id,type,slug,title,excerpt,content,meta_desc,keywords,cover_image,author,status,featured,editor_mode,lang,trans_group,category_id,published_at,created_at,updated_at)
			SELECT id,type,slug,title,COALESCE(excerpt,''),COALESCE(content,''),COALESCE(meta_desc,''),COALESCE(keywords,''),
			       COALESCE(cover_image,''),COALESCE(author,''),COALESCE(status,'draft'),COALESCE(featured,0),COALESCE(editor_mode,'markdown'),
			       'zh','zh:'||slug,category_id,published_at,created_at,updated_at FROM posts`,
		`DROP TABLE posts`,
		`ALTER TABLE posts_new RENAME TO posts`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ---------- 时间辅助 ----------

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseTime(s sql.NullString) time.Time {
	if !s.Valid || s.String == "" {
		return time.Time{}
	}
	for _, f := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(f, s.String); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ---------- 后台会话 ----------

type AdminSession struct {
	User        string
	CSRF        string
	ExpiresAt   time.Time
	PwDismissed bool
}

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateAdminSession(token, user, csrf string, expiresAt time.Time) error {
	now := time.Now()
	_, _ = s.db.Exec(`DELETE FROM admin_sessions WHERE expires_at<=?`, fmtTime(now))
	_, err := s.db.Exec(`INSERT INTO admin_sessions(token_hash,user,csrf,expires_at,pw_dismissed,created_at,updated_at)
		VALUES(?,?,?,?,0,?,?)`,
		sessionTokenHash(token), user, csrf, fmtTime(expiresAt), fmtTime(now), fmtTime(now))
	return err
}

func (s *Store) GetAdminSession(token string) (AdminSession, bool, error) {
	var sess AdminSession
	var expires string
	var dismissed int
	err := s.db.QueryRow(`SELECT user,csrf,expires_at,pw_dismissed FROM admin_sessions WHERE token_hash=?`, sessionTokenHash(token)).
		Scan(&sess.User, &sess.CSRF, &expires, &dismissed)
	if err == sql.ErrNoRows {
		return AdminSession{}, false, nil
	}
	if err != nil {
		return AdminSession{}, false, err
	}
	t, err := time.Parse(time.RFC3339, expires)
	if err != nil || time.Now().After(t) {
		_ = s.DeleteAdminSession(token)
		return AdminSession{}, false, nil
	}
	sess.ExpiresAt = t
	sess.PwDismissed = dismissed == 1
	return sess, true, nil
}

func (s *Store) DeleteAdminSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM admin_sessions WHERE token_hash=?`, sessionTokenHash(token))
	return err
}

func (s *Store) DismissAdminPasswordWarning(token string) error {
	_, err := s.db.Exec(`UPDATE admin_sessions SET pw_dismissed=1,updated_at=? WHERE token_hash=?`, fmtTime(time.Now()), sessionTokenHash(token))
	return err
}

// ---------- 查询：公开站点 ----------

const postCols = `p.id,p.type,p.slug,p.title,p.excerpt,p.content,p.meta_desc,p.keywords,
	p.cover_image,p.author,p.status,p.featured,p.editor_mode,p.link_url,p.lang,p.trans_group,p.category_id,p.published_at,p.created_at,p.updated_at,
	c.id,c.slug,c.name,c.description`

const postSummaryCols = `p.id,p.type,p.slug,p.title,p.excerpt,'' AS content,p.meta_desc,p.keywords,
	p.cover_image,p.author,p.status,p.featured,p.editor_mode,p.link_url,p.lang,p.trans_group,p.category_id,p.published_at,p.created_at,p.updated_at,
	c.id,c.slug,c.name,c.description,length(p.content)`

func scanPost(sc interface{ Scan(...any) error }, hasContentLen bool) (*Post, error) {
	var p Post
	var pub, created, updated sql.NullString
	var cID sql.NullInt64
	var cSlug, cName, cDesc sql.NullString
	var featured int
	var contentLen sql.NullInt64
	dest := []any{&p.ID, &p.Type, &p.Slug, &p.Title, &p.Excerpt, &p.Content, &p.MetaDesc,
		&p.Keywords, &p.CoverImage, &p.Author, &p.Status, &featured, &p.EditorMode, &p.LinkURL, &p.Lang, &p.TransGroup,
		&p.CategoryID, &pub, &created, &updated,
		&cID, &cSlug, &cName, &cDesc}
	if hasContentLen {
		dest = append(dest, &contentLen)
	}
	err := sc.Scan(dest...)
	if err != nil {
		return nil, err
	}
	p.Featured = featured != 0
	if contentLen.Valid {
		p.ContentLen = int(contentLen.Int64)
	} else if p.Content != "" {
		p.ContentLen = len([]rune(p.Content))
	}
	p.PublishedAt = parseTime(pub)
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	if cID.Valid {
		p.Category = &Category{ID: cID.Int64, Slug: cSlug.String, Name: cName.String, Description: cDesc.String}
	}
	return &p, nil
}

func (s *Store) queryPosts(where string, args ...any) ([]*Post, error) {
	q := `SELECT ` + postCols + ` FROM posts p LEFT JOIN categories c ON c.id = p.category_id ` + where
	return s.queryPostRows(q, false, args...)
}

func (s *Store) queryPostSummaries(where string, args ...any) ([]*Post, error) {
	q := `SELECT ` + postSummaryCols + ` FROM posts p LEFT JOIN categories c ON c.id = p.category_id ` + where
	return s.queryPostRows(q, true, args...)
}

func (s *Store) queryPostRows(q string, hasContentLen bool, args ...any) ([]*Post, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Post
	for rows.Next() {
		p, err := scanPost(rows, hasContentLen)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPublished 返回某语种已发布文章（按发布时间倒序，分页）。
func (s *Store) ListPublished(lang string, offset, limit int) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=?
		ORDER BY p.published_at DESC LIMIT ? OFFSET ?`, lang, limit, offset)
}

func (s *Store) CountPublished(lang string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE type='post' AND status='published' AND lang=?`, lang).Scan(&n)
	return n, err
}

// GetPostBySlug 取某语种单篇文章。includeDrafts 为 true 时也返回草稿（供后台预览）。
func (s *Store) GetPostBySlug(lang, slug string, includeDrafts bool) (*Post, error) {
	where := `WHERE p.slug=? AND p.lang=? AND p.type='post'`
	if !includeDrafts {
		where += ` AND p.status='published'`
	}
	posts, err := s.queryPosts(where+` LIMIT 1`, slug, lang)
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

// GetPage 取某语种单页（type=page），如 about。
func (s *Store) GetPage(lang, slug string) (*Post, error) {
	posts, err := s.queryPosts(`WHERE p.slug=? AND p.lang=? AND p.type='page' AND p.status='published' LIMIT 1`, slug, lang)
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

func (s *Store) ListByCategory(catID int64, offset, limit int) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.category_id=?
		ORDER BY p.published_at DESC LIMIT ? OFFSET ?`, catID, limit, offset)
}

func (s *Store) CountByCategory(catID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE type='post' AND status='published' AND category_id=?`, catID).Scan(&n)
	return n, err
}

func (s *Store) GetCategoryBySlug(lang, slug string) (*Category, error) {
	var c Category
	err := s.db.QueryRow(`SELECT id,slug,name,description,position,lang,trans_group,kind FROM categories WHERE slug=? AND lang=?`, slug, lang).
		Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.Position, &c.Lang, &c.TransGroup, &c.Kind)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

func (s *Store) GetCategoryByID(id int64) (*Category, error) {
	var c Category
	err := s.db.QueryRow(`SELECT id,slug,name,description,position,lang,trans_group,kind FROM categories WHERE id=?`, id).
		Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.Position, &c.Lang, &c.TransGroup, &c.Kind)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

func (s *Store) CreateCategory(c *Category) (int64, error) {
	c.Lang = nz(c.Lang, "zh")
	c.Kind = nz(c.Kind, "post")
	if c.TransGroup == "" {
		c.TransGroup = c.Lang + ":" + c.Slug
	}
	var pos int
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(position),-1)+1 FROM categories WHERE lang=? AND kind=?`, c.Lang, c.Kind).Scan(&pos)
	res, err := s.db.Exec(`INSERT INTO categories(slug,name,description,position,lang,trans_group,kind) VALUES(?,?,?,?,?,?,?)`,
		c.Slug, c.Name, c.Description, pos, c.Lang, c.TransGroup, c.Kind)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateCategory(c *Category) error {
	_, err := s.db.Exec(`UPDATE categories SET slug=?,name=?,description=?,trans_group=? WHERE id=?`,
		c.Slug, c.Name, c.Description, c.TransGroup, c.ID)
	return err
}

func (s *Store) DeleteCategory(id int64) error {
	// 外键 ON DELETE SET NULL：文章的 category_id 自动置空。
	_, err := s.db.Exec(`DELETE FROM categories WHERE id=?`, id)
	return err
}

func (s *Store) CategorySlugExists(lang, slug string, exceptID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM categories WHERE slug=? AND lang=? AND id<>?`, slug, lang, exceptID).Scan(&n)
	return n > 0, err
}

// ListCategories 返回某语种、某类型（post|link）的全部分类及各自已发布条目数。
func (s *Store) ListCategories(lang, kind string) ([]*Category, error) {
	return s.scanCategories(`
		SELECT c.id,c.slug,c.name,c.description,c.position,c.lang,c.trans_group,c.kind,
			(SELECT COUNT(*) FROM posts p WHERE p.category_id=c.id AND p.status='published')
		FROM categories c WHERE c.lang=? AND c.kind=? ORDER BY c.position, c.id`, lang, kind)
}

// AllCategories 返回所有语种、某类型的分类（供 sitemap；kind 为空则全部）。
func (s *Store) AllCategories(kind string) ([]*Category, error) {
	where := ""
	var args []any
	if kind != "" {
		where = "WHERE c.kind=?"
		args = append(args, kind)
	}
	return s.scanCategories(`
		SELECT c.id,c.slug,c.name,c.description,c.position,c.lang,c.trans_group,c.kind,
			(SELECT COUNT(*) FROM posts p WHERE p.category_id=c.id AND p.status='published')
		FROM categories c `+where+` ORDER BY c.lang, c.position, c.id`, args...)
}

// CategoryTranslations 返回与某 trans_group 关联的各语种分类（互译版本）。
func (s *Store) CategoryTranslations(group string) ([]*Category, error) {
	if group == "" {
		return nil, nil
	}
	return s.scanCategories(`
		SELECT c.id,c.slug,c.name,c.description,c.position,c.lang,c.trans_group,c.kind,0
		FROM categories c WHERE c.trans_group=? ORDER BY c.lang`, group)
}

func (s *Store) scanCategories(q string, args ...any) ([]*Category, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.Position, &c.Lang, &c.TransGroup, &c.Kind, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// ReorderCategories 按给定 id 顺序写入 position。
func (s *Store) ReorderCategories(ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE categories SET position=? WHERE id=?`, i, id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// PrevPost / NextPost 用于文章详情页的上一篇/下一篇导航（同语种内）。
func (s *Store) PrevPost(p *Post) (*Post, error) {
	posts, err := s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=? AND p.published_at < ?
		ORDER BY p.published_at DESC LIMIT 1`, p.Lang, fmtTime(p.PublishedAt))
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

func (s *Store) NextPost(p *Post) (*Post, error) {
	posts, err := s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=? AND p.published_at > ?
		ORDER BY p.published_at ASC LIMIT 1`, p.Lang, fmtTime(p.PublishedAt))
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

// Related 同语种、同分类的相关文章（排除自身）。
func (s *Store) Related(p *Post, limit int) ([]*Post, error) {
	if !p.CategoryID.Valid {
		return nil, nil
	}
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=? AND p.category_id=? AND p.id<>?
		ORDER BY p.published_at DESC LIMIT ?`, p.Lang, p.CategoryID.Int64, p.ID, limit)
}

// Search 在某语种的标题、摘要与正文中检索；长词优先走 FTS5，短词保留 LIKE 回退。
func (s *Store) Search(lang, q string, limit int) ([]*Post, error) {
	if len([]rune(q)) >= 3 {
		if posts, err := s.searchFTS(lang, q, limit); err == nil {
			return posts, nil
		}
	}
	return s.searchLike(lang, q, limit)
}

func (s *Store) searchFTS(lang, q string, limit int) ([]*Post, error) {
	match := `"` + strings.ReplaceAll(strings.TrimSpace(q), `"`, `""`) + `"`
	sql := `SELECT ` + postSummaryCols + `
		FROM posts p
		JOIN (
			SELECT rowid, rank FROM post_search
			WHERE post_search MATCH ? AND lang=? AND type='post' AND status='published'
			ORDER BY rank LIMIT ?
		) hit ON hit.rowid = p.id
		LEFT JOIN categories c ON c.id = p.category_id
		ORDER BY hit.rank, p.published_at DESC`
	return s.queryPostRows(sql, true, match, lang, limit)
}

func (s *Store) searchLike(lang, q string, limit int) ([]*Post, error) {
	like := "%" + q + "%"
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=?
		AND (p.title LIKE ? OR p.excerpt LIKE ? OR p.content LIKE ?)
		ORDER BY p.published_at DESC LIMIT ?`, lang, like, like, like, limit)
}

// AllPublished 某语种全部已发布文章，供 rss 使用。
func (s *Store) AllPublished(lang string) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=? ORDER BY p.published_at DESC`, lang)
}

// RecentPublished 返回某语种最近的已发布文章，供 rss 使用。
func (s *Store) RecentPublished(lang string, limit int) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=?
		ORDER BY p.published_at DESC LIMIT ?`, lang, limit)
}

// AllPublishedAllLangs 所有语种的已发布文章，供 sitemap 使用。
func (s *Store) AllPublishedAllLangs() ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' ORDER BY p.lang, p.published_at DESC`)
}

// AllPagesAllLangs 所有语种的已发布独立页面（type=page），供 sitemap 使用。
func (s *Store) AllPagesAllLangs() ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='page' AND p.status='published' ORDER BY p.lang`)
}

// ---------- 查询：链接（type=link）----------

// ListLinks 返回某语种已发布链接（可按分类过滤，置顶优先、发布时间倒序，分页）。
func (s *Store) ListLinks(lang string, catID int64, offset, limit int) ([]*Post, error) {
	if catID > 0 {
		return s.queryPostSummaries(`WHERE p.type='link' AND p.status='published' AND p.lang=? AND p.category_id=?
			ORDER BY p.featured DESC, p.published_at DESC LIMIT ? OFFSET ?`, lang, catID, limit, offset)
	}
	return s.queryPostSummaries(`WHERE p.type='link' AND p.status='published' AND p.lang=?
		ORDER BY p.featured DESC, p.published_at DESC LIMIT ? OFFSET ?`, lang, limit, offset)
}

// CountLinks 统计某语种已发布链接数（可按分类）。
func (s *Store) CountLinks(lang string, catID int64) (int, error) {
	q := `SELECT COUNT(*) FROM posts WHERE type='link' AND status='published' AND lang=?`
	args := []any{lang}
	if catID > 0 {
		q += ` AND category_id=?`
		args = append(args, catID)
	}
	var n int
	err := s.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// GetLinkBySlug 取某语种单条链接。includeDrafts 为 true 时也返回草稿。
func (s *Store) GetLinkBySlug(lang, slug string, includeDrafts bool) (*Post, error) {
	where := `WHERE p.slug=? AND p.lang=? AND p.type='link'`
	if !includeDrafts {
		where += ` AND p.status='published'`
	}
	posts, err := s.queryPosts(where+` LIMIT 1`, slug, lang)
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

// RelatedLinks 同语种、同分类的相关链接（排除自身）。
func (s *Store) RelatedLinks(p *Post, limit int) ([]*Post, error) {
	if !p.CategoryID.Valid {
		return nil, nil
	}
	return s.queryPostSummaries(`WHERE p.type='link' AND p.status='published' AND p.lang=? AND p.category_id=? AND p.id<>?
		ORDER BY p.published_at DESC LIMIT ?`, p.Lang, p.CategoryID.Int64, p.ID, limit)
}

// AllLinksAllLangs 所有语种的已发布链接，供 sitemap 使用。
func (s *Store) AllLinksAllLangs() ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='link' AND p.status='published' ORDER BY p.lang, p.published_at DESC`)
}

// ListAllLinks 后台：某语种全部链接（含草稿）。
func (s *Store) ListAllLinks(lang string) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='link' AND p.lang=? ORDER BY p.updated_at DESC`, lang)
}

// TranslationsPublished 返回与某 trans_group 关联、已发布的各语种内容（含 post 与 page）。
// 供前台构建语言切换与 hreflang 备份链接。
func (s *Store) TranslationsPublished(group string) ([]*Post, error) {
	if group == "" {
		return nil, nil
	}
	return s.queryPostSummaries(`WHERE p.trans_group=? AND p.status='published' ORDER BY p.lang`, group)
}

// ---------- 查询：后台 ----------

func (s *Store) ListAllPosts(lang string) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='post' AND p.lang=? ORDER BY p.updated_at DESC`, lang)
}

func (s *Store) ListPages(lang string) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='page' AND p.lang=? ORDER BY p.updated_at DESC`, lang)
}

func (s *Store) GetPostByID(id int64) (*Post, error) {
	posts, err := s.queryPosts(`WHERE p.id=? LIMIT 1`, id)
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

// TranslationsAll 返回与某 trans_group 关联的各语种内容（含草稿，排除自身），供后台展示互译版本。
func (s *Store) TranslationsAll(group string, exceptID int64) ([]*Post, error) {
	if group == "" {
		return nil, nil
	}
	return s.queryPostSummaries(`WHERE p.trans_group=? AND p.id<>? ORDER BY p.lang`, group, exceptID)
}

func (s *Store) CreatePost(p *Post) (int64, error) {
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	if p.Status == "published" && p.PublishedAt.IsZero() {
		p.PublishedAt = now
	}
	p.Lang = nz(p.Lang, "zh")
	if p.TransGroup == "" {
		p.TransGroup = p.Lang + ":" + p.Slug
	}
	res, err := s.db.Exec(`INSERT INTO posts
		(type,slug,title,excerpt,content,meta_desc,keywords,cover_image,author,status,featured,editor_mode,link_url,lang,trans_group,category_id,published_at,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		nz(p.Type, "post"), p.Slug, p.Title, p.Excerpt, p.Content, p.MetaDesc, p.Keywords, p.CoverImage,
		p.Author, p.Status, boolInt(p.Featured), nz(p.EditorMode, "markdown"), p.LinkURL, p.Lang, p.TransGroup,
		p.CategoryID, nullTime(p.PublishedAt), fmtTime(p.CreatedAt), fmtTime(p.UpdatedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdatePost(p *Post) error {
	p.UpdatedAt = time.Now()
	if p.Status == "published" && p.PublishedAt.IsZero() {
		p.PublishedAt = p.UpdatedAt
	}
	_, err := s.db.Exec(`UPDATE posts SET
		slug=?,title=?,excerpt=?,content=?,meta_desc=?,keywords=?,cover_image=?,author=?,status=?,featured=?,editor_mode=?,link_url=?,trans_group=?,category_id=?,published_at=?,updated_at=?
		WHERE id=?`,
		p.Slug, p.Title, p.Excerpt, p.Content, p.MetaDesc, p.Keywords, p.CoverImage, p.Author, p.Status,
		boolInt(p.Featured), nz(p.EditorMode, "markdown"), p.LinkURL, p.TransGroup, p.CategoryID, nullTime(p.PublishedAt), fmtTime(p.UpdatedAt), p.ID)
	return err
}

// SetFeatured 单独切换置顶（不动其它字段）。
func (s *Store) SetFeatured(id int64, on bool) error {
	_, err := s.db.Exec(`UPDATE posts SET featured=? WHERE id=?`, boolInt(on), id)
	return err
}

// FeaturedPosts 返回某语种置顶的已发布文章（按发布时间倒序），供首页精选列表使用。
func (s *Store) FeaturedPosts(lang string, limit int) ([]*Post, error) {
	now := fmtTime(time.Now())
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=? AND p.featured=1 AND p.published_at<=?
		ORDER BY p.published_at DESC LIMIT ?`, lang, now, limit)
}

// FeaturedLinks 取该语种下「置顶」的链接，供首页链接模块展示；无置顶则返回空（首页隐藏该模块）。
func (s *Store) FeaturedLinks(lang string, limit int) ([]*Post, error) {
	now := fmtTime(time.Now())
	return s.queryPostSummaries(`WHERE p.type='link' AND p.status='published' AND p.lang=? AND p.featured=1 AND p.published_at<=?
		ORDER BY p.published_at DESC LIMIT ?`, lang, now, limit)
}

func (s *Store) DeletePost(id int64) error {
	_, err := s.db.Exec(`DELETE FROM posts WHERE id=?`, id)
	return err
}

// PublishDue 把到点的「定时发布」文章翻为「已发布」，返回处理条数。由后台定时器调用。
func (s *Store) PublishDue() (int64, error) {
	res, err := s.db.Exec(`UPDATE posts SET status='published' WHERE status='scheduled' AND published_at<=?`, fmtTime(time.Now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SlugExists 校验某语种内 slug 是否被其它文章占用（exceptID 为 0 表示新建）。
func (s *Store) SlugExists(lang, slug string, exceptID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE slug=? AND lang=? AND id<>?`, slug, lang, exceptID).Scan(&n)
	return n > 0, err
}

// ---------- 设置 ----------

func (s *Store) loadSettings() error {
	rows, err := s.db.Query(`SELECT key,value FROM settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	next := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		next[key] = value
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.settingsMu.Lock()
	s.settings = next
	s.settingsLoaded = true
	s.settingsMu.Unlock()
	return nil
}

func (s *Store) GetSetting(key string) (string, error) {
	s.settingsMu.RLock()
	if s.settingsLoaded {
		v := s.settings[key]
		s.settingsMu.RUnlock()
		return v, nil
	}
	s.settingsMu.RUnlock()

	if err := s.loadSettings(); err != nil {
		return "", err
	}
	s.settingsMu.RLock()
	v := s.settings[key]
	s.settingsMu.RUnlock()
	return v, nil
}

// Setting 便捷读取（忽略错误，缺失返回空串）。
func (s *Store) Setting(key string) string {
	v, _ := s.GetSetting(key)
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return err
	}
	s.settingsMu.Lock()
	if s.settingsLoaded {
		if s.settings == nil {
			s.settings = map[string]string{}
		}
		s.settings[key] = value
	}
	s.settingsMu.Unlock()
	return err
}

// ---------- 小工具 ----------

func nz(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return fmtTime(t)
}

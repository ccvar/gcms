package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/store"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// Renderer 持有按页面预解析好的模板集合。
type Renderer struct {
	sets map[string]*template.Template
}

// markdown 转换器：GFM 扩展 + 自动为标题生成锚点 id（供大纲跳转）。
// 默认转义裸 HTML，内容由后台可信作者撰写。
var gmark = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

var (
	imgLoadingAttrRE  = regexp.MustCompile(`(?i)\sloading\s*=`)
	imgDecodingAttrRE = regexp.MustCompile(`(?i)\sdecoding\s*=`)
	imgWidthAttrRE    = regexp.MustCompile(`(?i)\swidth\s*=`)
	imgHeightAttrRE   = regexp.MustCompile(`(?i)\sheight\s*=`)
	imgSrcAttrRE      = regexp.MustCompile(`(?is)\ssrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
)

func renderMarkdown(src string, imageSizes map[string]ImageSize) template.HTML {
	source := []byte(src)
	doc := gmark.Parser().Parse(text.NewReader(source))
	decorateMarkdownImages(doc)

	var buf bytes.Buffer
	if err := gmark.Renderer().Render(&buf, source, doc); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(addImageLoadingHints(buf.String(), imageSizes))
}

func decorateMarkdownImages(doc ast.Node) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if img, ok := n.(*ast.Image); ok {
			img.SetAttributeString("loading", []byte("lazy"))
			img.SetAttributeString("decoding", []byte("async"))
		}
		return ast.WalkContinue, nil
	})
}

// Heading 是文章大纲中的一个条目。
type Heading struct {
	Level int
	ID    string
	Text  string
}

// RenderContent 渲染正文并提取 h2/h3 大纲。解析一次，先为每个标题写入「保留中文」
// 的可读锚点 id，再渲染——这样大纲锚点、正文标题 id、分享链接三者一致且语义清晰。
func RenderContent(src string) (template.HTML, []Heading) {
	return RenderContentWithImages(src, nil)
}

func RenderContentWithImages(src string, imageSizes map[string]ImageSize) (template.HTML, []Heading) {
	return RenderContentWithLinkPolicy(src, imageSizes, nil)
}

func RenderContentWithLinkPolicy(src string, imageSizes map[string]ImageSize, linkPolicy *ExternalLinkPolicy) (template.HTML, []Heading) {
	source := []byte(src)
	doc := gmark.Parser().Parse(text.NewReader(source))
	decorateMarkdownLinks(doc, linkPolicy)

	var toc []Heading
	used := map[string]int{}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if h, ok := n.(*ast.Heading); ok && h.Level >= 2 && h.Level <= 3 {
			txt := string(h.Text(source))
			base := slugHeading(txt)
			if base == "" {
				base = "section"
			}
			id := base
			if k := used[base]; k > 0 {
				id = base + "-" + strconv.Itoa(k)
			}
			used[base]++
			h.SetAttributeString("id", []byte(id))
			toc = append(toc, Heading{Level: h.Level, ID: id, Text: txt})
		}
		if img, ok := n.(*ast.Image); ok {
			img.SetAttributeString("loading", []byte("lazy"))
			img.SetAttributeString("decoding", []byte("async"))
		}
		return ast.WalkContinue, nil
	})

	var buf bytes.Buffer
	if err := gmark.Renderer().Render(&buf, source, doc); err != nil {
		return template.HTML(template.HTMLEscapeString(src)), nil
	}
	return template.HTML(addImageLoadingHints(buf.String(), imageSizes)), toc
}

func addImageLoadingHints(html string, imageSizes map[string]ImageSize) string {
	if !strings.Contains(strings.ToLower(html), "<img") {
		return html
	}
	var out strings.Builder
	out.Grow(len(html) + 64)
	for i := 0; i < len(html); {
		rest := strings.ToLower(html[i:])
		rel := strings.Index(rest, "<img")
		if rel < 0 {
			out.WriteString(html[i:])
			break
		}
		start := i + rel
		afterName := start + len("<img")
		if afterName < len(html) && isHTMLNameChar(html[afterName]) {
			out.WriteString(html[i:afterName])
			i = afterName
			continue
		}
		end := htmlTagEnd(html, afterName)
		if end < 0 {
			out.WriteString(html[i:])
			break
		}
		out.WriteString(html[i:start])
		out.WriteString(addAttrsToImgTag(html[start:end], imageSizes))
		out.WriteByte('>')
		i = end + 1
	}
	return out.String()
}

func addAttrsToImgTag(tag string, imageSizes map[string]ImageSize) string {
	attrs := ""
	if !imgLoadingAttrRE.MatchString(tag) {
		attrs += ` loading="lazy"`
	}
	if !imgDecodingAttrRE.MatchString(tag) {
		attrs += ` decoding="async"`
	}
	if size, ok := lookupImageSize(imgTagSrc(tag), imageSizes); ok {
		if !imgWidthAttrRE.MatchString(tag) {
			attrs += fmt.Sprintf(` width="%d"`, size.Width)
		}
		if !imgHeightAttrRE.MatchString(tag) {
			attrs += fmt.Sprintf(` height="%d"`, size.Height)
		}
	}
	if attrs == "" {
		return tag
	}
	if strings.HasSuffix(strings.TrimSpace(tag), "/") {
		slash := strings.LastIndex(tag, "/")
		if slash > 0 {
			return strings.TrimRight(tag[:slash], " \t\r\n") + attrs + " /"
		}
	}
	return tag + attrs
}

func imgTagSrc(tag string) string {
	m := imgSrcAttrRE.FindStringSubmatch(tag)
	for i := 1; i < len(m); i++ {
		if m[i] != "" {
			return m[i]
		}
	}
	return ""
}

func htmlTagEnd(s string, start int) int {
	var quote byte
	for i := start; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i
		}
	}
	return -1
}

func isHTMLNameChar(c byte) bool {
	return c == '-' || c == '_' || c == ':' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// slugHeading 生成锚点：保留中日韩文字与字母数字，其余转连字符。
func slugHeading(s string) string {
	var b strings.Builder
	dash := false
	emit := func(r rune) { b.WriteRune(r); dash = false }
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			emit(r + 32)
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			emit(r)
		case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hiragana, r),
			unicode.Is(unicode.Katakana, r), unicode.Is(unicode.Hangul, r):
			emit(r)
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func automationLogParts(message string) struct {
	Action string
	Title  string
} {
	message = strings.TrimSpace(message)
	action, title, ok := strings.Cut(message, "：")
	if !ok {
		action, title, ok = strings.Cut(message, ":")
	}
	if !ok {
		return struct {
			Action string
			Title  string
		}{Title: message}
	}
	return struct {
		Action string
		Title  string
	}{
		Action: strings.TrimSpace(action),
		Title:  strings.TrimSpace(title),
	}
}

func funcMap(imageSizes map[string]ImageSize) template.FuncMap {
	return template.FuncMap{
		"md": func(s string) template.HTML { return renderMarkdown(s, imageSizes) },
		"imgAttrs": func(src string) template.HTMLAttr {
			size, ok := lookupImageSize(src, imageSizes)
			if !ok {
				return ""
			}
			return template.HTMLAttr(fmt.Sprintf(` width="%d" height="%d"`, size.Width, size.Height))
		},
		// safeHTML 把可信（后台录入）的字符串作为原始 HTML 输出，用于内联 SVG。
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		// contentURL 按内容类型返回其公开路径（搜索结果链接用）。
		"contentURL": func(p *store.Post) string { return publicContentPath(p.Type, p.Slug) },
		// typeName 扩展类型的语种名（搜索结果徽标用）：内置 post/page/link 与
		// 未注册的数据库自定义类型返回空（模板容错不显示徽标）。
		"typeName": func(typ, lang string) string {
			if ct := contentTypeByKey(typ); ct != nil && !ct.Builtin {
				return ct.Name(lang)
			}
			return ""
		},
		// typePrefix 内容类型的公开 URL 前缀（搜索结果的分类链接用）；
		// 数据库自定义类型前缀恒等于 key。
		"typePrefix": func(typ string) string {
			if ct := contentTypeByKey(typ); ct != nil {
				return ct.URLPrefix
			}
			return typ
		},
		// productChips 商品卡「卖点规格」chip（工厂骨架首页：材质/起订量，最多 2 个）。
		"productChips": productChips,
		// productSKU 从规格 repeater 嗅探型号/SKU（技术骨架首页的产品索引表「型号」列）。
		"productSKU": productSKU,
		"json": func(v any) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
		"date": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("2006 年 1 月 2 日")
		},
		"adminDateTime": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Local().Format("2006-01-02 15:04:05")
		},
		"maskEmail": func(email string) string {
			email = strings.TrimSpace(email)
			parts := strings.SplitN(email, "@", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return email
			}
			local := parts[0]
			if len([]rune(local)) > 2 {
				local = string([]rune(local)[:2])
			}
			return local + "***@" + parts[1]
		},
		"adminPublicContentPath": adminPublicContentPath,
		"automationLogParts":     automationLogParts,
		"isodate": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("2006-01-02")
		},
		"dtlocal": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Local().Format("2006-01-02T15:04")
		},
		"initial": func(s string) string {
			r := []rune(s)
			if len(r) == 0 {
				return "·"
			}
			return string(r[0])
		},
		"categoryCount": func(categories []*store.Category, slug string) int {
			for _, category := range categories {
				if category != nil && category.Slug == slug {
					return category.Count
				}
			}
			return 0
		},
		"nl2br": func(s string) template.HTML {
			return template.HTML(strings.ReplaceAll(template.HTMLEscapeString(s), "\n", "<br>"))
		},
		"filesize":                   func(n int64) string { return formatBytes(n) },
		"automationScopeBadges":      automationScopeBadges,
		"automationScopeBadgesAdmin": automationScopeBadgesAdmin,
		"add":                        func(a, b int) int { return a + b },
		"sub":                        func(a, b int) int { return a - b },
		"hasLang": func(posts []*store.Post, code string) bool {
			for _, p := range posts {
				if p.Lang == code {
					return true
				}
			}
			return false
		},
		"localeOn": func(ls []i18n.Locale, code string) bool {
			for _, l := range ls {
				if l.Code == code {
					return true
				}
			}
			return false
		},
		"pages": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i + 1
			}
			return out
		},
		"pageURL": func(tr *i18n.Tr, base string, page int) string {
			if tr == nil {
				return paginationPath(base, page)
			}
			return tr.U(paginationPath(base, page))
		},
	}
}

func adminPublicContentPath(routePrefix, lang, typ, slug string) string {
	typ = strings.TrimSpace(typ)
	slug = strings.TrimSpace(slug)
	slug = strings.TrimLeft(slug, "/")
	switch typ {
	case "page":
		if slug == "" {
			return localizedPath(routePrefix, lang, "/")
		}
		return localizedPath(routePrefix, lang, publicContentPath(typ, slug))
	default:
		return localizedPath(routePrefix, lang, publicContentPath(typ, slug))
	}
}

func paginationPath(base string, page int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "/"
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	if page <= 1 {
		if base == "/" {
			return "/"
		}
		return strings.TrimRight(base, "/")
	}
	if base == "/" {
		return "/page/" + strconv.Itoa(page) + "/"
	}
	return strings.TrimRight(base, "/") + "/page/" + strconv.Itoa(page) + "/"
}

func formatBytes(n int64) string {
	if n <= 0 {
		return ""
	}
	units := []string{"B", "KB", "MB", "GB"}
	value := float64(n)
	unit := units[0]
	for i := 1; i < len(units) && value >= 1024; i++ {
		value /= 1024
		unit = units[i]
	}
	if unit == "B" {
		return strconv.FormatInt(n, 10) + " " + unit
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " " + unit
}

// NewRenderer 从 templates 目录解析全部页面模板。
func NewRenderer(tplFS fs.FS, imageSizes map[string]ImageSize) (*Renderer, error) {
	sub, err := fs.Sub(tplFS, "templates")
	if err != nil {
		return nil, err
	}
	r := &Renderer{sets: map[string]*template.Template{}}
	partials := []string{"layout.html", "partials/head.html", "partials/header.html", "partials/footer.html", "partials/home_bento.html", "partials/home_index.html", "partials/home_field_ledger.html", "partials/home_signal_archive.html", "partials/home_paper_current.html", "partials/home_night_watch.html", "partials/home_orbit_index.html", "partials/home_column_stage.html", "partials/home_type_cascade.html", "partials/home_split.html", "partials/home_axis.html", "partials/home_bands.html", "partials/home_ticker.html", "partials/home_board.html", "partials/home_timeline.html", "partials/home_liftoff.html", "partials/home_deck.html", "partials/home_poster.html", "partials/home_uptime.html", "partials/home_profile.html", "partials/home_bloom.html", "partials/home_desktop.html", "partials/home_cinema.html", "partials/home_collage.html", "partials/home_constellation.html", "partials/home_masonry.html", "partials/home_feed.html", "partials/home_gazette.html", "partials/home_manual.html", "partials/home_almanac.html", "partials/home_inbox.html", "partials/home_catalog.html", "partials/home_broadcast.html", "partials/home_exhibit.html", "partials/home_factory_catalog.html", "partials/home_factory_showcase.html", "partials/home_factory_onepage.html", "partials/home_factory_solutions.html", "partials/home_factory_engineering.html", "partials/home_factory_trade.html", "partials/home_factory_sidebar.html", "partials/home_factory_vision.html", "partials/home_factory_herofold.html", "partials/home_dtc_flagship.html", "partials/home_dtc_solo.html", "partials/home_dtc_lookbook.html"}

	for _, name := range []string{"home", "article", "category", "links", "link", "page", "search", "api_docs", "404", "generic_list", "generic_detail", "doc_list", "doc_detail", "product_detail"} {
		files := append([]string{}, partials...)
		files = append(files, name+".html")
		t, err := template.New(name).Funcs(funcMap(imageSizes)).ParseFS(sub, files...)
		if err != nil {
			return nil, err
		}
		r.sets[name] = t
	}

	tp, err := template.New("theme_preview").Funcs(funcMap(imageSizes)).ParseFS(sub, "theme_preview.html", "partials/header.html", "partials/footer.html", "partials/home_bento.html", "partials/home_index.html", "partials/home_field_ledger.html", "partials/home_signal_archive.html", "partials/home_paper_current.html", "partials/home_night_watch.html", "partials/home_orbit_index.html", "partials/home_column_stage.html", "partials/home_type_cascade.html", "partials/home_split.html", "partials/home_axis.html", "partials/home_bands.html", "partials/home_ticker.html", "partials/home_board.html", "partials/home_timeline.html", "partials/home_liftoff.html", "partials/home_deck.html", "partials/home_poster.html", "partials/home_uptime.html", "partials/home_profile.html", "partials/home_bloom.html", "partials/home_desktop.html", "partials/home_cinema.html", "partials/home_collage.html", "partials/home_constellation.html", "partials/home_masonry.html", "partials/home_feed.html", "partials/home_gazette.html", "partials/home_manual.html", "partials/home_almanac.html", "partials/home_inbox.html", "partials/home_catalog.html", "partials/home_broadcast.html", "partials/home_exhibit.html", "partials/home_factory_catalog.html", "partials/home_factory_showcase.html", "partials/home_factory_onepage.html", "partials/home_factory_solutions.html", "partials/home_factory_engineering.html", "partials/home_factory_trade.html", "partials/home_factory_sidebar.html", "partials/home_factory_vision.html", "partials/home_factory_herofold.html", "partials/home_dtc_flagship.html", "partials/home_dtc_solo.html", "partials/home_dtc_lookbook.html")
	if err != nil {
		return nil, err
	}
	r.sets["theme_preview"] = tp

	for _, name := range []string{"login", "dashboard", "posts", "edit", "settings", "pages", "links", "visual", "sites", "platform_settings", "platform_automation", "backups", "archived_sites", "media_cleanup", "updates", "admin_i18n", "security", "extensions", "ext_list", "ext_edit", "ext_archive", "ext_type_edit"} {
		t, err := template.New("admin_"+name).Funcs(funcMap(imageSizes)).ParseFS(sub, "admin/layout.html", "admin/"+name+".html")
		if err != nil {
			return nil, err
		}
		r.sets["admin_"+name] = t
	}
	return r, nil
}

func (r *Renderer) execute(w http.ResponseWriter, key, layout string, status int, data any) {
	t, ok := r.sets[key]
	if !ok {
		http.Error(w, "模板不存在: "+key, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, layout, data); err != nil {
		log.Printf("渲染 %s 失败: %v", key, err)
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// Public 渲染公开页面。
func (r *Renderer) Public(w http.ResponseWriter, name string, status int, data any) {
	if v, ok := data.(*View); ok && v.ForceNoindex {
		v.SEO.Robots = "noindex, nofollow"
		v.SEO.Alternates = nil
	}
	r.execute(w, name, "public_layout", status, data)
}

// ThemePreview 渲染后台主题卡片使用的轻量缩略图页面。
func (r *Renderer) ThemePreview(w http.ResponseWriter, status int, data any) {
	r.execute(w, "theme_preview", "theme_preview", status, data)
}

// Admin 渲染后台页面。
func (r *Renderer) Admin(w http.ResponseWriter, name string, status int, data any) {
	w.Header().Set("Cache-Control", "no-store")
	r.execute(w, "admin_"+name, "admin_layout", status, data)
}

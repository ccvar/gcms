package web

import (
	"bytes"
	"html/template"
	"io/fs"
	"log"
	"net/http"
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

func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := gmark.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(buf.String())
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
	source := []byte(src)
	doc := gmark.Parser().Parse(text.NewReader(source))

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
		return ast.WalkContinue, nil
	})

	var buf bytes.Buffer
	if err := gmark.Renderer().Render(&buf, source, doc); err != nil {
		return template.HTML(template.HTMLEscapeString(src)), nil
	}
	return template.HTML(buf.String()), toc
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

func funcMap() template.FuncMap {
	return template.FuncMap{
		"md": renderMarkdown,
		// safeHTML 把可信（后台录入）的字符串作为原始 HTML 输出，用于内联 SVG。
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"date": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("2006 年 1 月 2 日")
		},
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
		"nl2br": func(s string) template.HTML {
			return template.HTML(strings.ReplaceAll(template.HTMLEscapeString(s), "\n", "<br>"))
		},
		"filesize":              func(n int64) string { return formatBytes(n) },
		"automationScopeBadges": automationScopeBadges,
		"add":                   func(a, b int) int { return a + b },
		"sub":                   func(a, b int) int { return a - b },
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
	}
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
func NewRenderer(tplFS fs.FS) (*Renderer, error) {
	sub, err := fs.Sub(tplFS, "templates")
	if err != nil {
		return nil, err
	}
	r := &Renderer{sets: map[string]*template.Template{}}
	partials := []string{"layout.html", "partials/head.html", "partials/header.html", "partials/footer.html"}

	for _, name := range []string{"home", "article", "category", "links", "link", "page", "search", "api_docs", "404"} {
		files := append([]string{}, partials...)
		files = append(files, name+".html")
		t, err := template.New(name).Funcs(funcMap()).ParseFS(sub, files...)
		if err != nil {
			return nil, err
		}
		r.sets[name] = t
	}

	tp, err := template.New("theme_preview").Funcs(funcMap()).ParseFS(sub, "theme_preview.html")
	if err != nil {
		return nil, err
	}
	r.sets["theme_preview"] = tp

	for _, name := range []string{"login", "dashboard", "edit", "settings", "pages", "links", "visual"} {
		t, err := template.New("admin_"+name).Funcs(funcMap()).ParseFS(sub, "admin/layout.html", "admin/"+name+".html")
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

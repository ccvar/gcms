package web

// quality_gate.go 发布质量门（托管防判责）：仅在自动化 API 把 posts 的 status 显式置为
// published 的两条路径（创建即发布、更新改状态）上做硬校验；admin 后台人工发布不拦。
// 不达标返回 422：{"error":"quality_gate","failures":["body_too_short (380/400)",...]}，
// 让 AI 按提示补齐后重试。

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"cms.ccvar.com/internal/store"
)

const (
	qualityGateMinBodyWords = 400 // 正文有效长度下限（CJK 每字符记 1、拉丁按空格分词记 1）
	qualityGateTitleMin     = 8   // 标题最短字符数
	qualityGateTitleMax     = 120 // 标题最长字符数
)

// qualityGateApplies 判定本次自动化请求是否命中发布质量门：仅 posts、且本次请求显式把
// status 置为 published（创建即发布 / 更新改状态）。草稿、定时与其它集合不拦。
func qualityGateApplies(kind string, in *apiContentInput, p *store.Post) bool {
	if kind != "post" || p == nil || p.Status != "published" {
		return false
	}
	return in != nil && in.Status != nil && strings.TrimSpace(*in.Status) == "published"
}

// qualityGateFailures 纯函数：返回不达标项列表（空 = 通过）。
func qualityGateFailures(p *store.Post) []string {
	var failures []string
	words := effectiveWordCount(stripMarkdown(p.Content))
	if words < qualityGateMinBodyWords {
		failures = append(failures, fmt.Sprintf("body_too_short (%d/%d)", words, qualityGateMinBodyWords))
	}
	if strings.TrimSpace(p.Excerpt) == "" {
		failures = append(failures, "excerpt_missing")
	}
	if strings.TrimSpace(p.MetaDesc) == "" {
		failures = append(failures, "meta_desc_missing")
	}
	titleLen := len([]rune(strings.TrimSpace(p.Title)))
	if titleLen < qualityGateTitleMin {
		failures = append(failures, fmt.Sprintf("title_too_short (%d/%d)", titleLen, qualityGateTitleMin))
	} else if titleLen > qualityGateTitleMax {
		failures = append(failures, fmt.Sprintf("title_too_long (%d/%d)", titleLen, qualityGateTitleMax))
	}
	return failures
}

// apiQualityGateError 422 结构化错误：error 固定 quality_gate，failures 逐项给出缺口。
func apiQualityGateError(w http.ResponseWriter, failures []string) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error":    "quality_gate",
		"message":  "内容质量未达发布标准，请按 failures 逐项补齐后重试（也可先存草稿）。",
		"failures": failures,
	})
}

// stripMarkdown 去掉常见 Markdown 语法，只留可读正文：围栏代码块整块移除（代码不算正文），
// 图片移除、链接留锚文本、HTML 标签移除，标题/引用/列表/强调等标记剥掉。
func stripMarkdown(md string) string {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	var out []string
	inFence := false
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		line = stripMarkdownLinePrefix(trimmed)
		if isMarkdownRuleLine(line) {
			continue
		}
		out = append(out, stripMarkdownInline(line))
	}
	return strings.Join(out, "\n")
}

// stripMarkdownLinePrefix 剥掉行首的标题 #、引用 >、列表 -/*/+ 与有序列表编号。
func stripMarkdownLinePrefix(line string) string {
	for {
		next := strings.TrimLeft(line, "#> \t")
		for _, marker := range []string{"- ", "* ", "+ "} {
			next = strings.TrimPrefix(next, marker)
		}
		// 有序列表 "1. " / "12) "
		i := 0
		for i < len(next) && next[i] >= '0' && next[i] <= '9' {
			i++
		}
		if i > 0 && i < len(next) && (next[i] == '.' || next[i] == ')') && i+1 < len(next) && next[i+1] == ' ' {
			next = next[i+2:]
		}
		if next == line {
			return line
		}
		line = next
	}
}

// isMarkdownRuleLine 分隔线（--- / *** / ___）或表格分隔行（|---|---|）不算正文。
func isMarkdownRuleLine(line string) bool {
	if line == "" {
		return false
	}
	for _, r := range line {
		switch r {
		case '-', '*', '_', '|', ':', ' ', '\t', '=':
		default:
			return false
		}
	}
	return true
}

// stripMarkdownInline 处理行内语法：图片整体移除、链接留锚文本、HTML 标签移除、
// 强调与行内代码标记剥掉、表格竖线换成空格。
func stripMarkdownInline(line string) string {
	var b strings.Builder
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '!':
			// 图片 ![alt](url) → 整体丢弃
			if i+1 < len(runes) && runes[i+1] == '[' {
				if end := markdownSpanEnd(runes, i+1); end > 0 {
					i = end
					continue
				}
			}
			b.WriteRune(r)
		case '[':
			// 链接 [text](url) → 留 text
			if close := indexRune(runes, i+1, ']'); close > 0 {
				end := close
				if close+1 < len(runes) && runes[close+1] == '(' {
					if p := indexRune(runes, close+2, ')'); p > 0 {
						end = p
					}
				}
				b.WriteString(string(runes[i+1 : close]))
				i = end
				continue
			}
			b.WriteRune(r)
		case '<':
			// HTML 标签 <...> → 移除
			if close := indexRune(runes, i+1, '>'); close > 0 {
				i = close
				continue
			}
			b.WriteRune(r)
		case '*', '_', '`', '~':
			// 强调 / 行内代码 / 删除线标记 → 剥掉
		case '|':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// markdownSpanEnd 从 runes[start]=='[' 起找 "](...)" 的收尾 ')' 下标；不完整返回 -1。
func markdownSpanEnd(runes []rune, start int) int {
	close := indexRune(runes, start+1, ']')
	if close < 0 {
		return -1
	}
	if close+1 < len(runes) && runes[close+1] == '(' {
		if p := indexRune(runes, close+2, ')'); p > 0 {
			return p
		}
	}
	return close
}

func indexRune(runes []rune, from int, target rune) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

// effectiveWordCount 正文有效长度：CJK 每字符记 1，非 CJK 的字母/数字连续串按词记 1。
func effectiveWordCount(text string) int {
	count := 0
	inWord := false
	for _, r := range text {
		switch {
		case isCJKChar(r):
			count++
			inWord = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if !inWord {
				count++
				inWord = true
			}
		default:
			inWord = false
		}
	}
	return count
}

// isCJKChar 汉字/假名/谚文按「每字符记 1」处理。
func isCJKChar(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

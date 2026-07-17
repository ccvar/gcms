package store

// similar.go 站内查重：按标题做近似匹配，供 AI 在写新内容前检查站内是否已有雷同主题
// （含已发布与草稿）。优先复用 FTS5 索引（标题分词后 OR match，bm25 排序）；
// FTS 不可用或标题过短（<3 字符）时回退到「分词 LIKE 重叠计数」。

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// SimilarRow 是查重结果的一行；Score 归一化到 0..1（保留两位小数，1 = 最相似）。
type SimilarRow struct {
	ID     int64
	Title  string
	Slug   string
	Status string
	Lang   string
	Score  float64
}

// similarLikeCandidateCap 回退路径先取的候选行上限（再在内存里按重叠计数排序）。
const similarLikeCandidateCap = 200

// similarTokens 把标题拆成查重词元：拉丁字母/数字串按词切；CJK 连续段按「滑动三字」切
// （与 post_search 的 trigram 分词对齐，两字及以下的 CJK 段整段保留）。全部小写、去重。
func similarTokens(title string) []string {
	var tokens []string
	seen := map[string]bool{}
	add := func(tok string) {
		tok = strings.ToLower(tok)
		if tok != "" && !seen[tok] {
			seen[tok] = true
			tokens = append(tokens, tok)
		}
	}
	var latin, cjk []rune
	flushLatin := func() {
		if len(latin) > 0 {
			add(string(latin))
			latin = latin[:0]
		}
	}
	flushCJK := func() {
		if n := len(cjk); n > 0 {
			if n <= 3 {
				add(string(cjk))
			} else {
				for i := 0; i+3 <= n; i++ {
					add(string(cjk[i : i+3]))
				}
			}
			cjk = cjk[:0]
		}
	}
	for _, r := range title {
		switch {
		case isCJKRune(r):
			flushLatin()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			latin = append(latin, r)
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return tokens
}

// isCJKRune 判断是否 CJK 字符（汉字/假名/谚文；查重与质量门共用同一判定口径）。
func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// SimilarByTitle 返回与 title 近似的同类型内容（lang 为空 = 全语种；含已发布 + 草稿，
// 但排除 AI 已标记报废的条目——报废中的稿不算「站内已有同题」）。
func (s *Store) SimilarByTitle(kind, lang, title string, limit int) ([]SimilarRow, error) {
	tokens := similarTokens(title)
	if len(tokens) == 0 || limit <= 0 {
		return nil, nil
	}
	if len([]rune(strings.TrimSpace(title))) >= 3 {
		if fts := similarFTSTokens(tokens); len(fts) > 0 {
			if rows, err := s.similarFTS(kind, lang, fts, limit); err == nil {
				return rows, nil
			}
		}
	}
	return s.similarLike(kind, lang, tokens, limit)
}

// similarFTSTokens 过滤出 trigram 分词下有效的词元（≥3 字符；更短的在 FTS 里永远匹配不到）。
func similarFTSTokens(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if len([]rune(tok)) >= 3 {
			out = append(out, tok)
		}
		if len(out) >= 24 { // 长标题词元封顶，防止 MATCH 表达式膨胀
			break
		}
	}
	return out
}

// similarFTS FTS5 路径：`title : ("t1" OR "t2" ...)`，bm25 rank 升序（越负越相似），
// 分数按 rank/bestRank 归一化到 0..1。
func (s *Store) similarFTS(kind, lang string, tokens []string, limit int) ([]SimilarRow, error) {
	quoted := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		quoted = append(quoted, `"`+strings.ReplaceAll(tok, `"`, `""`)+`"`)
	}
	match := `title : (` + strings.Join(quoted, " OR ") + `)`
	// 排除 AI 已标记报废的条目（在子查询内过滤，避免 LIMIT 被排除项占坑）：
	// 别让 AI 把自己报废的稿当「站内已有同题」。
	where := `post_search MATCH ? AND type=? AND rowid NOT IN (SELECT id FROM posts WHERE discarded_at IS NOT NULL)`
	args := []any{match, kind}
	if lang != "" {
		where += ` AND lang=?`
		args = append(args, lang)
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT p.id, p.title, p.slug, p.status, p.lang, hit.rank
		FROM posts p
		JOIN (
			SELECT rowid, rank FROM post_search
			WHERE `+where+`
			ORDER BY rank LIMIT ?
		) hit ON hit.rowid = p.id
		ORDER BY hit.rank, p.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SimilarRow
	var ranks []float64
	for rows.Next() {
		var row SimilarRow
		var rank float64
		if err := rows.Scan(&row.ID, &row.Title, &row.Slug, &row.Status, &row.Lang, &rank); err != nil {
			return nil, err
		}
		ranks = append(ranks, rank)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		best := ranks[0] // bm25 rank 为负数，首行最负（最相似）
		for i := range out {
			out[i].Score = similarScore(ranks[i], best)
		}
	}
	return out, nil
}

// similarScore rank/bestRank ∈ (0,1]，两位小数；best 非负（异常数据）时全部记 1。
func similarScore(rank, best float64) float64 {
	if best >= 0 || rank >= 0 {
		return 1
	}
	return math.Round(rank/best*100) / 100
}

// similarLike 回退路径：LIKE 捞候选行，按「命中词元数 / 词元总数」计分排序。
func (s *Store) similarLike(kind, lang string, tokens []string, limit int) ([]SimilarRow, error) {
	// 与 FTS 路径同口径：排除 AI 已标记报废的条目。
	where := `p.type=? AND p.discarded_at IS NULL`
	args := []any{kind}
	if lang != "" {
		where += ` AND p.lang=?`
		args = append(args, lang)
	}
	likes := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		likes = append(likes, `lower(p.title) LIKE ?`)
		args = append(args, "%"+tok+"%")
	}
	where += ` AND (` + strings.Join(likes, " OR ") + `)`
	args = append(args, similarLikeCandidateCap)
	rows, err := s.db.Query(`SELECT p.id, p.title, p.slug, p.status, p.lang
		FROM posts p WHERE `+where+` ORDER BY p.updated_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SimilarRow
	for rows.Next() {
		var row SimilarRow
		if err := rows.Scan(&row.ID, &row.Title, &row.Slug, &row.Status, &row.Lang); err != nil {
			return nil, err
		}
		lower := strings.ToLower(row.Title)
		hit := 0
		for _, tok := range tokens {
			if strings.Contains(lower, tok) {
				hit++
			}
		}
		row.Score = math.Round(float64(hit)/float64(len(tokens))*100) / 100
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

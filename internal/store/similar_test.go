package store

import "testing"

// openEmptyStoreForSimilar 打开测试库并清掉演示内容，避免样板文章（gcms/SEO 主题）干扰查重断言。
func openEmptyStoreForSimilar(t *testing.T) *Store {
	t.Helper()
	st := openSeededTestStore(t)
	if err := st.ClearDemoContent(); err != nil {
		t.Fatalf("clear demo content: %v", err)
	}
	return st
}

// TestSimilarByTitleFTS 中文标题近似匹配：命中率最高的旧文排第一（score=1），
// 草稿也计入，且不跨内容类型。
func TestSimilarByTitleFTS(t *testing.T) {
	st := openEmptyStoreForSimilar(t)
	seed := []Post{
		{Type: "post", Lang: "zh", Slug: "gcms-guide", Title: "GCMS 内容管理入门教程", Status: "published"},
		{Type: "post", Lang: "zh", Slug: "gcms-deploy", Title: "GCMS 部署实践", Status: "draft"},
		{Type: "post", Lang: "zh", Slug: "totally-else", Title: "完全无关的旅行随笔", Status: "published"},
		{Type: "page", Lang: "zh", Slug: "about-gcms", Title: "GCMS 内容管理入门教程", Status: "published"},
	}
	for i := range seed {
		if _, err := st.CreatePost(&seed[i]); err != nil {
			t.Fatalf("seed %s: %v", seed[i].Slug, err)
		}
	}

	rows, err := st.SimilarByTitle("post", "zh", "GCMS 内容管理进阶教程", 5)
	if err != nil {
		t.Fatalf("similar: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("similar 没有返回近似行")
	}
	if rows[0].Slug != "gcms-guide" {
		t.Fatalf("top = %+v, want gcms-guide", rows[0])
	}
	if rows[0].Score != 1 {
		t.Fatalf("top score = %v, want 1", rows[0].Score)
	}
	for _, row := range rows {
		if row.Score < 0 || row.Score > 1 {
			t.Fatalf("score 越界：%+v", row)
		}
		if row.Slug == "about-gcms" {
			t.Fatalf("不该跨类型命中 page：%+v", row)
		}
		if row.Slug == "totally-else" {
			t.Fatalf("无关标题不该命中：%+v", row)
		}
	}
}

// TestSimilarByTitleIncludesDrafts 草稿必须计入查重结果。
func TestSimilarByTitleIncludesDrafts(t *testing.T) {
	st := openEmptyStoreForSimilar(t)
	if _, err := st.CreatePost(&Post{Type: "post", Lang: "zh", Slug: "draft-topic", Title: "站内查重草稿选题", Status: "draft"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := st.SimilarByTitle("post", "zh", "站内查重草稿选题指南", 5)
	if err != nil {
		t.Fatalf("similar: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Slug == "draft-topic" && row.Status == "draft" {
			found = true
		}
	}
	if !found {
		t.Fatalf("草稿未计入查重：%+v", rows)
	}
}

// TestSimilarByTitleShortFallback 标题 <3 字符：回退到分词 LIKE 重叠计数。
func TestSimilarByTitleShortFallback(t *testing.T) {
	st := openEmptyStoreForSimilar(t)
	if _, err := st.CreatePost(&Post{Type: "post", Lang: "zh", Slug: "seo-post", Title: "SEO 手册", Status: "published"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := st.SimilarByTitle("post", "zh", "SE", 5)
	if err != nil {
		t.Fatalf("similar short: %v", err)
	}
	if len(rows) != 1 || rows[0].Slug != "seo-post" {
		t.Fatalf("short fallback rows = %+v", rows)
	}
	if rows[0].Score != 1 { // 唯一词元 "se" 命中 → 1/1
		t.Fatalf("short fallback score = %v, want 1", rows[0].Score)
	}
}

// TestSimilarTokens 分词口径：拉丁按词、CJK 段滑动三字（≤3 字整段），去重小写。
func TestSimilarTokens(t *testing.T) {
	got := similarTokens("GCMS 内容管理 guide")
	want := map[string]bool{"gcms": true, "内容管": true, "容管理": true, "guide": true}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v", got)
	}
	for _, tok := range got {
		if !want[tok] {
			t.Fatalf("意外词元 %q（全部：%v）", tok, got)
		}
	}
	if toks := similarTokens("教程"); len(toks) != 1 || toks[0] != "教程" {
		t.Fatalf("短 CJK 段应整段保留：%v", toks)
	}
}

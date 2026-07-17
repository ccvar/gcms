package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSeedFactoryDemoProductsHaveNoPrice 工厂演示商品不填价格（外贸常态是「面议」，
// price 是自由文本字段），但规格 repeater 齐备；幂等：二次调用不重复写入。
func TestSeedFactoryDemoProductsHaveNoPrice(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.SeedFactoryDemoProducts(); err != nil {
		t.Fatalf("seed factory demo: %v", err)
	}
	for _, lang := range []string{"zh", "en"} {
		posts, err := st.ListAllByType("product", lang)
		if err != nil {
			t.Fatalf("list %s products: %v", lang, err)
		}
		if len(posts) != 6 {
			t.Fatalf("%s products = %d, want 6", lang, len(posts))
		}
		for _, p := range posts {
			if strings.Contains(p.Extra, `"price"`) {
				t.Fatalf("演示商品 %s/%s 不该带 price：%s", lang, p.Slug, p.Extra)
			}
			if !strings.Contains(p.Extra, `"specs"`) {
				t.Fatalf("演示商品 %s/%s 缺 specs：%s", lang, p.Slug, p.Extra)
			}
			if p.CoverImage == "" {
				t.Fatalf("演示商品 %s/%s 缺封面", lang, p.Slug)
			}
		}
	}
	// 幂等
	if err := st.SeedFactoryDemoProducts(); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if posts, _ := st.ListAllByType("product", "zh"); len(posts) != 6 {
		t.Fatalf("re-seed 后 zh products = %d, want 6（应幂等）", len(posts))
	}
}

// TestAdminContentSearchByTitle 扩展类型的后台列表：标题关键字搜索 + 状态过滤 + 分页，
// kind 用扩展类型键（product）也能走同一套查询；LIKE 通配符被转义。
func TestAdminContentSearchByTitle(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	seed := []*Post{
		{Type: "product", Lang: "zh", Slug: "p-a", Title: "精密轴承 6204", Status: "published", EditorMode: "markdown"},
		{Type: "product", Lang: "zh", Slug: "p-b", Title: "精密轴承 6301", Status: "draft", EditorMode: "markdown"},
		{Type: "product", Lang: "zh", Slug: "p-c", Title: "LED 工矿灯", Status: "published", EditorMode: "markdown"},
		{Type: "product", Lang: "zh", Slug: "p-d", Title: "100% 纯棉毛巾", Status: "published", EditorMode: "markdown"},
	}
	for i, p := range seed {
		if _, err := st.CreatePost(p); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	if n, err := st.CountAdminContentSearch("product", "zh", "", "", "轴承"); err != nil || n != 2 {
		t.Fatalf("搜索「轴承」= (%d, %v), want 2", n, err)
	}
	if n, err := st.CountAdminContentSearch("product", "zh", "published", "", "轴承"); err != nil || n != 1 {
		t.Fatalf("搜索「轴承」+published = (%d, %v), want 1", n, err)
	}
	// LIKE 通配符转义：% 是字面量，不该匹配全部。
	if n, err := st.CountAdminContentSearch("product", "zh", "", "", "%"); err != nil || n != 1 {
		t.Fatalf("搜索字面 %% = (%d, %v), want 1（仅「100%% 纯棉毛巾」）", n, err)
	}
	got, err := st.ListAdminContentSearch("product", "zh", "", "", "轴承", 0, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("搜索分页 limit=1 = (%d 条, %v), want 1", len(got), err)
	}
	if n, err := st.CountAdminContentSearch("product", "zh", "", "", "不存在的词"); err != nil || n != 0 {
		t.Fatalf("无命中 = (%d, %v), want 0", n, err)
	}
}

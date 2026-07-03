package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLastPublicUpdate(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Open 会种一篇默认「关于」页，先清空以测纯净状态。
	if _, err := st.db.Exec(`DELETE FROM posts`); err != nil {
		t.Fatal(err)
	}

	// 无任何内容 → false。
	if _, ok, err := st.LastPublicUpdate(); err != nil || ok {
		t.Fatalf("empty store: ok=%v err=%v, want ok=false", ok, err)
	}

	// 只有草稿 → 不计入（未对外）。
	if _, err := st.CreatePost(&Post{Type: "post", Lang: "zh", Slug: "d1", Title: "Draft", Status: "draft"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.LastPublicUpdate(); ok {
		t.Fatalf("draft-only should not count as public update")
	}

	// 定时（发布时间在未来）→ 尚未对外，不计入。
	if _, err := st.CreatePost(&Post{Type: "post", Lang: "zh", Slug: "future", Title: "Future", Status: "published", PublishedAt: time.Now().Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.LastPublicUpdate(); ok {
		t.Fatalf("future-scheduled published post should be excluded")
	}

	// 一篇已生效的已发布文章 → true，时间约等于现在。
	before := time.Now().Add(-3 * time.Second)
	if _, err := st.CreatePost(&Post{Type: "post", Lang: "zh", Slug: "p1", Title: "Live", Status: "published"}); err != nil {
		t.Fatal(err)
	}
	tt, ok, err := st.LastPublicUpdate()
	if err != nil || !ok {
		t.Fatalf("published post: ok=%v err=%v, want ok=true", ok, err)
	}
	if tt.Before(before) {
		t.Fatalf("returned update time %v is before %v", tt, before)
	}

	// 已发布的页面（非 post 类型）也算"对外内容"。
	if _, err := st.CreatePost(&Post{Type: "page", Lang: "en", Slug: "about", Title: "About", Status: "published"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.LastPublicUpdate(); !ok {
		t.Fatalf("published page should count as public content")
	}
}

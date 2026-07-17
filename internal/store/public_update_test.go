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

	// ---- 聚合口径：max(published_at, updated_at)，且扩展类型一并计入 ----
	// 把库里已有行统一钉到受控的过去时间，后面的断言不跟"刚创建≈现在"的时间赛跑。
	base := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second)
	if _, err := st.db.Exec(`UPDATE posts SET updated_at=?, published_at=?`, fmtTime(base), fmtTime(base)); err != nil {
		t.Fatal(err)
	}

	// 扩展内容类型（type=自定义 slug，与内置类型同存 posts 表）计入聚合。
	extID, err := st.CreatePost(&Post{Type: "product", Lang: "zh", Slug: "ext-1", Title: "Ext", Status: "published"})
	if err != nil {
		t.Fatal(err)
	}
	extAt := base.Add(2 * time.Hour)
	if _, err := st.db.Exec(`UPDATE posts SET updated_at=?, published_at=? WHERE id=?`, fmtTime(extAt), fmtTime(base), extID); err != nil {
		t.Fatal(err)
	}
	tt, ok, err = st.LastPublicUpdate()
	if err != nil || !ok {
		t.Fatalf("with ext content: ok=%v err=%v", ok, err)
	}
	if !tt.Equal(extAt) {
		t.Fatalf("ext-type update not aggregated: got %v, want %v", tt, extAt)
	}

	// 定时内容到点生效：前台可感知的变化时间是 published_at（updated_at 停在最后一次编辑），
	// 聚合应取两者较大者。
	schedID, err := st.CreatePost(&Post{Type: "post", Lang: "zh", Slug: "sched", Title: "Sched", Status: "published"})
	if err != nil {
		t.Fatal(err)
	}
	schedPub := base.Add(3 * time.Hour) // 已过去，且晚于 ext 行
	if _, err := st.db.Exec(`UPDATE posts SET published_at=?, updated_at=? WHERE id=?`,
		fmtTime(schedPub), fmtTime(base.Add(time.Hour)), schedID); err != nil {
		t.Fatal(err)
	}
	tt, ok, err = st.LastPublicUpdate()
	if err != nil || !ok {
		t.Fatalf("scheduled post gone live: ok=%v err=%v", ok, err)
	}
	if !tt.Equal(schedPub) {
		t.Fatalf("aggregate should use max(published_at, updated_at): got %v, want %v", tt, schedPub)
	}
}

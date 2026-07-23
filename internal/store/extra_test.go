package store

import (
	"path/filepath"
	"testing"
)

// TestPostExtraRoundTrip 验证「扩展」自定义字段（extra JSON 列）在
// CreatePost / GetPostByID / UpdatePost 以及重开库后都能完整保存。
func TestPostExtraRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	const extra = `{"price":199,"gallery":["/u/a.webp","/u/b.webp"]}`
	id, err := st.CreatePost(&Post{
		Type:   "product",
		Lang:   "zh",
		Slug:   "demo-product",
		Title:  "演示商品",
		Status: "published",
		Extra:  extra,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetPostByID(id)
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.Extra != extra {
		t.Fatalf("extra mismatch after create: got %q want %q", got.Extra, extra)
	}

	const extra2 = `{"price":259}`
	got.Extra = extra2
	if err := st.UpdatePost(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 重开库会再次跑 migrate()，验证幂等且数据持久。
	re, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = re.Close() })
	got2, err := re.GetPostByID(id)
	if err != nil || got2 == nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if got2.Extra != extra2 {
		t.Fatalf("extra mismatch after reopen: got %q want %q", got2.Extra, extra2)
	}

	// 内置类型默认空 extra，不受影响。
	pid, err := re.CreatePost(&Post{Type: "post", Lang: "zh", Slug: "plain", Title: "普通文章", Status: "published"})
	if err != nil {
		t.Fatalf("create plain: %v", err)
	}
	plain, err := re.GetPostByID(pid)
	if err != nil || plain == nil {
		t.Fatalf("get plain: %v", err)
	}
	if plain.Extra != "" {
		t.Fatalf("plain post extra should be empty, got %q", plain.Extra)
	}
}

// TestPostExtraColumnAddedToLegacyDB 模拟旧库（无 extra 列），验证
// 重开库时 migrate() 通过 addColumnIfMissing 自动补回该列——这正是多站点下
// 各站点库与未来新建站点获得新列的机制。
func TestPostExtraColumnAddedToLegacyDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := st.db.Exec(`ALTER TABLE posts DROP COLUMN extra`); err != nil {
		_ = st.Close()
		t.Skipf("当前 SQLite 不支持 DROP COLUMN，跳过旧库模拟: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	re, err := Open(path) // 应通过 addColumnIfMissing 重新补上 extra 列
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = re.Close() })

	id, err := re.CreatePost(&Post{Type: "product", Lang: "zh", Slug: "x", Title: "x", Status: "published", Extra: `{"a":1}`})
	if err != nil {
		t.Fatalf("create after re-add: %v", err)
	}
	got, err := re.GetPostByID(id)
	if err != nil || got == nil || got.Extra != `{"a":1}` {
		t.Fatalf("extra not restored after migrate: got=%+v err=%v", got, err)
	}
}

func TestCompareAndSetSettingTreatsMissingAsEmptyAndKeepsCacheCurrent(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const key = "test.compare_and_set.missing"
	updated, err := st.CompareAndSetSetting(key, "", "first")
	if err != nil || !updated {
		t.Fatalf("insert missing setting: updated=%v err=%v", updated, err)
	}
	if got := st.Setting(key); got != "first" {
		t.Fatalf("cached inserted setting = %q, want first", got)
	}
	updated, err = st.CompareAndSetSetting(key, "stale", "wrong")
	if err != nil || updated {
		t.Fatalf("stale setting compare: updated=%v err=%v", updated, err)
	}
	if got := st.Setting(key); got != "first" {
		t.Fatalf("stale compare changed cache to %q", got)
	}
	updated, err = st.CompareAndSetSetting(key, "first", "second")
	if err != nil || !updated {
		t.Fatalf("update matched setting: updated=%v err=%v", updated, err)
	}
	if got := st.Setting(key); got != "second" {
		t.Fatalf("cached updated setting = %q, want second", got)
	}
}

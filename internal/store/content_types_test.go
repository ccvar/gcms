package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestContentTypeCRUD 验证自定义类型定义的增改查删（含 key 冲突时的 upsert）。
func TestContentTypeCRUD(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.SaveContentType(&ContentTypeRow{
		Key: "recipe", Name: `{"zh":"菜谱"}`, URLPrefix: "recipe",
		Fields: `[{"key":"servings","type":"number"}]`, Searchable: true,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := st.GetContentType("recipe")
	if err != nil || got == nil || got.Key != "recipe" || !got.Searchable {
		t.Fatalf("get: err=%v row=%+v", err, got)
	}
	if !strings.Contains(got.Fields, "servings") {
		t.Fatalf("fields not stored: %q", got.Fields)
	}

	// 同 key 再保存 = 更新
	if err := st.SaveContentType(&ContentTypeRow{
		Key: "recipe", Name: `{"zh":"食谱"}`, URLPrefix: "recipe", Fields: "[]", Hierarchical: true,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ = st.GetContentType("recipe")
	if !strings.Contains(got.Name, "食谱") || !got.Hierarchical {
		t.Fatalf("upsert did not apply: %+v", got)
	}

	list, _ := st.ListContentTypes()
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}

	if err := st.DeleteContentType("recipe"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := st.GetContentType("recipe"); got != nil {
		t.Fatalf("recipe still present after delete")
	}
}

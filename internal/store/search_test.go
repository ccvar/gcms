package store

import (
	"path/filepath"
	"testing"
)

func TestSearchMatchesPostKeywords(t *testing.T) {
	st := openSeededTestStore(t)
	id, err := st.CreatePost(&Post{
		Type:     "post",
		Lang:     "zh",
		Slug:     "wallet-security",
		Title:    "安全入门",
		Excerpt:  "整理常见风险。",
		Content:  "正文不直接出现目标查询词。",
		Keywords: "Web3钱包,交易所安全",
		Status:   "published",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	posts, err := st.Search("zh", "Web3钱包", 100)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !hasPostID(posts, id) {
		t.Fatalf("search by keyword did not return post %d", id)
	}
}

func TestSearchIndexMigratesKeywordColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	id, err := st.CreatePost(&Post{
		Type:     "post",
		Lang:     "zh",
		Slug:     "wallet-migration",
		Title:    "迁移测试",
		Content:  "正文没有查询词。",
		Keywords: "Web3钱包",
		Status:   "published",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	for _, q := range []string{
		`DROP TRIGGER IF EXISTS posts_search_ai`,
		`DROP TRIGGER IF EXISTS posts_search_au`,
		`DROP TRIGGER IF EXISTS posts_search_ad`,
		`DROP TABLE IF EXISTS post_search`,
		`CREATE VIRTUAL TABLE post_search USING fts5(
			title, excerpt, content,
			lang UNINDEXED, type UNINDEXED, status UNINDEXED, published_at UNINDEXED,
			tokenize='trigram'
		)`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("simulate old search index: %v", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	posts, err := reopened.Search("zh", "Web3钱包", 100)
	if err != nil {
		t.Fatalf("search after migration: %v", err)
	}
	if !hasPostID(posts, id) {
		t.Fatalf("search by keyword after migration did not return post %d", id)
	}
}

func hasPostID(posts []*Post, id int64) bool {
	for _, p := range posts {
		if p.ID == id {
			return true
		}
	}
	return false
}

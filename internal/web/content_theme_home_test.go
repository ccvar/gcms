package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
)

// TestContentThemesHonorHomePostLimitWithMultipleFeatured 锁定独立内容骨架的
// 首页数量口径：后台设置 N 条时，渲染 1 篇主推 + N-1 篇列表；除主推外的
// 置顶文章仍进入列表，且第 N+1 篇不会越界出现。
func TestContentThemesHonorHomePostLimitWithMultipleFeatured(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(homePostsPerPageKey, "6"); err != nil {
		t.Fatalf("set home posts per page: %v", err)
	}

	now := time.Now().UTC().Add(-time.Hour)
	ids := make([]int64, 7)
	for i := range ids {
		title := fmt.Sprintf("内容骨架计数文章 %02d", i+1)
		id, err := s.store.CreatePost(&store.Post{
			Type:        "post",
			Lang:        "zh",
			Slug:        fmt.Sprintf("content-theme-count-%02d", i+1),
			Title:       title,
			Excerpt:     title + "摘要",
			Content:     title + "正文",
			Status:      "published",
			EditorMode:  "markdown",
			PublishedAt: now.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatalf("create post %d: %v", i+1, err)
		}
		ids[i] = id
	}
	// 最新 5 篇全部置顶：旧拆分会把其中 4 篇从列表中丢掉。
	for _, id := range ids[2:] {
		if err := s.store.SetFeatured(id, true); err != nil {
			t.Fatalf("feature post %d: %v", id, err)
		}
	}

	for _, theme := range []string{"field-ledger", "signal-archive", "paper-current", "night-watch", "orbit-index", "column-stage", "type-cascade"} {
		t.Run(theme, func(t *testing.T) {
			if err := s.store.SetSetting("theme", theme); err != nil {
				t.Fatalf("set theme: %v", err)
			}
			s.clearGeneratedCaches()

			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("home status = %d, body = %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for i := 2; i <= 7; i++ {
				want := fmt.Sprintf("内容骨架计数文章 %02d", i)
				if !strings.Contains(body, want) {
					t.Errorf("home missing %q", want)
				}
			}
			if strings.Contains(body, "内容骨架计数文章 01") {
				t.Error("home rendered the seventh article beyond home.posts_per_page=6")
			}
		})
	}
}

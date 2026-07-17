package store

import (
	"testing"
	"time"
)

// TestSetDiscardDraftOnly 红线：报废标记只能作用于草稿——已发布/定时内容在存储层被
// WHERE status='draft' 拒绝（返回 false），上层据此回 409 not_draft。
func TestSetDiscardDraftOnly(t *testing.T) {
	st := openEmptyStoreForSimilar(t)
	mk := func(slug, status string) int64 {
		id, err := st.CreatePost(&Post{Type: "post", Lang: "zh", Slug: slug, Title: "报废标记 " + slug, Status: status})
		if err != nil {
			t.Fatalf("seed %s: %v", slug, err)
		}
		return id
	}
	draft := mk("d1", "draft")
	published := mk("p1", "published")
	scheduled := mk("s1", "scheduled")

	ok, err := st.SetDiscard(draft, "重复选题")
	if err != nil || !ok {
		t.Fatalf("draft mark = %v %v, want true", ok, err)
	}
	p, _ := st.GetPostByID(draft)
	if !p.Discarded() || p.DiscardReason != "重复选题" || p.DiscardedAt.IsZero() {
		t.Fatalf("mark not persisted: %+v", p)
	}
	// 重复标记＝更新理由（幂等）
	if ok, err := st.SetDiscard(draft, "质量不可救"); err != nil || !ok {
		t.Fatalf("re-mark = %v %v, want true", ok, err)
	}
	p, _ = st.GetPostByID(draft)
	if p.DiscardReason != "质量不可救" {
		t.Fatalf("re-mark reason = %q, want 更新后的理由", p.DiscardReason)
	}

	for _, id := range []int64{published, scheduled} {
		if ok, err := st.SetDiscard(id, "不该被标记"); err != nil || ok {
			t.Fatalf("non-draft mark = %v %v, want false", ok, err)
		}
		p, _ := st.GetPostByID(id)
		if p.Discarded() {
			t.Fatalf("non-draft got marked: %+v", p)
		}
	}

	// 撤销：幂等且零破坏
	if err := st.ClearDiscard(draft); err != nil {
		t.Fatalf("clear: %v", err)
	}
	p, _ = st.GetPostByID(draft)
	if p.Discarded() || p.DiscardReason != "" {
		t.Fatalf("clear not effective: %+v", p)
	}
	if err := st.ClearDiscard(draft); err != nil {
		t.Fatalf("clear twice: %v", err)
	}
}

// TestPublishClearsDiscard 人的发布动作覆盖 AI 报废申请：UpdatePostFrom 把状态置为
// published 时同一条 UPDATE 清除标记；置为 scheduled 时保留标记，由 PublishDue 翻发布时清除。
func TestPublishClearsDiscard(t *testing.T) {
	st := openEmptyStoreForSimilar(t)
	id, err := st.CreatePost(&Post{Type: "post", Lang: "zh", Slug: "pub-clears", Title: "发布清标测试", Status: "draft"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if ok, _ := st.SetDiscard(id, "AI 认为可弃用"); !ok {
		t.Fatalf("mark failed")
	}

	// admin/API 发布路径（UpdatePost / UpdatePostFrom 是内容更新唯一入口）
	p, _ := st.GetPostByID(id)
	p.Status = "published"
	if err := st.UpdatePost(p); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, _ := st.GetPostByID(id)
	if got.Discarded() || got.DiscardReason != "" {
		t.Fatalf("publish did not clear mark: %+v", got)
	}
	if p.Discarded() { // 内存对象同步清空
		t.Fatalf("in-memory post still marked after publish")
	}

	// 定时翻发布路径：标记草稿 → 人为定时（标记保留）→ PublishDue 翻发布（标记清除）
	id2, err := st.CreatePost(&Post{Type: "post", Lang: "zh", Slug: "sched-clears", Title: "定时清标测试", Status: "draft"})
	if err != nil {
		t.Fatalf("seed2: %v", err)
	}
	if ok, _ := st.SetDiscard(id2, "AI 认为可弃用"); !ok {
		t.Fatalf("mark2 failed")
	}
	p2, _ := st.GetPostByID(id2)
	p2.Status = "scheduled"
	p2.PublishedAt = time.Now().Add(-time.Minute)
	if err := st.UpdatePost(p2); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if got, _ := st.GetPostByID(id2); !got.Discarded() {
		t.Fatalf("scheduling should keep the mark until it flips: %+v", got)
	}
	due, err := st.PublishDue()
	if err != nil {
		t.Fatalf("publish due: %v", err)
	}
	if len(due) != 1 || due[0].ID != id2 || due[0].Discarded() {
		t.Fatalf("due = %+v, want flipped id2 with cleared mark", due)
	}
	if got, _ := st.GetPostByID(id2); got.Status != "published" || got.Discarded() || got.DiscardReason != "" {
		t.Fatalf("PublishDue did not clear mark: %+v", got)
	}
}

// TestSimilarByTitleExcludesDiscarded 查重排除：AI 已标记报废的稿不算「站内已有同题」
// （FTS 与 LIKE 回退两条路径同口径）。
func TestSimilarByTitleExcludesDiscarded(t *testing.T) {
	st := openEmptyStoreForSimilar(t)
	id, err := st.CreatePost(&Post{Type: "post", Lang: "zh", Slug: "dup-topic", Title: "站内查重报废排除教程", Status: "draft"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	rows, err := st.SimilarByTitle("post", "zh", "站内查重报废排除指南", 5)
	if err != nil || len(rows) == 0 {
		t.Fatalf("before mark: rows=%v err=%v, want hit", rows, err)
	}

	if ok, _ := st.SetDiscard(id, "重复选题"); !ok {
		t.Fatalf("mark failed")
	}
	rows, err = st.SimilarByTitle("post", "zh", "站内查重报废排除指南", 5)
	if err != nil {
		t.Fatalf("after mark: %v", err)
	}
	for _, row := range rows {
		if row.ID == id {
			t.Fatalf("discarded item still in similar rows: %+v", row)
		}
	}

	// LIKE 回退路径（标题 <3 字符走不了 FTS）：同样排除
	id2, err := st.CreatePost(&Post{Type: "post", Lang: "zh", Slug: "se-note", Title: "SE 优化短札", Status: "draft"})
	if err != nil {
		t.Fatalf("seed2: %v", err)
	}
	if ok, _ := st.SetDiscard(id2, "短稿弃用"); !ok {
		t.Fatalf("mark2 failed")
	}
	rows, err = st.SimilarByTitle("post", "zh", "SE", 5)
	if err != nil {
		t.Fatalf("like fallback: %v", err)
	}
	for _, row := range rows {
		if row.ID == id2 {
			t.Fatalf("discarded item leaked via LIKE fallback: %+v", row)
		}
	}
}

// TestPurgeDiscardedDrafts 批量清空只删「标记中的草稿」：未标记草稿、已标记后被
// 定时的内容、其它语种/类型均不动。
func TestPurgeDiscardedDrafts(t *testing.T) {
	st := openEmptyStoreForSimilar(t)
	mk := func(kind, lang, slug, status string) int64 {
		id, err := st.CreatePost(&Post{Type: kind, Lang: lang, Slug: slug, Title: "清空测试 " + slug, Status: status})
		if err != nil {
			t.Fatalf("seed %s: %v", slug, err)
		}
		return id
	}
	markedDraft1 := mk("post", "zh", "m1", "draft")
	markedDraft2 := mk("post", "zh", "m2", "draft")
	plainDraft := mk("post", "zh", "plain", "draft")
	otherLang := mk("post", "en", "m-en", "draft")
	otherKind := mk("page", "zh", "m-page", "draft")
	for _, id := range []int64{markedDraft1, markedDraft2, otherLang, otherKind} {
		if ok, _ := st.SetDiscard(id, "批量清空测试"); !ok {
			t.Fatalf("mark %d failed", id)
		}
	}
	// 标记后被人为定时：不属于「标记中的草稿」，不删
	markedScheduled := mk("post", "zh", "m-sched", "draft")
	if ok, _ := st.SetDiscard(markedScheduled, "先标记再定时"); !ok {
		t.Fatalf("mark scheduled failed")
	}
	ps, _ := st.GetPostByID(markedScheduled)
	ps.Status = "scheduled"
	ps.PublishedAt = time.Now().Add(time.Hour)
	if err := st.UpdatePost(ps); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	if n, err := st.CountDiscardedDrafts("post", "zh"); err != nil || n != 2 {
		t.Fatalf("count = %d %v, want 2", n, err)
	}
	n, err := st.PurgeDiscardedDrafts("post", "zh")
	if err != nil || n != 2 {
		t.Fatalf("purge = %d %v, want 2", n, err)
	}
	for _, id := range []int64{markedDraft1, markedDraft2} {
		if p, _ := st.GetPostByID(id); p != nil {
			t.Fatalf("marked draft %d survived purge", id)
		}
	}
	for _, id := range []int64{plainDraft, otherLang, otherKind, markedScheduled} {
		if p, _ := st.GetPostByID(id); p == nil {
			t.Fatalf("post %d wrongly purged", id)
		}
	}
	if n, _ := st.CountDiscardedDrafts("post", "zh"); n != 0 {
		t.Fatalf("count after purge = %d, want 0", n)
	}
}

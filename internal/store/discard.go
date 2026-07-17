package store

// discard.go AI 报废申请（标记删除）：Pilot 的 AI 写作会产生废稿，而 AI 没有删除权
// （托管红线）——这里给 AI 一个「申请报废」出口：只能标记 + 写理由，删除永远由管理员执行。
// 红线：标记只能作用于草稿（SetDiscard 的 WHERE status='draft' 是最后一道硬校验）；
// 标记动作零破坏可撤销（不动 updated_at、不写修订快照）；任何发布路径自动清除标记
// （见 UpdatePostFrom / PublishDue）。

import "time"

// SetDiscard 给一篇「草稿」打 AI 报废标记（写理由 + 时间）。
// 返回 false 表示目标当前不是草稿（或不存在），上层应回 409 not_draft。
// 重复标记＝更新理由与时间（幂等）。
func (s *Store) SetDiscard(id int64, reason string) (bool, error) {
	res, err := s.db.Exec(`UPDATE posts SET discard_reason=?, discarded_at=? WHERE id=? AND status='draft'`,
		reason, fmtTime(time.Now()), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ClearDiscard 撤销 AI 报废标记（幂等；未标记时空转）。
func (s *Store) ClearDiscard(id int64) error {
	_, err := s.db.Exec(`UPDATE posts SET discard_reason='', discarded_at=NULL WHERE id=?`, id)
	return err
}

// CountDiscardedDrafts 统计某类型某语种「待清理」条数——已标记且仍是草稿，
// 即批量清空按钮将删除的确切数量。
func (s *Store) CountDiscardedDrafts(kind, lang string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE type=? AND lang=? AND status='draft' AND discarded_at IS NOT NULL`,
		kind, lang).Scan(&n)
	return n, err
}

// PurgeDiscardedDrafts 批量删除某类型某语种下「已标记的草稿」，返回删除条数。
// 只删标记中的草稿：非草稿即使带着残留标记也绝不删。
func (s *Store) PurgeDiscardedDrafts(kind, lang string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM posts WHERE type=? AND lang=? AND status='draft' AND discarded_at IS NOT NULL`,
		kind, lang)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

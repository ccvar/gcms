package web

import (
	"fmt"

	"cms.ccvar.com/internal/store"
)

// invalidatePageProjectDraft refreshes only draft-scoped state.
//
// Page project revisions and signed previews are revision-addressed and are
// read directly from SQLite today, so there is no shared in-memory draft cache
// to evict. Keeping this as an explicit no-op is intentional: draft autosave
// must not clear public HTML caches or schedule a Cloudflare deployment.
func (s *Server) invalidatePageProjectDraft() {}

// invalidatePageProjectPublication is called only after the public revision
// pointer has changed atomically. Existing public cache invalidation also marks
// a configured Cloudflare static site pending (or schedules it according to the
// site's sync policy), which is exactly the publication boundary.
func (s *Server) invalidatePageProjectPublication() {
	s.clearGeneratedCaches()
}

// initialPagePublicationDeliveryStatus reports whether the source server is
// already the final delivery target. A configured Cloudflare static frontend
// needs a separate deployment; a dynamic-only site is live as soon as the
// publication transaction commits.
func (s *Server) initialPagePublicationDeliveryStatus() string {
	if s.cloudflareConfig().configured() {
		return store.PageDeliveryQueued
	}
	return store.PageDeliveryLive
}

// beginQueuedPageProjectDeliveries binds the publications included in the
// current immutable export snapshot to one deployment job. Publications that
// were superseded before a deployment are marked failed rather than falsely
// reported as delivered.
func (s *Server) beginQueuedPageProjectDeliveries(jobID string) ([]int64, error) {
	if jobID == "" {
		return nil, fmt.Errorf("deployment job id is required")
	}
	projects, err := s.store.ListPageProjects()
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, project := range projects {
		if project == nil {
			continue
		}
		publications, err := s.store.ListPagePublications(project.ID, 500)
		if err != nil {
			return nil, err
		}
		for _, publication := range publications {
			if publication == nil ||
				publication.Status != store.PagePublicationPublished ||
				publication.DeliveryStatus != store.PageDeliveryQueued {
				continue
			}
			if publication.RevisionID != project.PublishedRevisionID {
				if _, err := s.store.UpdatePagePublicationDelivery(
					publication.ID, store.PageDeliveryFailed, "superseded:"+jobID,
				); err != nil {
					return nil, err
				}
				continue
			}
			if _, err := s.store.UpdatePagePublicationDelivery(
				publication.ID, store.PageDeliveryQueued, jobID,
			); err != nil {
				return nil, err
			}
			ids = append(ids, publication.ID)
		}
	}
	return ids, nil
}

func (s *Server) finishPageProjectDeliveries(ids []int64, jobID, status string) {
	if status != store.PageDeliveryLive && status != store.PageDeliveryFailed {
		return
	}
	for _, id := range ids {
		publication, err := s.store.GetPagePublication(id)
		if err != nil || publication == nil ||
			publication.DeliveryStatus != store.PageDeliveryQueued ||
			publication.DeploymentJobID != jobID {
			continue
		}
		_, _ = s.store.UpdatePagePublicationDelivery(id, status, jobID)
	}
}

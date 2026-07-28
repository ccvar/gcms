package web

import (
	"errors"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

func TestPageStaticExportKeepsStandardAndPrivateConversionDraftsCompatible(t *testing.T) {
	s := newTestPublicServer(t, "")
	post := createStaticExportPageProjectPost(t, s, "conversion-draft")
	if _, err := s.store.CreatePageProject(store.CreatePageProjectInput{
		PostID: post.ID, Mode: store.PageModeComposition, SchemaVersion: 1,
		ShellMode: store.PageShellSite, CreatedBy: store.PageOriginAdmin,
	}); err != nil {
		t.Fatalf("create private conversion draft: %v", err)
	}

	var rendered []string
	err := s.exportPagePages(func(_ string, output string) error {
		rendered = append(rendered, output)
		return nil
	}, "zh", "/zh")
	if err != nil {
		t.Fatalf("standard-compatible export: %v", err)
	}
	if !containsStr(rendered, "/zh/conversion-draft/index.html") {
		t.Fatalf("private conversion draft hid the standard public page: %v", rendered)
	}
}

func TestPageStaticExportFailsClosedForUnimplementedPublishedModes(t *testing.T) {
	for _, mode := range []string{store.PageModeComposition, store.PageModeApp} {
		t.Run(mode, func(t *testing.T) {
			s := newTestPublicServer(t, "")
			post := createStaticExportPageProjectPost(t, s, "published-"+mode)
			project, err := s.store.CreatePageProject(store.CreatePageProjectInput{
				PostID: post.ID, Mode: mode, SchemaVersion: 1,
				ShellMode: store.PageShellSite, CreatedBy: store.PageOriginAdmin,
			})
			if err != nil {
				t.Fatalf("create project: %v", err)
			}
			metaJSON, err := store.PageRevisionMetaFromPost(post).CanonicalJSON()
			if err != nil {
				t.Fatalf("page metadata: %v", err)
			}
			revision, _, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
				ProjectID: project.ID, RevisionKind: mode,
				PageMetaJSON: metaJSON, ManifestJSON: `{"schema_version":1,"sections":[]}`,
				Origin: store.PageOriginAdmin, ActorID: "admin",
				RequestID:      "static-export-revision-" + mode,
				ValidationJSON: `{"valid":true}`,
			})
			if err != nil {
				t.Fatalf("create revision: %v", err)
			}
			build, err := s.store.CreatePageBuild(store.CreatePageBuildInput{
				ProjectID: project.ID, RevisionID: revision.ID,
				Status: store.PageBuildReady, RuntimeVersion: mode + "-v1",
				ArtifactRef: func() string {
					if mode == store.PageModeApp {
						return "artifacts/test-app"
					}
					return ""
				}(),
				ArtifactHash: func() string {
					if mode == store.PageModeApp {
						return store.SHA256Hex([]byte("test-app-artifact"))
					}
					return ""
				}(),
			})
			if err != nil {
				t.Fatalf("create build: %v", err)
			}
			if _, _, err := s.store.PublishPageProject(store.PublishPageProjectInput{
				ProjectID: project.ID, RevisionID: revision.ID, BuildID: build.ID,
				ExpectedWorkingRevisionID: revision.ID,
				Action:                    store.PagePublicationPublish, ApprovalID: "admin-session",
				ActorID: "admin", Origin: store.PageOriginAdmin,
				RequestID:      "static-export-publication-" + mode,
				DeliveryStatus: store.PageDeliveryQueued,
			}); err != nil {
				t.Fatalf("publish fixture: %v", err)
			}

			var rendered []string
			err = s.exportPagePages(func(_ string, output string) error {
				rendered = append(rendered, output)
				return nil
			}, "zh", "/zh")
			if !errors.Is(err, ErrPageProjectStaticExportUnavailable) {
				t.Fatalf("export error = %v, want unavailable", err)
			}
			var unavailable *PageProjectStaticExportUnavailableError
			if !errors.As(err, &unavailable) ||
				unavailable.ProjectID != project.ID ||
				unavailable.RevisionID != revision.ID ||
				unavailable.Mode != mode {
				t.Fatalf("unavailable error lacks project identity: %#v", err)
			}
			if strings.Contains(strings.Join(rendered, "\n"), "/published-"+mode+"/index.html") {
				t.Fatalf("%s page was exported through the standard fallback", mode)
			}
		})
	}
}

func createStaticExportPageProjectPost(t *testing.T, s *Server, slug string) *store.Post {
	t.Helper()
	id, err := s.store.CreatePost(&store.Post{
		Type: "page", Slug: slug, Title: slug, Content: "standard fallback",
		Status: "published", EditorMode: "markdown", Lang: "zh", Author: "tester",
	})
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	post, err := s.store.GetPostByID(id)
	if err != nil || post == nil {
		t.Fatalf("read page: post=%+v err=%v", post, err)
	}
	return post
}

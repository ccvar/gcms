package backup

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
)

func TestPageProjectBackupPathsUsePrivateSiteRoot(t *testing.T) {
	siteDir := filepath.Join(t.TempDir(), "sites", "docs")
	site := &platform.Site{Slug: "docs", DBPath: filepath.Join(siteDir, "cms.db")}
	paths := PageProjectBackupPaths(site)
	if len(paths) != 1 {
		t.Fatalf("paths = %#v, want one private project root", paths)
	}
	if got, want := paths[0].SourcePath, filepath.Join(siteDir, PageProjectsDirectoryName); got != want {
		t.Fatalf("source path = %q, want %q", got, want)
	}
	if got, want := paths[0].ArchivePath, "sites/docs/page-projects"; got != want {
		t.Fatalf("archive path = %q, want %q", got, want)
	}
	if paths[0].Kind != "site_page_projects" {
		t.Fatalf("kind = %q", paths[0].Kind)
	}
	if paths := PageProjectBackupPaths(&platform.Site{Slug: "no-db"}); len(paths) != 0 {
		t.Fatalf("site without a database unexpectedly has private paths: %#v", paths)
	}
}

func TestPlatformBackupRestoresPageProjectDatabaseAndPrivateFiles(t *testing.T) {
	root := t.TempDir()
	siteDir := filepath.Join(root, "sites", "docs")
	dbPath := filepath.Join(siteDir, "cms.db")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open site store: %v", err)
	}

	postID, err := st.CreatePost(&store.Post{
		Type:       "page",
		Slug:       "campaign",
		Title:      "Campaign",
		Content:    "standard fallback remains intact",
		Status:     "draft",
		EditorMode: "markdown",
		Lang:       "zh",
		Author:     "tester",
	})
	if err != nil {
		_ = st.Close()
		t.Fatalf("create page: %v", err)
	}
	post, err := st.GetPostByID(postID)
	if err != nil {
		_ = st.Close()
		t.Fatalf("read page: %v", err)
	}
	project, err := st.CreatePageProject(store.CreatePageProjectInput{
		PostID: postID, Mode: store.PageModeComposition, SchemaVersion: 1,
		ShellMode: store.PageShellSite, CreatedBy: store.PageOriginAdmin,
	})
	if err != nil {
		_ = st.Close()
		t.Fatalf("create project: %v", err)
	}
	metaJSON, err := store.PageRevisionMetaFromPost(post).CanonicalJSON()
	if err != nil {
		_ = st.Close()
		t.Fatalf("canonical page metadata: %v", err)
	}
	revision, _, err := st.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: project.ID, RevisionKind: store.PageRevisionComposition,
		PageMetaJSON: metaJSON, ManifestJSON: `{"schema_version":1,"sections":[]}`,
		Origin: store.PageOriginAdmin, ActorID: "admin", RequestID: "backup-revision",
	})
	if err != nil {
		_ = st.Close()
		t.Fatalf("create revision: %v", err)
	}

	privateFiles := map[string]string{
		"blobs/sha256-image":                     "blob-bytes",
		"sources/project-1/revision-1/source.js": "export const page = true;",
		"artifacts/build-1/index.html":           "<main>immutable artifact</main>",
	}
	projectRoot := filepath.Join(siteDir, PageProjectsDirectoryName)
	for rel, body := range privateFiles {
		path := filepath.Join(projectRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			_ = st.Close()
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			_ = st.Close()
			t.Fatal(err)
		}
	}

	backupDir := filepath.Join(root, "backups")
	rec, err := CreatePlatformBackup(Options{
		BackupDir: backupDir,
		Sites: []*platform.Site{{
			ID: 7, Slug: "docs", Name: "Docs", Status: "enabled",
			DBPath: dbPath, UploadDir: filepath.Join(siteDir, "uploads"),
		}},
	})
	if err != nil {
		_ = st.Close()
		t.Fatalf("create backup: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close source store: %v", err)
	}

	restoreRoot := filepath.Join(root, "restored")
	restoreArchivePrefix(t, ZipPath(backupDir, rec.Name), "sites/docs/", restoreRoot)
	restoredDB := filepath.Join(restoreRoot, "cms.db")
	restoredStore, err := store.Open(restoredDB)
	if err != nil {
		t.Fatalf("open restored site store: %v", err)
	}
	defer restoredStore.Close()

	restoredProject, err := restoredStore.GetPageProjectByPostID(postID)
	if err != nil || restoredProject == nil {
		t.Fatalf("restored project = %+v, err = %v", restoredProject, err)
	}
	revisions, err := restoredStore.ListPageProjectRevisions(restoredProject.ID, 10)
	if err != nil || len(revisions) != 1 || revisions[0].ID != revision.ID {
		t.Fatalf("restored revisions = %+v, err = %v", revisions, err)
	}
	for rel, want := range privateFiles {
		data, err := os.ReadFile(filepath.Join(restoreRoot, PageProjectsDirectoryName, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read restored private file %s: %v", rel, err)
		}
		if string(data) != want {
			t.Fatalf("restored %s = %q, want %q", rel, data, want)
		}
	}
}

func restoreArchivePrefix(t *testing.T, zipPath, prefix, target string) {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, file := range zr.File {
		if !strings.HasPrefix(file.Name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(file.Name, prefix)
		if rel == "" || strings.HasPrefix(rel, "../") {
			continue
		}
		dst := filepath.Join(target, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		src, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			_ = src.Close()
			t.Fatal(err)
		}
		_, copyErr := io.Copy(out, src)
		closeErr := out.Close()
		_ = src.Close()
		if copyErr != nil {
			t.Fatal(copyErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}

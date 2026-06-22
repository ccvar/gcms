package backup

import (
	"archive/zip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/version"

	_ "modernc.org/sqlite"
)

const (
	ConfigSettingKey = "backup.config"
	DefaultKeep      = 8
)

type Config struct {
	AutoSync        bool   `json:"auto_sync"`
	KeepLocal       int    `json:"keep_local"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	PathStyle       bool   `json:"path_style"`
}

func ParseConfig(raw string) Config {
	cfg := Config{KeepLocal: DefaultKeep, Region: "auto", PathStyle: true}
	_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &cfg)
	return NormalizeConfig(cfg)
}

func NormalizeConfig(cfg Config) Config {
	if cfg.KeepLocal < 1 {
		cfg.KeepLocal = DefaultKeep
	}
	if strings.TrimSpace(cfg.Region) == "" {
		cfg.Region = "auto"
	}
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.Region = strings.TrimSpace(cfg.Region)
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	cfg.Prefix = strings.Trim(strings.TrimSpace(cfg.Prefix), "/")
	cfg.AccessKeyID = strings.TrimSpace(cfg.AccessKeyID)
	cfg.SecretAccessKey = strings.TrimSpace(cfg.SecretAccessKey)
	return cfg
}

func (c Config) RemoteConfigured() bool {
	return strings.TrimSpace(c.Endpoint) != "" &&
		strings.TrimSpace(c.Bucket) != "" &&
		strings.TrimSpace(c.AccessKeyID) != "" &&
		strings.TrimSpace(c.SecretAccessKey) != ""
}

func (c Config) Sanitized() Config {
	c.SecretAccessKey = ""
	return c
}

type Options struct {
	BackupDir    string
	SystemDBPath string
	Sites        []*platform.Site
	Archived     []*platform.ArchivedSite
}

type Manifest struct {
	FormatVersion int               `json:"format_version"`
	AppVersion    version.Info      `json:"app_version"`
	CreatedAt     time.Time         `json:"created_at"`
	Kind          string            `json:"kind"`
	SystemDB      string            `json:"system_db"`
	Sites         []ManifestSite    `json:"sites"`
	ArchivedSites []ManifestArchive `json:"archived_sites,omitempty"`
	Entries       []ManifestEntry   `json:"entries"`
	Warnings      []string          `json:"warnings,omitempty"`
}

type ManifestSite struct {
	ID        int64  `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	IsDefault bool   `json:"is_default"`
	DBPath    string `json:"db_path"`
	UploadDir string `json:"upload_dir"`
}

type ManifestArchive struct {
	ID          int64     `json:"id"`
	OriginalID  int64     `json:"original_id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	ArchivePath string    `json:"archive_path"`
	ArchivedAt  time.Time `json:"archived_at"`
}

type ManifestEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type BackupRecord struct {
	Name      string        `json:"name"`
	Size      int64         `json:"size"`
	SHA256    string        `json:"sha256"`
	CreatedAt time.Time     `json:"created_at"`
	Sites     int           `json:"sites"`
	Entries   int           `json:"entries"`
	Remote    *RemoteStatus `json:"remote,omitempty"`
}

type RemoteStatus struct {
	Status    string    `json:"status"`
	ObjectKey string    `json:"object_key,omitempty"`
	SyncedAt  time.Time `json:"synced_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func CreatePlatformBackup(opts Options) (*BackupRecord, error) {
	if strings.TrimSpace(opts.BackupDir) == "" {
		return nil, errors.New("备份目录不能为空")
	}
	if err := os.MkdirAll(opts.BackupDir, 0o755); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	stamp := now.Format("20060102-150405")
	name := "gcms-platform-backup-" + stamp + ".zip"
	tmpDir, err := os.MkdirTemp(opts.BackupDir, ".building-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	tmpZip := filepath.Join(tmpDir, name)
	out, err := os.Create(tmpZip)
	if err != nil {
		return nil, err
	}
	zw := zip.NewWriter(out)
	manifest := Manifest{
		FormatVersion: 1,
		AppVersion:    version.Current(),
		CreatedAt:     now,
		Kind:          "platform",
		SystemDB:      opts.SystemDBPath,
	}
	addWarning := func(format string, args ...any) {
		manifest.Warnings = append(manifest.Warnings, fmt.Sprintf(format, args...))
	}

	if strings.TrimSpace(opts.SystemDBPath) != "" {
		if err := addSQLiteSnapshot(zw, tmpDir, opts.SystemDBPath, "system/system.db", "system_db", &manifest); err != nil {
			addWarning("system.db 备份失败：%v", err)
		}
	}

	for _, site := range opts.Sites {
		if site == nil {
			continue
		}
		siteInfo := ManifestSite{
			ID: site.ID, Slug: site.Slug, Name: site.Name, Status: site.Status, IsDefault: site.IsDefault,
			DBPath: site.DBPath, UploadDir: site.UploadDir,
		}
		manifest.Sites = append(manifest.Sites, siteInfo)
		base := "sites/" + safeZipSegment(site.Slug)
		if strings.TrimSpace(site.DBPath) != "" {
			if err := addSQLiteSnapshot(zw, tmpDir, site.DBPath, base+"/cms.db", "site_db", &manifest); err != nil {
				addWarning("站点 %s 数据库备份失败：%v", site.Slug, err)
			}
		}
		if strings.TrimSpace(site.UploadDir) != "" {
			if err := addDirectory(zw, site.UploadDir, base+"/uploads", "site_upload", &manifest); err != nil {
				addWarning("站点 %s 上传目录备份失败：%v", site.Slug, err)
			}
		}
	}

	for _, archived := range opts.Archived {
		if archived == nil {
			continue
		}
		manifest.ArchivedSites = append(manifest.ArchivedSites, ManifestArchive{
			ID: archived.ID, OriginalID: archived.OriginalSiteID, Slug: archived.Slug, Name: archived.Name,
			ArchivePath: archived.ArchivePath, ArchivedAt: archived.ArchivedAt,
		})
		if strings.TrimSpace(archived.ArchivePath) != "" {
			base := "archived/" + safeZipSegment(archived.Slug)
			if err := addDirectory(zw, archived.ArchivePath, base, "archived_site", &manifest); err != nil {
				addWarning("归档站点 %s 目录备份失败：%v", archived.Slug, err)
			}
		}
	}

	readme := strings.Join([]string{
		"GCMS platform backup",
		"",
		"Contents:",
		"- system/system.db: platform database",
		"- sites/{slug}/cms.db: site content database snapshots",
		"- sites/{slug}/uploads/: site uploaded files",
		"- archived/{slug}/: archived site directories when available",
		"",
		"Restore manually by stopping gcms, copying the databases and uploads back to their paths recorded in manifest.json, then starting gcms again.",
		"",
	}, "\n")
	if err := addBytes(zw, "README.txt", "readme", []byte(readme), &manifest); err != nil {
		_ = zw.Close()
		_ = out.Close()
		return nil, err
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = zw.Close()
		_ = out.Close()
		return nil, err
	}
	if err := addBytes(zw, "manifest.json", "manifest", manifestBytes, &manifest); err != nil {
		_ = zw.Close()
		_ = out.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		return nil, err
	}
	if err := out.Close(); err != nil {
		return nil, err
	}

	finalZip := filepath.Join(opts.BackupDir, name)
	if err := os.Rename(tmpZip, finalZip); err != nil {
		return nil, err
	}
	rec, err := recordForFile(finalZip, manifest)
	if err != nil {
		return nil, err
	}
	if err := WriteRecord(opts.BackupDir, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

func addSQLiteSnapshot(zw *zip.Writer, tmpDir, src, zipPath, kind string, manifest *Manifest) error {
	src = filepath.Clean(strings.TrimSpace(src))
	if src == "" || src == "." {
		return errors.New("数据库路径为空")
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("数据库路径是目录")
	}
	dst := filepath.Join(tmpDir, safeZipSegment(strings.TrimSuffix(filepath.Base(zipPath), ".db"))+"-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".db")
	db, err := sql.Open("sqlite", "file:"+src+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("VACUUM INTO ?", dst); err != nil {
		return err
	}
	return addFile(zw, dst, zipPath, kind, manifest)
}

func addDirectory(zw *zip.Writer, root, zipRoot, kind string, manifest *Manifest) error {
	root = filepath.Clean(strings.TrimSpace(root))
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return errors.New("不是目录")
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return addFile(zw, path, zipRoot+"/"+filepath.ToSlash(rel), kind, manifest)
	})
}

func addBytes(zw *zip.Writer, zipPath, kind string, data []byte, manifest *Manifest) error {
	h := sha256.Sum256(data)
	header := &zip.FileHeader{Name: cleanZipPath(zipPath), Method: zip.Deflate}
	header.SetModTime(time.Now().UTC())
	header.SetMode(0o644)
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	manifest.Entries = append(manifest.Entries, ManifestEntry{Path: header.Name, Kind: kind, Size: int64(len(data)), SHA256: hex.EncodeToString(h[:])})
	return nil
}

func addFile(zw *zip.Writer, src, zipPath, kind string, manifest *Manifest) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = cleanZipPath(zipPath)
	header.Method = zip.Deflate
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := copyHash(w, in, h)
	if err != nil {
		return err
	}
	manifest.Entries = append(manifest.Entries, ManifestEntry{Path: header.Name, Kind: kind, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
	return nil
}

func copyHash(dst io.Writer, src io.Reader, h hash.Hash) (int64, error) {
	return io.Copy(dst, io.TeeReader(src, h))
}

func ListRecords(backupDir string) ([]*BackupRecord, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*BackupRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".zip") || !ValidName(entry.Name()) {
			continue
		}
		rec, err := ReadRecord(backupDir, entry.Name())
		if err != nil {
			info, statErr := entry.Info()
			if statErr != nil {
				return nil, statErr
			}
			rec = &BackupRecord{Name: entry.Name(), Size: info.Size(), CreatedAt: info.ModTime()}
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func ReadRecord(backupDir, name string) (*BackupRecord, error) {
	if !ValidName(name) {
		return nil, errors.New("备份文件名无效")
	}
	data, err := os.ReadFile(recordPath(backupDir, name))
	if err != nil {
		return nil, err
	}
	var rec BackupRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	if rec.Name == "" {
		rec.Name = name
	}
	return &rec, nil
}

func WriteRecord(backupDir string, rec *BackupRecord) error {
	if rec == nil || !ValidName(rec.Name) {
		return errors.New("备份记录无效")
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(recordPath(backupDir, rec.Name), data, 0o600)
}

func DeleteRecord(backupDir, name string) error {
	if !ValidName(name) {
		return errors.New("备份文件名无效")
	}
	if err := os.Remove(ZipPath(backupDir, name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(recordPath(backupDir, name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func ApplyRetention(backupDir string, keep int) error {
	if keep < 1 {
		return nil
	}
	records, err := ListRecords(backupDir)
	if err != nil {
		return err
	}
	for i, rec := range records {
		if i < keep {
			continue
		}
		if err := DeleteRecord(backupDir, rec.Name); err != nil {
			return err
		}
	}
	return nil
}

func ZipPath(backupDir, name string) string {
	return filepath.Join(backupDir, filepath.Base(name))
}

func ValidName(name string) bool {
	name = filepath.Base(strings.TrimSpace(name))
	return strings.HasPrefix(name, "gcms-platform-backup-") && strings.HasSuffix(name, ".zip") &&
		!strings.ContainsAny(name, `/\`)
}

func recordForFile(path string, manifest Manifest) (*BackupRecord, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	h, err := fileSHA256(path)
	if err != nil {
		return nil, err
	}
	return &BackupRecord{
		Name:      filepath.Base(path),
		Size:      info.Size(),
		SHA256:    h,
		CreatedAt: manifest.CreatedAt,
		Sites:     len(manifest.Sites),
		Entries:   len(manifest.Entries),
	}, nil
}

func fileSHA256(path string) (string, error) {
	in, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer in.Close()
	h := sha256.New()
	if _, err := io.Copy(h, in); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func recordPath(backupDir, name string) string {
	return filepath.Join(backupDir, strings.TrimSuffix(filepath.Base(name), ".zip")+".json")
}

func safeZipSegment(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "item"
	}
	return b.String()
}

func cleanZipPath(p string) string {
	p = filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
	p = strings.TrimPrefix(p, "../")
	p = strings.TrimPrefix(p, "/")
	if p == "." || p == "" {
		return "file"
	}
	return p
}

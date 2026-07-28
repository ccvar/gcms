package web

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"cms.ccvar.com/internal/store"
)

var compositionAssetCommitMu sync.Mutex

type compositionAssetUpload struct {
	TempPath   string
	Filename   string
	LogicalKey string
	Origin     string
	Provenance json.RawMessage
	MediaType  string
	ByteSize   int64
	SHA256     string
	Width      int
	Height     int
}

type compositionAssetRequestIdentity struct {
	LogicalKey string          `json:"logical_key"`
	Origin     string          `json:"origin"`
	Provenance json.RawMessage `json:"provenance"`
	MediaType  string          `json:"media_type"`
	ByteSize   int64           `json:"byte_size"`
	SHA256     string          `json:"sha256"`
	Width      int             `json:"width"`
	Height     int             `json:"height"`
}

func readCompositionAssetTextPart(part *multipart.Part) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(part, 64<<10+1))
	if err != nil {
		return "", err
	}
	if len(raw) > 64<<10 {
		return "", errors.New("multipart field is too large")
	}
	return string(raw), nil
}

func readCompositionAssetMultipart(
	w http.ResponseWriter,
	r *http.Request,
	tempDir string,
	maxBytes int64,
) (compositionAssetUpload, error) {
	var out compositionAssetUpload
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		return out, errors.New("Content-Type 必须是 multipart/form-data")
	}
	seen := map[string]bool{}
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return out, nextErr
		}
		name := part.FormName()
		if name == "" || seen[name] {
			_ = part.Close()
			return out, errors.New("表单字段为空或重复")
		}
		seen[name] = true
		switch name {
		case "file":
			if part.FileName() == "" {
				_ = part.Close()
				return out, errors.New("file 字段必须包含文件")
			}
			file, createErr := os.CreateTemp(tempDir, ".upload-*")
			if createErr != nil {
				_ = part.Close()
				return out, createErr
			}
			out.TempPath = file.Name()
			out.Filename = path.Base(strings.ReplaceAll(part.FileName(), `\`, "/"))
			hash := sha256.New()
			written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(part, maxBytes+1))
			closeErr := file.Close()
			_ = part.Close()
			if copyErr != nil {
				return out, copyErr
			}
			if closeErr != nil {
				return out, closeErr
			}
			if written <= 0 || written > maxBytes {
				return out, errors.New("资源为空或超过大小限制")
			}
			out.ByteSize = written
			out.SHA256 = hex.EncodeToString(hash.Sum(nil))
		case "logical_key", "origin", "provenance":
			value, readErr := readCompositionAssetTextPart(part)
			_ = part.Close()
			if readErr != nil {
				return out, readErr
			}
			switch name {
			case "logical_key":
				out.LogicalKey = value
			case "origin":
				out.Origin = value
			case "provenance":
				out.Provenance = json.RawMessage(value)
			}
		default:
			_ = part.Close()
			return out, fmt.Errorf("未知 multipart 字段：%s", name)
		}
	}
	if out.TempPath == "" {
		return out, errors.New("缺少 file 字段")
	}
	out.LogicalKey = strings.TrimSpace(out.LogicalKey)
	if out.LogicalKey == "" {
		out.LogicalKey = out.Filename
	}
	out.Origin = strings.TrimSpace(out.Origin)
	if out.Origin == "" {
		out.Origin = "upload"
	}
	switch out.Origin {
	case "upload", "pilot", "generated", "library":
	default:
		return out, errors.New("origin 不在白名单中")
	}
	if len(out.Provenance) == 0 {
		out.Provenance = json.RawMessage(`{}`)
	}
	var provenance map[string]any
	if err := decodeCompositionStrict(out.Provenance, &provenance); err != nil || provenance == nil {
		return out, errors.New("provenance 必须是 JSON 对象")
	}
	return out, nil
}

func detectCompositionBitmap(filename string) (mediaType string, width, height int, err error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", 0, 0, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64<<10)
	peekSize := int(fileSize(filename))
	if peekSize > 64<<10 {
		peekSize = 64 << 10
	}
	head, err := reader.Peek(peekSize)
	if err != nil && !errors.Is(err, bufio.ErrBufferFull) && !errors.Is(err, io.EOF) {
		return "", 0, 0, err
	}
	switch {
	case len(head) >= 8 && string(head[:8]) == "\x89PNG\r\n\x1a\n":
		mediaType = "image/png"
	case len(head) >= 3 && head[0] == 0xff && head[1] == 0xd8 && head[2] == 0xff:
		mediaType = "image/jpeg"
	case len(head) >= 6 && (string(head[:6]) == "GIF87a" || string(head[:6]) == "GIF89a"):
		mediaType = "image/gif"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		width, height, err = compositionWebPDimensions(head)
		if err != nil {
			return "", 0, 0, err
		}
		if err := validateCompositionBitmapDimensions(width, height); err != nil {
			return "", 0, 0, err
		}
		return "image/webp", width, height, nil
	default:
		return "", 0, 0, errors.New("只允许 PNG、JPEG、GIF 或 WebP 位图")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, 0, err
	}
	config, format, err := image.DecodeConfig(file)
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return "", 0, 0, errors.New("图片内容损坏或格式不完整")
	}
	expectedFormat := strings.TrimPrefix(mediaType, "image/")
	if format != expectedFormat {
		return "", 0, 0, errors.New("图片魔数与解码格式不一致")
	}
	if err := validateCompositionBitmapDimensions(config.Width, config.Height); err != nil {
		return "", 0, 0, err
	}
	return mediaType, config.Width, config.Height, nil
}

func validateCompositionBitmapDimensions(width, height int) error {
	const (
		maxDimension = 16_384
		maxPixels    = 100_000_000
	)
	if width <= 0 || height <= 0 || width > maxDimension || height > maxDimension ||
		int64(width)*int64(height) > maxPixels {
		return errors.New("图片像素尺寸超过安全限制")
	}
	return nil
}

func fileSize(filename string) int64 {
	info, err := os.Stat(filename)
	if err != nil || info.Size() < 0 {
		return 0
	}
	return info.Size()
}

func compositionFileMatches(filename, wantHash string, wantSize int64) bool {
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() != wantSize {
		return false
	}
	file, err := os.Open(filename)
	if err != nil {
		return false
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, io.LimitReader(file, wantSize+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written != wantSize {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), wantHash)
}

func compositionWebPDimensions(raw []byte) (int, int, error) {
	for offset := 12; offset+8 <= len(raw); {
		chunkType := string(raw[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
		payload := offset + 8
		if size < 0 || payload+size > len(raw) {
			return 0, 0, errors.New("WebP 区块不完整")
		}
		chunk := raw[payload : payload+size]
		switch chunkType {
		case "VP8X":
			if len(chunk) < 10 {
				return 0, 0, errors.New("WebP VP8X 头无效")
			}
			width := 1 + int(chunk[4]) + (int(chunk[5]) << 8) + (int(chunk[6]) << 16)
			height := 1 + int(chunk[7]) + (int(chunk[8]) << 8) + (int(chunk[9]) << 16)
			return width, height, nil
		case "VP8L":
			if len(chunk) < 5 || chunk[0] != 0x2f {
				return 0, 0, errors.New("WebP VP8L 头无效")
			}
			width := 1 + int(chunk[1]) + ((int(chunk[2]) & 0x3f) << 8)
			height := 1 + (int(chunk[2]) >> 6) + (int(chunk[3]) << 2) + ((int(chunk[4]) & 0x0f) << 10)
			return width, height, nil
		case "VP8 ":
			if len(chunk) < 10 || chunk[3] != 0x9d || chunk[4] != 0x01 || chunk[5] != 0x2a {
				return 0, 0, errors.New("WebP VP8 头无效")
			}
			width := int(binary.LittleEndian.Uint16(chunk[6:8]) & 0x3fff)
			height := int(binary.LittleEndian.Uint16(chunk[8:10]) & 0x3fff)
			if width <= 0 || height <= 0 {
				return 0, 0, errors.New("WebP 尺寸无效")
			}
			return width, height, nil
		}
		offset = payload + size
		if size&1 == 1 {
			offset++
		}
	}
	return 0, 0, errors.New("WebP 缺少图像区块")
}

func (s *Server) createCompositionAssetUpload(
	w http.ResponseWriter,
	r *http.Request,
	project *store.PageProject,
	auth *automationAuth,
	requestID string,
) {
	if project == nil || project.Mode != store.PageModeComposition {
		apiError(w, http.StatusUnprocessableEntity, "page_mode_unsupported",
			"页面素材上传当前只用于自由编排工程。")
		return
	}
	root := s.store.PageProjectStorageDir()
	if strings.TrimSpace(root) == "" {
		apiError(w, http.StatusInternalServerError, "asset_storage_unavailable", "页面素材私有存储未初始化。")
		return
	}
	assetDir := filepath.Join(root, strconv.FormatInt(project.ID, 10), "assets")
	if err := os.MkdirAll(assetDir, 0o700); err != nil {
		apiError(w, http.StatusInternalServerError, "asset_storage_failed", "创建页面素材目录失败。")
		return
	}
	upload, err := readCompositionAssetMultipart(
		w, r, assetDir, pagePlatformServerLimits().MaxAssetBytes,
	)
	if upload.TempPath != "" {
		defer os.Remove(upload.TempPath)
	}
	if err != nil {
		apiError(w, http.StatusBadRequest, "asset_invalid", err.Error())
		return
	}
	upload.MediaType, upload.Width, upload.Height, err = detectCompositionBitmap(upload.TempPath)
	if err != nil {
		apiError(w, http.StatusUnprocessableEntity, "asset_invalid", err.Error())
		return
	}
	canonicalProvenance, _, err := store.CanonicalJSONHash(string(upload.Provenance))
	if err != nil {
		apiError(w, http.StatusUnprocessableEntity, "asset_invalid", "provenance 必须是 JSON 对象。")
		return
	}
	upload.Provenance = json.RawMessage(canonicalProvenance)
	requestIdentity, err := json.Marshal(compositionAssetRequestIdentity{
		LogicalKey: upload.LogicalKey, Origin: upload.Origin,
		Provenance: upload.Provenance, MediaType: upload.MediaType,
		ByteSize: upload.ByteSize, SHA256: upload.SHA256,
		Width: upload.Width, Height: upload.Height,
	})
	if err != nil {
		apiError(w, http.StatusInternalServerError, "asset_storage_failed", "生成素材请求摘要失败。")
		return
	}
	requestHash := store.SHA256Hex(requestIdentity)
	storageRef := path.Join(
		"page-projects", strconv.FormatInt(project.ID, 10), "assets", upload.SHA256,
	)
	input := pageAssetInput{
		LogicalKey: upload.LogicalKey, StorageRef: storageRef,
		MediaType: upload.MediaType, ByteSize: upload.ByteSize, SHA256: upload.SHA256,
		Origin: upload.Origin, Provenance: upload.Provenance,
		Width: upload.Width, Height: upload.Height,
	}
	// The DB row and content-addressed link cannot share one filesystem/SQLite
	// transaction. Serialize the short commit window so limit checks, linking,
	// and receipt creation observe a stable local-server state.
	compositionAssetCommitMu.Lock()
	defer compositionAssetCommitMu.Unlock()
	existing, err := s.store.ListPageAssets(project.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	for _, asset := range existing {
		if asset != nil && asset.SHA256 == upload.SHA256 {
			if _, err := s.readCompositionAsset(asset); err != nil {
				apiError(w, http.StatusConflict, "asset_storage_conflict",
					"相同哈希的既有素材记录缺少可验证文件，请先修复存储。")
				return
			}
			break
		}
	}
	if err := validPageAssetMetadata(project.ID, &input, existing); err != nil {
		apiError(w, http.StatusUnprocessableEntity, "asset_invalid", err.Error())
		return
	}
	finalPath := filepath.Join(assetDir, upload.SHA256)
	if _, statErr := os.Lstat(finalPath); statErr == nil {
		if !compositionFileMatches(finalPath, upload.SHA256, upload.ByteSize) {
			apiError(w, http.StatusConflict, "asset_storage_conflict", "素材目标路径已被其他文件占用。")
			return
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		apiError(w, http.StatusInternalServerError, "asset_storage_failed", "检查素材目标路径失败。")
		return
	} else {
		if err := os.Link(upload.TempPath, finalPath); err != nil {
			if !compositionFileMatches(finalPath, upload.SHA256, upload.ByteSize) {
				apiError(w, http.StatusInternalServerError, "asset_storage_failed", "提交不可变素材失败。")
				return
			}
		}
	}
	asset, created, err := s.store.CreatePageAsset(store.CreatePageAssetInput{
		ProjectID: project.ID, RequestID: requestID, RequestHash: requestHash,
		LogicalKey: input.LogicalKey, StorageRef: storageRef,
		MediaType: input.MediaType, ByteSize: input.ByteSize, SHA256: input.SHA256,
		Origin: input.Origin, ProvenanceJSON: pageJSON(input.Provenance, "{}"),
		Width: input.Width, Height: input.Height,
	})
	if err != nil {
		// Never remove a content-addressed final path after it has been linked.
		// A concurrent request may already have committed a DB row for the same
		// hash. Retaining an unreferenced immutable blob is safe and recoverable;
		// deleting a blob referenced by a winner would corrupt published pages.
		pageStoreError(w, err)
		return
	}
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	s.recordAutomationLog(auth, "create", "page_asset", asset.ID, "上传并登记页面素材")
	w.Header().Set("ETag", project.ETag())
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"asset": asset, "created": created})
}

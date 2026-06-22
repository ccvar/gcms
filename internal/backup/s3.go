package backup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

func SyncToS3(ctx context.Context, cfg Config, backupDir string, rec *BackupRecord) (*RemoteStatus, error) {
	if rec == nil || !ValidName(rec.Name) {
		return nil, fmt.Errorf("备份记录无效")
	}
	cfg = NormalizeConfig(cfg)
	if !cfg.RemoteConfigured() {
		return nil, fmt.Errorf("远程对象存储配置不完整")
	}
	zipPath := ZipPath(backupDir, rec.Name)
	info, err := os.Stat(zipPath)
	if err != nil {
		return nil, err
	}
	payloadHash, err := fileSHA256(zipPath)
	if err != nil {
		return nil, err
	}
	key := strings.Trim(path.Join(cfg.Prefix, rec.Name), "/")
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, fmt.Errorf("Endpoint 必须是完整 URL，例如 https://xxx.r2.cloudflarestorage.com")
	}
	objectURL := objectURL(*endpoint, cfg.Bucket, key, cfg.PathStyle)
	body, err := os.Open(zipPath)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, objectURL.String(), body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", "application/zip")
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	signS3Request(req, cfg, payloadHash, time.Now().UTC())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(readSmall(resp.Body, 2048))
		if msg == "" {
			msg = resp.Status
		}
		return &RemoteStatus{Status: "failed", ObjectKey: key, Error: msg}, fmt.Errorf("OSS 同步失败：HTTP %d", resp.StatusCode)
	}
	return &RemoteStatus{Status: "synced", ObjectKey: key, SyncedAt: time.Now().UTC()}, nil
}

func objectURL(endpoint url.URL, bucket, key string, pathStyle bool) url.URL {
	endpoint.Path = strings.TrimRight(endpoint.Path, "/")
	keyPath := escapePath(key)
	if pathStyle {
		endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/" + escapePath(bucket) + "/" + keyPath
		return endpoint
	}
	endpoint.Host = bucket + "." + endpoint.Host
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/" + keyPath
	return endpoint
}

func signS3Request(req *http.Request, cfg Config, payloadHash string, now time.Time) {
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "auto"
	}
	scope := date + "/" + region + "/s3/aws4_request"
	req.Header.Set("X-Amz-Date", amzDate)
	headers := map[string]string{
		"host":                 req.URL.Host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	signedHeaders := signedHeaderNames(headers)
	canonical := req.Method + "\n" +
		canonicalURI(req.URL.EscapedPath()) + "\n" +
		req.URL.RawQuery + "\n" +
		canonicalHeaders(headers) + "\n" +
		strings.Join(signedHeaders, ";") + "\n" +
		payloadHash
	sum := sha256.Sum256([]byte(canonical))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	signingKey := s3SigningKey(cfg.SecretAccessKey, date, region)
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+cfg.AccessKeyID+"/"+scope+", SignedHeaders="+strings.Join(signedHeaders, ";")+", Signature="+signature)
}

func canonicalURI(escapedPath string) string {
	if escapedPath == "" {
		return "/"
	}
	if !strings.HasPrefix(escapedPath, "/") {
		return "/" + escapedPath
	}
	return escapedPath
}

func canonicalHeaders(headers map[string]string) string {
	names := signedHeaderNames(headers)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(strings.TrimSpace(headers[name]))
		b.WriteByte('\n')
	}
	return b.String()
}

func signedHeaderNames(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	return names
}

func s3SigningKey(secret, date, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, "s3")
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(data))
	return h.Sum(nil)
}

func escapePath(raw string) string {
	parts := strings.Split(raw, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func readSmall(r io.Reader, limit int64) string {
	var buf bytes.Buffer
	_, _ = io.CopyN(&buf, r, limit)
	return buf.String()
}

package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"time"

	"cms.ccvar.com/internal/version"
)

const (
	updateChannelSetting       = "system.update_channel"
	updateChannelActivatedAt   = "system.preview_activated_at"
	updateChannelStable        = "stable"
	updateChannelPreview       = "preview"
	previewActivationRateLimit = 10
)

func normalizeUpdateChannel(channel string) string {
	if strings.EqualFold(strings.TrimSpace(channel), updateChannelPreview) {
		return updateChannelPreview
	}
	return updateChannelStable
}

func (s *Server) updateChannel() string {
	if s != nil && s.platform != nil {
		if value, ok, err := s.platform.LookupSetting(updateChannelSetting); err == nil && ok {
			return normalizeUpdateChannel(value)
		}
	}
	if s != nil && s.store != nil {
		return normalizeUpdateChannel(s.store.Setting(updateChannelSetting))
	}
	return updateChannelStable
}

func (s *Server) setUpdateChannel(channel string) error {
	channel = normalizeUpdateChannel(channel)
	if s.platform != nil {
		if err := s.platform.SetSetting(updateChannelSetting, channel); err != nil {
			return err
		}
		if channel == updateChannelPreview {
			return s.platform.SetSetting(updateChannelActivatedAt, time.Now().UTC().Format(time.RFC3339))
		}
		return nil
	}
	if err := s.store.SetSetting(updateChannelSetting, channel); err != nil {
		return err
	}
	if channel == updateChannelPreview {
		return s.store.SetSetting(updateChannelActivatedAt, time.Now().UTC().Format(time.RFC3339))
	}
	return nil
}

func previewActivationCodeHash() []byte {
	configured := strings.TrimSpace(os.Getenv("GCMS_PREVIEW_ACTIVATION_CODE_HASH"))
	if configured == "" {
		configured = strings.TrimSpace(version.PreviewActivationCodeHash)
	}
	decoded, err := hex.DecodeString(configured)
	if err != nil || len(decoded) != sha256.Size {
		return nil
	}
	return decoded
}

func validPreviewActivationCode(code string) bool {
	expected := previewActivationCodeHash()
	code = strings.TrimSpace(code)
	if len(expected) != sha256.Size || len(code) < 16 || len(code) > 256 {
		return false
	}
	actual := sha256.Sum256([]byte(code))
	return hmac.Equal(expected, actual[:])
}

func (s *Server) adminPreviewActivate(w http.ResponseWriter, r *http.Request) {
	s.adminSetPreviewChannel(w, r, updateChannelPreview)
}

func (s *Server) adminPreviewDeactivate(w http.ResponseWriter, r *http.Request) {
	s.adminSetPreviewChannel(w, r, updateChannelStable)
}

func (s *Server) adminSetPreviewChannel(w http.ResponseWriter, r *http.Request, channel string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if s.apiLimiter != nil {
		if retry, ok := s.apiLimiter.allow(
			"preview-activate:"+clientIP(r),
			previewActivationRateLimit,
			time.Minute,
		); !ok {
			apiRateLimitError(w, retry)
			return
		}
	}
	// 未配置哈希和错误 code 均返回 404，不向外部探测者暴露过渡版本能力。
	if !validPreviewActivationCode(r.URL.Query().Get("code")) {
		http.NotFound(w, r)
		return
	}
	channel = normalizeUpdateChannel(channel)
	if err := s.setUpdateChannel(channel); err != nil {
		s.serverError(w, err)
		return
	}
	message := "已切换到 Preview 更新通道。"
	location := "/admin/updates?preview=active"
	if channel == updateChannelStable {
		message = "已停止接收新的 Preview 更新；当前程序不会自动降级。"
		location = "/admin/updates?preview=left"
	}
	s.sess.setSettingsFlash(
		sessionToken(r),
		settingsFlash{Flash: message},
	)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (s *Server) currentUpdateInfo() *UpdateInfo {
	return currentUpdateInfoForChannel(s.updateChannel())
}

func (s *Server) checkLatestRelease(ctx context.Context) *UpdateInfo {
	return checkLatestReleaseForChannel(ctx, s.updateChannel())
}

func (s *Server) selectedUpdateManifestURL() string {
	return updateManifestURLForChannel(version.Current(), s.updateChannel())
}

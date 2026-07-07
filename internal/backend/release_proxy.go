package backend

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
)

var releaseDownloadBase = "https://github.com/Jkaotlic/wg-monitor/releases/download"

func releaseAssetProxyHandler(_ Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		version := strings.TrimSpace(r.PathValue("version"))
		asset := strings.TrimSpace(r.PathValue("asset"))
		version, tagErr := releaseorigin.ValidateReleaseTag(version)
		if tagErr != nil || !isAllowedReleaseAsset(asset) {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "invalid release asset")
			return
		}
		u := strings.TrimRight(releaseDownloadBase, "/") + "/" + url.PathEscape(version) + "/" + url.PathEscape(asset)
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		// Shares releaseFetchTransport (release_verify.go) — same CDN, same
		// flaky-home-uplink TLS-handshake fragility — but keeps its own
		// longer overall timeout since this proxies a full binary/archive,
		// not a small checksums file. Deliberately no retry here: unlike
		// fetchReleaseVerifyAsset's plain fetch, this handler already
		// distinguishes oversized-Content-Length, oversized-chunked, and
		// truncated-body failures with their own status codes/messages
		// (see wizard_handler_test.go's ReleaseAssetProxy* tests), and
		// retrying would risk conflating a retryable transport hiccup with
		// those non-retryable validation failures.
		resp, err := (&http.Client{Timeout: 90 * time.Second, Transport: releaseFetchTransport}).Do(req)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, errCodeInternal, "release fetch: "+err.Error())
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			writeJSONError(w, http.StatusBadGateway, errCodeInternal, "release fetch: HTTP "+resp.Status)
			return
		}
		if resp.ContentLength > maxSelfUpdateProxyBytes {
			writeJSONError(w, http.StatusBadGateway, errCodeInternal, "release fetch: response too large")
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxSelfUpdateProxyBytes+1))
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, errCodeInternal, "release fetch: "+err.Error())
			return
		}
		if int64(len(body)) > maxSelfUpdateProxyBytes {
			writeJSONError(w, http.StatusBadGateway, errCodeInternal, "release fetch: response too large")
			return
		}
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}
}

const maxSelfUpdateProxyBytes = 64 << 20

func isAllowedReleaseAsset(asset string) bool {
	switch asset {
	case "checksums.txt",
		"checksums.txt.sig",
		"wg-monitor-agent-linux-arm64",
		"wg-monitor-agent-linux-mipsle",
		"wg-monitor-backend-linux-amd64",
		"wg-monitor-backend-linux-arm64":
		return true
	default:
		return false
	}
}

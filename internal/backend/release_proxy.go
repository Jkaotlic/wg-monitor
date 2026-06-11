package backend

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var releaseDownloadBase = "https://github.com/Jkaotlic/wg-monitor/releases/download"

var releaseTagRE = regexp.MustCompile(`^v[0-9][0-9A-Za-z._-]*$`)

func releaseAssetProxyHandler(_ Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		version := strings.TrimSpace(r.PathValue("version"))
		asset := strings.TrimSpace(r.PathValue("asset"))
		if !releaseTagRE.MatchString(version) || !isAllowedReleaseAsset(asset) {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "invalid release asset")
			return
		}
		u := strings.TrimRight(releaseDownloadBase, "/") + "/" + url.PathEscape(version) + "/" + url.PathEscape(asset)
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, err.Error())
			return
		}
		resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, errCodeInternal, "release fetch: "+err.Error())
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			writeJSONError(w, http.StatusBadGateway, errCodeInternal, "release fetch: HTTP "+resp.Status)
			return
		}
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			w.Header().Set("Content-Length", cl)
		}
		_, _ = io.Copy(w, io.LimitReader(resp.Body, maxSelfUpdateProxyBytes))
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

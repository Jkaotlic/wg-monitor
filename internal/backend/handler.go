package backend

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/anex/wg-monitor/pkg/wire"
)

const maxReportBytes = 64 * 1024 // 64 KiB — heartbeat-only report is ~200 B

// NewMux builds the backend HTTP handler.
// tokenToNickname maps bearer-tokens to agent nicknames (loaded from config).
func NewMux(logger *slog.Logger, tokenToNickname map[string]string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	auth := AuthMiddleware(tokenToNickname)
	mux.Handle("/v1/report", auth(http.HandlerFunc(reportHandler(logger))))
	return mux
}

func reportHandler(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxReportBytes+1))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if len(body) > maxReportBytes {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		var rep wire.Report
		if err := json.Unmarshal(body, &rep); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		nick := NicknameFromContext(r.Context())
		logger.Info("report",
			"nickname", nick,
			"agent_version", rep.AgentVersion,
			"ts", rep.Timestamp,
			"check_count", len(rep.Checks),
			"checks", checkSummary(rep.Checks),
		)
		w.WriteHeader(http.StatusOK)
	}
}

func checkSummary(checks []wire.Check) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.Name + "=" + c.Status
	}
	return out
}

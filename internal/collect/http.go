package collect

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// InternalPrefix is where the internal endpoints live on the listener.
const InternalPrefix = "/internal/"

// SweepStatus is GET/POST /internal/sweep's answer.
type SweepStatus struct {
	Running bool                    `json:"running"`
	Started bool                    `json:"started,omitempty"`
	Last    *inventory.SweepSummary `json:"last,omitempty"`
}

// InternalHandler serves the sweep control behind a static bearer token; an
// empty token disables the endpoints (404). The reconciler does not call
// here: its runs are pulled from GitHub (RunReconcilerPoll).
func (c *Collector) InternalHandler(token string) http.Handler {
	token = strings.TrimSpace(token)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/sweep", func(w http.ResponseWriter, r *http.Request) {
		started := c.StartSweep()
		last, _ := c.store.Sweep(r.Context())
		status := http.StatusAccepted
		if !started {
			status = http.StatusConflict
		}
		writeJSON(w, status, SweepStatus{Running: true, Started: started, Last: last})
	})
	mux.HandleFunc("GET /internal/sweep", func(w http.ResponseWriter, r *http.Request) {
		last, err := c.store.Sweep(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, SweepStatus{Running: c.Running(), Last: last})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			writeJSON(w, http.StatusNotFound, errBody("internal endpoints are disabled (INTERNAL_TOKEN)"))
			return
		}
		got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="internal"`)
			writeJSON(w, http.StatusUnauthorized, errBody("a bearer token is required"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// errBody is the error answer of the internal endpoints.
func errBody(msg string) map[string]string { return map[string]string{"error": msg} }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

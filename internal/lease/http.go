package lease

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPWitness talks to `qabas dr witness serve` on a third host.
type HTTPWitness struct {
	Label  string
	URL    string
	Token  string
	Client *http.Client
}

func (h *HTTPWitness) Name() string { return h.Label }

func (h *HTTPWitness) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

func (h *HTTPWitness) endpoint() string { return strings.TrimRight(h.URL, "/") + "/v1/lease" }

func (h *HTTPWitness) do(ctx context.Context, method string, body []byte) (Record, int, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.endpoint(), rd)
	if err != nil {
		return Record{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client().Do(req)
	if err != nil {
		return Record{}, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		return Record{}, resp.StatusCode, fmt.Errorf("witness %s: HTTP %d", h.Label, resp.StatusCode)
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return Record{}, resp.StatusCode, fmt.Errorf("witness %s: bad response", h.Label)
	}
	return r, resp.StatusCode, nil
}

func (h *HTTPWitness) Get(ctx context.Context) (Record, error) {
	r, _, err := h.do(ctx, http.MethodGet, nil)
	return r, err
}

func (h *HTTPWitness) Propose(ctx context.Context, next Record) (Record, bool, error) {
	body, _ := json.Marshal(next)
	r, code, err := h.do(ctx, http.MethodPost, body)
	if err != nil {
		return r, false, err
	}
	return r, code == http.StatusOK, nil
}

// Handler serves the witness protocol backed by a FileWitness. token must be non-empty.
func Handler(store *FileWitness, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/lease", func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			rec, err := store.Get(r.Context())
			if err != nil {
				http.Error(w, "store error", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(rec)
		case http.MethodPost:
			var next Record
			if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&next); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			rec, ok, err := store.Propose(r.Context(), next)
			if err != nil {
				http.Error(w, "store error", http.StatusInternalServerError)
				return
			}
			if !ok {
				w.WriteHeader(http.StatusConflict)
			}
			_ = json.NewEncoder(w).Encode(rec)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	return mux
}

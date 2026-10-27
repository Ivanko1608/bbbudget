package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"bbbudget/internal/devices"
)

// pairPath is the one API route a device can call before it has a token.
const pairPath = "/api/pair"

// maxPairBody is 1 KiB: a code is 11 characters and a name is capped at 64
// runes (up to 4 bytes each), so 11 + 256 plus the JSON around them fits.
const maxPairBody = 1 << 10

// requireDevice lets API and MCP requests through only with a paired
// device's token. The UI's own files stay public: they hold no data.
func (s *server) requireDevice(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !needsDevice(r) {
			next.ServeHTTP(w, r)
			return
		}
		token, ok := bearerToken(r)
		if !ok {
			unauthorized(w, "send a paired device's token as Authorization: Bearer <token>")
			return
		}
		_, err := s.opts.Devices.Authenticate(token)
		if errors.Is(err, devices.ErrBadToken) {
			unauthorized(w, err.Error())
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func needsDevice(r *http.Request) bool {
	path := r.URL.Path
	if path == pairPath {
		return false
	}
	return strings.HasPrefix(path, "/api/") || path == "/mcp"
}

func bearerToken(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return token, ok && token != ""
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeErr(w, http.StatusUnauthorized, msg, nil)
}

type deviceView struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	PairedAt time.Time `json:"paired_at"`
}

func viewDevice(d devices.Device) deviceView {
	return deviceView{ID: d.ID, Name: d.Name, PairedAt: d.PairedAt}
}

type pairRequest struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type pairResponse struct {
	Device deviceView `json:"device"`
	Token  string     `json:"token"`
}

func (s *server) pair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxPairBody)
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	paired, err := s.opts.Devices.Pair(req.Code, req.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.opts.Log.Info("device paired", "id", paired.Device.ID, "name", paired.Device.Name)
	writeJSON(w, http.StatusCreated, pairResponse{
		Device: viewDevice(paired.Device),
		Token:  paired.Token,
	})
}

func (s *server) listDevices(w http.ResponseWriter, r *http.Request) {
	list, err := s.opts.Devices.List()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	views := make([]deviceView, len(list))
	for i, device := range list {
		views[i] = viewDevice(device)
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.opts.Devices.Revoke(r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

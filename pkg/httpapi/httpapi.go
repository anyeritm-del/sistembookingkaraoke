// Package httpapi exposes the booking service as a JSON API under /api.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"karaoke/pkg/auth"
	"karaoke/pkg/booking"
)

// Server holds the dependencies of the API handlers.
type Server struct {
	svc   *booking.Service
	auth  *auth.Auth
	tvKey string
	mux   *http.ServeMux
}

// New builds the API handler.
func New(svc *booking.Service, a *auth.Auth, tvKey string) *Server {
	s := &Server{svc: svc, auth: a, tvKey: tvKey, mux: http.NewServeMux()}

	s.mux.HandleFunc("GET /api/health", s.health)
	s.mux.HandleFunc("POST /api/login", s.login)
	s.mux.HandleFunc("POST /api/logout", s.logout)
	s.mux.HandleFunc("GET /api/me", s.me)
	s.mux.HandleFunc("GET /api/tv", s.tv)

	s.mux.Handle("GET /api/rooms", s.staff(s.rooms))
	s.mux.Handle("GET /api/bookings", s.staff(s.dayBookings))
	s.mux.Handle("POST /api/bookings", s.staff(s.createBooking))
	s.mux.Handle("POST /api/bookings/{id}/extend", s.staff(s.extend))
	s.mux.Handle("POST /api/bookings/{id}/checkin", s.staff(s.action(s.svc.CheckIn)))
	s.mux.Handle("POST /api/bookings/{id}/checkout", s.staff(s.action(s.svc.CheckOut)))
	s.mux.Handle("POST /api/bookings/{id}/cancel", s.staff(s.action(s.svc.Cancel)))
	s.mux.Handle("GET /api/report", s.staff(s.report))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.mux.ServeHTTP(w, r)
}

// staff allows only logged-in staff. State-changing requests must be JSON,
// which together with the SameSite=Strict cookie blocks cross-site forms.
func (s *Server) staff(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.LoggedIn(r) {
			writeError(w, http.StatusUnauthorized, "silakan login dulu")
			return
		}
		if r.Method != http.MethodGet && !isJSON(r) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type harus application/json")
			return
		}
		h(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": s.svc.Now()})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PIN string `json:"pin"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.auth.CheckPIN(in.PIN) {
		time.Sleep(time.Second) // slow down guessing
		writeError(w, http.StatusUnauthorized, "PIN salah")
		return
	}
	s.auth.SetCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	auth.ClearCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"logged_in": s.auth.LoggedIn(r),
		"now":       s.svc.Now(),
		"timezone":  s.svc.Location().String(),
	})
}

func (s *Server) rooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := s.svc.Rooms(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rooms)
}

func (s *Server) dayBookings(w http.ResponseWriter, r *http.Request) {
	day, ok := s.dateParam(w, r)
	if !ok {
		return
	}
	list, err := s.svc.DayBookings(r.Context(), day)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func (s *Server) createBooking(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RoomID          string `json:"room_id"`
		CustomerName    string `json:"customer_name"`
		Phone           string `json:"phone"`
		Notes           string `json:"notes"`
		Start           string `json:"start"` // "2006-01-02T15:04" in business time zone
		DurationMinutes int    `json:"duration_minutes"`
	}
	if !decode(w, r, &in) {
		return
	}
	start, err := time.ParseInLocation("2006-01-02T15:04", in.Start, s.svc.Location())
	if err != nil {
		writeError(w, http.StatusBadRequest, "format jam mulai harus YYYY-MM-DDTHH:MM")
		return
	}
	b, err := s.svc.Create(r.Context(), booking.CreateInput{
		RoomID: in.RoomID, CustomerName: in.CustomerName, Phone: in.Phone,
		Notes: in.Notes, Start: start, DurationMinutes: in.DurationMinutes,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) extend(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Minutes int `json:"minutes"`
	}
	if !decode(w, r, &in) {
		return
	}
	b, err := s.svc.Extend(r.Context(), r.PathValue("id"), in.Minutes)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) action(fn func(ctx context.Context, id string) (booking.Booking, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fn(r.Context(), r.PathValue("id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, b)
	}
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	day, ok := s.dateParam(w, r)
	if !ok {
		return
	}
	rep, err := s.svc.Report(r.Context(), day)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// tv serves the room TV. It needs the TV key, not a staff login, so a TV
// can run unattended. It only returns the guest name and times.
func (s *Server) tv(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-TV-Key")
	if key == "" {
		key = r.URL.Query().Get("key")
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.tvKey)) != 1 {
		writeError(w, http.StatusUnauthorized, "TV key salah")
		return
	}
	st, err := s.svc.RoomStatus(r.Context(), r.URL.Query().Get("room"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) dateParam(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	v := r.URL.Query().Get("date")
	if v == "" {
		return s.svc.Now(), true
	}
	d, err := time.ParseInLocation(time.DateOnly, v, s.svc.Location())
	if err != nil {
		writeError(w, http.StatusBadRequest, "format tanggal harus YYYY-MM-DD")
		return time.Time{}, false
	}
	return d, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "body JSON tidak valid")
		return false
	}
	return true
}

func isJSON(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, booking.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, booking.ErrConflict), errors.Is(err, booking.ErrWrongState):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, booking.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("internal error: %v", err)
		writeError(w, http.StatusInternalServerError, "terjadi kesalahan server, coba lagi")
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

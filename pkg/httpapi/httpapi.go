// Package httpapi exposes the booking service as a JSON API under /api.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
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

// userHandler is a handler for a logged-in user.
type userHandler func(w http.ResponseWriter, r *http.Request, u booking.User)

// New builds the API handler.
func New(svc *booking.Service, a *auth.Auth, tvKey string) *Server {
	s := &Server{svc: svc, auth: a, tvKey: tvKey, mux: http.NewServeMux()}

	s.mux.HandleFunc("GET /api/health", s.health)
	s.mux.HandleFunc("POST /api/login", s.login)
	s.mux.HandleFunc("POST /api/logout", s.logout)
	s.mux.HandleFunc("GET /api/me", s.me)
	s.mux.HandleFunc("GET /api/tv", s.tv)
	s.mux.HandleFunc("POST /api/tv/pair", s.pair)

	// Every route below needs a login. The service checks the permission
	// again, so a missing check here cannot open access.
	s.route("POST /api/me/pin", s.changeOwnPIN)
	s.route("GET /api/rooms", s.rooms)
	s.route("POST /api/rooms", s.createRoom)
	s.route("PUT /api/rooms/{id}", s.updateRoom)
	s.route("GET /api/bookings", s.dayBookings)
	s.route("POST /api/bookings", s.createBooking)
	s.route("POST /api/bookings/{id}/extend", s.extend)
	s.route("GET /api/bookings/list", s.listBookings)
	s.route("GET /api/bookings/quote", s.quote)
	s.route("GET /api/pricing", s.pricing)
	s.route("PUT /api/pricing", s.updatePricing)
	s.route("POST /api/bookings/{id}/confirm", s.action(s.svc.Confirm))
	s.route("POST /api/bookings/{id}/checkin", s.action(s.svc.CheckIn))
	s.route("POST /api/bookings/{id}/checkout", s.checkout)
	s.route("POST /api/bookings/{id}/cancel", s.action(s.svc.Cancel))
	s.route("GET /api/report", s.report)
	s.route("GET /api/activity", s.activity)
	s.route("GET /api/export/bookings.csv", s.exportBookings)
	s.route("GET /api/export/report.csv", s.exportReport)
	s.route("GET /api/users", s.users)
	s.route("POST /api/users", s.createUser)
	s.route("PUT /api/users/{username}", s.updateUser)
	s.route("POST /api/users/{username}/pin", s.resetPIN)
	s.route("GET /api/devices", s.devices)
	s.route("POST /api/devices", s.createDevice)
	s.route("POST /api/devices/{id}/revoke", s.revokeDevice)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.mux.ServeHTTP(w, r)
}

// route registers a handler that needs a logged-in, active user.
// State-changing requests must be JSON, which together with the
// SameSite=Strict cookie blocks cross-site form posts.
func (s *Server) route(pattern string, h userHandler) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(w, r)
		if !ok {
			return
		}
		if r.Method != http.MethodGet && !isJSON(r) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type harus application/json")
			return
		}
		h(w, r, u)
	})
}

// currentUser loads the session's user from the store, so a role change or
// deactivation takes effect on the next request.
func (s *Server) currentUser(w http.ResponseWriter, r *http.Request) (booking.User, bool) {
	name, ok := s.auth.RequestUsername(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "silakan login dulu")
		return booking.User{}, false
	}
	u, err := s.svc.SessionUser(r.Context(), name)
	if errors.Is(err, booking.ErrBadLogin) {
		auth.ClearCookie(w)
		writeError(w, http.StatusUnauthorized, "akun tidak aktif, silakan login lagi")
		return booking.User{}, false
	}
	if err != nil {
		writeServiceError(w, err)
		return booking.User{}, false
	}
	return u, true
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": s.svc.Now()})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		PIN      string `json:"pin"`
	}
	if !decode(w, r, &in) {
		return
	}
	u, err := s.svc.Login(r.Context(), in.Username, in.PIN)
	if errors.Is(err, booking.ErrBadLogin) {
		time.Sleep(time.Second) // slow down guessing
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	s.auth.SetCookie(w, u.Username)
	writeJSON(w, http.StatusOK, s.meBody(u))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	auth.ClearCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{"logged_in": false, "now": s.svc.Now(), "timezone": s.svc.Location().String()}
	if name, ok := s.auth.RequestUsername(r); ok {
		if u, err := s.svc.SessionUser(r.Context(), name); err == nil {
			body = s.meBody(u)
		}
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) meBody(u booking.User) map[string]any {
	return map[string]any{
		"logged_in":   true,
		"user":        u,
		"permissions": u.Role.Permissions(),
		"now":         s.svc.Now(),
		"timezone":    s.svc.Location().String(),
	}
}

func (s *Server) changeOwnPIN(w http.ResponseWriter, r *http.Request, u booking.User) {
	var in struct {
		OldPIN string `json:"old_pin"`
		NewPIN string `json:"new_pin"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.svc.ChangeOwnPIN(r.Context(), u, in.OldPIN, in.NewPIN); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- Rooms ----------

func (s *Server) rooms(w http.ResponseWriter, r *http.Request, _ booking.User) {
	rooms, err := s.svc.Rooms(r.Context())
	respond(w, http.StatusOK, rooms, err)
}

func (s *Server) createRoom(w http.ResponseWriter, r *http.Request, u booking.User) {
	var in booking.RoomInput
	if !decode(w, r, &in) {
		return
	}
	room, err := s.svc.CreateRoom(r.Context(), u, in)
	respond(w, http.StatusCreated, room, err)
}

func (s *Server) updateRoom(w http.ResponseWriter, r *http.Request, u booking.User) {
	var in booking.RoomInput
	if !decode(w, r, &in) {
		return
	}
	room, err := s.svc.UpdateRoom(r.Context(), u, r.PathValue("id"), in)
	respond(w, http.StatusOK, room, err)
}

// ---------- Bookings ----------

func (s *Server) dayBookings(w http.ResponseWriter, r *http.Request, u booking.User) {
	day, ok := s.dateParam(w, r)
	if !ok {
		return
	}
	list, err := s.svc.DayBookings(r.Context(), u, day)
	respond(w, http.StatusOK, nonNil(list), err)
}

func (s *Server) createBooking(w http.ResponseWriter, r *http.Request, u booking.User) {
	var in struct {
		RoomID           string `json:"room_id"`
		CustomerName     string `json:"customer_name"`
		Phone            string `json:"phone"`
		Notes            string `json:"notes"`
		Start            string `json:"start"` // "2006-01-02T15:04" in business time zone
		DurationMinutes  int    `json:"duration_minutes"`
		Tentative        bool   `json:"tentative"`
		Complimentary    bool   `json:"complimentary"`
		ComplimentReason string `json:"compliment_reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	start, err := time.ParseInLocation("2006-01-02T15:04", in.Start, s.svc.Location())
	if err != nil {
		writeError(w, http.StatusBadRequest, "format jam mulai harus YYYY-MM-DDTHH:MM")
		return
	}
	b, err := s.svc.Create(r.Context(), u, booking.CreateInput{
		RoomID: in.RoomID, CustomerName: in.CustomerName, Phone: in.Phone,
		Notes: in.Notes, Start: start, DurationMinutes: in.DurationMinutes, Tentative: in.Tentative,
		Complimentary: in.Complimentary, ComplimentReason: in.ComplimentReason,
	})
	respond(w, http.StatusCreated, b, err)
}

// quote returns the price for ?room_id=&start=YYYY-MM-DDTHH:MM&duration_minutes=
// so the booking form shows exactly what the server will charge.
func (s *Server) quote(w http.ResponseWriter, r *http.Request, _ booking.User) {
	q := r.URL.Query()
	start, err := time.ParseInLocation("2006-01-02T15:04", q.Get("start"), s.svc.Location())
	if err != nil {
		writeError(w, http.StatusBadRequest, "format jam mulai harus YYYY-MM-DDTHH:MM")
		return
	}
	minutes, _ := strconv.Atoi(q.Get("duration_minutes"))
	qt, err := s.svc.QuoteFor(r.Context(), q.Get("room_id"), start, minutes)
	respond(w, http.StatusOK, qt, err)
}

func (s *Server) pricing(w http.ResponseWriter, r *http.Request, _ booking.User) {
	rules, err := s.svc.Pricing(r.Context())
	respond(w, http.StatusOK, nonNil(rules), err)
}

func (s *Server) updatePricing(w http.ResponseWriter, r *http.Request, u booking.User) {
	var in struct {
		Rules []booking.PriceRule `json:"rules"`
	}
	if !decode(w, r, &in) {
		return
	}
	rules, err := s.svc.UpdatePricing(r.Context(), u, in.Rules)
	respond(w, http.StatusOK, rules, err)
}

// listBookings serves the booking list: ?from=&to=&status=&q=&room=
// Dates default to today .. today+30.
func (s *Server) listBookings(w http.ResponseWriter, r *http.Request, u booking.User) {
	q := r.URL.Query()
	today := s.svc.Now()
	from, ok := s.parseDate(w, q.Get("from"), today)
	if !ok {
		return
	}
	to, ok := s.parseDate(w, q.Get("to"), from.AddDate(0, 0, 30))
	if !ok {
		return
	}
	list, sum, err := s.svc.List(r.Context(), u, booking.ListFilter{
		From: from, To: to, Status: q.Get("status"), Query: q.Get("q"), RoomID: q.Get("room"),
	})
	respond(w, http.StatusOK, map[string]any{"bookings": nonNil(list), "summary": sum}, err)
}

// checkout takes {"bill":"usage"} to bill an early check-out by the time used;
// anything else bills the booked time.
func (s *Server) checkout(w http.ResponseWriter, r *http.Request, u booking.User) {
	var in struct {
		Bill string `json:"bill"`
	}
	if !decode(w, r, &in) {
		return
	}
	b, err := s.svc.CheckOut(r.Context(), u, r.PathValue("id"), in.Bill == "usage")
	respond(w, http.StatusOK, b, err)
}

func (s *Server) extend(w http.ResponseWriter, r *http.Request, u booking.User) {
	var in struct {
		Minutes int `json:"minutes"`
	}
	if !decode(w, r, &in) {
		return
	}
	b, err := s.svc.Extend(r.Context(), u, r.PathValue("id"), in.Minutes)
	respond(w, http.StatusOK, b, err)
}

func (s *Server) action(fn func(ctx context.Context, actor booking.User, id string) (booking.Booking, error)) userHandler {
	return func(w http.ResponseWriter, r *http.Request, u booking.User) {
		b, err := fn(r.Context(), u, r.PathValue("id"))
		respond(w, http.StatusOK, b, err)
	}
}

func (s *Server) report(w http.ResponseWriter, r *http.Request, u booking.User) {
	from, to, ok := s.reportRange(w, r)
	if !ok {
		return
	}
	rep, err := s.svc.Report(r.Context(), u, from, to)
	respond(w, http.StatusOK, rep, err)
}

// reportRange reads ?from=&to= (YYYY-MM-DD). The older ?date= gives one day;
// nothing gives today.
func (s *Server) reportRange(w http.ResponseWriter, r *http.Request) (time.Time, time.Time, bool) {
	q := r.URL.Query()
	day, ok := s.parseDate(w, q.Get("date"), s.svc.Now())
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	from, ok := s.parseDate(w, q.Get("from"), day)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	to, ok := s.parseDate(w, q.Get("to"), from)
	return from, to, ok
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request, u booking.User) {
	day, ok := s.dateParam(w, r)
	if !ok {
		return
	}
	list, err := s.svc.Activity(r.Context(), u, day)
	respond(w, http.StatusOK, nonNil(list), err)
}

// ---------- Users ----------

func (s *Server) users(w http.ResponseWriter, r *http.Request, u booking.User) {
	list, err := s.svc.Users(r.Context(), u)
	respond(w, http.StatusOK, nonNil(list), err)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, u booking.User) {
	var in booking.UserInput
	if !decode(w, r, &in) {
		return
	}
	nu, err := s.svc.CreateUser(r.Context(), u, in)
	respond(w, http.StatusCreated, nu, err)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, u booking.User) {
	var in booking.UserUpdate
	if !decode(w, r, &in) {
		return
	}
	nu, err := s.svc.UpdateUser(r.Context(), u, r.PathValue("username"), in)
	respond(w, http.StatusOK, nu, err)
}

func (s *Server) resetPIN(w http.ResponseWriter, r *http.Request, u booking.User) {
	var in struct {
		PIN string `json:"pin"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.svc.ResetPIN(r.Context(), u, r.PathValue("username"), in.PIN)
	respond(w, http.StatusOK, map[string]bool{"ok": true}, err)
}

// ---------- TV ----------

// tv serves the room TV without a staff login, so a TV can run unattended.
// A paired TV sends its own token (X-TV-Token) and gets its room's status.
// The older way, a shared TV key plus ?room=, still works.
// It only returns the guest name and times.
func (s *Server) tv(w http.ResponseWriter, r *http.Request) {
	if token := r.Header.Get("X-TV-Token"); token != "" {
		st, err := s.svc.DeviceRoomStatus(r.Context(), token)
		if errors.Is(err, booking.ErrBadPairCode) {
			writeError(w, http.StatusUnauthorized, "TV ini dicabut atau belum dipairing; minta kode pairing baru ke admin")
			return
		}
		respond(w, http.StatusOK, st, err)
		return
	}
	key := r.Header.Get("X-TV-Key")
	if key == "" {
		key = r.URL.Query().Get("key")
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.tvKey)) != 1 {
		writeError(w, http.StatusUnauthorized, "TV key salah")
		return
	}
	st, err := s.svc.RoomStatus(r.Context(), r.URL.Query().Get("room"))
	respond(w, http.StatusOK, st, err)
}

// pair exchanges a 6-digit pairing code for the TV's own token.
func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	d, token, err := s.svc.Pair(r.Context(), in.Code, clientIP(r))
	if errors.Is(err, booking.ErrBadPairCode) {
		time.Sleep(time.Second) // slow down guessing
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "device": d})
}

// ---------- Devices (admin) ----------

func (s *Server) devices(w http.ResponseWriter, r *http.Request, u booking.User) {
	list, err := s.svc.Devices(r.Context(), u)
	respond(w, http.StatusOK, nonNil(list), err)
}

func (s *Server) createDevice(w http.ResponseWriter, r *http.Request, u booking.User) {
	var in booking.DeviceInput
	if !decode(w, r, &in) {
		return
	}
	pc, err := s.svc.CreatePairing(r.Context(), u, in)
	respond(w, http.StatusCreated, pc, err)
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request, u booking.User) {
	d, err := s.svc.RevokeDevice(r.Context(), u, r.PathValue("id"))
	respond(w, http.StatusOK, d, err)
}

// ---------- Helpers ----------

// clientIP returns the caller's address. On Vercel the platform sets
// X-Real-IP; it is only used for rate limiting, never for access control.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		first, _, _ := strings.Cut(fwd, ",")
		return strings.TrimSpace(first)
	}
	host, _, _ := strings.Cut(r.RemoteAddr, ":")
	return host
}

func (s *Server) dateParam(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	return s.parseDate(w, r.URL.Query().Get("date"), s.svc.Now())
}

// parseDate parses YYYY-MM-DD, or returns def when v is empty.
func (s *Server) parseDate(w http.ResponseWriter, v string, def time.Time) (time.Time, bool) {
	if v == "" {
		return def, true
	}
	d, err := time.ParseInLocation(time.DateOnly, v, s.svc.Location())
	if err != nil {
		writeError(w, http.StatusBadRequest, "format tanggal harus YYYY-MM-DD")
		return time.Time{}, false
	}
	return d, true
}

func respond(w http.ResponseWriter, code int, v any, err error) {
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, code, v)
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
	case errors.Is(err, booking.ErrForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, booking.ErrBadLogin), errors.Is(err, booking.ErrBadPairCode):
		writeError(w, http.StatusUnauthorized, err.Error())
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

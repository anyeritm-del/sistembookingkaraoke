package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"karaoke/pkg/auth"
	"karaoke/pkg/booking"
	"karaoke/pkg/memstore"
)

const tvKey = "tv-key-0123456789"

func newTestServer(t *testing.T) *Server {
	t.Helper()
	loc, _ := time.LoadLocation("Asia/Jakarta")
	pins, err := auth.NewPINHasher(strings.Repeat("p", 32))
	if err != nil {
		t.Fatal(err)
	}
	svc := booking.NewService(memstore.New(memstore.DemoRooms()...), loc, pins, "112233")
	now := time.Date(2026, 10, 1, 18, 0, 0, 0, loc)
	svc.SetClock(func() time.Time { return now })
	a, err := auth.New(strings.Repeat("s", 32), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return New(svc, a, tvKey)
}

func do(t *testing.T, h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func login(t *testing.T, s *Server, user, pin string) *http.Cookie {
	t.Helper()
	rec := do(t, s, "POST", "/api/login", `{"username":"`+user+`","pin":"`+pin+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", user, rec.Code, rec.Body)
	}
	c := rec.Result().Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie flags: %+v", c)
	}
	return c
}

func TestRolesOverHTTP(t *testing.T) {
	s := newTestServer(t)

	if rec := do(t, s, "GET", "/api/bookings", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no login: %d", rec.Code)
	}
	if rec := do(t, s, "POST", "/api/login", `{"username":"admin","pin":"000000"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong pin: %d", rec.Code)
	}
	admin := login(t, s, "admin", "112233")

	// /api/me returns the user and permissions, never the PIN hash.
	rec := do(t, s, "GET", "/api/me", "", admin)
	if !strings.Contains(rec.Body.String(), `"users.manage"`) || strings.Contains(rec.Body.String(), "v1$") {
		t.Errorf("me: %s", rec.Body)
	}

	for _, u := range []string{
		`{"username":"sari","name":"Sari","role":"staff","pin":"582047"}`,
		`{"username":"budi","name":"Budi","role":"supervisor","pin":"693158"}`,
	} {
		if rec := do(t, s, "POST", "/api/users", u, admin); rec.Code != http.StatusCreated {
			t.Fatalf("create user: %d %s", rec.Code, rec.Body)
		}
	}
	rec = do(t, s, "GET", "/api/users", "", admin)
	if strings.Contains(rec.Body.String(), "pin_hash") || strings.Contains(rec.Body.String(), "v1$") {
		t.Errorf("users list leaks PIN hash: %s", rec.Body)
	}
	staff := login(t, s, "sari", "582047")
	sup := login(t, s, "budi", "693158")

	rec = do(t, s, "POST", "/api/bookings",
		`{"room_id":"R01","customer_name":"Tamu","start":"2026-10-01T18:00","duration_minutes":60}`, staff)
	if rec.Code != http.StatusCreated {
		t.Fatalf("staff create: %d %s", rec.Code, rec.Body)
	}
	var b booking.Booking
	json.Unmarshal(rec.Body.Bytes(), &b)
	if b.CreatedBy != "sari" {
		t.Errorf("created_by = %q", b.CreatedBy)
	}

	checks := []struct {
		name   string
		cookie *http.Cookie
		method string
		path   string
		body   string
		want   int
	}{
		{"staff cancel", staff, "POST", "/api/bookings/" + b.ID + "/cancel", "{}", 403},
		{"staff report", staff, "GET", "/api/report", "", 403},
		{"staff activity", staff, "GET", "/api/activity", "", 403},
		{"staff users", staff, "GET", "/api/users", "", 403},
		{"staff checkin", staff, "POST", "/api/bookings/" + b.ID + "/checkin", "{}", 200},
		{"staff extend", staff, "POST", "/api/bookings/" + b.ID + "/extend", `{"minutes":30}`, 200},
		{"sup report", sup, "GET", "/api/report", "", 200},
		{"sup activity", sup, "GET", "/api/activity", "", 200},
		{"sup create room", sup, "POST", "/api/rooms", `{"id":"R09","name":"X","rate_per_hour":1,"active":true}`, 403},
		{"sup users", sup, "GET", "/api/users", "", 403},
		{"sup checkout", sup, "POST", "/api/bookings/" + b.ID + "/checkout", "{}", 200},
		{"admin create room", admin, "POST", "/api/rooms", `{"id":"R09","name":"Room 9","rate_per_hour":120000,"active":true}`, 201},
		{"admin update room", admin, "PUT", "/api/rooms/R09", `{"name":"Room 9","rate_per_hour":130000,"active":true}`, 200},
		{"admin reset pin", admin, "POST", "/api/users/sari/pin", `{"pin":"740291"}`, 200},
		{"staff change own pin", staff, "POST", "/api/me/pin", `{"old_pin":"740291","new_pin":"628403"}`, 200},
	}
	for _, c := range checks {
		rec := do(t, s, c.method, c.path, c.body, c.cookie)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
	}

	// Activity view shows who did what.
	rec = do(t, s, "GET", "/api/activity?date=2026-10-01", "", sup)
	var log []booking.Activity
	json.Unmarshal(rec.Body.Bytes(), &log)
	seen := map[string]bool{}
	for _, a := range log {
		seen[a.Username+" "+a.Action] = true
	}
	for _, want := range []string{"sari booking.create", "sari booking.checkin", "sari booking.extend", "budi booking.checkout", "admin room.create", "admin user.pin_reset", "sari pin.change"} {
		if !seen[want] {
			t.Errorf("activity missing %q; got %v", want, seen)
		}
	}

	// Deactivating a user ends their session on the next request.
	if rec := do(t, s, "PUT", "/api/users/sari", `{"name":"Sari","role":"staff","active":false}`, admin); rec.Code != 200 {
		t.Fatalf("deactivate: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, s, "GET", "/api/bookings", "", staff); rec.Code != http.StatusUnauthorized {
		t.Errorf("deactivated session: %d", rec.Code)
	}
}

func TestTV(t *testing.T) {
	s := newTestServer(t)
	admin := login(t, s, "admin", "112233")
	rec := do(t, s, "POST", "/api/bookings",
		`{"room_id":"R01","customer_name":"Sari","phone":"0812","start":"2026-10-01T18:00","duration_minutes":60}`, admin)
	var b booking.Booking
	json.Unmarshal(rec.Body.Bytes(), &b)
	do(t, s, "POST", "/api/bookings/"+b.ID+"/checkin", "{}", admin)

	if rec := do(t, s, "GET", "/api/tv?room=R01&key=wrong", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("tv wrong key: %d", rec.Code)
	}
	rec = do(t, s, "GET", "/api/tv?room=R01&key="+tvKey, "")
	var st booking.TVStatus
	json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != http.StatusOK || st.Current == nil || st.Current.CustomerName != "Sari" {
		t.Fatalf("tv: %d %s", rec.Code, rec.Body)
	}
	for _, private := range []string{"total_price", "phone", "created_by"} {
		if strings.Contains(rec.Body.String(), private) {
			t.Errorf("tv response leaks %s: %s", private, rec.Body)
		}
	}
}

func TestPostRequiresJSON(t *testing.T) {
	s := newTestServer(t)
	cookie := login(t, s, "admin", "112233")
	req := httptest.NewRequest("POST", "/api/bookings/x/cancel", strings.NewReader("a=b"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d", rec.Code)
	}
}

func TestListAndConfirm(t *testing.T) {
	s := newTestServer(t)
	admin := login(t, s, "admin", "112233")
	do(t, s, "POST", "/api/users", `{"username":"sari","name":"Sari","role":"staff","pin":"582047"}`, admin)
	staff := login(t, s, "sari", "582047")

	rec := do(t, s, "POST", "/api/bookings",
		`{"room_id":"R01","customer_name":"Tentatif","start":"2026-10-01T23:00","duration_minutes":60,"tentative":true}`, staff)
	var tb booking.Booking
	json.Unmarshal(rec.Body.Bytes(), &tb)
	if rec.Code != 201 || tb.Status != booking.StatusTentative || tb.HoldUntil.IsZero() {
		t.Fatalf("create tentative: %d %s", rec.Code, rec.Body)
	}

	var page struct {
		Bookings []booking.Booking   `json:"bookings"`
		Summary  booking.ListSummary `json:"summary"`
	}
	rec = do(t, s, "GET", "/api/bookings/list?from=2026-10-01&to=2026-10-07&status=tentative", "", staff)
	json.Unmarshal(rec.Body.Bytes(), &page)
	if rec.Code != 200 || page.Summary.Count != 1 || page.Bookings[0].ID != tb.ID {
		t.Fatalf("list tentative: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, s, "GET", "/api/bookings/list?from=2026-10-01&to=bad", "", staff); rec.Code != 400 {
		t.Errorf("bad date: %d", rec.Code)
	}

	if rec := do(t, s, "POST", "/api/bookings/"+tb.ID+"/confirm", "{}", staff); rec.Code != 200 {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, s, "GET", "/api/bookings/list?status=booked&from=2026-10-01", "", staff)
	json.Unmarshal(rec.Body.Bytes(), &page)
	if page.Summary.Count != 1 || page.Bookings[0].ConfirmedBy != "sari" {
		t.Errorf("list confirmed: %s", rec.Body)
	}
	// Confirmed booking: staff can no longer cancel it.
	if rec := do(t, s, "POST", "/api/bookings/"+tb.ID+"/cancel", "{}", staff); rec.Code != 403 {
		t.Errorf("staff cancel confirmed: %d", rec.Code)
	}
}

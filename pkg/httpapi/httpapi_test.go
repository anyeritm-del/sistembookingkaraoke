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

func TestTVPairingOverHTTP(t *testing.T) {
	s := newTestServer(t)
	admin := login(t, s, "admin", "112233")
	rec := do(t, s, "POST", "/api/devices", `{"name":"TV Room 2","room_id":"R02"}`, admin)
	var pc booking.PairingCode
	json.Unmarshal(rec.Body.Bytes(), &pc)
	if rec.Code != 201 || len(pc.Code) != 6 {
		t.Fatalf("create pairing: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "hash") {
		t.Errorf("pairing response leaks hashes: %s", rec.Body)
	}

	rec = do(t, s, "POST", "/api/tv/pair", `{"code":"`+pc.Code+`"}`)
	var paired struct {
		Token  string         `json:"token"`
		Device booking.Device `json:"device"`
	}
	json.Unmarshal(rec.Body.Bytes(), &paired)
	if rec.Code != 200 || paired.Token == "" || paired.Device.RoomID != "R02" {
		t.Fatalf("pair: %d %s", rec.Code, rec.Body)
	}

	tvGet := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/tv", nil)
		req.Header.Set("X-TV-Token", token)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}
	rec = tvGet(paired.Token)
	var st booking.TVStatus
	json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != 200 || st.Room.ID != "R02" {
		t.Fatalf("tv with token: %d %s", rec.Code, rec.Body)
	}
	if rec := tvGet("tvd_wrong"); rec.Code != 401 {
		t.Errorf("tv wrong token: %d", rec.Code)
	}

	rec = do(t, s, "GET", "/api/devices", "", admin)
	if strings.Contains(rec.Body.String(), "hash") || !strings.Contains(rec.Body.String(), `"active"`) {
		t.Errorf("devices list: %s", rec.Body)
	}
	if rec := do(t, s, "POST", "/api/devices/"+paired.Device.ID+"/revoke", "{}", admin); rec.Code != 200 {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if rec := tvGet(paired.Token); rec.Code != 401 {
		t.Errorf("tv after revoke: %d", rec.Code)
	}
	if rec := do(t, s, "POST", "/api/tv/pair", `{"code":"`+pc.Code+`"}`); rec.Code != 401 {
		t.Errorf("reuse code: %d", rec.Code)
	}
}

func TestAccountingViewOnlyAndExport(t *testing.T) {
	s := newTestServer(t)
	admin := login(t, s, "admin", "112233")
	for _, u := range []string{
		`{"username":"ani","name":"Ani Accounting","role":"accounting","pin":"582047"}`,
		`{"username":"sari","name":"Sari","role":"staff","pin":"693158"}`,
	} {
		if rec := do(t, s, "POST", "/api/users", u, admin); rec.Code != 201 {
			t.Fatalf("create user: %d %s", rec.Code, rec.Body)
		}
	}
	rec := do(t, s, "POST", "/api/bookings",
		`{"room_id":"R01","customer_name":"=HYPERLINK(\"http://x\")","phone":"+62812","start":"2026-10-01T18:00","duration_minutes":60}`, admin)
	var b booking.Booking
	json.Unmarshal(rec.Body.Bytes(), &b)
	acc := login(t, s, "ani", "582047")
	staff := login(t, s, "sari", "693158")

	checks := []struct {
		name, method, path, body string
		want                     int
	}{
		{"list", "GET", "/api/bookings/list?from=2026-10-01", "", 200},
		{"day", "GET", "/api/bookings?date=2026-10-01", "", 200},
		{"report", "GET", "/api/report?date=2026-10-01", "", 200},
		{"activity", "GET", "/api/activity?date=2026-10-01", "", 200},
		{"create", "POST", "/api/bookings", `{"room_id":"R02","customer_name":"X","start":"2026-10-01T20:00","duration_minutes":60}`, 403},
		{"checkin", "POST", "/api/bookings/" + b.ID + "/checkin", "{}", 403},
		{"extend", "POST", "/api/bookings/" + b.ID + "/extend", `{"minutes":30}`, 403},
		{"cancel", "POST", "/api/bookings/" + b.ID + "/cancel", "{}", 403},
		{"confirm", "POST", "/api/bookings/" + b.ID + "/confirm", "{}", 403},
		{"users", "GET", "/api/users", "", 403},
		{"rooms write", "POST", "/api/rooms", `{"id":"R9","name":"x","rate_per_hour":1,"active":true}`, 403},
		{"pricing write", "PUT", "/api/pricing", `{"rules":[]}`, 403},
		{"devices", "GET", "/api/devices", "", 403},
		{"own pin", "POST", "/api/me/pin", `{"old_pin":"582047","new_pin":"628403"}`, 200},
	}
	for _, c := range checks {
		if rec := do(t, s, c.method, c.path, c.body, acc); rec.Code != c.want {
			t.Errorf("accounting %s: %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
	}

	rec = do(t, s, "GET", "/api/export/bookings.csv?from=2026-10-01&to=2026-10-01", "", acc)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(body, "\xEF\xBB\xBF") || !strings.Contains(rec.Header().Get("Content-Disposition"), "booking_20261001_20261001.csv") {
		t.Fatalf("export bookings: %d %q %s", rec.Code, rec.Header(), body)
	}
	if !strings.Contains(body, "id,tanggal,mulai") || !strings.Contains(body, b.ID+",2026-10-01,18:00,19:00,60,R01,Room 01 - Small") {
		t.Errorf("export rows: %s", body)
	}
	if strings.Contains(body, ",=HYPERLINK") || !strings.Contains(body, `'=HYPERLINK`) || !strings.Contains(body, "'+62812") {
		t.Errorf("formula not neutralised: %s", body)
	}

	rec = do(t, s, "GET", "/api/export/report.csv?date=2026-10-01", "", acc)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "TOTAL") {
		t.Errorf("export report: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, s, "GET", "/api/export/bookings.csv", "", staff); rec.Code != 403 {
		t.Errorf("staff export: %d", rec.Code)
	}

	// Downloads are in the activity log.
	rec = do(t, s, "GET", "/api/activity?date=2026-10-01", "", admin)
	if n := strings.Count(rec.Body.String(), `"action":"export"`); n != 2 {
		t.Errorf("export audit lines = %d: %s", n, rec.Body)
	}
	// /api/me tells the page this user has no write permissions.
	rec = do(t, s, "GET", "/api/me", "", acc)
	for _, p := range []string{"booking.create", "booking.checkout", "booking.cancel"} {
		if strings.Contains(rec.Body.String(), `"`+p+`"`) {
			t.Errorf("accounting has %s: %s", p, rec.Body)
		}
	}
}

func TestReportRangeOverHTTP(t *testing.T) {
	s := newTestServer(t)
	admin := login(t, s, "admin", "112233")
	rec := do(t, s, "GET", "/api/report?from=2026-10-01&to=2026-10-31", "", admin)
	var rep booking.SalesReport
	json.Unmarshal(rec.Body.Bytes(), &rep)
	if rec.Code != 200 || rep.From != "2026-10-01" || rep.To != "2026-10-31" || len(rep.Days) != 31 {
		t.Fatalf("range report: %d %s..%s %d days", rec.Code, rep.From, rep.To, len(rep.Days))
	}
	rec = do(t, s, "GET", "/api/report?date=2026-10-05", "", admin)
	json.Unmarshal(rec.Body.Bytes(), &rep)
	if rep.From != "2026-10-05" || rep.To != "2026-10-05" {
		t.Errorf("legacy ?date=: %s..%s", rep.From, rep.To)
	}
	if rec := do(t, s, "GET", "/api/report?from=2026-10-10&to=2026-10-01", "", admin); rec.Code != 400 {
		t.Errorf("reversed: %d", rec.Code)
	}
	rec = do(t, s, "GET", "/api/export/report.csv?from=2026-10-01&to=2026-10-31", "", admin)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "laporan_20261001_20261031.csv") ||
		!strings.Contains(rec.Body.String(), "per hari") || !strings.Contains(rec.Body.String(), "2026-10-31,0,0,0") {
		t.Errorf("range csv: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}

func TestComplimentOverHTTP(t *testing.T) {
	s := newTestServer(t)
	admin := login(t, s, "admin", "112233")
	do(t, s, "POST", "/api/users", `{"username":"sari","name":"Sari","role":"staff","pin":"582047"}`, admin)
	staff := login(t, s, "sari", "582047")
	body := `{"room_id":"R01","customer_name":"VIP","start":"2026-10-01T19:00","duration_minutes":60,"complimentary":true,"compliment_reason":"kompensasi keluhan"}`
	if rec := do(t, s, "POST", "/api/bookings", body, staff); rec.Code != 403 {
		t.Errorf("staff compliment: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, s, "POST", "/api/bookings", body, admin)
	var b booking.Booking
	json.Unmarshal(rec.Body.Bytes(), &b)
	if rec.Code != 201 || !b.Complimentary || b.TotalPrice != 0 {
		t.Fatalf("admin compliment: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, s, "GET", "/api/me", "", staff); strings.Contains(rec.Body.String(), "booking.compliment") {
		t.Errorf("staff has compliment permission: %s", rec.Body)
	}
	rec = do(t, s, "GET", "/api/export/bookings.csv?from=2026-10-01&to=2026-10-01", "", admin)
	if !strings.Contains(rec.Body.String(), ",ya,kompensasi keluhan,100000") {
		t.Errorf("csv compliment columns: %s", rec.Body)
	}
}

func TestEarlyCheckoutOverHTTP(t *testing.T) {
	s := newTestServer(t) // clock 18:00
	admin := login(t, s, "admin", "112233")
	rec := do(t, s, "POST", "/api/bookings", `{"room_id":"R01","customer_name":"A","start":"2026-10-01T17:30","duration_minutes":120}`, admin)
	var b booking.Booking
	json.Unmarshal(rec.Body.Bytes(), &b)
	do(t, s, "POST", "/api/bookings/"+b.ID+"/checkin", "{}", admin)
	rec = do(t, s, "POST", "/api/bookings/"+b.ID+"/checkout", `{"bill":"usage"}`, admin)
	json.Unmarshal(rec.Body.Bytes(), &b)
	// Checked in and out at 18:00: used 0 -> billed the 1-hour minimum.
	if rec.Code != 200 || b.BilledMinutes != 60 || b.TotalPrice != 100000 {
		t.Fatalf("checkout by usage: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, s, "GET", "/api/export/bookings.csv?from=2026-10-01&to=2026-10-01", "", admin)
	if !strings.Contains(rec.Body.String(), "menit_pakai,menit_ditagih") || !strings.Contains(rec.Body.String(), ",0,60") { // 0 minutes used, 60 billed
		t.Errorf("csv minutes: %s", rec.Body)
	}
}

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

func newTestServer(t *testing.T) (*Server, *booking.Service) {
	t.Helper()
	loc, _ := time.LoadLocation("Asia/Jakarta")
	svc := booking.NewService(memstore.New(memstore.DemoRooms()...), loc)
	now := time.Date(2026, 10, 1, 18, 0, 0, 0, loc)
	svc.SetClock(func() time.Time { return now })
	a, err := auth.New("123456", strings.Repeat("s", 32), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return New(svc, a, tvKey), svc
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

func TestStaffFlow(t *testing.T) {
	s, _ := newTestServer(t)

	if rec := do(t, s, "GET", "/api/bookings", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no login: %d", rec.Code)
	}
	if rec := do(t, s, "POST", "/api/login", `{"pin":"000000"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong pin: %d", rec.Code)
	}
	rec := do(t, s, "POST", "/api/login", `{"pin":"123456"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	cookie := rec.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie flags: %+v", cookie)
	}

	rec = do(t, s, "POST", "/api/bookings",
		`{"room_id":"R01","customer_name":"Sari","start":"2026-10-01T18:00","duration_minutes":60}`, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var b booking.Booking
	json.Unmarshal(rec.Body.Bytes(), &b)
	if b.TotalPrice != 100000 {
		t.Errorf("price %d", b.TotalPrice)
	}

	rec = do(t, s, "POST", "/api/bookings",
		`{"room_id":"R01","customer_name":"Lain","start":"2026-10-01T18:30","duration_minutes":60}`, cookie)
	if rec.Code != http.StatusConflict {
		t.Errorf("conflict: %d %s", rec.Code, rec.Body)
	}

	for _, step := range []struct{ path, body string }{
		{"/api/bookings/" + b.ID + "/checkin", "{}"},
		{"/api/bookings/" + b.ID + "/extend", `{"minutes":30}`},
	} {
		if rec := do(t, s, "POST", step.path, step.body, cookie); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", step.path, rec.Code, rec.Body)
		}
	}

	// TV sees the checked-in guest; key is required.
	if rec := do(t, s, "GET", "/api/tv?room=R01&key=wrong", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("tv wrong key: %d", rec.Code)
	}
	rec = do(t, s, "GET", "/api/tv?room=R01&key="+tvKey, "")
	var st booking.TVStatus
	json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != http.StatusOK || st.Current == nil || st.Current.CustomerName != "Sari" {
		t.Fatalf("tv: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "total_price") || strings.Contains(rec.Body.String(), "phone") {
		t.Errorf("tv response leaks private fields: %s", rec.Body)
	}

	if rec := do(t, s, "POST", "/api/bookings/"+b.ID+"/checkout", "{}", cookie); rec.Code != http.StatusOK {
		t.Fatalf("checkout: %d", rec.Code)
	}
	rec = do(t, s, "GET", "/api/report?date=2026-10-01", "", cookie)
	var rep booking.DailyReport
	json.Unmarshal(rec.Body.Bytes(), &rep)
	if rep.Revenue != 150000 || rep.Finished != 1 {
		t.Errorf("report: %s", rec.Body)
	}
}

func TestPostRequiresJSON(t *testing.T) {
	s, _ := newTestServer(t)
	login := do(t, s, "POST", "/api/login", `{"pin":"123456"}`)
	cookie := login.Result().Cookies()[0]
	req := httptest.NewRequest("POST", "/api/bookings/x/cancel", strings.NewReader("a=b"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d", rec.Code)
	}
}

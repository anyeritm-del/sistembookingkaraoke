package sheetstore

import (
	"testing"
	"time"

	"karaoke/pkg/booking"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	return &Store{loc: loc}
}

var (
	roomsTab = [][]any{
		{"id", "name", "rate_per_hour", "active"},
		{"R01", "Room 01", "100.000", true},
		{"R02", "Room 02", float64(150000), "FALSE"},
		{"", "", "", ""},
	}
	usersTab = [][]any{
		toAny(UserColumns),
		{"Budi", "Budi S", "Supervisor", "v1$1$AA$AA", true, "2026-10-01 10:00:00", "bad time"},
	}
	activityHeader = [][]any{toAny(ActivityColumns)}
	pricingTab     = [][]any{
		toAny(PricingColumns),
		{"weekday", "11:00", "17:00", float64(60000)},
		{"Weekend", "17:00:00", "11:00", "170.000"},
	}
	devicesTab = [][]any{
		toAny(DeviceColumns),
		{"TV-1", "TV Room 1", "R01", "active", "abc", "", "", "admin", "2026-10-02 10:00:00", "2026-10-02 10:05:00", ""},
	}
)

func TestParseAndWriteRoundTrip(t *testing.T) {
	s := testStore(t)
	// Columns in a different order plus an extra staff column, as values come
	// back from the API with UNFORMATTED_VALUE.
	header := []any{"room_id", "id", "customer_name", "phone", "start", "end", "duration_minutes",
		"status", "rate_per_hour", "total_price", "notes", "checked_in_at", "checked_out_at",
		"created_at", "updated_at", "staff_note", "created_by", "checked_in_by", "checked_out_by", "cancelled_by",
		"confirmed_by", "hold_until"}
	row := []any{"R01", "BK-1", "Budi", "0812", "2026-10-01 19:00", "2026-10-01 20:30", float64(90),
		"checked_in", float64(100000), float64(150000), "", "2026-10-01 18:55:10", "",
		"2026-10-01 10:00:00", "2026-10-01 18:55:10", "VIP guest", "sari", "sari", "", "", "budi", "2026-10-01 17:00"}

	snap, err := s.parse(roomsTab, [][]any{header, row, {}}, usersTab, activityHeader, devicesTab, pricingTab)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.rooms) != 2 || snap.rooms[0].RatePerHour != 100000 || !snap.rooms[0].Active || snap.rooms[1].Active {
		t.Errorf("rooms = %+v", snap.rooms)
	}
	bt := snap.tabs[BookingsSheet]
	if len(snap.bookings) != 1 || bt.rowOf["bk-1"] != 2 {
		t.Fatalf("bookings = %+v rows %v", snap.bookings, bt.rowOf)
	}
	b := snap.bookings[0]
	if b.Status != booking.StatusCheckedIn || b.DurationMinutes() != 90 || b.TotalPrice != 150000 ||
		b.Phone != "0812" || b.CreatedBy != "sari" || b.CheckedInBy != "sari" || b.CheckedOutBy != "" ||
		b.ConfirmedBy != "budi" || b.HoldUntil.Hour() != 17 {
		t.Errorf("booking = %+v", b)
	}
	if b.Start.Hour() != 19 || b.Start.Location() != s.loc {
		t.Errorf("start = %v", b.Start)
	}

	b.Status = booking.StatusFinished
	b.CheckedOutBy = "andi"
	out := buildRow(s.bookingValues(b), bt.cols, bt.raw["bk-1"])
	if out[0] != "R01" || out[1] != "BK-1" || out[7] != "finished" || out[15] != "VIP guest" || out[18] != "andi" {
		t.Errorf("row = %v", out)
	}
	if out[21] != "2026-10-01 17:00" || out[20] != "budi" {
		t.Errorf("hold/confirmed cells = %v %v", out[20], out[21])
	}
	if out[4] != "2026-10-01 19:00" {
		t.Errorf("start cell = %v", out[4])
	}
}

func TestParseUsers(t *testing.T) {
	s := testStore(t)
	snap, err := s.parse(roomsTab, [][]any{toAny(BookingColumns)}, usersTab, activityHeader, devicesTab, pricingTab)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.users) != 1 {
		t.Fatalf("users = %+v", snap.users)
	}
	u := snap.users[0]
	// Username and role are normalised; a bad timestamp does not fail the load.
	if u.Username != "budi" || u.Role != booking.RoleSupervisor || !u.Active || u.PINHash != "v1$1$AA$AA" {
		t.Errorf("user = %+v", u)
	}
	if snap.tabs[UsersSheet].rowOf["budi"] != 2 {
		t.Errorf("row map = %v", snap.tabs[UsersSheet].rowOf)
	}
	row := buildRow(s.userValues(u), snap.tabs[UsersSheet].cols, nil)
	if row[0] != "budi" || row[2] != "supervisor" || row[4] != true {
		t.Errorf("user row = %v", row)
	}
}

func TestParseDevices(t *testing.T) {
	s := testStore(t)
	snap, err := s.parse(roomsTab, [][]any{toAny(BookingColumns)}, usersTab, activityHeader, devicesTab, pricingTab)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.devices) != 1 {
		t.Fatalf("devices = %+v", snap.devices)
	}
	d := snap.devices[0]
	if d.ID != "TV-1" || d.RoomID != "R01" || d.Status != booking.DeviceActive || d.TokenHash != "abc" || d.PairedAt.Minute() != 5 {
		t.Errorf("device = %+v", d)
	}
	row := buildRow(s.deviceValues(d), snap.tabs[DevicesSheet].cols, snap.tabs[DevicesSheet].raw["tv-1"])
	if row[0] != "TV-1" || row[3] != "active" || row[9] != "2026-10-02 10:05:00" {
		t.Errorf("device row = %v", row)
	}
}

func TestParsePricing(t *testing.T) {
	s := testStore(t)
	snap, err := s.parse(roomsTab, [][]any{toAny(BookingColumns)}, usersTab, activityHeader, devicesTab, pricingTab)
	if err != nil {
		t.Fatal(err)
	}
	want := []booking.PriceRule{
		{DayType: booking.Weekday, Start: 660, End: 1020, RatePerHour: 60000},
		{DayType: booking.Weekend, Start: 1020, End: 660, RatePerHour: 170000},
	}
	if len(snap.pricing) != 2 || snap.pricing[0] != want[0] || snap.pricing[1] != want[1] {
		t.Errorf("pricing = %+v", snap.pricing)
	}
	bad := [][]any{toAny(PricingColumns), {"weekday", "jam 11", "17:00", 1}}
	if _, err := s.parse(roomsTab, [][]any{toAny(BookingColumns)}, usersTab, activityHeader, devicesTab, bad); err == nil {
		t.Error("want error for bad time")
	}
}

func TestParseMissingColumn(t *testing.T) {
	s := testStore(t)
	// An old Bookings tab without the *_by columns must ask for setup.
	old := toAny(BookingColumns[:15])
	if _, err := s.parse(roomsTab, [][]any{old}, usersTab, activityHeader, devicesTab, pricingTab); err == nil {
		t.Fatal("want error for missing columns")
	}
	if _, err := s.parse(roomsTab, [][]any{toAny(BookingColumns)}, nil, activityHeader, devicesTab, pricingTab); err == nil {
		t.Fatal("want error for empty Users tab")
	}
}

func TestParseBadTime(t *testing.T) {
	s := testStore(t)
	row := []any{"BK-1", "R01", "A", "", "besok", "2026-10-01 20:00"}
	if _, err := s.parse(roomsTab, [][]any{toAny(BookingColumns), row}, usersTab, activityHeader, devicesTab, pricingTab); err == nil {
		t.Fatal("want error for bad time")
	}
}

func TestBuildRowKeepsUnknownCells(t *testing.T) {
	cols := map[string]int{"a": 0, "c": 2}
	got := buildRow(map[string]any{"a": 1, "c": 3}, cols, []any{"x", "keep", "y", "tail"})
	want := []any{1, "keep", 3, "tail"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row = %v, want %v", got, want)
		}
	}
}

func TestColumnLetter(t *testing.T) {
	for i, want := range map[int]string{0: "A", 14: "O", 25: "Z", 26: "AA", 27: "AB", 701: "ZZ", 702: "AAA"} {
		if got := columnLetter(i); got != want {
			t.Errorf("columnLetter(%d) = %s, want %s", i, got, want)
		}
	}
}

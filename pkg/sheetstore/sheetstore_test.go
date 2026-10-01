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

func TestParseAndWriteRoundTrip(t *testing.T) {
	s := testStore(t)
	// Columns in a different order plus an extra staff column, as values come
	// back from the API with UNFORMATTED_VALUE.
	header := []any{"room_id", "id", "customer_name", "phone", "start", "end", "duration_minutes",
		"status", "rate_per_hour", "total_price", "notes", "checked_in_at", "checked_out_at",
		"created_at", "updated_at", "staff_note"}
	row := []any{"R01", "BK-1", "Budi", "0812", "2026-10-01 19:00", "2026-10-01 20:30", float64(90),
		"checked_in", float64(100000), float64(150000), "", "2026-10-01 18:55:10", "",
		"2026-10-01 10:00:00", "2026-10-01 18:55:10", "VIP guest"}
	rooms := [][]any{
		{"id", "name", "rate_per_hour", "active"},
		{"R01", "Room 01", "100.000", true},
		{"R02", "Room 02", float64(150000), "FALSE"},
		{"", "", "", ""},
	}

	snap, err := s.parse(rooms, [][]any{header, row, {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.rooms) != 2 || snap.rooms[0].RatePerHour != 100000 || !snap.rooms[0].Active || snap.rooms[1].Active {
		t.Errorf("rooms = %+v", snap.rooms)
	}
	if len(snap.bookings) != 1 || snap.bookingRow["BK-1"] != 2 {
		t.Fatalf("bookings = %+v rows %v", snap.bookings, snap.bookingRow)
	}
	b := snap.bookings[0]
	if b.Status != booking.StatusCheckedIn || b.DurationMinutes() != 90 || b.TotalPrice != 150000 || b.Phone != "0812" {
		t.Errorf("booking = %+v", b)
	}
	if b.Start.Hour() != 19 || b.Start.Location() != s.loc {
		t.Errorf("start = %v", b.Start)
	}

	b.Status = booking.StatusFinished
	out := s.bookingRow(b, snap.bookingCols, snap.bookingRaw["BK-1"])
	if out[0] != "R01" || out[1] != "BK-1" || out[7] != "finished" || out[15] != "VIP guest" {
		t.Errorf("row = %v", out)
	}
	if out[4] != "2026-10-01 19:00" {
		t.Errorf("start cell = %v", out[4])
	}
}

func TestParseMissingColumn(t *testing.T) {
	s := testStore(t)
	_, err := s.parse([][]any{{"id", "name"}}, [][]any{{"id"}})
	if err == nil {
		t.Fatal("want error for missing columns")
	}
}

func TestParseBadTime(t *testing.T) {
	s := testStore(t)
	rooms := [][]any{{"id", "name", "rate_per_hour", "active"}}
	header := toAny(BookingColumns)
	row := []any{"BK-1", "R01", "A", "", "besok", "2026-10-01 20:00"}
	if _, err := s.parse(rooms, [][]any{header, row}); err == nil {
		t.Fatal("want error for bad time")
	}
}

func TestColumnLetter(t *testing.T) {
	for i, want := range map[int]string{0: "A", 14: "O", 25: "Z", 26: "AA", 27: "AB", 701: "ZZ", 702: "AAA"} {
		if got := columnLetter(i); got != want {
			t.Errorf("columnLetter(%d) = %s, want %s", i, got, want)
		}
	}
}

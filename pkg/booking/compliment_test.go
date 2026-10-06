package booking_test

import (
	"errors"
	"strings"
	"testing"

	"karaoke/pkg/booking"
)

func TestComplimentBooking(t *testing.T) {
	f := newFixture(t) // Thursday 18:00, no price table: R01 = 100000/hour
	in := booking.CreateInput{RoomID: "R01", CustomerName: "Tamu VIP", Start: at("18:00"),
		DurationMinutes: 120, Complimentary: true, ComplimentReason: "  tamu GM  "}

	if _, err := f.svc.Create(f.ctx, f.staff, in); !errors.Is(err, booking.ErrForbidden) {
		t.Errorf("staff compliment: %v", err)
	}
	noReason := in
	noReason.ComplimentReason = " "
	if _, err := f.svc.Create(f.ctx, f.sup, noReason); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("no reason: %v", err)
	}
	tent := in
	tent.Tentative = true
	tent.Start = at("21:00")
	if _, err := f.svc.Create(f.ctx, f.sup, tent); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("tentative compliment: %v", err)
	}

	b, err := f.svc.Create(f.ctx, f.sup, in)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Complimentary || b.ComplimentReason != "tamu GM" || b.TotalPrice != 0 ||
		b.RatePerHour != 100000 || b.NormalPrice() != 200000 || b.Status != booking.StatusBooked {
		t.Fatalf("compliment = %+v", b)
	}
	// A reason without the flag is dropped.
	plain, _ := f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: "R02", CustomerName: "Biasa",
		Start: at("18:00"), DurationMinutes: 60, ComplimentReason: "harusnya hilang"})
	if plain.Complimentary || plain.ComplimentReason != "" || plain.TotalPrice != 150000 {
		t.Errorf("plain = %+v", plain)
	}

	// Free also after extending; staff may extend it.
	f.svc.CheckIn(f.ctx, f.staff, b.ID)
	ext, err := f.svc.Extend(f.ctx, f.staff, b.ID, 60)
	if err != nil || ext.TotalPrice != 0 || ext.NormalPrice() != 300000 {
		t.Fatalf("extend: %+v %v", ext, err)
	}
	f.svc.CheckIn(f.ctx, f.staff, plain.ID)

	rep, err := f.svc.Report(f.ctx, f.sup, f.now, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Revenue != 150000 || rep.Compliments != 1 || rep.ComplimentMinutes != 180 ||
		rep.ComplimentValue != 300000 || rep.CheckedIn != 2 || rep.Minutes != 240 {
		t.Errorf("report: %+v", rep)
	}

	log, _ := f.svc.Activity(f.ctx, f.admin, f.now)
	found := false
	for _, a := range log {
		if a.Action == booking.ActBookingCreate && strings.Contains(a.Detail, "COMPLIMENT (nilai normal Rp200.000): tamu GM") {
			found = true
		}
	}
	if !found {
		t.Errorf("compliment audit missing: %+v", log)
	}
}

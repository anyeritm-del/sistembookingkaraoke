package booking_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"karaoke/pkg/booking"
)

func TestBillableMinutes(t *testing.T) {
	cases := []struct{ used, booked, want int }{
		{5, 120, 60}, // at least one hour
		{60, 120, 60},
		{61, 120, 90}, // rounds up to 30 minutes
		{65, 120, 90},
		{90, 120, 90},
		{119, 120, 120}, // never more than booked
		{200, 120, 120},
		{20, 30, 30}, // a 30-minute booking stays 30
	}
	for _, c := range cases {
		if got := booking.BillableMinutes(c.used, c.booked); got != c.want {
			t.Errorf("BillableMinutes(%d, %d) = %d, want %d", c.used, c.booked, got, c.want)
		}
	}
}

func TestEarlyCheckout(t *testing.T) {
	f := newFixture(t) // 18:00, R01 = 100000/hour
	mk := func(room string) booking.Booking {
		b, err := f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: room, CustomerName: "Tamu " + room, Start: at("18:00"), DurationMinutes: 120})
		if err != nil {
			t.Fatal(err)
		}
		f.now = at("18:00")
		if _, err := f.svc.CheckIn(f.ctx, f.staff, b.ID); err != nil {
			t.Fatal(err)
		}
		return b
	}

	// Default: bill the booked time, but record the real time used.
	a := mk("R01")
	f.now = at("19:05")
	got, err := f.svc.CheckOut(f.ctx, f.staff, a.ID, false)
	if err != nil || got.TotalPrice != 200000 || got.BilledMinutes != 0 || got.UsedMinutes() != 65 || got.ChargedMinutes() != 120 {
		t.Fatalf("booked billing: %+v %v", got, err)
	}

	// Staff may not bill by usage.
	b := mk("R02") // 150000/hour
	f.now = at("19:05")
	if _, err := f.svc.CheckOut(f.ctx, f.staff, b.ID, true); !errors.Is(err, booking.ErrForbidden) {
		t.Errorf("staff by usage: %v", err)
	}
	// Supervisor: 65 minutes used -> 90 minutes billed.
	got, err = f.svc.CheckOut(f.ctx, f.sup, b.ID, true)
	if err != nil || got.BilledMinutes != 90 || got.TotalPrice != 225000 || got.ChargedMinutes() != 90 {
		t.Fatalf("usage billing: %+v %v", got, err)
	}

	// Report counts real minutes used.
	rep, _ := f.svc.Report(f.ctx, f.sup, f.now, f.now)
	if rep.Minutes != 130 || rep.Revenue != 425000 {
		t.Errorf("report minutes %d revenue %d", rep.Minutes, rep.Revenue)
	}
	log, _ := f.svc.Activity(f.ctx, f.admin, f.now)
	found := false
	for _, x := range log {
		if strings.Contains(x.Detail, "tagih sesuai pemakaian: 65 menit dipakai, ditagih 90 dari 120 menit, total Rp225.000 (sebelumnya Rp300.000)") {
			found = true
		}
	}
	if !found {
		t.Errorf("usage billing audit missing")
	}
}

func TestByUsageIgnoredWhenNotEarlyOrFree(t *testing.T) {
	f := newFixture(t)
	f.now = at("17:00")
	b, _ := f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: "R01", CustomerName: "Telat", Start: at("17:00"), DurationMinutes: 60})
	f.svc.CheckIn(f.ctx, f.staff, b.ID)
	f.now = at("18:10") // past the end
	got, _ := f.svc.CheckOut(f.ctx, f.sup, b.ID, true)
	if got.TotalPrice != 100000 || got.BilledMinutes != 0 {
		t.Errorf("late check-out: %+v", got)
	}

	f.now = at("18:00")
	c, _ := f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: "R02", CustomerName: "VIP", Start: at("18:00"),
		DurationMinutes: 120, Complimentary: true, ComplimentReason: "GM"})
	f.svc.CheckIn(f.ctx, f.staff, c.ID)
	f.now = f.now.Add(20 * time.Minute)
	got, _ = f.svc.CheckOut(f.ctx, f.sup, c.ID, true)
	if got.TotalPrice != 0 || got.BilledMinutes != 0 {
		t.Errorf("compliment early: %+v", got)
	}
}

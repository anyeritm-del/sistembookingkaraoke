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

func TestComplimentVoucher(t *testing.T) {
	f := newFixture(t)
	comp := func(room, start, voucher string) (booking.Booking, error) {
		return f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: room, CustomerName: "Tamu " + room,
			Start: at(start), DurationMinutes: 60, Complimentary: true, ComplimentReason: "voucher", VoucherNumber: voucher})
	}
	a, err := comp("R01", "19:00", "  hk-2026/001 ")
	if err != nil || a.VoucherNumber != "HK-2026/001" {
		t.Fatalf("first use: %+v %v", a, err)
	}
	// Same voucher again, any case: refused with the booking that has it.
	_, err = comp("R02", "19:00", "hk-2026/001")
	if !errors.Is(err, booking.ErrVoucherUsed) || !strings.Contains(err.Error(), a.ID) {
		t.Errorf("reuse: %v", err)
	}
	// Cancelling the first booking frees the voucher.
	if _, err := f.svc.Cancel(f.ctx, f.sup, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := comp("R02", "19:00", "HK-2026/001"); err != nil {
		t.Errorf("after cancel: %v", err)
	}
	if _, err := comp("R01", "21:00", "bad voucher!"); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("bad format: %v", err)
	}
	// Optional: a compliment without a voucher is fine.
	if b, err := comp("R01", "22:00", ""); err != nil || b.VoucherNumber != "" {
		t.Errorf("no voucher: %+v %v", b, err)
	}
	// Not a compliment: the voucher is dropped.
	plain, _ := f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: "R02", CustomerName: "Biasa",
		Start: at("21:00"), DurationMinutes: 60, VoucherNumber: "X-1"})
	if plain.VoucherNumber != "" {
		t.Errorf("plain kept voucher: %+v", plain)
	}
	// The list search finds bookings by voucher.
	list, _, _ := f.svc.List(f.ctx, f.sup, booking.ListFilter{From: f.now, To: f.now, Query: "2026/001"})
	if len(list) != 2 {
		t.Errorf("search by voucher: %d results", len(list))
	}
}

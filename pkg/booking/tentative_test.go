package booking_test

import (
	"errors"
	"testing"
	"time"

	"karaoke/pkg/booking"
)

func (f *fixture) tentative(t *testing.T, actor booking.User, room, start string, minutes int) booking.Booking {
	t.Helper()
	b, err := f.svc.Create(f.ctx, actor, booking.CreateInput{
		RoomID: room, CustomerName: "Tamu " + start, Start: at(start), DurationMinutes: minutes, Tentative: true,
	})
	if err != nil {
		t.Fatalf("tentative %s %s: %v", room, start, err)
	}
	return b
}

func TestTentativeHoldTime(t *testing.T) {
	f := newFixture(t) // now 18:00
	far := f.tentative(t, f.staff, "R01", "23:00", 60)
	if far.Status != booking.StatusTentative || !far.HoldUntil.Equal(at("21:00")) {
		t.Errorf("far: status %s hold %v, want 21:00", far.Status, far.HoldUntil)
	}
	// Start in 1 hour: 2 hours before is in the past, so hold 30 minutes.
	soon := f.tentative(t, f.staff, "R02", "19:00", 60)
	if !soon.HoldUntil.Equal(at("18:30")) {
		t.Errorf("soon hold = %v, want 18:30", soon.HoldUntil)
	}
	// Start in 10 minutes: hold never goes past the start.
	f.now = at("18:50")
	vsoon := f.tentative(t, f.staff, "R01", "19:00", 60)
	if !vsoon.HoldUntil.Equal(at("19:00")) {
		t.Errorf("very soon hold = %v, want 19:00", vsoon.HoldUntil)
	}
	// Tentative for a start in the past is refused.
	if _, err := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R02", CustomerName: "X",
		Start: at("18:30"), DurationMinutes: 60, Tentative: true}); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("past tentative: %v", err)
	}
}

func TestTentativeBlocksUntilExpiry(t *testing.T) {
	f := newFixture(t)
	tb := f.tentative(t, f.staff, "R01", "23:00", 60) // holds until 21:00

	_, err := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R01", CustomerName: "Lain", Start: at("23:00"), DurationMinutes: 60})
	if !errors.Is(err, booking.ErrConflict) {
		t.Fatalf("slot held by tentative: %v", err)
	}

	f.now = at("21:00") // expired: slot free again
	other, err := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R01", CustomerName: "Lain", Start: at("23:00"), DurationMinutes: 60})
	if err != nil {
		t.Fatalf("slot after expiry: %v", err)
	}
	// The expired one cannot be confirmed now, because the slot is taken.
	if _, err := f.svc.Confirm(f.ctx, f.staff, tb.ID); !errors.Is(err, booking.ErrConflict) {
		t.Errorf("confirm expired with slot taken: %v", err)
	}
	if _, err := f.svc.Cancel(f.ctx, f.sup, other.ID); err != nil {
		t.Fatal(err)
	}
	// Slot free again: an expired tentative can still be confirmed.
	got, err := f.svc.Confirm(f.ctx, f.staff, tb.ID)
	if err != nil {
		t.Fatalf("confirm expired with free slot: %v", err)
	}
	if got.Status != booking.StatusBooked || got.ConfirmedBy != "sari" {
		t.Errorf("after confirm: %+v", got)
	}
}

func TestTentativeRules(t *testing.T) {
	f := newFixture(t)
	tb := f.tentative(t, f.staff, "R01", "18:30", 60)

	if _, err := f.svc.CheckIn(f.ctx, f.staff, tb.ID); !errors.Is(err, booking.ErrWrongState) {
		t.Errorf("check-in tentative: %v", err)
	}
	if _, err := f.svc.Extend(f.ctx, f.staff, tb.ID, 30); !errors.Is(err, booking.ErrWrongState) {
		t.Errorf("extend tentative: %v", err)
	}
	if _, err := f.svc.Confirm(f.ctx, f.staff, tb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Confirm(f.ctx, f.staff, tb.ID); !errors.Is(err, booking.ErrWrongState) {
		t.Errorf("confirm twice: %v", err)
	}
	if _, err := f.svc.CheckIn(f.ctx, f.staff, tb.ID); err != nil {
		t.Errorf("check-in after confirm: %v", err)
	}
}

func TestCancelPermissionsByStatus(t *testing.T) {
	f := newFixture(t)
	tb := f.tentative(t, f.staff, "R01", "22:00", 60)
	cb, _ := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R02", CustomerName: "C", Start: at("22:00"), DurationMinutes: 60})

	if _, err := f.svc.Cancel(f.ctx, f.staff, cb.ID); !errors.Is(err, booking.ErrForbidden) {
		t.Errorf("staff cancel confirm: %v", err)
	}
	got, err := f.svc.Cancel(f.ctx, f.staff, tb.ID)
	if err != nil || got.Status != booking.StatusCancelled || got.CancelledBy != "sari" {
		t.Errorf("staff cancel tentative: %+v %v", got, err)
	}
	if _, err := f.svc.Cancel(f.ctx, f.sup, cb.ID); err != nil {
		t.Errorf("supervisor cancel confirm: %v", err)
	}
}

func TestList(t *testing.T) {
	f := newFixture(t)
	c1, _ := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R01", CustomerName: "Andi", Phone: "0811", Start: at("19:00"), DurationMinutes: 60})
	t1 := f.tentative(t, f.staff, "R02", "23:00", 60) // holds until 21:00
	t2 := f.tentative(t, f.staff, "R02", "19:30", 60) // holds until 18:30
	x, _ := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R01", CustomerName: "Batal", Start: at("21:00"), DurationMinutes: 60})
	f.svc.Cancel(f.ctx, f.sup, x.ID)
	next, _ := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R01", CustomerName: "Besok", Start: at("19:00").AddDate(0, 0, 1), DurationMinutes: 60})

	f.now = at("18:45") // t2 expired, t1 not
	today := at("12:00")
	ids := func(st, q string, to time.Time) []string {
		list, _, err := f.svc.List(f.ctx, f.staff, booking.ListFilter{From: today, To: to, Status: st, Query: q})
		if err != nil {
			t.Fatalf("list %q: %v", st, err)
		}
		var out []string
		for _, b := range list {
			out = append(out, b.ID)
		}
		return out
	}
	eq := func(name string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
			return
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s: got %v, want %v", name, got, want)
				return
			}
		}
	}
	eq("all today", ids("", "", today), c1.ID, t2.ID, x.ID, t1.ID)
	eq("confirm", ids("booked", "", today), c1.ID)
	eq("tentative", ids("tentative", "", today), t1.ID)
	eq("expired", ids("expired", "", today), t2.ID)
	eq("cancelled", ids("cancelled", "", today), x.ID)
	eq("two days", ids("booked", "", today.AddDate(0, 0, 1)), c1.ID, next.ID)
	eq("search phone", ids("", "0811", today), c1.ID)
	eq("search name", ids("", "BESOK", today.AddDate(0, 0, 1)), next.ID)

	_, sum, _ := f.svc.List(f.ctx, f.staff, booking.ListFilter{From: today, To: today, Status: "booked"})
	if sum.Count != 1 || sum.TotalPrice != 100000 {
		t.Errorf("summary = %+v", sum)
	}
	if _, _, err := f.svc.List(f.ctx, f.staff, booking.ListFilter{From: today, To: today, Status: "nope"}); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("bad status: %v", err)
	}
	if _, _, err := f.svc.List(f.ctx, f.staff, booking.ListFilter{From: today, To: today.AddDate(0, 0, 100)}); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("range too long: %v", err)
	}
	if _, _, err := f.svc.List(f.ctx, f.staff, booking.ListFilter{From: today, To: today.AddDate(0, 0, -1)}); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("reversed range: %v", err)
	}
}

func TestReportCountsTentative(t *testing.T) {
	f := newFixture(t)
	f.tentative(t, f.staff, "R01", "22:00", 60)
	rep, err := f.svc.Report(f.ctx, f.sup, f.now, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Tentative != 1 || rep.Revenue != 0 {
		t.Errorf("report = %+v", rep)
	}
}

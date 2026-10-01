package booking_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"karaoke/pkg/booking"
	"karaoke/pkg/memstore"
)

var wib = mustLoc("Asia/Jakarta")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func at(hhmm string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", "2026-10-01 "+hhmm, wib)
	if err != nil {
		panic(err)
	}
	return t
}

type fixture struct {
	svc *booking.Service
	now time.Time
	ctx context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: at("18:00"), ctx: context.Background()}
	store := memstore.New(
		booking.Room{ID: "R01", Name: "Room 01", RatePerHour: 100000, Active: true},
		booking.Room{ID: "R02", Name: "Room 02", RatePerHour: 150000, Active: true},
		booking.Room{ID: "OLD", Name: "Closed", RatePerHour: 50000, Active: false},
	)
	f.svc = booking.NewService(store, wib)
	f.svc.SetClock(func() time.Time { return f.now })
	return f
}

func (f *fixture) create(t *testing.T, room, start string, minutes int) booking.Booking {
	t.Helper()
	b, err := f.svc.Create(f.ctx, booking.CreateInput{
		RoomID: room, CustomerName: "Budi", Start: at(start), DurationMinutes: minutes,
	})
	if err != nil {
		t.Fatalf("create %s %s: %v", room, start, err)
	}
	return b
}

func TestCreatePriceAndStatus(t *testing.T) {
	f := newFixture(t)
	b := f.create(t, "R02", "19:00", 90)
	if b.TotalPrice != 225000 {
		t.Errorf("price = %d, want 225000", b.TotalPrice)
	}
	if b.Status != booking.StatusBooked {
		t.Errorf("status = %s", b.Status)
	}
	if !b.End.Equal(at("20:30")) {
		t.Errorf("end = %v", b.End)
	}
}

func TestCreateRejectsInvalid(t *testing.T) {
	f := newFixture(t)
	cases := map[string]booking.CreateInput{
		"no name":       {RoomID: "R01", Start: at("19:00"), DurationMinutes: 60},
		"bad duration":  {RoomID: "R01", CustomerName: "A", Start: at("19:00"), DurationMinutes: 45},
		"too long":      {RoomID: "R01", CustomerName: "A", Start: at("19:00"), DurationMinutes: 13 * 60},
		"already over":  {RoomID: "R01", CustomerName: "A", Start: at("16:00"), DurationMinutes: 60},
		"inactive room": {RoomID: "OLD", CustomerName: "A", Start: at("19:00"), DurationMinutes: 60},
		"no start":      {RoomID: "R01", CustomerName: "A", DurationMinutes: 60},
	}
	for name, in := range cases {
		if _, err := f.svc.Create(f.ctx, in); !errors.Is(err, booking.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := f.svc.Create(f.ctx, booking.CreateInput{RoomID: "X", CustomerName: "A", Start: at("19:00"), DurationMinutes: 60}); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("unknown room: err = %v", err)
	}
}

func TestCreateConflict(t *testing.T) {
	f := newFixture(t)
	f.create(t, "R01", "19:00", 120) // 19:00-21:00

	for _, start := range []string{"18:30", "20:00", "20:30"} {
		_, err := f.svc.Create(f.ctx, booking.CreateInput{RoomID: "R01", CustomerName: "X", Start: at(start), DurationMinutes: 60})
		if !errors.Is(err, booking.ErrConflict) {
			t.Errorf("start %s: err = %v, want ErrConflict", start, err)
		}
	}
	// Back-to-back and other rooms are fine.
	f.create(t, "R01", "21:00", 60)
	f.create(t, "R01", "18:00", 60)
	f.create(t, "R02", "19:30", 60)
}

func TestCancelledBookingFreesSlot(t *testing.T) {
	f := newFixture(t)
	b := f.create(t, "R01", "19:00", 60)
	if _, err := f.svc.Cancel(f.ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	f.create(t, "R01", "19:00", 60)
}

func TestExtend(t *testing.T) {
	f := newFixture(t)
	b := f.create(t, "R01", "19:00", 60)
	f.create(t, "R01", "21:00", 60)

	got, err := f.svc.Extend(f.ctx, b.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	if !got.End.Equal(at("21:00")) || got.TotalPrice != 200000 {
		t.Errorf("after extend: end %v price %d", got.End, got.TotalPrice)
	}
	if _, err := f.svc.Extend(f.ctx, b.ID, 30); !errors.Is(err, booking.ErrConflict) {
		t.Errorf("extend into next booking: err = %v", err)
	}
	if _, err := f.svc.Extend(f.ctx, b.ID, 20); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("extend 20 min: err = %v", err)
	}
}

func TestExtendKeepsOriginalRate(t *testing.T) {
	f := newFixture(t)
	b := f.create(t, "R01", "19:00", 60)
	// RatePerHour is frozen on the booking, so a 30-minute extension adds half of 100000.
	got, err := f.svc.Extend(f.ctx, b.ID, 30)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalPrice != 150000 {
		t.Errorf("price = %d", got.TotalPrice)
	}
}

func TestCheckInCheckOutFlow(t *testing.T) {
	f := newFixture(t)
	b := f.create(t, "R01", "20:00", 60)

	if _, err := f.svc.CheckIn(f.ctx, b.ID); !errors.Is(err, booking.ErrWrongState) {
		t.Errorf("check-in 2h early: err = %v", err)
	}
	f.now = at("19:30")
	got, err := f.svc.CheckIn(f.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != booking.StatusCheckedIn || !got.CheckedInAt.Equal(f.now) {
		t.Errorf("after check-in: %+v", got)
	}
	if _, err := f.svc.Cancel(f.ctx, b.ID); !errors.Is(err, booking.ErrWrongState) {
		t.Errorf("cancel checked-in: err = %v", err)
	}
	if _, err := f.svc.CheckIn(f.ctx, b.ID); !errors.Is(err, booking.ErrWrongState) {
		t.Errorf("double check-in: err = %v", err)
	}
	f.now = at("21:05")
	got, err = f.svc.CheckOut(f.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != booking.StatusFinished {
		t.Errorf("status = %s", got.Status)
	}
	if _, err := f.svc.CheckOut(f.ctx, "nope"); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("unknown id: err = %v", err)
	}
}

func TestCheckInBlockedWhileRoomOccupied(t *testing.T) {
	f := newFixture(t)
	first := f.create(t, "R01", "18:00", 60)
	second := f.create(t, "R01", "19:00", 60)
	if _, err := f.svc.CheckIn(f.ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	f.now = at("19:05") // first guest overstays and was not checked out
	if _, err := f.svc.CheckIn(f.ctx, second.ID); !errors.Is(err, booking.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestRoomStatus(t *testing.T) {
	f := newFixture(t)
	cur := f.create(t, "R01", "18:00", 60)
	next := f.create(t, "R01", "20:00", 60)
	f.create(t, "R02", "18:00", 60)

	st, err := f.svc.RoomStatus(f.ctx, "R01")
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != nil {
		t.Errorf("not checked in yet, current = %+v", st.Current)
	}
	if st.Next == nil || st.Next.ID != cur.ID {
		t.Errorf("next = %+v, want %s", st.Next, cur.ID)
	}

	if _, err := f.svc.CheckIn(f.ctx, cur.ID); err != nil {
		t.Fatal(err)
	}
	f.now = at("19:10") // time is up but not checked out: TV must keep alarming
	st, _ = f.svc.RoomStatus(f.ctx, "r01")
	if st.Current == nil || st.Current.ID != cur.ID {
		t.Fatalf("current = %+v, want %s", st.Current, cur.ID)
	}
	if st.Next == nil || st.Next.ID != next.ID {
		t.Errorf("next = %+v, want %s", st.Next, next.ID)
	}
	if !st.ServerTime.Equal(f.now) {
		t.Errorf("server time = %v", st.ServerTime)
	}
}

func TestReport(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, "R01", "18:00", 60)  // finished 100000
	b := f.create(t, "R02", "18:00", 120) // checked in 300000
	c := f.create(t, "R01", "20:00", 60)  // cancelled
	f.create(t, "R01", "22:00", 60)       // still booked

	// Next-day booking must not count.
	f.svc.Create(f.ctx, booking.CreateInput{RoomID: "R02", CustomerName: "Z", Start: at("18:00").AddDate(0, 0, 1), DurationMinutes: 60})

	ok := mustOK(t)
	ok(f.svc.CheckIn(f.ctx, a.ID))
	ok(f.svc.CheckOut(f.ctx, a.ID))
	ok(f.svc.CheckIn(f.ctx, b.ID))
	ok(f.svc.Cancel(f.ctx, c.ID))

	rep, err := f.svc.Report(f.ctx, at("12:00"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Date != "2026-10-01" || rep.Finished != 1 || rep.CheckedIn != 1 || rep.Cancelled != 1 || rep.Booked != 1 {
		t.Errorf("counts: %+v", rep)
	}
	if rep.Revenue != 400000 || rep.Minutes != 180 {
		t.Errorf("revenue %d minutes %d", rep.Revenue, rep.Minutes)
	}
	if len(rep.Rooms) != 3 || rep.Rooms[0].Revenue != 100000 || rep.Rooms[1].Revenue != 300000 {
		t.Errorf("rooms: %+v", rep.Rooms)
	}
}

func TestDayBookingsIncludesOvernight(t *testing.T) {
	f := newFixture(t)
	f.now = at("23:00")
	late := f.create(t, "R01", "23:30", 120) // ends 01:30 next day
	next, err := f.svc.DayBookings(f.ctx, at("12:00").AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].ID != late.ID {
		t.Errorf("next day = %+v", next)
	}
}

func TestPrice(t *testing.T) {
	cases := []struct {
		rate    int64
		minutes int
		want    int64
	}{
		{100000, 60, 100000},
		{100000, 30, 50000},
		{125000, 90, 187500},
		{99999, 30, 50000}, // 49999.5 rounds up
	}
	for _, c := range cases {
		if got := booking.Price(c.rate, c.minutes); got != c.want {
			t.Errorf("Price(%d, %d) = %d, want %d", c.rate, c.minutes, got, c.want)
		}
	}
}

func mustOK(t *testing.T) func(booking.Booking, error) {
	return func(_ booking.Booking, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

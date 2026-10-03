package booking_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"karaoke/pkg/auth"
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
	svc   *booking.Service
	store *memstore.Store
	now   time.Time
	ctx   context.Context
	// Users with each role; sup runs the booking tests.
	staff, sup, admin booking.User
}

var testPINs = func() *auth.PINHasher {
	h, err := auth.NewPINHasher("test-pepper-test-pepper-test-pepper")
	if err != nil {
		panic(err)
	}
	return h
}()

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: at("18:00"), ctx: context.Background()}
	store := memstore.New(
		booking.Room{ID: "R01", Name: "Room 01", RatePerHour: 100000, Active: true},
		booking.Room{ID: "R02", Name: "Room 02", RatePerHour: 150000, Active: true},
		booking.Room{ID: "OLD", Name: "Closed", RatePerHour: 50000, Active: false},
	)
	f.store = store
	f.svc = booking.NewService(store, wib, testPINs, "112233")
	f.svc.SetClock(func() time.Time { return f.now })
	f.staff = booking.User{Username: "sari", Name: "Sari", Role: booking.RoleStaff, Active: true}
	f.sup = booking.User{Username: "budi", Name: "Budi", Role: booking.RoleSupervisor, Active: true}
	f.admin = booking.User{Username: "admin", Name: "Admin", Role: booking.RoleAdmin, Active: true}
	return f
}

func (f *fixture) create(t *testing.T, room, start string, minutes int) booking.Booking {
	t.Helper()
	b, err := f.svc.Create(f.ctx, f.sup, booking.CreateInput{
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
		if _, err := f.svc.Create(f.ctx, f.sup, in); !errors.Is(err, booking.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: "X", CustomerName: "A", Start: at("19:00"), DurationMinutes: 60}); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("unknown room: err = %v", err)
	}
}

func TestCreateConflict(t *testing.T) {
	f := newFixture(t)
	f.create(t, "R01", "19:00", 120) // 19:00-21:00

	for _, start := range []string{"18:30", "20:00", "20:30"} {
		_, err := f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: "R01", CustomerName: "X", Start: at(start), DurationMinutes: 60})
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
	if _, err := f.svc.Cancel(f.ctx, f.sup, b.ID); err != nil {
		t.Fatal(err)
	}
	f.create(t, "R01", "19:00", 60)
}

func TestExtend(t *testing.T) {
	f := newFixture(t)
	b := f.create(t, "R01", "19:00", 60)
	f.create(t, "R01", "21:00", 60)

	got, err := f.svc.Extend(f.ctx, f.sup, b.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	if !got.End.Equal(at("21:00")) || got.TotalPrice != 200000 {
		t.Errorf("after extend: end %v price %d", got.End, got.TotalPrice)
	}
	if _, err := f.svc.Extend(f.ctx, f.sup, b.ID, 30); !errors.Is(err, booking.ErrConflict) {
		t.Errorf("extend into next booking: err = %v", err)
	}
	if _, err := f.svc.Extend(f.ctx, f.sup, b.ID, 20); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("extend 20 min: err = %v", err)
	}
}

func TestExtendKeepsOriginalRate(t *testing.T) {
	f := newFixture(t)
	b := f.create(t, "R01", "19:00", 60)
	// RatePerHour is frozen on the booking, so a 30-minute extension adds half of 100000.
	got, err := f.svc.Extend(f.ctx, f.sup, b.ID, 30)
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

	if _, err := f.svc.CheckIn(f.ctx, f.sup, b.ID); !errors.Is(err, booking.ErrWrongState) {
		t.Errorf("check-in 2h early: err = %v", err)
	}
	f.now = at("19:30")
	got, err := f.svc.CheckIn(f.ctx, f.sup, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != booking.StatusCheckedIn || !got.CheckedInAt.Equal(f.now) {
		t.Errorf("after check-in: %+v", got)
	}
	if _, err := f.svc.Cancel(f.ctx, f.sup, b.ID); !errors.Is(err, booking.ErrWrongState) {
		t.Errorf("cancel checked-in: err = %v", err)
	}
	if _, err := f.svc.CheckIn(f.ctx, f.sup, b.ID); !errors.Is(err, booking.ErrWrongState) {
		t.Errorf("double check-in: err = %v", err)
	}
	f.now = at("21:05")
	got, err = f.svc.CheckOut(f.ctx, f.sup, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != booking.StatusFinished {
		t.Errorf("status = %s", got.Status)
	}
	if _, err := f.svc.CheckOut(f.ctx, f.sup, "nope"); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("unknown id: err = %v", err)
	}
}

func TestCheckInBlockedWhileRoomOccupied(t *testing.T) {
	f := newFixture(t)
	first := f.create(t, "R01", "18:00", 60)
	second := f.create(t, "R01", "19:00", 60)
	if _, err := f.svc.CheckIn(f.ctx, f.sup, first.ID); err != nil {
		t.Fatal(err)
	}
	f.now = at("19:05") // first guest overstays and was not checked out
	if _, err := f.svc.CheckIn(f.ctx, f.sup, second.ID); !errors.Is(err, booking.ErrConflict) {
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

	if _, err := f.svc.CheckIn(f.ctx, f.sup, cur.ID); err != nil {
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
	f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: "R02", CustomerName: "Z", Start: at("18:00").AddDate(0, 0, 1), DurationMinutes: 60})

	ok := mustOK(t)
	ok(f.svc.CheckIn(f.ctx, f.sup, a.ID))
	ok(f.svc.CheckOut(f.ctx, f.sup, a.ID))
	ok(f.svc.CheckIn(f.ctx, f.sup, b.ID))
	ok(f.svc.Cancel(f.ctx, f.sup, c.ID))

	rep, err := f.svc.Report(f.ctx, f.sup, at("12:00"), at("12:00"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.From != "2026-10-01" || rep.To != "2026-10-01" || rep.Finished != 1 || rep.CheckedIn != 1 || rep.Cancelled != 1 || rep.Booked != 1 {
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
	next, err := f.svc.DayBookings(f.ctx, f.sup, at("12:00").AddDate(0, 0, 1))
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

func TestReportRange(t *testing.T) {
	f := newFixture(t) // Thursday 2026-10-01 18:00
	mk := func(room string, day time.Time, minutes int) booking.Booking {
		b, err := f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: room, CustomerName: "X", Start: day, DurationMinutes: minutes})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	thu := mk("R01", at("19:00"), 60)                   // 100000
	sat := mk("R02", at("20:00").AddDate(0, 0, 2), 120) // 300000, Saturday
	mk("R01", at("20:00").AddDate(0, 0, 5), 60)         // next Tuesday, outside the range
	f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: "R02", CustomerName: "T", Start: at("21:00"), DurationMinutes: 60, Tentative: true})

	// Make them count as sales: check in and out on their day.
	f.now = at("19:05")
	f.svc.CheckIn(f.ctx, f.sup, thu.ID)
	f.svc.CheckOut(f.ctx, f.sup, thu.ID)
	f.now = at("20:05").AddDate(0, 0, 2)
	f.svc.CheckIn(f.ctx, f.sup, sat.ID)

	rep, err := f.svc.Report(f.ctx, f.sup, at("00:00"), at("00:00").AddDate(0, 0, 3)) // Thu..Sun
	if err != nil {
		t.Fatal(err)
	}
	if rep.From != "2026-10-01" || rep.To != "2026-10-04" || len(rep.Days) != 4 {
		t.Fatalf("range: %s..%s days %d", rep.From, rep.To, len(rep.Days))
	}
	if rep.Revenue != 400000 || rep.Minutes != 180 || rep.Finished != 1 || rep.CheckedIn != 1 || rep.Tentative != 1 {
		t.Errorf("totals: %+v", rep)
	}
	wantDays := map[string]int64{"2026-10-01": 100000, "2026-10-02": 0, "2026-10-03": 300000, "2026-10-04": 0}
	for _, d := range rep.Days {
		if d.Revenue != wantDays[d.Date] {
			t.Errorf("day %s revenue %d, want %d", d.Date, d.Revenue, wantDays[d.Date])
		}
	}
	if rep.Rooms[0].Revenue != 100000 || rep.Rooms[1].Revenue != 300000 {
		t.Errorf("rooms: %+v", rep.Rooms)
	}

	if _, err := f.svc.Report(f.ctx, f.sup, at("12:00"), at("12:00").AddDate(0, 0, -1)); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("reversed range: %v", err)
	}
	if _, err := f.svc.Report(f.ctx, f.sup, at("12:00"), at("12:00").AddDate(0, 0, 92)); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("93 days: %v", err)
	}
	if _, err := f.svc.Report(f.ctx, f.sup, at("12:00"), at("12:00").AddDate(0, 0, 91)); err != nil {
		t.Errorf("92 days: %v", err)
	}
}

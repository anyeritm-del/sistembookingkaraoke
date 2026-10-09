package booking_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"karaoke/pkg/booking"
)

// dt returns a time in WIB, e.g. dt("2026-10-03 16:59").
func dt(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, wib)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRateAt(t *testing.T) {
	rules := booking.DefaultPricing()
	cases := []struct {
		at   string
		want int64
	}{
		// 2026-10-01 is a Thursday, 2026-10-02 Friday, 10-03 Saturday, 10-04 Sunday, 10-05 Monday.
		// Weekday = Monday-Thursday, weekend = Friday-Sunday.
		{"2026-10-01 11:00", 60000},
		{"2026-10-01 16:59", 60000},
		{"2026-10-01 17:00", 120000},
		{"2026-10-01 22:30", 120000},
		{"2026-10-01 10:59", 120000}, // before opening counts as evening rate
		{"2026-10-01 23:30", 120000}, // Thursday night is still weekday
		{"2026-10-02 00:30", 170000}, // after midnight it is Friday: weekend
		{"2026-10-02 11:00", 85000},  // Friday day
		{"2026-10-02 23:30", 170000}, // Friday night
		{"2026-10-03 00:30", 170000}, // Saturday early morning
		{"2026-10-03 11:00", 85000},
		{"2026-10-03 16:30", 85000},
		{"2026-10-03 17:00", 170000},
		{"2026-10-04 12:00", 85000},  // Sunday day
		{"2026-10-04 20:00", 170000}, // Sunday evening
		{"2026-10-05 11:30", 60000},  // Monday
	}
	for _, c := range cases {
		got, ok := booking.RateAt(rules, dt(c.at))
		if !ok || got != c.want {
			t.Errorf("RateAt(%s) = %d %v, want %d", c.at, got, ok, c.want)
		}
	}
}

func TestValidatePricing(t *testing.T) {
	if err := booking.ValidatePricing(booking.DefaultPricing()); err != nil {
		t.Fatalf("default: %v", err)
	}
	if err := booking.ValidatePricing(nil); err != nil {
		t.Errorf("empty table means room rates: %v", err)
	}
	d := booking.DefaultPricing()
	cases := map[string][]booking.PriceRule{
		"gap":          {d[0], {DayType: booking.Weekday, Start: 18 * 60, End: 11 * 60, RatePerHour: 1}, d[2], d[3]},
		"overlap":      {d[0], {DayType: booking.Weekday, Start: 16 * 60, End: 11 * 60, RatePerHour: 1}, d[2], d[3]},
		"no weekend":   {d[0], d[1]},
		"zero price":   {d[0], {DayType: booking.Weekday, Start: 17 * 60, End: 11 * 60}, d[2], d[3]},
		"bad day type": {d[0], d[1], d[2], {DayType: "holiday", Start: 17 * 60, End: 11 * 60, RatePerHour: 1}},
	}
	for name, rules := range cases {
		if err := booking.ValidatePricing(rules); !errors.Is(err, booking.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// One rule per day type with start == end covers the whole day.
	allDay := []booking.PriceRule{
		{DayType: booking.Weekday, Start: 0, End: 0, RatePerHour: 1},
		{DayType: booking.Weekend, Start: 600, End: 600, RatePerHour: 2},
	}
	if err := booking.ValidatePricing(allDay); err != nil {
		t.Errorf("all day: %v", err)
	}
}

func TestBookingUsesPriceTable(t *testing.T) {
	f := newFixture(t) // Thursday 2026-10-01 18:00
	// No table: the room's own rate (R01 = 100000).
	b, _ := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R01", CustomerName: "A", Start: at("19:00"), DurationMinutes: 60})
	if b.RatePerHour != 100000 {
		t.Fatalf("without table: %d", b.RatePerHour)
	}

	if _, err := f.svc.UpdatePricing(f.ctx, f.sup, booking.DefaultPricing()); !errors.Is(err, booking.ErrForbidden) {
		t.Errorf("supervisor update pricing: %v", err)
	}
	if _, err := f.svc.UpdatePricing(f.ctx, f.admin, booking.DefaultPricing()); err != nil {
		t.Fatal(err)
	}

	// Same price for every room; starting 16:00 for 2 hours uses the day rate
	// for the whole booking ("ikut harga jam mulai").
	f.now = dt("2026-10-01 10:00")
	day, err := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R02", CustomerName: "Siang", Start: dt("2026-10-01 16:00"), DurationMinutes: 120})
	if err != nil {
		t.Fatal(err)
	}
	if day.RatePerHour != 60000 || day.TotalPrice != 120000 {
		t.Errorf("weekday day: rate %d total %d", day.RatePerHour, day.TotalPrice)
	}
	// Extension keeps the start rate.
	f.now = dt("2026-10-01 16:00")
	f.svc.CheckIn(f.ctx, f.staff, day.ID)
	ext, err := f.svc.Extend(f.ctx, f.staff, day.ID, 60)
	if err != nil || ext.TotalPrice != 180000 {
		t.Errorf("extend: %d %v", ext.TotalPrice, err)
	}

	f.now = dt("2026-10-03 10:00")
	sat, _ := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R01", CustomerName: "Malam", Start: dt("2026-10-03 20:00"), DurationMinutes: 90})
	if sat.RatePerHour != 170000 || sat.TotalPrice != 255000 {
		t.Errorf("weekend evening: rate %d total %d", sat.RatePerHour, sat.TotalPrice)
	}

	// Quote gives the same numbers without saving.
	q, err := f.svc.QuoteFor(f.ctx, "R02", dt("2026-10-04 11:30"), 60)
	if err != nil || q.RatePerHour != 85000 || q.TotalPrice != 85000 {
		t.Errorf("quote: %+v %v", q, err)
	}

	// The audit log shows old and new prices.
	log, _ := f.svc.Activity(f.ctx, f.admin, dt("2026-10-01 12:00"))
	found := false
	for _, a := range log {
		if a.Action == booking.ActPricingUpdate && strings.Contains(a.Detail, "weekend 17:00-11:00 Rp170.000") {
			found = true
		}
	}
	if !found {
		t.Errorf("pricing audit missing: %+v", log)
	}
}

func TestUpdatePricingRejectsBadTable(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.UpdatePricing(f.ctx, f.admin, nil); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("empty table: %v", err)
	}
	d := booking.DefaultPricing()
	if _, err := f.svc.UpdatePricing(f.ctx, f.admin, d[:3]); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("incomplete table: %v", err)
	}
	if got, _ := f.svc.Pricing(f.ctx); len(got) != 0 {
		t.Errorf("bad table was saved: %+v", got)
	}
}

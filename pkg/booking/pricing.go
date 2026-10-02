package booking

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DayType groups days that share prices.
type DayType string

const (
	Weekday DayType = "weekday" // Monday to Friday
	Weekend DayType = "weekend" // Saturday and Sunday
)

// DayTypeOf returns the day type of t's calendar date.
func DayTypeOf(t time.Time) DayType {
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return Weekend
	}
	return Weekday
}

// Clock is a time of day in minutes after midnight (0..1439).
type Clock int

const minutesPerDay = 24 * 60

// ParseClock parses "HH:MM".
func ParseClock(s string) (Clock, error) {
	v := strings.TrimSpace(s)
	if len(v) == len("11:00:00") && strings.HasSuffix(v, ":00") {
		v = v[:5] // a time typed in the sheet may come back with seconds
	}
	h, m, ok := strings.Cut(v, ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("%w: jam %q harus HH:MM", ErrInvalid, s)
	}
	return Clock(hh*60 + mm), nil
}

func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", int(c)/60, int(c)%60) }

func (c Clock) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }

func (c *Clock) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := ParseClock(s)
	*c = v
	return err
}

// PriceRule is the hourly rate for bookings that START in [Start, End) on
// a day type. End before Start wraps past midnight (17:00-11:00 is the
// evening and the night). The whole booking, including extensions, uses
// the rate of its start time.
type PriceRule struct {
	DayType     DayType `json:"day_type"`
	Start       Clock   `json:"start"`
	End         Clock   `json:"end"`
	RatePerHour int64   `json:"rate_per_hour"`
}

// covers reports whether minute m of the day falls in the rule.
func (r PriceRule) covers(m Clock) bool {
	switch {
	case r.Start == r.End: // whole day
		return true
	case r.Start < r.End:
		return m >= r.Start && m < r.End
	default:
		return m >= r.Start || m < r.End
	}
}

// RateAt returns the hourly rate for a booking starting at t.
func RateAt(rules []PriceRule, t time.Time) (int64, bool) {
	dt := DayTypeOf(t)
	m := Clock(t.Hour()*60 + t.Minute())
	for _, r := range rules {
		if r.DayType == dt && r.covers(m) {
			return r.RatePerHour, true
		}
	}
	return 0, false
}

// ValidatePricing checks that each day type covers every minute of the day
// exactly once, so every start time has exactly one price. No rules at all
// is valid: then each room's own rate is used.
func ValidatePricing(rules []PriceRule) error {
	if len(rules) == 0 {
		return nil
	}
	for _, r := range rules {
		if r.DayType != Weekday && r.DayType != Weekend {
			return fmt.Errorf("%w: jenis hari harus weekday atau weekend", ErrInvalid)
		}
		if r.RatePerHour <= 0 || r.RatePerHour > 100_000_000 {
			return fmt.Errorf("%w: harga per jam harus lebih dari 0", ErrInvalid)
		}
		if r.Start < 0 || r.Start >= minutesPerDay || r.End < 0 || r.End >= minutesPerDay {
			return fmt.Errorf("%w: jam tidak valid", ErrInvalid)
		}
	}
	for _, dt := range []DayType{Weekday, Weekend} {
		label := map[DayType]string{Weekday: "Senin-Jumat", Weekend: "Sabtu-Minggu"}[dt]
		var count [minutesPerDay]int
		for _, r := range rules {
			if r.DayType != dt {
				continue
			}
			for m := Clock(0); m < minutesPerDay; m++ {
				if r.covers(m) {
					count[m]++
				}
			}
		}
		for m := Clock(0); m < minutesPerDay; m++ {
			switch {
			case count[m] == 0:
				return fmt.Errorf("%w: harga %s belum diatur untuk jam %s", ErrInvalid, label, m)
			case count[m] > 1:
				return fmt.Errorf("%w: harga %s tumpang tindih di jam %s", ErrInvalid, label, m)
			}
		}
	}
	return nil
}

// sortPricing orders rules by day type, then start time.
func sortPricing(rules []PriceRule) {
	slices.SortFunc(rules, func(a, b PriceRule) int {
		if a.DayType != b.DayType {
			return strings.Compare(string(a.DayType), string(b.DayType)) // "weekday" < "weekend"
		}
		return int(a.Start - b.Start)
	})
}

// Pricing returns the price table, ordered for display.
func (s *Service) Pricing(ctx context.Context) ([]PriceRule, error) {
	rules, err := s.store.ListPricing(ctx)
	if err != nil {
		return nil, err
	}
	sortPricing(rules)
	return rules, nil
}

// UpdatePricing replaces the whole price table. Existing bookings keep
// their price; the new table applies to new bookings.
func (s *Service) UpdatePricing(ctx context.Context, actor User, rules []PriceRule) ([]PriceRule, error) {
	if !actor.Can(PermManageRooms) {
		return nil, ErrForbidden
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("%w: tabel harga tidak boleh kosong", ErrInvalid)
	}
	if err := ValidatePricing(rules); err != nil {
		return nil, err
	}
	rules = slices.Clone(rules)
	sortPricing(rules)

	s.mu.Lock()
	defer s.mu.Unlock()
	ctx = WithFreshRead(ctx)
	old, err := s.store.ListPricing(ctx)
	if err != nil {
		return nil, err
	}
	sortPricing(old)
	if slices.Equal(old, rules) {
		return rules, nil
	}
	if err := s.store.ReplacePricing(ctx, rules); err != nil {
		return nil, err
	}
	s.audit(ctx, actor, ActPricingUpdate, "", "", pricingText(old)+" -> "+pricingText(rules))
	return rules, nil
}

func pricingText(rules []PriceRule) string {
	if len(rules) == 0 {
		return "(tarif per room)"
	}
	parts := make([]string, len(rules))
	for i, r := range rules {
		parts[i] = fmt.Sprintf("%s %s-%s %s", r.DayType, r.Start, r.End, rupiah(r.RatePerHour))
	}
	return strings.Join(parts, "; ")
}

// Quote is the price a booking would get.
type Quote struct {
	RatePerHour int64 `json:"rate_per_hour"`
	TotalPrice  int64 `json:"total_price"`
}

// QuoteFor returns the price of a booking without saving anything.
func (s *Service) QuoteFor(ctx context.Context, roomID string, start time.Time, minutes int) (Quote, error) {
	if err := validDuration(minutes); err != nil {
		return Quote{}, err
	}
	room, err := s.findRoom(ctx, roomID)
	if err != nil {
		return Quote{}, err
	}
	rate, err := s.rateFor(ctx, room, start.In(s.loc))
	if err != nil {
		return Quote{}, err
	}
	return Quote{RatePerHour: rate, TotalPrice: Price(rate, minutes)}, nil
}

// rateFor returns the hourly rate for a booking in room starting at start:
// from the price table when it has rules, else the room's own rate.
func (s *Service) rateFor(ctx context.Context, room Room, start time.Time) (int64, error) {
	rules, err := s.store.ListPricing(ctx)
	if err != nil {
		return 0, err
	}
	if len(rules) == 0 {
		return room.RatePerHour, nil
	}
	if err := ValidatePricing(rules); err != nil {
		return 0, fmt.Errorf("tabel harga di tab Pricing tidak valid, perbaiki di menu Harga: %w", err)
	}
	rate, ok := RateAt(rules, start)
	if !ok {
		return 0, fmt.Errorf("%w: tidak ada harga untuk %s", ErrInvalid, start.Format("Mon 15:04"))
	}
	return rate, nil
}

// DefaultPricing is the price table agreed on 2026-10-02.
func DefaultPricing() []PriceRule {
	return []PriceRule{
		{DayType: Weekday, Start: 11 * 60, End: 17 * 60, RatePerHour: 60_000},
		{DayType: Weekday, Start: 17 * 60, End: 11 * 60, RatePerHour: 120_000},
		{DayType: Weekend, Start: 11 * 60, End: 17 * 60, RatePerHour: 85_000},
		{DayType: Weekend, Start: 17 * 60, End: 11 * 60, RatePerHour: 170_000},
	}
}

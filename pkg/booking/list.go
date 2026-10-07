package booking

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ListFilter selects bookings for the booking list. Bookings are matched by
// the day they start, from From to To inclusive.
type ListFilter struct {
	From time.Time
	To   time.Time
	// Status is a Status, "expired" (tentative past its hold time), or "" for all.
	Status string
	// Query matches guest name, phone, notes, booking ID or voucher, ignoring case.
	Query  string
	RoomID string
}

// ListSummary totals the listed bookings.
type ListSummary struct {
	Count      int   `json:"count"`
	TotalPrice int64 `json:"total_price"`
}

// List returns bookings matching the filter, ordered by start time.
func (s *Service) List(ctx context.Context, actor User, f ListFilter) ([]Booking, ListSummary, error) {
	if !actor.Can(PermViewSchedule) {
		return nil, ListSummary{}, ErrForbidden
	}
	from, _ := s.dayRange(f.From)
	_, to := s.dayRange(f.To)
	if !to.After(from) {
		return nil, ListSummary{}, fmt.Errorf("%w: tanggal akhir sebelum tanggal awal", ErrInvalid)
	}
	if to.Sub(from) > MaxListDays*24*time.Hour {
		return nil, ListSummary{}, fmt.Errorf("%w: rentang maksimal %d hari", ErrInvalid, MaxListDays)
	}
	if f.Status != "" && f.Status != "expired" && !Status(f.Status).Valid() {
		return nil, ListSummary{}, fmt.Errorf("%w: status %q tidak dikenal", ErrInvalid, f.Status)
	}

	all, err := s.store.ListBookings(ctx)
	if err != nil {
		return nil, ListSummary{}, err
	}
	now := s.Now()
	q := strings.ToLower(strings.TrimSpace(f.Query))
	var out []Booking
	var sum ListSummary
	for _, b := range all {
		start := b.Start.In(s.loc)
		if start.Before(from) || !start.Before(to) {
			continue
		}
		if f.RoomID != "" && !strings.EqualFold(b.RoomID, f.RoomID) {
			continue
		}
		switch {
		case f.Status == "":
		case f.Status == "expired":
			if !b.Expired(now) {
				continue
			}
		case Status(f.Status) == StatusTentative:
			// "Tentative" lists the ones still holding a slot; expired ones have their own filter.
			if b.Status != StatusTentative || b.Expired(now) {
				continue
			}
		default:
			if b.Status != Status(f.Status) {
				continue
			}
		}
		if q != "" && !matches(b, q) {
			continue
		}
		out = append(out, b)
		sum.Count++
		sum.TotalPrice += b.TotalPrice
	}
	slices.SortFunc(out, func(a, b Booking) int { return a.Start.Compare(b.Start) })
	return out, sum, nil
}

func matches(b Booking, q string) bool {
	for _, field := range []string{b.CustomerName, b.Phone, b.Notes, b.ID, b.VoucherNumber} {
		if strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

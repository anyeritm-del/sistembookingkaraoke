package booking

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Booking limits. Durations move in 30-minute steps.
const (
	StepMinutes        = 30
	MaxDurationMinutes = 12 * 60
	// EarlyCheckIn is how long before the start time a guest may check in.
	EarlyCheckIn = 60 * time.Minute
)

// Service applies the booking rules on top of a Store.
//
// Writes are serialised with a mutex inside one process. Google Sheets has
// no transactions, so two server instances writing at the same moment can
// still race; with a small staff team this is rare, and the conflict check
// runs again on every write.
type Service struct {
	store Store
	loc   *time.Location
	now   func() time.Time
	mu    sync.Mutex
}

// NewService creates a Service. All dates are interpreted in loc.
func NewService(store Store, loc *time.Location) *Service {
	return &Service{store: store, loc: loc, now: time.Now}
}

// SetClock replaces the clock. Used by tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Location returns the business time zone.
func (s *Service) Location() *time.Location { return s.loc }

// Now returns the current time in the business time zone.
func (s *Service) Now() time.Time { return s.now().In(s.loc) }

// Rooms returns all rooms, active first, ordered by ID.
func (s *Service) Rooms(ctx context.Context) ([]Room, error) {
	rooms, err := s.store.ListRooms(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(rooms, func(a, b Room) int {
		if a.Active != b.Active {
			if a.Active {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return rooms, nil
}

// CreateInput is the data needed to make a booking.
type CreateInput struct {
	RoomID          string    `json:"room_id"`
	CustomerName    string    `json:"customer_name"`
	Phone           string    `json:"phone"`
	Notes           string    `json:"notes"`
	Start           time.Time `json:"start"`
	DurationMinutes int       `json:"duration_minutes"`
}

// Create validates the input, checks the schedule and saves a new booking.
func (s *Service) Create(ctx context.Context, in CreateInput) (Booking, error) {
	in.CustomerName = strings.TrimSpace(in.CustomerName)
	in.Phone = strings.TrimSpace(in.Phone)
	in.Notes = strings.TrimSpace(in.Notes)
	if in.CustomerName == "" {
		return Booking{}, fmt.Errorf("%w: nama tamu wajib diisi", ErrInvalid)
	}
	if err := validDuration(in.DurationMinutes); err != nil {
		return Booking{}, err
	}
	if in.Start.IsZero() {
		return Booking{}, fmt.Errorf("%w: jam mulai wajib diisi", ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ctx = WithFreshRead(ctx)

	room, err := s.findRoom(ctx, in.RoomID)
	if err != nil {
		return Booking{}, err
	}
	if !room.Active {
		return Booking{}, fmt.Errorf("%w: room %s tidak aktif", ErrInvalid, room.ID)
	}

	now := s.Now()
	start := in.Start.In(s.loc).Truncate(time.Minute)
	end := start.Add(time.Duration(in.DurationMinutes) * time.Minute)
	if !end.After(now) {
		return Booking{}, fmt.Errorf("%w: jam selesai sudah lewat", ErrInvalid)
	}

	all, err := s.store.ListBookings(ctx)
	if err != nil {
		return Booking{}, err
	}
	if c, ok := findConflict(all, room.ID, start, end, ""); ok {
		return Booking{}, conflictError(c)
	}

	b := Booking{
		ID:           newID(now),
		RoomID:       room.ID,
		CustomerName: in.CustomerName,
		Phone:        in.Phone,
		Notes:        in.Notes,
		Start:        start,
		End:          end,
		Status:       StatusBooked,
		RatePerHour:  room.RatePerHour,
		TotalPrice:   Price(room.RatePerHour, in.DurationMinutes),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.store.AddBooking(ctx, b); err != nil {
		return Booking{}, err
	}
	return b, nil
}

// Extend adds minutes to the end of an active booking and updates the price.
func (s *Service) Extend(ctx context.Context, id string, minutes int) (Booking, error) {
	if minutes <= 0 || minutes%StepMinutes != 0 || minutes > MaxDurationMinutes {
		return Booking{}, fmt.Errorf("%w: perpanjangan harus kelipatan %d menit", ErrInvalid, StepMinutes)
	}
	return s.change(ctx, id, func(b *Booking, all []Booking, now time.Time) error {
		if !b.Status.Active() {
			return ErrWrongState
		}
		newEnd := b.End.Add(time.Duration(minutes) * time.Minute)
		if b.DurationMinutes()+minutes > MaxDurationMinutes {
			return fmt.Errorf("%w: durasi maksimal %d jam", ErrInvalid, MaxDurationMinutes/60)
		}
		if c, ok := findConflict(all, b.RoomID, b.End, newEnd, b.ID); ok {
			return conflictError(c)
		}
		b.End = newEnd
		b.TotalPrice = Price(b.RatePerHour, b.DurationMinutes())
		return nil
	})
}

// CheckIn marks the guest as arrived. The TV timer starts showing this booking.
func (s *Service) CheckIn(ctx context.Context, id string) (Booking, error) {
	return s.change(ctx, id, func(b *Booking, all []Booking, now time.Time) error {
		if b.Status != StatusBooked {
			return ErrWrongState
		}
		if now.Before(b.Start.Add(-EarlyCheckIn)) {
			return fmt.Errorf("%w: check-in paling cepat %d menit sebelum jam mulai", ErrWrongState, int(EarlyCheckIn/time.Minute))
		}
		if !now.Before(b.End) {
			return fmt.Errorf("%w: waktu booking sudah habis", ErrWrongState)
		}
		for _, o := range all {
			if o.ID != b.ID && o.RoomID == b.RoomID && o.Status == StatusCheckedIn {
				return fmt.Errorf("%w: room masih dipakai tamu %s (belum check-out)", ErrConflict, o.CustomerName)
			}
		}
		b.Status = StatusCheckedIn
		b.CheckedInAt = now
		return nil
	})
}

// CheckOut finishes a checked-in booking. The TV alarm stops.
func (s *Service) CheckOut(ctx context.Context, id string) (Booking, error) {
	return s.change(ctx, id, func(b *Booking, _ []Booking, now time.Time) error {
		if b.Status != StatusCheckedIn {
			return ErrWrongState
		}
		b.Status = StatusFinished
		b.CheckedOutAt = now
		return nil
	})
}

// Cancel cancels a booking that has not been checked in.
func (s *Service) Cancel(ctx context.Context, id string) (Booking, error) {
	return s.change(ctx, id, func(b *Booking, _ []Booking, _ time.Time) error {
		if b.Status != StatusBooked {
			return ErrWrongState
		}
		b.Status = StatusCancelled
		return nil
	})
}

// change loads one booking, applies fn and saves the result.
func (s *Service) change(ctx context.Context, id string, fn func(b *Booking, all []Booking, now time.Time) error) (Booking, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx = WithFreshRead(ctx)

	all, err := s.store.ListBookings(ctx)
	if err != nil {
		return Booking{}, err
	}
	i := slices.IndexFunc(all, func(b Booking) bool { return b.ID == id })
	if i < 0 {
		return Booking{}, ErrNotFound
	}
	b := all[i]
	now := s.Now()
	if err := fn(&b, all, now); err != nil {
		return Booking{}, err
	}
	b.UpdatedAt = now
	if err := s.store.UpdateBooking(ctx, b); err != nil {
		return Booking{}, err
	}
	return b, nil
}

// DayBookings returns every booking that overlaps the given day, ordered by start.
func (s *Service) DayBookings(ctx context.Context, day time.Time) ([]Booking, error) {
	all, err := s.store.ListBookings(ctx)
	if err != nil {
		return nil, err
	}
	from, to := s.dayRange(day)
	var out []Booking
	for _, b := range all {
		if b.Overlaps(from, to) {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b Booking) int { return a.Start.Compare(b.Start) })
	return out, nil
}

// RoomReport is one room's line in the daily report.
type RoomReport struct {
	RoomID   string `json:"room_id"`
	RoomName string `json:"room_name"`
	Bookings int    `json:"bookings"`
	Minutes  int    `json:"minutes"`
	Revenue  int64  `json:"revenue"`
}

// DailyReport sums up one day. A booking belongs to the day it starts.
// Revenue and Minutes count finished and checked-in bookings only.
type DailyReport struct {
	Date      string       `json:"date"`
	Finished  int          `json:"finished"`
	CheckedIn int          `json:"checked_in"`
	Booked    int          `json:"booked"`
	Cancelled int          `json:"cancelled"`
	Minutes   int          `json:"minutes"`
	Revenue   int64        `json:"revenue"`
	Rooms     []RoomReport `json:"rooms"`
}

// Report builds the daily report for the given day.
func (s *Service) Report(ctx context.Context, day time.Time) (DailyReport, error) {
	rooms, err := s.Rooms(ctx)
	if err != nil {
		return DailyReport{}, err
	}
	all, err := s.store.ListBookings(ctx)
	if err != nil {
		return DailyReport{}, err
	}
	from, to := s.dayRange(day)
	rep := DailyReport{Date: from.Format(time.DateOnly)}
	order := make([]string, 0, len(rooms))
	byRoom := map[string]*RoomReport{}
	for _, r := range rooms {
		order = append(order, r.ID)
		byRoom[r.ID] = &RoomReport{RoomID: r.ID, RoomName: r.Name}
	}

	for _, b := range all {
		start := b.Start.In(s.loc)
		if start.Before(from) || !start.Before(to) {
			continue
		}
		switch b.Status {
		case StatusFinished:
			rep.Finished++
		case StatusCheckedIn:
			rep.CheckedIn++
		case StatusBooked:
			rep.Booked++
			continue
		case StatusCancelled:
			rep.Cancelled++
			continue
		}
		rep.Minutes += b.DurationMinutes()
		rep.Revenue += b.TotalPrice
		rr, ok := byRoom[b.RoomID]
		if !ok { // room was deleted from the sheet
			rr = &RoomReport{RoomID: b.RoomID, RoomName: b.RoomID}
			byRoom[b.RoomID] = rr
			order = append(order, b.RoomID)
		}
		rr.Bookings++
		rr.Minutes += b.DurationMinutes()
		rr.Revenue += b.TotalPrice
	}
	for _, id := range order {
		rep.Rooms = append(rep.Rooms, *byRoom[id])
	}
	return rep, nil
}

// TVBooking is the part of a booking that the room TV may show.
// Phone number and price are left out on purpose.
type TVBooking struct {
	ID           string    `json:"id"`
	CustomerName string    `json:"customer_name"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
}

// TVStatus is what a room TV needs to show the timer and play alarms.
type TVStatus struct {
	Room       Room       `json:"room"`
	ServerTime time.Time  `json:"server_time"`
	Current    *TVBooking `json:"current"`
	Next       *TVBooking `json:"next"`
}

// RoomStatus returns the checked-in booking (if any) and the next booking
// for a room. The TV keeps alarming while Current exists and its End has
// passed, until staff checks out or extends.
func (s *Service) RoomStatus(ctx context.Context, roomID string) (TVStatus, error) {
	room, err := s.findRoom(ctx, roomID)
	if err != nil {
		return TVStatus{}, err
	}
	all, err := s.store.ListBookings(ctx)
	if err != nil {
		return TVStatus{}, err
	}
	now := s.Now()
	st := TVStatus{Room: room, ServerTime: now}
	var cur, next *Booking
	for i := range all {
		b := &all[i]
		if b.RoomID != room.ID {
			continue
		}
		switch {
		case b.Status == StatusCheckedIn:
			if cur == nil || b.End.Before(cur.End) {
				cur = b
			}
		case b.Status == StatusBooked && b.End.After(now) && b.Start.Before(now.Add(24*time.Hour)):
			if next == nil || b.Start.Before(next.Start) {
				next = b
			}
		}
	}
	st.Current = toTV(cur)
	st.Next = toTV(next)
	return st, nil
}

func toTV(b *Booking) *TVBooking {
	if b == nil {
		return nil
	}
	return &TVBooking{ID: b.ID, CustomerName: b.CustomerName, Start: b.Start, End: b.End}
}

// Price returns the price in rupiah for a duration, rounded to the nearest rupiah.
func Price(ratePerHour int64, minutes int) int64 {
	return (ratePerHour*int64(minutes) + 30) / 60
}

func validDuration(minutes int) error {
	if minutes < StepMinutes || minutes > MaxDurationMinutes || minutes%StepMinutes != 0 {
		return fmt.Errorf("%w: durasi harus %d menit sampai %d jam, kelipatan %d menit",
			ErrInvalid, StepMinutes, MaxDurationMinutes/60, StepMinutes)
	}
	return nil
}

// findConflict returns an active booking in the room that overlaps [start, end),
// ignoring the booking with ID skipID.
func findConflict(all []Booking, roomID string, start, end time.Time, skipID string) (Booking, bool) {
	for _, b := range all {
		if b.ID == skipID || b.RoomID != roomID || !b.Status.Active() {
			continue
		}
		if b.Overlaps(start, end) {
			return b, true
		}
	}
	return Booking{}, false
}

func conflictError(c Booking) error {
	return fmt.Errorf("%w: %s %s-%s (%s)", ErrConflict, c.RoomID,
		c.Start.Format("02/01 15:04"), c.End.Format("15:04"), c.CustomerName)
}

func (s *Service) findRoom(ctx context.Context, id string) (Room, error) {
	rooms, err := s.store.ListRooms(ctx)
	if err != nil {
		return Room{}, err
	}
	for _, r := range rooms {
		if strings.EqualFold(r.ID, strings.TrimSpace(id)) {
			return r, nil
		}
	}
	return Room{}, fmt.Errorf("%w: room %q", ErrNotFound, id)
}

// dayRange returns [00:00, next day 00:00) of day in the business time zone.
func (s *Service) dayRange(day time.Time) (time.Time, time.Time) {
	d := day.In(s.loc)
	from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, s.loc)
	return from, from.AddDate(0, 0, 1)
}

// newID returns an ID like BK-261001-7Q2M9X. The date part helps staff find
// rows in the sheet; the random part avoids collisions between instances.
func newID(now time.Time) string {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return "BK-" + now.Format("060102") + "-" + string(buf)
}

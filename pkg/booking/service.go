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
// The hourly rate comes from the price table (see pricing.go) for the start
// time, or from the room when there is no table, and is then frozen on the booking.
const (
	StepMinutes        = 30
	MaxDurationMinutes = 12 * 60
	// EarlyCheckIn is how long before the start time a guest may check in.
	EarlyCheckIn = 60 * time.Minute
	// A tentative booking holds its slot until TentativeHoldBefore the start.
	// If that is too soon, it holds for TentativeMinHold (never past the start).
	TentativeHoldBefore = 2 * time.Hour
	TentativeMinHold    = 30 * time.Minute
	// MaxListDays limits the date range of the booking list.
	MaxListDays = 92
)

// Service applies the booking rules on top of a Store.
//
// Writes are serialised with a mutex inside one process. Google Sheets has
// no transactions, so two server instances writing at the same moment can
// still race; with a small staff team this is rare, and the conflict check
// runs again on every write.
type Service struct {
	store        Store
	loc          *time.Location
	now          func() time.Time
	mu           sync.Mutex
	pins         PINHasher
	bootstrapPIN string
	guard        loginGuard
	pairGuard    loginGuard
}

// NewService creates a Service. All dates are interpreted in loc.
// bootstrapPIN is the ADMIN_PIN used to create the first admin while there
// are no users; it may be empty once users exist.
func NewService(store Store, loc *time.Location, pins PINHasher, bootstrapPIN string) *Service {
	return &Service{store: store, loc: loc, now: time.Now, pins: pins, bootstrapPIN: bootstrapPIN}
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
	// Tentative makes a booking that holds the slot only until HoldUntil.
	Tentative bool `json:"tentative"`
	// Complimentary makes a free, confirmed booking; it needs PermCompliment
	// and a reason.
	Complimentary    bool   `json:"complimentary"`
	ComplimentReason string `json:"compliment_reason"`
}

// Create validates the input, checks the schedule and saves a new booking.
func (s *Service) Create(ctx context.Context, actor User, in CreateInput) (Booking, error) {
	if !actor.Can(PermCreateBooking) {
		return Booking{}, ErrForbidden
	}
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
	in.ComplimentReason = strings.TrimSpace(in.ComplimentReason)
	if in.Complimentary {
		if !actor.Can(PermCompliment) {
			return Booking{}, fmt.Errorf("%w: booking compliment hanya untuk supervisor atau admin", ErrForbidden)
		}
		if in.Tentative {
			return Booking{}, fmt.Errorf("%w: compliment tidak bisa tentative", ErrInvalid)
		}
		if in.ComplimentReason == "" || len(in.ComplimentReason) > 200 {
			return Booking{}, fmt.Errorf("%w: alasan compliment wajib diisi (maks. 200 karakter)", ErrInvalid)
		}
	} else {
		in.ComplimentReason = ""
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
	if in.Tentative && !start.After(now) {
		return Booking{}, fmt.Errorf("%w: tentative hanya untuk booking yang belum mulai; pakai confirm untuk tamu yang sudah datang", ErrInvalid)
	}

	all, err := s.store.ListBookings(ctx)
	if err != nil {
		return Booking{}, err
	}
	if c, ok := findConflict(all, room.ID, start, end, "", now); ok {
		return Booking{}, conflictError(c)
	}
	rate, err := s.rateFor(ctx, room, start)
	if err != nil {
		return Booking{}, err
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
		RatePerHour:  rate,
		TotalPrice:   Price(rate, in.DurationMinutes),
		CreatedAt:    now,
		UpdatedAt:    now,
		CreatedBy:    actor.Username,
	}
	kind := "confirm"
	if in.Complimentary {
		b.Complimentary, b.ComplimentReason, b.TotalPrice = true, in.ComplimentReason, 0
		kind = fmt.Sprintf("COMPLIMENT (nilai normal %s): %s", rupiah(b.NormalPrice()), in.ComplimentReason)
	}
	if in.Tentative {
		b.Status = StatusTentative
		b.HoldUntil = tentativeHold(start, now)
		kind = "tentative s/d " + b.HoldUntil.Format("02/01 15:04")
	}
	if err := s.store.AddBooking(ctx, b); err != nil {
		return Booking{}, err
	}
	s.audit(ctx, actor, ActBookingCreate, b.ID, b.RoomID, fmt.Sprintf("%s %s-%s %s, %s",
		b.CustomerName, b.Start.Format("02/01 15:04"), b.End.Format("15:04"), rupiah(b.TotalPrice), kind))
	return b, nil
}

// Extend adds minutes to the end of an active booking and updates the price.
func (s *Service) Extend(ctx context.Context, actor User, id string, minutes int) (Booking, error) {
	if minutes <= 0 || minutes%StepMinutes != 0 || minutes > MaxDurationMinutes {
		return Booking{}, fmt.Errorf("%w: perpanjangan harus kelipatan %d menit", ErrInvalid, StepMinutes)
	}
	return s.change(ctx, actor, PermExtend, ActExtend, id, func(b *Booking, all []Booking, now time.Time) (string, error) {
		if b.Status != StatusBooked && b.Status != StatusCheckedIn {
			return "", fmt.Errorf("%w: hanya booking confirm atau check-in yang bisa diperpanjang", ErrWrongState)
		}
		newEnd := b.End.Add(time.Duration(minutes) * time.Minute)
		if b.DurationMinutes()+minutes > MaxDurationMinutes {
			return "", fmt.Errorf("%w: durasi maksimal %d jam", ErrInvalid, MaxDurationMinutes/60)
		}
		if c, ok := findConflict(all, b.RoomID, b.End, newEnd, b.ID, now); ok {
			return "", conflictError(c)
		}
		b.End = newEnd
		b.TotalPrice = Price(b.RatePerHour, b.DurationMinutes())
		if b.Complimentary {
			b.TotalPrice = 0 // the whole stay is free, extensions too
		}
		return fmt.Sprintf("+%d menit, selesai %s, total %s", minutes, newEnd.Format("15:04"), rupiah(b.TotalPrice)), nil
	})
}

// Confirm turns a tentative booking into a confirmed one. An expired
// tentative booking can still be confirmed if its slot is free.
func (s *Service) Confirm(ctx context.Context, actor User, id string) (Booking, error) {
	return s.change(ctx, actor, PermConfirm, ActConfirm, id, func(b *Booking, all []Booking, now time.Time) (string, error) {
		if b.Status != StatusTentative {
			return "", fmt.Errorf("%w: hanya booking tentative yang bisa dikonfirmasi", ErrWrongState)
		}
		if !now.Before(b.End) {
			return "", fmt.Errorf("%w: waktu booking sudah lewat", ErrWrongState)
		}
		if c, ok := findConflict(all, b.RoomID, b.Start, b.End, b.ID, now); ok {
			return "", conflictError(c)
		}
		detail := b.CustomerName
		if b.Expired(now) {
			detail += " (sudah kedaluwarsa, slot masih kosong)"
		}
		b.Status = StatusBooked
		b.ConfirmedBy = actor.Username
		return detail, nil
	})
}

// CheckIn marks the guest as arrived. The TV timer starts showing this booking.
func (s *Service) CheckIn(ctx context.Context, actor User, id string) (Booking, error) {
	return s.change(ctx, actor, PermCheckIn, ActCheckIn, id, func(b *Booking, all []Booking, now time.Time) (string, error) {
		if b.Status == StatusTentative {
			return "", fmt.Errorf("%w: konfirmasi booking tentative dulu sebelum check-in", ErrWrongState)
		}
		if b.Status != StatusBooked {
			return "", ErrWrongState
		}
		if now.Before(b.Start.Add(-EarlyCheckIn)) {
			return "", fmt.Errorf("%w: check-in paling cepat %d menit sebelum jam mulai", ErrWrongState, int(EarlyCheckIn/time.Minute))
		}
		if !now.Before(b.End) {
			return "", fmt.Errorf("%w: waktu booking sudah habis", ErrWrongState)
		}
		for _, o := range all {
			if o.ID != b.ID && o.RoomID == b.RoomID && o.Status == StatusCheckedIn {
				return "", fmt.Errorf("%w: room masih dipakai tamu %s (belum check-out)", ErrConflict, o.CustomerName)
			}
		}
		b.Status = StatusCheckedIn
		b.CheckedInAt = now
		b.CheckedInBy = actor.Username
		return b.CustomerName, nil
	})
}

// CheckOut finishes a checked-in booking. The TV alarm stops.
// With byUsage, an early check-out is billed for the time used (see
// BillableMinutes) instead of the booked time; that needs PermBillByUsage.
func (s *Service) CheckOut(ctx context.Context, actor User, id string, byUsage bool) (Booking, error) {
	if byUsage && !actor.Can(PermBillByUsage) {
		return Booking{}, fmt.Errorf("%w: tagih sesuai pemakaian hanya untuk supervisor atau admin", ErrForbidden)
	}
	return s.change(ctx, actor, PermCheckOut, ActCheckOut, id, func(b *Booking, _ []Booking, now time.Time) (string, error) {
		if b.Status != StatusCheckedIn {
			return "", ErrWrongState
		}
		b.Status = StatusFinished
		b.CheckedOutAt = now
		b.CheckedOutBy = actor.Username
		detail := fmt.Sprintf("%s, total %s", b.CustomerName, rupiah(b.TotalPrice))
		if !byUsage || b.Complimentary || !now.Before(b.End) {
			return detail, nil
		}
		booked := b.DurationMinutes()
		billed := BillableMinutes(b.UsedMinutes(), booked)
		if billed >= booked {
			return detail + " (pemakaian penuh, tagihan tetap)", nil
		}
		before := b.TotalPrice
		b.BilledMinutes = billed
		b.TotalPrice = Price(b.RatePerHour, billed)
		return fmt.Sprintf("%s, check-out lebih awal, tagih sesuai pemakaian: %d menit dipakai, ditagih %d dari %d menit, total %s (sebelumnya %s)",
			b.CustomerName, b.UsedMinutes(), billed, booked, rupiah(b.TotalPrice), rupiah(before)), nil
	})
}

// Cancel cancels a booking that has not been checked in. Cancelling a
// tentative booking needs PermCancelTentative; a confirmed one needs PermCancel.
func (s *Service) Cancel(ctx context.Context, actor User, id string) (Booking, error) {
	return s.change(ctx, actor, PermCancelTentative, ActCancel, id, func(b *Booking, _ []Booking, _ time.Time) (string, error) {
		if b.Status != StatusBooked && b.Status != StatusTentative {
			return "", ErrWrongState
		}
		if b.Status == StatusBooked && !actor.Can(PermCancel) {
			return "", fmt.Errorf("%w: membatalkan booking confirm hanya untuk supervisor atau admin", ErrForbidden)
		}
		b.Status = StatusCancelled
		b.CancelledBy = actor.Username
		return fmt.Sprintf("%s %s", b.CustomerName, b.Start.Format("02/01 15:04")), nil
	})
}

// change checks the permission, loads one booking, applies fn, saves the
// result and writes the audit line. fn returns the audit detail text.
func (s *Service) change(ctx context.Context, actor User, perm Permission, action, id string,
	fn func(b *Booking, all []Booking, now time.Time) (string, error)) (Booking, error) {
	if !actor.Can(perm) {
		return Booking{}, ErrForbidden
	}
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
	detail, err := fn(&b, all, now)
	if err != nil {
		return Booking{}, err
	}
	b.UpdatedAt = now
	if err := s.store.UpdateBooking(ctx, b); err != nil {
		return Booking{}, err
	}
	s.audit(ctx, actor, action, b.ID, b.RoomID, detail)
	return b, nil
}

// DayBookings returns every booking that overlaps the given day, ordered by start.
func (s *Service) DayBookings(ctx context.Context, actor User, day time.Time) ([]Booking, error) {
	if !actor.Can(PermViewSchedule) {
		return nil, ErrForbidden
	}
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

// RoomReport is one room's line in the report.
type RoomReport struct {
	RoomID   string `json:"room_id"`
	RoomName string `json:"room_name"`
	Bookings int    `json:"bookings"`
	Minutes  int    `json:"minutes"`
	Revenue  int64  `json:"revenue"`
}

// DayReport is one day's line in the report.
type DayReport struct {
	Date     string `json:"date"`
	Bookings int    `json:"bookings"`
	Minutes  int    `json:"minutes"`
	Revenue  int64  `json:"revenue"`
}

// SalesReport sums up the days From..To (inclusive). A booking belongs to
// the day it starts. Revenue and Minutes count finished and checked-in
// bookings only; the other statuses are only counted.
type SalesReport struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Finished  int    `json:"finished"`
	CheckedIn int    `json:"checked_in"`
	Booked    int    `json:"booked"`
	Tentative int    `json:"tentative"`
	Cancelled int    `json:"cancelled"`
	Minutes   int    `json:"minutes"`
	Revenue   int64  `json:"revenue"`
	// Compliments are the finished and checked-in free bookings (already
	// inside Finished/CheckedIn/Minutes); ComplimentValue is their normal price.
	Compliments       int          `json:"compliments"`
	ComplimentMinutes int          `json:"compliment_minutes"`
	ComplimentValue   int64        `json:"compliment_value"`
	Rooms             []RoomReport `json:"rooms"`
	Days              []DayReport  `json:"days"`
}

// Report builds the report for the days fromDay..toDay (inclusive, at most
// MaxListDays). Every day in the range has a line, also days without sales.
func (s *Service) Report(ctx context.Context, actor User, fromDay, toDay time.Time) (SalesReport, error) {
	if !actor.Can(PermViewReport) {
		return SalesReport{}, ErrForbidden
	}
	from, _ := s.dayRange(fromDay)
	_, to := s.dayRange(toDay)
	if !to.After(from) {
		return SalesReport{}, fmt.Errorf("%w: tanggal akhir sebelum tanggal awal", ErrInvalid)
	}
	if to.Sub(from) > MaxListDays*24*time.Hour {
		return SalesReport{}, fmt.Errorf("%w: rentang laporan maksimal %d hari", ErrInvalid, MaxListDays)
	}
	rooms, err := s.Rooms(ctx)
	if err != nil {
		return SalesReport{}, err
	}
	all, err := s.store.ListBookings(ctx)
	if err != nil {
		return SalesReport{}, err
	}

	rep := SalesReport{From: from.Format(time.DateOnly), To: to.AddDate(0, 0, -1).Format(time.DateOnly)}
	order := make([]string, 0, len(rooms))
	byRoom := map[string]*RoomReport{}
	for _, r := range rooms {
		order = append(order, r.ID)
		byRoom[r.ID] = &RoomReport{RoomID: r.ID, RoomName: r.Name}
	}
	byDay := map[string]*DayReport{}
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		rep.Days = append(rep.Days, DayReport{Date: d.Format(time.DateOnly)})
	}
	for i := range rep.Days {
		byDay[rep.Days[i].Date] = &rep.Days[i]
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
		case StatusTentative:
			rep.Tentative++
			continue
		case StatusCancelled:
			rep.Cancelled++
			continue
		default:
			continue
		}
		minutes := b.UsedMinutes()
		rep.Minutes += minutes
		rep.Revenue += b.TotalPrice
		if b.Complimentary {
			rep.Compliments++
			rep.ComplimentMinutes += minutes
			rep.ComplimentValue += b.NormalPrice()
		}
		rr, ok := byRoom[b.RoomID]
		if !ok { // room was deleted from the sheet
			rr = &RoomReport{RoomID: b.RoomID, RoomName: b.RoomID}
			byRoom[b.RoomID] = rr
			order = append(order, b.RoomID)
		}
		rr.Bookings++
		rr.Minutes += minutes
		rr.Revenue += b.TotalPrice
		if dr := byDay[start.Format(time.DateOnly)]; dr != nil {
			dr.Bookings++
			dr.Minutes += minutes
			dr.Revenue += b.TotalPrice
		}
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

// findConflict returns a booking in the room that holds a slot overlapping
// [start, end) at now, ignoring the booking with ID skipID.
func findConflict(all []Booking, roomID string, start, end time.Time, skipID string, now time.Time) (Booking, bool) {
	for _, b := range all {
		if b.ID == skipID || b.RoomID != roomID || !b.HoldsSlot(now) {
			continue
		}
		if b.Overlaps(start, end) {
			return b, true
		}
	}
	return Booking{}, false
}

func conflictError(c Booking) error {
	kind := ""
	if c.Status == StatusTentative {
		kind = ", tentative s/d " + c.HoldUntil.Format("02/01 15:04")
	}
	return fmt.Errorf("%w: %s %s-%s (%s%s)", ErrConflict, c.RoomID,
		c.Start.Format("02/01 15:04"), c.End.Format("15:04"), c.CustomerName, kind)
}

// tentativeHold returns when a new tentative booking stops holding its slot.
func tentativeHold(start, now time.Time) time.Time {
	hold := start.Add(-TentativeHoldBefore)
	if minHold := now.Add(TentativeMinHold); hold.Before(minHold) {
		hold = minHold
	}
	if hold.After(start) {
		hold = start
	}
	return hold.Truncate(time.Minute)
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

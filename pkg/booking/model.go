// Package booking holds the karaoke booking rules: pricing, schedule
// conflicts, status changes, daily reports and the TV room status.
// It does not know where data is stored; see the Store interface.
package booking

import (
	"context"
	"errors"
	"time"
)

// Status of a booking. The normal flow is booked -> checked_in -> finished.
// "booked" is shown as "Confirm" to staff. A tentative booking holds the slot
// until HoldUntil and must be confirmed before check-in. A booking that has
// not been checked in can be cancelled.
type Status string

const (
	StatusTentative Status = "tentative"
	StatusBooked    Status = "booked"
	StatusCheckedIn Status = "checked_in"
	StatusFinished  Status = "finished"
	StatusCancelled Status = "cancelled"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusTentative, StatusBooked, StatusCheckedIn, StatusFinished, StatusCancelled:
		return true
	}
	return false
}

// Room is a karaoke room. RatePerHour is in rupiah.
type Room struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RatePerHour int64  `json:"rate_per_hour"`
	Active      bool   `json:"active"`
}

// Booking is one reservation of a room for a time range [Start, End).
// RatePerHour is copied from the room when the booking is created, so a
// later price change does not change existing bookings.
type Booking struct {
	ID           string    `json:"id"`
	RoomID       string    `json:"room_id"`
	CustomerName string    `json:"customer_name"`
	Phone        string    `json:"phone"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	Status       Status    `json:"status"`
	RatePerHour  int64     `json:"rate_per_hour"`
	TotalPrice   int64     `json:"total_price"`
	Notes        string    `json:"notes"`
	CheckedInAt  time.Time `json:"checked_in_at"`
	CheckedOutAt time.Time `json:"checked_out_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	// Usernames of who did each step, for accountability.
	CreatedBy    string `json:"created_by"`
	CheckedInBy  string `json:"checked_in_by"`
	CheckedOutBy string `json:"checked_out_by"`
	CancelledBy  string `json:"cancelled_by"`
	ConfirmedBy  string `json:"confirmed_by"`
	// HoldUntil is when a tentative booking stops holding the slot.
	HoldUntil time.Time `json:"hold_until"`
	// Complimentary bookings are free (TotalPrice 0, also after extending).
	// RatePerHour keeps the normal rate so reports can show what was given away.
	Complimentary    bool   `json:"complimentary"`
	ComplimentReason string `json:"compliment_reason"`
	// VoucherNumber is the voucher a compliment was given for. A voucher can
	// be used by one booking that is not cancelled.
	VoucherNumber string `json:"voucher_number"`
	// BilledMinutes is set when an early check-out was billed by usage;
	// 0 means the booked duration was billed.
	BilledMinutes int `json:"billed_minutes"`
}

// ChargedMinutes is the duration the guest pays for.
func (b Booking) ChargedMinutes() int {
	if b.BilledMinutes > 0 {
		return b.BilledMinutes
	}
	return b.DurationMinutes()
}

// UsedMinutes is the real time in the room: check-in to check-out for a
// finished booking, otherwise the booked duration.
func (b Booking) UsedMinutes() int {
	if b.Status == StatusFinished && !b.CheckedInAt.IsZero() && !b.CheckedOutAt.IsZero() && !b.CheckedOutAt.Before(b.CheckedInAt) {
		d := b.CheckedOutAt.Sub(b.CheckedInAt)
		return int((d + time.Minute - 1) / time.Minute) // round up to the minute
	}
	return b.DurationMinutes()
}

// NormalPrice is what the booking would cost without a compliment.
func (b Booking) NormalPrice() int64 {
	return Price(b.RatePerHour, b.ChargedMinutes())
}

// BillableMinutes is what an early check-out costs when billed by usage:
// the used time rounded up to StepMinutes, at least one hour, and never
// more than the booked time.
func BillableMinutes(used, booked int) int {
	m := (used + StepMinutes - 1) / StepMinutes * StepMinutes
	m = max(m, 60)
	return min(m, booked)
}

// HoldsSlot reports whether the booking blocks its time slot at now:
// confirmed and checked-in bookings always do, tentative ones until HoldUntil.
func (b Booking) HoldsSlot(now time.Time) bool {
	switch b.Status {
	case StatusBooked, StatusCheckedIn:
		return true
	case StatusTentative:
		return now.Before(b.HoldUntil)
	}
	return false
}

// Expired reports whether a tentative booking has passed its hold time.
func (b Booking) Expired(now time.Time) bool {
	return b.Status == StatusTentative && !now.Before(b.HoldUntil)
}

// DurationMinutes is the booked length in minutes.
func (b Booking) DurationMinutes() int {
	return int(b.End.Sub(b.Start) / time.Minute)
}

// Overlaps reports whether [start, end) overlaps the booking's time range.
func (b Booking) Overlaps(start, end time.Time) bool {
	return start.Before(b.End) && b.Start.Before(end)
}

// Store is the persistence layer. Implementations: Google Sheets and memory.
type Store interface {
	ListRooms(ctx context.Context) ([]Room, error)
	ListBookings(ctx context.Context) ([]Booking, error)
	AddBooking(ctx context.Context, b Booking) error
	// UpdateBooking replaces the booking with the same ID.
	// It returns ErrNotFound if the ID does not exist.
	UpdateBooking(ctx context.Context, b Booking) error

	AddRoom(ctx context.Context, r Room) error
	// UpdateRoom replaces the room with the same ID, or returns ErrNotFound.
	UpdateRoom(ctx context.Context, r Room) error

	ListUsers(ctx context.Context) ([]User, error)
	AddUser(ctx context.Context, u User) error
	// UpdateUser replaces the user with the same username, or returns ErrNotFound.
	UpdateUser(ctx context.Context, u User) error

	// ListPricing returns the price table; empty means "use each room's rate".
	ListPricing(ctx context.Context) ([]PriceRule, error)
	ReplacePricing(ctx context.Context, rules []PriceRule) error

	ListDevices(ctx context.Context) ([]Device, error)
	AddDevice(ctx context.Context, d Device) error
	// UpdateDevice replaces the device with the same ID, or returns ErrNotFound.
	UpdateDevice(ctx context.Context, d Device) error

	AddActivity(ctx context.Context, a Activity) error
	// ListActivity returns audit lines with from <= Time < to.
	ListActivity(ctx context.Context, from, to time.Time) ([]Activity, error)
}

type freshReadKey struct{}

// WithFreshRead marks ctx so the store skips any read cache. The service
// uses it before writes, so conflict checks always see the latest data.
func WithFreshRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshReadKey{}, true)
}

// FreshRead reports whether ctx was marked with WithFreshRead.
func FreshRead(ctx context.Context) bool {
	v, _ := ctx.Value(freshReadKey{}).(bool)
	return v
}

// Errors returned by the service. The HTTP layer maps them to status codes.
var (
	ErrNotFound    = errors.New("data tidak ditemukan")
	ErrConflict    = errors.New("jadwal bentrok dengan booking lain")
	ErrInvalid     = errors.New("data tidak valid")
	ErrWrongState  = errors.New("status booking tidak sesuai untuk aksi ini")
	ErrForbidden   = errors.New("anda tidak punya akses untuk aksi ini")
	ErrVoucherUsed = errors.New("voucher sudah dipakai")
)

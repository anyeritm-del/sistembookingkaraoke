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
// A booking that has not been checked in can be cancelled.
type Status string

const (
	StatusBooked    Status = "booked"
	StatusCheckedIn Status = "checked_in"
	StatusFinished  Status = "finished"
	StatusCancelled Status = "cancelled"
)

// Active reports whether the booking still holds its time slot.
func (s Status) Active() bool {
	return s == StatusBooked || s == StatusCheckedIn
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
	ErrNotFound   = errors.New("data tidak ditemukan")
	ErrConflict   = errors.New("jadwal bentrok dengan booking lain")
	ErrInvalid    = errors.New("data tidak valid")
	ErrWrongState = errors.New("status booking tidak sesuai untuk aksi ini")
)

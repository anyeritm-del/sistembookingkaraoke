// Package memstore is an in-memory booking.Store for tests and local
// development. Data is lost when the process stops.
package memstore

import (
	"context"
	"slices"
	"sync"

	"karaoke/pkg/booking"
)

// Store keeps rooms and bookings in memory. It is safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	rooms    []booking.Room
	bookings []booking.Booking
}

// New returns a store with the given rooms.
func New(rooms ...booking.Room) *Store {
	return &Store{rooms: slices.Clone(rooms)}
}

// DemoRooms is a small set of rooms for local development.
func DemoRooms() []booking.Room {
	return []booking.Room{
		{ID: "R01", Name: "Room 01 - Small", RatePerHour: 100000, Active: true},
		{ID: "R02", Name: "Room 02 - Medium", RatePerHour: 150000, Active: true},
		{ID: "R03", Name: "Room 03 - Large", RatePerHour: 250000, Active: true},
		{ID: "VIP1", Name: "VIP 1", RatePerHour: 400000, Active: true},
	}
}

func (s *Store) ListRooms(context.Context) ([]booking.Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.rooms), nil
}

func (s *Store) ListBookings(context.Context) ([]booking.Booking, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.bookings), nil
}

func (s *Store) AddBooking(_ context.Context, b booking.Booking) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bookings = append(s.bookings, b)
	return nil
}

func (s *Store) UpdateBooking(_ context.Context, b booking.Booking) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.bookings, func(x booking.Booking) bool { return x.ID == b.ID })
	if i < 0 {
		return booking.ErrNotFound
	}
	s.bookings[i] = b
	return nil
}

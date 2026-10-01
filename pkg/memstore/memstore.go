// Package memstore is an in-memory booking.Store for tests and local
// development. Data is lost when the process stops.
package memstore

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"karaoke/pkg/booking"
)

// Store keeps rooms and bookings in memory. It is safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	rooms    []booking.Room
	bookings []booking.Booking
	users    []booking.User
	activity []booking.Activity
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

func (s *Store) AddRoom(_ context.Context, r booking.Room) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rooms = append(s.rooms, r)
	return nil
}

func (s *Store) UpdateRoom(_ context.Context, r booking.Room) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.rooms, func(x booking.Room) bool { return strings.EqualFold(x.ID, r.ID) })
	if i < 0 {
		return booking.ErrNotFound
	}
	s.rooms[i] = r
	return nil
}

func (s *Store) ListUsers(context.Context) ([]booking.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.users), nil
}

func (s *Store) AddUser(_ context.Context, u booking.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users = append(s.users, u)
	return nil
}

func (s *Store) UpdateUser(_ context.Context, u booking.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.users, func(x booking.User) bool { return x.Username == u.Username })
	if i < 0 {
		return booking.ErrNotFound
	}
	s.users[i] = u
	return nil
}

func (s *Store) AddActivity(_ context.Context, a booking.Activity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activity = append(s.activity, a)
	return nil
}

func (s *Store) ListActivity(_ context.Context, from, to time.Time) ([]booking.Activity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []booking.Activity
	for _, a := range s.activity {
		if !a.Time.Before(from) && a.Time.Before(to) {
			out = append(out, a)
		}
	}
	return out, nil
}

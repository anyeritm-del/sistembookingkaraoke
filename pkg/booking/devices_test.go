package booking_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"karaoke/pkg/booking"
)

func TestPairingFlow(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.CreatePairing(f.ctx, f.sup, booking.DeviceInput{Name: "TV", RoomID: "R01"}); !errors.Is(err, booking.ErrForbidden) {
		t.Errorf("supervisor create pairing: %v", err)
	}
	if _, err := f.svc.CreatePairing(f.ctx, f.admin, booking.DeviceInput{Name: "TV", RoomID: "NOPE"}); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("unknown room: %v", err)
	}
	if _, err := f.svc.CreatePairing(f.ctx, f.admin, booking.DeviceInput{Name: " ", RoomID: "R01"}); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("empty name: %v", err)
	}

	pc, err := f.svc.CreatePairing(f.ctx, f.admin, booking.DeviceInput{Name: "TV Room 1", RoomID: "r01"})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9]{6}$`).MatchString(pc.Code) || pc.Device.Status != booking.DevicePending ||
		pc.Device.RoomID != "R01" || !pc.Expires.Equal(f.now.Add(15*time.Minute)) {
		t.Fatalf("pairing = %+v", pc)
	}
	// The stored device keeps only a hash of the code.
	stored, _ := f.store.ListDevices(f.ctx)
	if strings.Contains(stored[0].PairCodeHash, pc.Code) {
		t.Error("code stored in clear text")
	}

	// A TV with no token, or a wrong code, gets nothing.
	if _, err := f.svc.DeviceRoomStatus(f.ctx, ""); !errors.Is(err, booking.ErrBadPairCode) {
		t.Errorf("empty token: %v", err)
	}
	wrong := "000000"
	if pc.Code == wrong {
		wrong = "111111"
	}
	if _, _, err := f.svc.Pair(f.ctx, wrong, "1.1.1.1"); !errors.Is(err, booking.ErrBadPairCode) {
		t.Errorf("wrong code: %v", err)
	}

	d, token, err := f.svc.Pair(f.ctx, " "+pc.Code+" ", "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != booking.DeviceActive || !strings.HasPrefix(token, "tvd_") || len(token) < 40 {
		t.Errorf("paired = %+v token %q", d, token)
	}
	// Single use.
	if _, _, err := f.svc.Pair(f.ctx, pc.Code, "1.1.1.2"); !errors.Is(err, booking.ErrBadPairCode) {
		t.Errorf("code reused: %v", err)
	}

	// The token reads its own room only.
	b, _ := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R01", CustomerName: "Tamu", Start: at("18:00"), DurationMinutes: 60})
	f.svc.CheckIn(f.ctx, f.staff, b.ID)
	st, err := f.svc.DeviceRoomStatus(f.ctx, token)
	if err != nil || st.Room.ID != "R01" || st.Current == nil || st.Current.ID != b.ID {
		t.Fatalf("status = %+v %v", st, err)
	}

	// Revoke stops it at once.
	if _, err := f.svc.RevokeDevice(f.ctx, f.admin, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.DeviceRoomStatus(f.ctx, token); !errors.Is(err, booking.ErrBadPairCode) {
		t.Errorf("revoked token: %v", err)
	}

	log, _ := f.svc.Activity(f.ctx, f.admin, f.now)
	seen := map[string]bool{}
	for _, a := range log {
		seen[a.Action] = true
	}
	for _, act := range []string{booking.ActDeviceCreate, booking.ActDevicePair, booking.ActDeviceRevoke} {
		if !seen[act] {
			t.Errorf("activity missing %s", act)
		}
	}
}

func TestPairingExpiresAndRateLimit(t *testing.T) {
	f := newFixture(t)
	pc, _ := f.svc.CreatePairing(f.ctx, f.admin, booking.DeviceInput{Name: "TV", RoomID: "R02"})
	f.now = f.now.Add(15 * time.Minute)
	if _, _, err := f.svc.Pair(f.ctx, pc.Code, "2.2.2.2"); !errors.Is(err, booking.ErrBadPairCode) {
		t.Errorf("expired code: %v", err)
	}

	pc, _ = f.svc.CreatePairing(f.ctx, f.admin, booking.DeviceInput{Name: "TV 2", RoomID: "R02"})
	wrong := "000000"
	if pc.Code == wrong {
		wrong = "111111"
	}
	for range 10 {
		f.svc.Pair(f.ctx, wrong, "3.3.3.3")
	}
	if _, _, err := f.svc.Pair(f.ctx, pc.Code, "3.3.3.3"); !errors.Is(err, booking.ErrBadPairCode) {
		t.Errorf("locked client with right code: %v", err)
	}
	// Another client is not affected.
	if _, _, err := f.svc.Pair(f.ctx, pc.Code, "4.4.4.4"); err != nil {
		t.Errorf("other client: %v", err)
	}
}

func TestRevokePendingPairing(t *testing.T) {
	f := newFixture(t)
	pc, _ := f.svc.CreatePairing(f.ctx, f.admin, booking.DeviceInput{Name: "TV", RoomID: "R01"})
	if _, err := f.svc.RevokeDevice(f.ctx, f.admin, pc.Device.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Pair(f.ctx, pc.Code, "5.5.5.5"); !errors.Is(err, booking.ErrBadPairCode) {
		t.Errorf("revoked pending code: %v", err)
	}
	if _, err := f.svc.RevokeDevice(f.ctx, f.admin, "TV-NOPE"); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("unknown device: %v", err)
	}
	if _, err := f.svc.Devices(f.ctx, f.staff); !errors.Is(err, booking.ErrForbidden) {
		t.Errorf("staff list devices: %v", err)
	}
}

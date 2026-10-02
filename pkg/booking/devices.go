package booking

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
)

// DeviceStatus is the state of a room TV.
type DeviceStatus string

const (
	// DevicePending has a pairing code and waits for the TV to enter it.
	DevicePending DeviceStatus = "pending"
	// DeviceActive is paired; its token may read the room status.
	DeviceActive DeviceStatus = "active"
	// DeviceRevoked can no longer read anything.
	DeviceRevoked DeviceStatus = "revoked"
)

// Device is one room TV. Each TV has its own secret token, so a lost TV can
// be revoked without touching the others. Only hashes are stored.
type Device struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	RoomID       string       `json:"room_id"`
	Status       DeviceStatus `json:"status"`
	TokenHash    string       `json:"-"`
	PairCodeHash string       `json:"-"`
	PairExpires  time.Time    `json:"pair_expires"`
	CreatedBy    string       `json:"created_by"`
	CreatedAt    time.Time    `json:"created_at"`
	PairedAt     time.Time    `json:"paired_at"`
	RevokedBy    string       `json:"revoked_by"`
}

// Pairing limits. A code is 6 digits so it is easy to type with a TV remote;
// it is short-lived, single-use and attempts are rate limited.
const (
	PairCodeTTL     = 15 * time.Minute
	pairCodeDigits  = 6
	maxPairFails    = 10
	pairLockFor     = 10 * time.Minute
	deviceTokenSize = 32
)

// ErrBadPairCode is returned for a wrong, used or expired pairing code.
var ErrBadPairCode = errors.New("kode pairing salah atau sudah kedaluwarsa")

// DeviceInput is the data for a new TV.
type DeviceInput struct {
	Name   string `json:"name"`
	RoomID string `json:"room_id"`
}

// PairingCode is shown once to the admin, then only its hash is kept.
type PairingCode struct {
	Device  Device    `json:"device"`
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
}

// Devices lists all TVs.
func (s *Service) Devices(ctx context.Context, actor User) ([]Device, error) {
	if !actor.Can(PermManageDevices) {
		return nil, ErrForbidden
	}
	list, err := s.store.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(list, func(a, b Device) int {
		if c := strings.Compare(a.RoomID, b.RoomID); c != 0 {
			return c
		}
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	return list, nil
}

// CreatePairing registers a TV for a room and returns a one-time code.
func (s *Service) CreatePairing(ctx context.Context, actor User, in DeviceInput) (PairingCode, error) {
	if !actor.Can(PermManageDevices) {
		return PairingCode{}, ErrForbidden
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 60 {
		return PairingCode{}, fmt.Errorf("%w: nama TV wajib diisi (maks. 60 karakter)", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx = WithFreshRead(ctx)
	room, err := s.findRoom(ctx, in.RoomID)
	if err != nil {
		return PairingCode{}, err
	}

	now := s.Now()
	code := randomDigits(pairCodeDigits)
	d := Device{
		ID:          "TV-" + now.Format("060102") + "-" + randomDigits(4),
		Name:        in.Name,
		RoomID:      room.ID,
		Status:      DevicePending,
		PairExpires: now.Add(PairCodeTTL).Truncate(time.Second),
		CreatedBy:   actor.Username,
		CreatedAt:   now,
	}
	d.PairCodeHash = pairCodeHash(d.ID, code)
	if err := s.store.AddDevice(ctx, d); err != nil {
		return PairingCode{}, err
	}
	s.audit(ctx, actor, ActDeviceCreate, "", room.ID, fmt.Sprintf("%s (%s), kode berlaku s/d %s", d.Name, d.ID, d.PairExpires.Format("15:04")))
	return PairingCode{Device: d, Code: code, Expires: d.PairExpires}, nil
}

// Pair exchanges a pairing code for the TV's own token. client identifies
// the caller (its IP address) for rate limiting.
func (s *Service) Pair(ctx context.Context, code, client string) (Device, string, error) {
	now := s.Now()
	if s.pairGuard.locked(client, now) {
		return Device{}, "", fmt.Errorf("%w: terlalu banyak percobaan, coba lagi dalam %d menit", ErrBadPairCode, int(pairLockFor/time.Minute))
	}
	code = strings.TrimSpace(code)

	s.mu.Lock()
	defer s.mu.Unlock()
	ctx = WithFreshRead(ctx)
	list, err := s.store.ListDevices(ctx)
	if err != nil {
		return Device{}, "", err
	}
	for _, d := range list {
		if d.Status != DevicePending || !now.Before(d.PairExpires) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(pairCodeHash(d.ID, code)), []byte(d.PairCodeHash)) != 1 {
			continue
		}
		token := newDeviceToken()
		d.Status = DeviceActive
		d.TokenHash = tokenHash(token)
		d.PairCodeHash = "" // single use
		d.PairedAt = now
		if err := s.store.UpdateDevice(ctx, d); err != nil {
			return Device{}, "", err
		}
		s.pairGuard.ok(client)
		s.audit(ctx, User{Username: "tv:" + d.ID}, ActDevicePair, "", d.RoomID, d.Name)
		return d, token, nil
	}
	s.pairGuard.failN(client, now, maxPairFails, pairLockFor)
	return Device{}, "", ErrBadPairCode
}

// RevokeDevice stops a TV (or an unused pairing code) from working.
func (s *Service) RevokeDevice(ctx context.Context, actor User, id string) (Device, error) {
	if !actor.Can(PermManageDevices) {
		return Device{}, ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx = WithFreshRead(ctx)
	list, err := s.store.ListDevices(ctx)
	if err != nil {
		return Device{}, err
	}
	i := slices.IndexFunc(list, func(d Device) bool { return d.ID == id })
	if i < 0 {
		return Device{}, ErrNotFound
	}
	d := list[i]
	if d.Status == DeviceRevoked {
		return d, nil
	}
	d.Status, d.TokenHash, d.PairCodeHash, d.RevokedBy = DeviceRevoked, "", "", actor.Username
	if err := s.store.UpdateDevice(ctx, d); err != nil {
		return Device{}, err
	}
	s.audit(ctx, actor, ActDeviceRevoke, "", d.RoomID, fmt.Sprintf("%s (%s)", d.Name, d.ID))
	return d, nil
}

// DeviceRoomStatus returns the room status for a paired TV's token.
func (s *Service) DeviceRoomStatus(ctx context.Context, token string) (TVStatus, error) {
	d, err := s.deviceByToken(ctx, token)
	if err != nil {
		return TVStatus{}, err
	}
	return s.RoomStatus(ctx, d.RoomID)
}

func (s *Service) deviceByToken(ctx context.Context, token string) (Device, error) {
	if token == "" {
		return Device{}, ErrBadPairCode
	}
	h := tokenHash(token)
	list, err := s.store.ListDevices(ctx)
	if err != nil {
		return Device{}, err
	}
	for _, d := range list {
		if d.Status == DeviceActive && subtle.ConstantTimeCompare([]byte(h), []byte(d.TokenHash)) == 1 {
			return d, nil
		}
	}
	return Device{}, ErrBadPairCode
}

// pairCodeHash binds the code to the device, so equal codes for two devices
// hash differently. A plain hash is enough: codes live 15 minutes and are
// single-use, and a slow hash here would let anyone burn server CPU.
func pairCodeHash(deviceID, code string) string {
	sum := sha256.Sum256([]byte(deviceID + ":" + code))
	return hex.EncodeToString(sum[:])
}

// tokenHash hashes a 256-bit random token; a fast hash is safe for that.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newDeviceToken() string {
	b := make([]byte, deviceTokenSize)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return "tvd_" + base64.RawURLEncoding.EncodeToString(b)
}

func randomDigits(n int) string {
	var sb strings.Builder
	for range n {
		d, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			panic(err)
		}
		sb.WriteByte(byte('0' + d.Int64()))
	}
	return sb.String()
}

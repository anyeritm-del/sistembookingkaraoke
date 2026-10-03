package booking

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PINHasher hashes and checks PINs. pkg/auth implements it.
type PINHasher interface {
	Hash(pin string) (string, error)
	Verify(hash, pin string) bool
}

// ErrBadLogin is returned for any failed login, so it does not reveal
// whether the username exists.
var ErrBadLogin = errors.New("username atau PIN salah")

// BootstrapUsername is the first admin account. It can log in with
// ADMIN_PIN only while the Users tab is empty, and is then saved there.
const BootstrapUsername = "admin"

// Login lock: after maxFails wrong PINs a username is locked for lockFor.
// The counter lives in memory, so it is per server instance.
const (
	maxFails = 5
	lockFor  = 5 * time.Minute
)

var (
	usernameRe = regexp.MustCompile(`^[a-z0-9._-]{3,32}$`)
	roomIDRe   = regexp.MustCompile(`^[A-Z0-9_-]{1,16}$`)
	pinRe      = regexp.MustCompile(`^[0-9]{6,12}$`)
)

type loginGuard struct {
	mu    sync.Mutex
	fails map[string]int
	until map[string]time.Time
}

func (g *loginGuard) locked(user string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return now.Before(g.until[user])
}

func (g *loginGuard) fail(user string, now time.Time) {
	g.failN(user, now, maxFails, lockFor)
}

// failN counts a failure for key and locks it for lock after max failures.
func (g *loginGuard) failN(key string, now time.Time, max int, lock time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fails == nil {
		g.fails, g.until = map[string]int{}, map[string]time.Time{}
	}
	g.fails[key]++
	if g.fails[key] >= max {
		g.until[key] = now.Add(lock)
		g.fails[key] = 0
	}
}

func (g *loginGuard) ok(user string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, user)
	delete(g.until, user)
}

// ---------- Login and own account ----------

// Login checks username and PIN and returns the active user.
func (s *Service) Login(ctx context.Context, username, pin string) (User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	now := s.Now()
	if s.guard.locked(username, now) {
		return User{}, fmt.Errorf("%w: terlalu banyak percobaan, coba lagi dalam %d menit", ErrBadLogin, int(lockFor/time.Minute))
	}

	users, err := s.store.ListUsers(WithFreshRead(ctx))
	if err != nil {
		return User{}, err
	}
	if len(users) == 0 {
		return s.bootstrapLogin(ctx, username, pin)
	}
	i := slices.IndexFunc(users, func(u User) bool { return u.Username == username })
	if i < 0 || !users[i].Active || !s.pins.Verify(users[i].PINHash, pin) {
		s.guard.fail(username, now)
		return User{}, ErrBadLogin
	}
	s.guard.ok(username)
	s.audit(ctx, users[i], ActLogin, "", "", "")
	return users[i], nil
}

// bootstrapLogin creates the first admin from ADMIN_PIN when there are no users.
func (s *Service) bootstrapLogin(ctx context.Context, username, pin string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bootstrapPIN == "" || username != BootstrapUsername ||
		subtle.ConstantTimeCompare([]byte(pin), []byte(s.bootstrapPIN)) != 1 {
		s.guard.fail(username, s.Now())
		return User{}, ErrBadLogin
	}
	// Another instance may have created it a moment ago.
	if users, err := s.store.ListUsers(WithFreshRead(ctx)); err != nil || len(users) > 0 {
		if err != nil {
			return User{}, err
		}
		return User{}, ErrBadLogin
	}
	hash, err := s.pins.Hash(pin)
	if err != nil {
		return User{}, err
	}
	now := s.Now()
	u := User{Username: BootstrapUsername, Name: "Administrator", Role: RoleAdmin,
		PINHash: hash, Active: true, CreatedAt: now, UpdatedAt: now}
	if err := s.store.AddUser(ctx, u); err != nil {
		return User{}, err
	}
	s.audit(ctx, u, ActUserCreate, "", "", "akun admin awal dibuat dari ADMIN_PIN")
	s.audit(ctx, u, ActLogin, "", "", "")
	return u, nil
}

// SessionUser returns the active user for a session. A user who was
// deactivated or deleted from the sheet is logged out at once.
func (s *Service) SessionUser(ctx context.Context, username string) (User, error) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return User{}, err
	}
	for _, u := range users {
		if u.Username == username && u.Active && u.Role.Valid() {
			return u, nil
		}
	}
	return User{}, ErrBadLogin
}

// ChangeOwnPIN lets any user change their own PIN.
func (s *Service) ChangeOwnPIN(ctx context.Context, actor User, oldPIN, newPIN string) error {
	if !s.pins.Verify(actor.PINHash, oldPIN) {
		return fmt.Errorf("%w: PIN lama salah", ErrInvalid)
	}
	if err := s.setPIN(ctx, actor.Username, newPIN); err != nil {
		return err
	}
	s.audit(ctx, actor, ActPINChange, "", "", "")
	return nil
}

// ---------- Users (admin) ----------

// Users lists all users.
func (s *Service) Users(ctx context.Context, actor User) ([]User, error) {
	if !actor.Can(PermManageUsers) {
		return nil, ErrForbidden
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(users, func(a, b User) int { return strings.Compare(a.Username, b.Username) })
	return users, nil
}

// UserInput is the data for a new user.
type UserInput struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Role     Role   `json:"role"`
	PIN      string `json:"pin"`
}

// CreateUser adds a user.
func (s *Service) CreateUser(ctx context.Context, actor User, in UserInput) (User, error) {
	if !actor.Can(PermManageUsers) {
		return User{}, ErrForbidden
	}
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	in.Name = strings.TrimSpace(in.Name)
	if !usernameRe.MatchString(in.Username) {
		return User{}, fmt.Errorf("%w: username 3-32 karakter: huruf kecil, angka, titik, minus, garis bawah", ErrInvalid)
	}
	if in.Name == "" {
		return User{}, fmt.Errorf("%w: nama wajib diisi", ErrInvalid)
	}
	if !in.Role.Valid() {
		return User{}, fmt.Errorf("%w: role harus staff, supervisor, admin, atau accounting", ErrInvalid)
	}
	if err := validPIN(in.PIN); err != nil {
		return User{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ctx = WithFreshRead(ctx)
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return User{}, err
	}
	if slices.ContainsFunc(users, func(u User) bool { return u.Username == in.Username }) {
		return User{}, fmt.Errorf("%w: username %s sudah dipakai", ErrInvalid, in.Username)
	}
	hash, err := s.pins.Hash(in.PIN)
	if err != nil {
		return User{}, err
	}
	now := s.Now()
	u := User{Username: in.Username, Name: in.Name, Role: in.Role, PINHash: hash,
		Active: true, CreatedAt: now, UpdatedAt: now}
	if err := s.store.AddUser(ctx, u); err != nil {
		return User{}, err
	}
	s.audit(ctx, actor, ActUserCreate, "", "", fmt.Sprintf("%s (%s, %s)", u.Username, u.Name, u.Role))
	return u, nil
}

// UserUpdate changes a user's name, role or active flag.
type UserUpdate struct {
	Name   string `json:"name"`
	Role   Role   `json:"role"`
	Active bool   `json:"active"`
}

// UpdateUser changes a user. An admin cannot demote or deactivate
// themselves, and the last active admin cannot be removed.
func (s *Service) UpdateUser(ctx context.Context, actor User, username string, in UserUpdate) (User, error) {
	if !actor.Can(PermManageUsers) {
		return User{}, ErrForbidden
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return User{}, fmt.Errorf("%w: nama wajib diisi", ErrInvalid)
	}
	if !in.Role.Valid() {
		return User{}, fmt.Errorf("%w: role harus staff, supervisor, admin, atau accounting", ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ctx = WithFreshRead(ctx)
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return User{}, err
	}
	i := slices.IndexFunc(users, func(u User) bool { return u.Username == username })
	if i < 0 {
		return User{}, ErrNotFound
	}
	u := users[i]
	losesAdmin := u.Role == RoleAdmin && u.Active && (in.Role != RoleAdmin || !in.Active)
	if losesAdmin && u.Username == actor.Username {
		return User{}, fmt.Errorf("%w: anda tidak bisa menurunkan role atau menonaktifkan akun sendiri", ErrInvalid)
	}
	if losesAdmin && countActiveAdmins(users) <= 1 {
		return User{}, fmt.Errorf("%w: harus ada minimal satu admin aktif", ErrInvalid)
	}

	var changes []string
	if u.Name != in.Name {
		changes = append(changes, fmt.Sprintf("nama %q -> %q", u.Name, in.Name))
	}
	if u.Role != in.Role {
		changes = append(changes, fmt.Sprintf("role %s -> %s", u.Role, in.Role))
	}
	if u.Active != in.Active {
		changes = append(changes, fmt.Sprintf("aktif %s -> %s", yesNo(u.Active), yesNo(in.Active)))
	}
	if len(changes) == 0 {
		return u, nil
	}
	u.Name, u.Role, u.Active, u.UpdatedAt = in.Name, in.Role, in.Active, s.Now()
	if err := s.store.UpdateUser(ctx, u); err != nil {
		return User{}, err
	}
	s.audit(ctx, actor, ActUserUpdate, "", "", u.Username+": "+strings.Join(changes, ", "))
	return u, nil
}

// ResetPIN sets a new PIN for another user (for example, a forgotten PIN).
func (s *Service) ResetPIN(ctx context.Context, actor User, username, newPIN string) error {
	if !actor.Can(PermManageUsers) {
		return ErrForbidden
	}
	if err := s.setPIN(ctx, username, newPIN); err != nil {
		return err
	}
	s.guard.ok(username)
	s.audit(ctx, actor, ActUserPINReset, "", "", username)
	return nil
}

func (s *Service) setPIN(ctx context.Context, username, pin string) error {
	if err := validPIN(pin); err != nil {
		return err
	}
	hash, err := s.pins.Hash(pin)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx = WithFreshRead(ctx)
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(users, func(u User) bool { return u.Username == username })
	if i < 0 {
		return ErrNotFound
	}
	u := users[i]
	u.PINHash, u.UpdatedAt = hash, s.Now()
	return s.store.UpdateUser(ctx, u)
}

func countActiveAdmins(users []User) int {
	n := 0
	for _, u := range users {
		if u.Active && u.Role == RoleAdmin {
			n++
		}
	}
	return n
}

// validPIN requires 6-12 digits and refuses the easiest guesses.
func validPIN(pin string) error {
	if !pinRe.MatchString(pin) {
		return fmt.Errorf("%w: PIN harus 6-12 angka", ErrInvalid)
	}
	same, up, down := true, true, true
	for i := 1; i < len(pin); i++ {
		d := int(pin[i]) - int(pin[i-1])
		same = same && d == 0
		up = up && (d == 1 || d == -9)
		down = down && (d == -1 || d == 9)
	}
	if same || up || down {
		return fmt.Errorf("%w: PIN terlalu mudah ditebak (angka sama atau berurutan)", ErrInvalid)
	}
	return nil
}

// ---------- Rooms (admin) ----------

// RoomInput is the data for a new or changed room.
type RoomInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RatePerHour int64  `json:"rate_per_hour"`
	Active      bool   `json:"active"`
}

func (in *RoomInput) clean() error {
	in.ID = strings.ToUpper(strings.TrimSpace(in.ID))
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return fmt.Errorf("%w: nama room wajib diisi", ErrInvalid)
	}
	if in.RatePerHour <= 0 || in.RatePerHour > 100_000_000 {
		return fmt.Errorf("%w: tarif per jam harus lebih dari 0", ErrInvalid)
	}
	return nil
}

// CreateRoom adds a room. The ID cannot be changed later, because bookings
// and room TVs refer to it.
func (s *Service) CreateRoom(ctx context.Context, actor User, in RoomInput) (Room, error) {
	if !actor.Can(PermManageRooms) {
		return Room{}, ErrForbidden
	}
	if err := in.clean(); err != nil {
		return Room{}, err
	}
	if !roomIDRe.MatchString(in.ID) {
		return Room{}, fmt.Errorf("%w: kode room 1-16 karakter: huruf, angka, minus, garis bawah (contoh R05)", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx = WithFreshRead(ctx)
	rooms, err := s.store.ListRooms(ctx)
	if err != nil {
		return Room{}, err
	}
	if slices.ContainsFunc(rooms, func(r Room) bool { return strings.EqualFold(r.ID, in.ID) }) {
		return Room{}, fmt.Errorf("%w: kode room %s sudah dipakai", ErrInvalid, in.ID)
	}
	r := Room{ID: in.ID, Name: in.Name, RatePerHour: in.RatePerHour, Active: in.Active}
	if err := s.store.AddRoom(ctx, r); err != nil {
		return Room{}, err
	}
	s.audit(ctx, actor, ActRoomCreate, "", r.ID, fmt.Sprintf("%s, %s/jam", r.Name, rupiah(r.RatePerHour)))
	return r, nil
}

// UpdateRoom changes a room's name, rate or active flag. A new rate only
// applies to new bookings.
func (s *Service) UpdateRoom(ctx context.Context, actor User, id string, in RoomInput) (Room, error) {
	if !actor.Can(PermManageRooms) {
		return Room{}, ErrForbidden
	}
	if err := in.clean(); err != nil {
		return Room{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx = WithFreshRead(ctx)
	r, err := s.findRoom(ctx, id)
	if err != nil {
		return Room{}, err
	}
	var changes []string
	if r.Name != in.Name {
		changes = append(changes, fmt.Sprintf("nama %q -> %q", r.Name, in.Name))
	}
	if r.RatePerHour != in.RatePerHour {
		changes = append(changes, fmt.Sprintf("tarif %s -> %s", rupiah(r.RatePerHour), rupiah(in.RatePerHour)))
	}
	if r.Active != in.Active {
		changes = append(changes, fmt.Sprintf("aktif %s -> %s", yesNo(r.Active), yesNo(in.Active)))
	}
	if len(changes) == 0 {
		return r, nil
	}
	r.Name, r.RatePerHour, r.Active = in.Name, in.RatePerHour, in.Active
	if err := s.store.UpdateRoom(ctx, r); err != nil {
		return Room{}, err
	}
	s.audit(ctx, actor, ActRoomUpdate, "", r.ID, strings.Join(changes, ", "))
	return r, nil
}

// ---------- Audit log ----------

// Activity returns the audit log of one day, newest first.
func (s *Service) Activity(ctx context.Context, actor User, day time.Time) ([]Activity, error) {
	if !actor.Can(PermViewActivity) {
		return nil, ErrForbidden
	}
	from, to := s.dayRange(day)
	list, err := s.store.ListActivity(ctx, from, to)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(list, func(a, b Activity) int { return b.Time.Compare(a.Time) })
	return list, nil
}

// audit writes one audit line. The action itself has already been saved, so
// a failed audit write is logged but does not fail the request.
func (s *Service) audit(ctx context.Context, actor User, action, bookingID, roomID, detail string) {
	a := Activity{Time: s.Now(), Username: actor.Username, Action: action,
		BookingID: bookingID, RoomID: roomID, Detail: detail}
	if err := s.store.AddActivity(ctx, a); err != nil {
		log.Printf("audit write failed (%s %s %s): %v", a.Username, a.Action, a.BookingID, err)
	}
}

// rupiah formats 150000 as "Rp150.000".
func rupiah(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	return "Rp" + b.String()
}

func yesNo(v bool) string {
	if v {
		return "ya"
	}
	return "tidak"
}

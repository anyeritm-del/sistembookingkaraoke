package booking_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"karaoke/pkg/booking"
)

func TestPermissionTable(t *testing.T) {
	cases := []struct {
		role booking.Role
		perm booking.Permission
		want bool
	}{
		{booking.RoleStaff, booking.PermCreateBooking, true},
		{booking.RoleStaff, booking.PermExtend, true},
		{booking.RoleStaff, booking.PermCheckOut, true},
		{booking.RoleStaff, booking.PermCancel, false},
		{booking.RoleStaff, booking.PermViewReport, false},
		{booking.RoleStaff, booking.PermManageRooms, false},
		{booking.RoleSupervisor, booking.PermCancel, true},
		{booking.RoleSupervisor, booking.PermViewReport, true},
		{booking.RoleSupervisor, booking.PermViewActivity, true},
		{booking.RoleSupervisor, booking.PermManageRooms, false},
		{booking.RoleSupervisor, booking.PermManageUsers, false},
		{booking.RoleAdmin, booking.PermManageRooms, true},
		{booking.RoleAdmin, booking.PermManageUsers, true},
		{booking.RoleAccounting, booking.PermViewSchedule, true},
		{booking.RoleAccounting, booking.PermViewReport, true},
		{booking.RoleAccounting, booking.PermViewActivity, true},
		{booking.RoleAccounting, booking.PermExport, true},
		{booking.RoleAccounting, booking.PermCreateBooking, false},
		{booking.RoleAccounting, booking.PermCheckOut, false},
		{booking.RoleAccounting, booking.PermCancelTentative, false},
		{booking.RoleAccounting, booking.PermManageRooms, false},
		{booking.RoleStaff, booking.PermExport, false},
		{booking.RoleSupervisor, booking.PermExport, true},
		{booking.Role("owner"), booking.PermViewSchedule, false},
	}
	for _, c := range cases {
		if got := c.role.Can(c.perm); got != c.want {
			t.Errorf("%s can %s = %v, want %v", c.role, c.perm, got, c.want)
		}
	}
	inactive := booking.User{Role: booking.RoleAdmin, Active: false}
	if inactive.Can(booking.PermViewSchedule) {
		t.Error("inactive user must have no permissions")
	}
}

func TestServiceEnforcesPermissions(t *testing.T) {
	f := newFixture(t)
	b, err := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R01", CustomerName: "A", Start: at("19:00"), DurationMinutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Cancel(f.ctx, f.staff, b.ID); !errors.Is(err, booking.ErrForbidden) {
		t.Errorf("staff cancel: %v", err)
	}
	if _, err := f.svc.Report(f.ctx, f.staff, f.now, f.now); !errors.Is(err, booking.ErrForbidden) {
		t.Errorf("staff report: %v", err)
	}
	if _, err := f.svc.CreateRoom(f.ctx, f.sup, booking.RoomInput{ID: "R09", Name: "X", RatePerHour: 1}); !errors.Is(err, booking.ErrForbidden) {
		t.Errorf("supervisor create room: %v", err)
	}
	if _, err := f.svc.Users(f.ctx, f.sup); !errors.Is(err, booking.ErrForbidden) {
		t.Errorf("supervisor list users: %v", err)
	}
	if _, err := f.svc.Cancel(f.ctx, f.sup, b.ID); err != nil {
		t.Errorf("supervisor cancel: %v", err)
	}
}

func TestAuditAndBookingBy(t *testing.T) {
	f := newFixture(t)
	b, _ := f.svc.Create(f.ctx, f.staff, booking.CreateInput{RoomID: "R01", CustomerName: "Tamu", Start: at("18:00"), DurationMinutes: 60})
	b, _ = f.svc.CheckIn(f.ctx, f.staff, b.ID)
	b, _ = f.svc.Extend(f.ctx, f.sup, b.ID, 30)
	b, err := f.svc.CheckOut(f.ctx, f.sup, b.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if b.CreatedBy != "sari" || b.CheckedInBy != "sari" || b.CheckedOutBy != "budi" {
		t.Errorf("by fields: %+v", b)
	}

	log, err := f.svc.Activity(f.ctx, f.sup, f.now)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range log {
		got = append(got, a.Username+" "+a.Action)
		if a.BookingID != b.ID || a.RoomID != "R01" {
			t.Errorf("activity ids: %+v", a)
		}
	}
	// Newest first; all four have the same clock time in this test, so check as a set.
	want := map[string]bool{"sari booking.create": true, "sari booking.checkin": true, "budi booking.extend": true, "budi booking.checkout": true}
	if len(got) != 4 {
		t.Fatalf("activity = %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected activity %q", g)
		}
	}
	if _, err := f.svc.Activity(f.ctx, f.staff, f.now); !errors.Is(err, booking.ErrForbidden) {
		t.Errorf("staff activity: %v", err)
	}
}

func TestBootstrapLogin(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Login(f.ctx, "admin", "000000"); !errors.Is(err, booking.ErrBadLogin) {
		t.Errorf("wrong bootstrap PIN: %v", err)
	}
	if _, err := f.svc.Login(f.ctx, "budi", "112233"); !errors.Is(err, booking.ErrBadLogin) {
		t.Errorf("bootstrap PIN with other username: %v", err)
	}
	u, err := f.svc.Login(f.ctx, " Admin ", "112233")
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "admin" || u.Role != booking.RoleAdmin || u.PINHash == "" || strings.Contains(u.PINHash, "112233") {
		t.Errorf("bootstrap user = %+v", u)
	}
	// Now saved: normal login works, and it is the only user.
	users, _ := f.store.ListUsers(f.ctx)
	if len(users) != 1 {
		t.Fatalf("users = %+v", users)
	}
	if _, err := f.svc.Login(f.ctx, "admin", "112233"); err != nil {
		t.Errorf("second login: %v", err)
	}
}

func TestBootstrapOffOnceUsersExist(t *testing.T) {
	f := newFixture(t)
	admin := mustLogin(t, f, "admin", "112233")
	if err := f.svc.ChangeOwnPIN(f.ctx, admin, "000000", "583920"); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("change PIN with wrong old PIN: %v", err)
	}
	if err := f.svc.ChangeOwnPIN(f.ctx, admin, "112233", "583920"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Login(f.ctx, "admin", "112233"); !errors.Is(err, booking.ErrBadLogin) {
		t.Errorf("old bootstrap PIN after change: %v", err)
	}
	mustLogin(t, f, "admin", "583920")
}

func TestLoginLockout(t *testing.T) {
	f := newFixture(t)
	admin := mustLogin(t, f, "admin", "112233")
	if _, err := f.svc.CreateUser(f.ctx, admin, booking.UserInput{Username: "sari", Name: "Sari", Role: booking.RoleStaff, PIN: "582047"}); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		f.svc.Login(f.ctx, "sari", "000001")
	}
	if _, err := f.svc.Login(f.ctx, "sari", "582047"); !errors.Is(err, booking.ErrBadLogin) {
		t.Errorf("locked user with right PIN: %v", err)
	}
	f.now = f.now.Add(6 * time.Minute)
	mustLogin(t, f, "sari", "582047")
}

func TestUserManagement(t *testing.T) {
	f := newFixture(t)
	admin := mustLogin(t, f, "admin", "112233")

	bad := []booking.UserInput{
		{Username: "ab", Name: "X", Role: booking.RoleStaff, PIN: "582047"},
		{Username: "Budi Santoso", Name: "X", Role: booking.RoleStaff, PIN: "582047"},
		{Username: "budi", Name: "", Role: booking.RoleStaff, PIN: "582047"},
		{Username: "budi", Name: "X", Role: "owner", PIN: "582047"},
		{Username: "budi", Name: "X", Role: booking.RoleStaff, PIN: "12345"},
		{Username: "budi", Name: "X", Role: booking.RoleStaff, PIN: "123456"},
		{Username: "budi", Name: "X", Role: booking.RoleStaff, PIN: "777777"},
		{Username: "budi", Name: "X", Role: booking.RoleStaff, PIN: "987654"},
		{Username: "admin", Name: "X", Role: booking.RoleStaff, PIN: "582047"},
	}
	for _, in := range bad {
		if _, err := f.svc.CreateUser(f.ctx, admin, in); !errors.Is(err, booking.ErrInvalid) {
			t.Errorf("create %+v: err = %v", in, err)
		}
	}

	budi, err := f.svc.CreateUser(f.ctx, admin, booking.UserInput{Username: "Budi", Name: "Budi", Role: booking.RoleSupervisor, PIN: "582047"})
	if err != nil {
		t.Fatal(err)
	}
	if budi.Username != "budi" {
		t.Errorf("username not normalised: %s", budi.Username)
	}
	mustLogin(t, f, "budi", "582047")

	// Admin cannot demote self, and the last admin cannot go.
	if _, err := f.svc.UpdateUser(f.ctx, admin, "admin", booking.UserUpdate{Name: "Admin", Role: booking.RoleStaff, Active: true}); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("self demote: %v", err)
	}
	// Promote budi, then budi may deactivate the first admin.
	budi, err = f.svc.UpdateUser(f.ctx, admin, "budi", booking.UserUpdate{Name: "Budi S", Role: booking.RoleAdmin, Active: true})
	if err != nil || budi.Role != booking.RoleAdmin {
		t.Fatalf("promote: %+v %v", budi, err)
	}
	if _, err := f.svc.UpdateUser(f.ctx, budi, "admin", booking.UserUpdate{Name: "Admin", Role: booking.RoleAdmin, Active: false}); err != nil {
		t.Fatalf("deactivate other admin: %v", err)
	}
	// Deactivated user: session and login stop at once.
	if _, err := f.svc.SessionUser(f.ctx, "admin"); !errors.Is(err, booking.ErrBadLogin) {
		t.Errorf("session of deactivated user: %v", err)
	}
	if _, err := f.svc.Login(f.ctx, "admin", "112233"); !errors.Is(err, booking.ErrBadLogin) {
		t.Errorf("login of deactivated user: %v", err)
	}
	// budi is now the only active admin and cannot remove himself.
	if _, err := f.svc.UpdateUser(f.ctx, budi, "budi", booking.UserUpdate{Name: "Budi", Role: booking.RoleAdmin, Active: false}); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("last admin deactivate self: %v", err)
	}

	// Reset PIN.
	if err := f.svc.ResetPIN(f.ctx, budi, "admin", "903817"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ResetPIN(f.ctx, budi, "nobody", "903817"); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("reset unknown: %v", err)
	}
	users, _ := f.svc.Users(f.ctx, budi)
	if len(users) != 2 || users[0].Username != "admin" {
		t.Errorf("users = %+v", users)
	}
}

func TestRoomManagement(t *testing.T) {
	f := newFixture(t)
	r, err := f.svc.CreateRoom(f.ctx, f.admin, booking.RoomInput{ID: " r09 ", Name: "Room 09", RatePerHour: 175000, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "R09" {
		t.Errorf("id = %s", r.ID)
	}
	for _, in := range []booking.RoomInput{
		{ID: "R09", Name: "Dup", RatePerHour: 1},
		{ID: "r01", Name: "Dup case", RatePerHour: 1},
		{ID: "R 10", Name: "Space", RatePerHour: 1},
		{ID: "R10", Name: "", RatePerHour: 1},
		{ID: "R10", Name: "Free", RatePerHour: 0},
	} {
		if _, err := f.svc.CreateRoom(f.ctx, f.admin, in); !errors.Is(err, booking.ErrInvalid) {
			t.Errorf("create %+v: %v", in, err)
		}
	}

	// A booking made before a price change keeps its price.
	b, _ := f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: "R09", CustomerName: "A", Start: at("19:00"), DurationMinutes: 60})
	if _, err := f.svc.UpdateRoom(f.ctx, f.admin, "R09", booking.RoomInput{Name: "Room 09 VIP", RatePerHour: 200000, Active: true}); err != nil {
		t.Fatal(err)
	}
	day, _ := f.svc.DayBookings(f.ctx, f.sup, f.now)
	for _, x := range day {
		if x.ID == b.ID && x.TotalPrice != 175000 {
			t.Errorf("old booking price changed: %d", x.TotalPrice)
		}
	}
	nb, _ := f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: "R09", CustomerName: "B", Start: at("21:00"), DurationMinutes: 60})
	if nb.TotalPrice != 200000 {
		t.Errorf("new booking price = %d", nb.TotalPrice)
	}

	// Inactive rooms cannot be booked.
	if _, err := f.svc.UpdateRoom(f.ctx, f.admin, "R09", booking.RoomInput{Name: "Room 09 VIP", RatePerHour: 200000, Active: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Create(f.ctx, f.sup, booking.CreateInput{RoomID: "R09", CustomerName: "C", Start: at("23:00"), DurationMinutes: 60}); !errors.Is(err, booking.ErrInvalid) {
		t.Errorf("book inactive room: %v", err)
	}
	if _, err := f.svc.UpdateRoom(f.ctx, f.admin, "NOPE", booking.RoomInput{Name: "x", RatePerHour: 1}); !errors.Is(err, booking.ErrNotFound) {
		t.Errorf("update unknown room: %v", err)
	}

	log, _ := f.svc.Activity(f.ctx, f.admin, f.now)
	var details []string
	for _, a := range log {
		if a.Action == booking.ActRoomUpdate {
			details = append(details, a.Detail)
		}
	}
	if len(details) != 2 || !strings.Contains(strings.Join(details, "|"), "tarif Rp175.000 -> Rp200.000") {
		t.Errorf("room audit = %v", details)
	}
}

func mustLogin(t *testing.T, f *fixture, user, pin string) booking.User {
	t.Helper()
	u, err := f.svc.Login(f.ctx, user, pin)
	if err != nil {
		t.Fatalf("login %s: %v", user, err)
	}
	return u
}

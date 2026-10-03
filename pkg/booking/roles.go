package booking

import (
	"slices"
	"time"
)

// Role is a user's level. Staff, supervisor and admin each add rights to the
// one before. Accounting is separate: it can only view and export.
type Role string

const (
	RoleStaff      Role = "staff"
	RoleSupervisor Role = "supervisor"
	RoleAdmin      Role = "admin"
	RoleAccounting Role = "accounting"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	_, ok := permissions[r]
	return ok
}

// Permission is one thing a user may do.
type Permission string

const (
	PermViewSchedule  Permission = "schedule.view"
	PermCreateBooking Permission = "booking.create"
	PermCheckIn       Permission = "booking.checkin"
	PermExtend        Permission = "booking.extend"
	PermCheckOut      Permission = "booking.checkout"
	PermConfirm       Permission = "booking.confirm"
	// PermCancelTentative allows cancelling tentative bookings only;
	// PermCancel allows cancelling any booking that is not checked in.
	PermCancelTentative Permission = "booking.cancel_tentative"
	PermCancel          Permission = "booking.cancel"
	PermViewReport      Permission = "report.view"
	PermViewActivity    Permission = "activity.view"
	PermExport          Permission = "report.export"
	PermManageRooms     Permission = "rooms.manage"
	PermManageUsers     Permission = "users.manage"
	PermManageDevices   Permission = "devices.manage"
)

// permissions is the single source of truth for access rights.
// The HTTP layer checks it on every request; the web page only uses it to
// hide buttons.
var permissions = map[Role][]Permission{
	RoleStaff: {
		PermViewSchedule, PermCreateBooking, PermCheckIn, PermExtend, PermCheckOut,
		PermConfirm, PermCancelTentative,
	},
	RoleSupervisor: {
		PermViewSchedule, PermCreateBooking, PermCheckIn, PermExtend, PermCheckOut,
		PermConfirm, PermCancelTentative,
		PermCancel, PermViewReport, PermViewActivity, PermExport,
	},
	RoleAdmin: {
		PermViewSchedule, PermCreateBooking, PermCheckIn, PermExtend, PermCheckOut,
		PermConfirm, PermCancelTentative,
		PermCancel, PermViewReport, PermViewActivity, PermExport,
		PermManageRooms, PermManageUsers, PermManageDevices,
	},
	// Accounting can look at everything about bookings and money, and
	// download it, but cannot change anything.
	RoleAccounting: {
		PermViewSchedule, PermViewReport, PermViewActivity, PermExport,
	},
}

// Can reports whether the role has the permission.
func (r Role) Can(p Permission) bool {
	return slices.Contains(permissions[r], p)
}

// Permissions lists the role's permissions.
func (r Role) Permissions() []Permission {
	return slices.Clone(permissions[r])
}

// User is a person who can log in. PINHash is never sent to the browser.
type User struct {
	Username  string    `json:"username"`
	Name      string    `json:"name"`
	Role      Role      `json:"role"`
	PINHash   string    `json:"-"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Can reports whether the user is active and has the permission.
func (u User) Can(p Permission) bool {
	return u.Active && u.Role.Can(p)
}

// Activity is one line in the audit log.
type Activity struct {
	Time      time.Time `json:"time"`
	Username  string    `json:"username"`
	Action    string    `json:"action"`
	BookingID string    `json:"booking_id"`
	RoomID    string    `json:"room_id"`
	Detail    string    `json:"detail"`
}

// Audit log actions.
const (
	ActLogin         = "login"
	ActPINChange     = "pin.change"
	ActBookingCreate = "booking.create"
	ActConfirm       = "booking.confirm"
	ActCheckIn       = "booking.checkin"
	ActExtend        = "booking.extend"
	ActCheckOut      = "booking.checkout"
	ActCancel        = "booking.cancel"
	ActRoomCreate    = "room.create"
	ActRoomUpdate    = "room.update"
	ActUserCreate    = "user.create"
	ActUserUpdate    = "user.update"
	ActUserPINReset  = "user.pin_reset"
	ActPricingUpdate = "pricing.update"
	ActExport        = "export"
	ActDeviceCreate  = "device.create"
	ActDevicePair    = "device.pair"
	ActDeviceRevoke  = "device.revoke"
)

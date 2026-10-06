// Package sheetstore stores rooms, bookings, users and the audit log in a
// Google Spreadsheet.
//
// Row 1 of each tab is the header. Columns are found by header name, so
// columns may be reordered or added, but known headers must not be renamed.
//
//	Rooms:    id | name | rate_per_hour | active
//	Bookings: id | room_id | customer_name | phone | start | end | duration_minutes |
//	          status | rate_per_hour | total_price | notes | checked_in_at |
//	          checked_out_at | created_at | updated_at | created_by |
//	          checked_in_by | checked_out_by | cancelled_by | confirmed_by | hold_until |
//	          complimentary | compliment_reason
//	Users:    username | name | role | pin_hash | active | created_at | updated_at
//	Activity: time | username | action | booking_id | room_id | detail
//	Pricing:  day_type | start | end | rate_per_hour   (weekday/weekend, HH:MM)
//	Devices:  id | name | room_id | status | token_hash | pair_code_hash |
//	          pair_expires | created_by | created_at | paired_at | revoked_by
//
// Times are written as text in the business time zone ("2006-01-02 15:04"),
// so the sheet is easy to read and does not depend on the sheet's locale.
package sheetstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"

	"karaoke/pkg/booking"
)

// Tab names in the spreadsheet.
const (
	RoomsSheet    = "Rooms"
	BookingsSheet = "Bookings"
	UsersSheet    = "Users"
	ActivitySheet = "Activity"
	DevicesSheet  = "Devices"
	PricingSheet  = "Pricing"
)

// Column headers, in the order used when a tab is created. New columns are
// added at the end, so existing sheets can be upgraded in place.
var (
	RoomColumns    = []string{"id", "name", "rate_per_hour", "active"}
	BookingColumns = []string{
		"id", "room_id", "customer_name", "phone", "start", "end", "duration_minutes",
		"status", "rate_per_hour", "total_price", "notes", "checked_in_at",
		"checked_out_at", "created_at", "updated_at",
		"created_by", "checked_in_by", "checked_out_by", "cancelled_by",
		"confirmed_by", "hold_until", "complimentary", "compliment_reason",
	}
	UserColumns     = []string{"username", "name", "role", "pin_hash", "active", "created_at", "updated_at"}
	ActivityColumns = []string{"time", "username", "action", "booking_id", "room_id", "detail"}
	PricingColumns  = []string{"day_type", "start", "end", "rate_per_hour"}
	DeviceColumns   = []string{
		"id", "name", "room_id", "status", "token_hash", "pair_code_hash",
		"pair_expires", "created_by", "created_at", "paired_at", "revoked_by",
	}
)

// Schema lists every tab with its columns.
var Schema = []struct {
	Name    string
	Columns []string
}{
	{RoomsSheet, RoomColumns},
	{BookingsSheet, BookingColumns},
	{UsersSheet, UserColumns},
	{ActivitySheet, ActivityColumns},
	{DevicesSheet, DeviceColumns},
	{PricingSheet, PricingColumns},
}

const (
	minuteLayout = "2006-01-02 15:04"
	secondLayout = "2006-01-02 15:04:05"
	// cacheTTL limits Sheets API reads. Google allows about 60 reads per
	// minute for one service account, and every TV polls this server.
	cacheTTL = 5 * time.Second
)

// Store implements booking.Store on Google Sheets.
type Store struct {
	api           *sheets.Service
	spreadsheetID string
	loc           *time.Location

	mu       sync.Mutex
	cachedAt time.Time
	snap     *snapshot
	// headers outlive the snapshot cache, so appends (bookings, audit lines)
	// do not need an extra read. They are refreshed on every load.
	headers map[string]map[string]int
}

// table is one tab as read: header positions and where each row is.
type table struct {
	cols  map[string]int   // header name -> column index
	rowOf map[string]int   // key -> 1-based sheet row
	raw   map[string][]any // key -> cells as read
}

// snapshot is one read of the Rooms, Bookings, Users and Devices tabs.
type snapshot struct {
	rooms    []booking.Room
	bookings []booking.Booking
	users    []booking.User
	devices  []booking.Device
	pricing  []booking.PriceRule
	tabs     map[string]*table
}

// New connects to the spreadsheet with a service account key (JSON).
// Share the spreadsheet with the service account e-mail as Editor.
func New(ctx context.Context, credentialsJSON []byte, spreadsheetID string, loc *time.Location) (*Store, error) {
	api, err := sheets.NewService(ctx,
		option.WithAuthCredentialsJSON(option.ServiceAccount, credentialsJSON),
		option.WithScopes(sheets.SpreadsheetsScope),
	)
	if err != nil {
		return nil, fmt.Errorf("sheets client: %w", err)
	}
	return &Store{api: api, spreadsheetID: spreadsheetID, loc: loc}, nil
}

// ---------- booking.Store ----------

func (s *Store) ListRooms(ctx context.Context) ([]booking.Room, error) {
	snap, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return slices.Clone(snap.rooms), nil
}

func (s *Store) ListBookings(ctx context.Context) ([]booking.Booking, error) {
	snap, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return slices.Clone(snap.bookings), nil
}

func (s *Store) ListUsers(ctx context.Context) ([]booking.User, error) {
	snap, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return slices.Clone(snap.users), nil
}

func (s *Store) ListPricing(ctx context.Context) ([]booking.PriceRule, error) {
	snap, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return slices.Clone(snap.pricing), nil
}

// ReplacePricing clears the Pricing rows under the header and writes rules.
func (s *Store) ReplacePricing(ctx context.Context, rules []booking.PriceRule) error {
	cols, err := s.header(ctx, PricingSheet)
	if err != nil {
		return err
	}
	rows := make([][]any, len(rules))
	for i, r := range rules {
		rows[i] = buildRow(map[string]any{
			"day_type": string(r.DayType), "start": r.Start.String(), "end": r.End.String(), "rate_per_hour": r.RatePerHour,
		}, cols, nil)
	}
	defer s.invalidate()
	if _, err := s.api.Spreadsheets.Values.Clear(s.spreadsheetID, PricingSheet+"!A2:Z",
		&sheets.ClearValuesRequest{}).Context(ctx).Do(); err != nil {
		return fmt.Errorf("sheets clear pricing: %w", err)
	}
	if _, err := s.api.Spreadsheets.Values.Update(s.spreadsheetID, PricingSheet+"!A2",
		&sheets.ValueRange{Values: rows}).ValueInputOption("RAW").Context(ctx).Do(); err != nil {
		return fmt.Errorf("sheets write pricing: %w", err)
	}
	return nil
}

func (s *Store) ListDevices(ctx context.Context) ([]booking.Device, error) {
	snap, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return slices.Clone(snap.devices), nil
}

func (s *Store) AddDevice(ctx context.Context, d booking.Device) error {
	return s.appendRow(ctx, DevicesSheet, s.deviceValues(d))
}

func (s *Store) UpdateDevice(ctx context.Context, d booking.Device) error {
	return s.updateRow(ctx, DevicesSheet, d.ID, s.deviceValues(d))
}

func (s *Store) AddBooking(ctx context.Context, b booking.Booking) error {
	return s.appendRow(ctx, BookingsSheet, s.bookingValues(b))
}

func (s *Store) UpdateBooking(ctx context.Context, b booking.Booking) error {
	return s.updateRow(ctx, BookingsSheet, b.ID, s.bookingValues(b))
}

func (s *Store) AddRoom(ctx context.Context, r booking.Room) error {
	return s.appendRow(ctx, RoomsSheet, roomValues(r))
}

func (s *Store) UpdateRoom(ctx context.Context, r booking.Room) error {
	return s.updateRow(ctx, RoomsSheet, r.ID, roomValues(r))
}

func (s *Store) AddUser(ctx context.Context, u booking.User) error {
	return s.appendRow(ctx, UsersSheet, s.userValues(u))
}

func (s *Store) UpdateUser(ctx context.Context, u booking.User) error {
	return s.updateRow(ctx, UsersSheet, u.Username, s.userValues(u))
}

func (s *Store) AddActivity(ctx context.Context, a booking.Activity) error {
	return s.appendRow(ctx, ActivitySheet, map[string]any{
		"time":       s.formatTime(a.Time, secondLayout),
		"username":   a.Username,
		"action":     a.Action,
		"booking_id": a.BookingID,
		"room_id":    a.RoomID,
		"detail":     a.Detail,
	})
}

// ListActivity reads the whole Activity tab (not cached; it is only read
// when someone opens the activity view) and filters by time.
func (s *Store) ListActivity(ctx context.Context, from, to time.Time) ([]booking.Activity, error) {
	resp, err := s.api.Spreadsheets.Values.Get(s.spreadsheetID, ActivitySheet).
		ValueRenderOption("UNFORMATTED_VALUE").DateTimeRenderOption("FORMATTED_STRING").
		Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("sheets read activity: %w", err)
	}
	c, err := headerIndex(ActivitySheet, resp.Values, ActivityColumns)
	if err != nil {
		return nil, err
	}
	var out []booking.Activity
	for _, row := range resp.Values[1:] {
		t, err := s.parseTime(cellString(row, c["time"]))
		if err != nil || t.IsZero() || t.Before(from) || !t.Before(to) {
			continue // skip broken or out-of-range lines instead of failing the view
		}
		out = append(out, booking.Activity{
			Time:      t,
			Username:  cellString(row, c["username"]),
			Action:    cellString(row, c["action"]),
			BookingID: cellString(row, c["booking_id"]),
			RoomID:    cellString(row, c["room_id"]),
			Detail:    cellString(row, c["detail"]),
		})
	}
	return out, nil
}

// ---------- Generic row writes ----------

func (s *Store) appendRow(ctx context.Context, sheet string, values map[string]any) error {
	cols, err := s.header(ctx, sheet)
	if err != nil {
		return err
	}
	row := buildRow(values, cols, nil)
	_, err = s.api.Spreadsheets.Values.Append(s.spreadsheetID, sheet+"!A1",
		&sheets.ValueRange{Values: [][]any{row}}).
		ValueInputOption("RAW").InsertDataOption("INSERT_ROWS").Context(ctx).Do()
	if sheet != ActivitySheet {
		s.invalidate()
	}
	if err != nil {
		return fmt.Errorf("sheets append %s: %w", sheet, err)
	}
	return nil
}

func (s *Store) updateRow(ctx context.Context, sheet, key string, values map[string]any) error {
	// Read again so a row added or deleted by hand does not shift the target row.
	snap, err := s.load(booking.WithFreshRead(ctx))
	if err != nil {
		return err
	}
	t := snap.tabs[sheet]
	k := rowKey(key)
	rowNum, ok := t.rowOf[k]
	if !ok {
		return booking.ErrNotFound
	}
	row := buildRow(values, t.cols, t.raw[k])
	rng := fmt.Sprintf("%s!A%d:%s%d", sheet, rowNum, columnLetter(len(row)-1), rowNum)
	_, err = s.api.Spreadsheets.Values.Update(s.spreadsheetID, rng,
		&sheets.ValueRange{Values: [][]any{row}}).
		ValueInputOption("RAW").Context(ctx).Do()
	s.invalidate()
	if err != nil {
		return fmt.Errorf("sheets update %s: %w", sheet, err)
	}
	return nil
}

// header returns the column positions of a tab, from the last load.
func (s *Store) header(ctx context.Context, sheet string) (map[string]int, error) {
	s.mu.Lock()
	cols := s.headers[sheet]
	s.mu.Unlock()
	if cols != nil {
		return cols, nil
	}
	if _, err := s.load(booking.WithFreshRead(ctx)); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headers[sheet], nil
}

// buildRow puts values in the sheet's column order. Cells in columns this
// program does not know keep their existing value.
func buildRow(values map[string]any, cols map[string]int, existing []any) []any {
	width := len(existing)
	for _, i := range cols {
		width = max(width, i+1)
	}
	row := make([]any, width)
	for i := range row {
		row[i] = ""
		if i < len(existing) && existing[i] != nil {
			row[i] = existing[i]
		}
	}
	for name, i := range cols {
		if v, ok := values[name]; ok {
			row[i] = v
		}
	}
	return row
}

// rowKey normalises IDs and usernames so lookups ignore case and spaces.
func rowKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ---------- Schema setup ----------

// SchemaStatus describes what EnsureSchema would change.
type SchemaStatus struct {
	Tab            string
	Exists         bool
	MissingColumns []string
}

// CheckSchema reports missing tabs and columns. It only reads.
func (s *Store) CheckSchema(ctx context.Context) ([]SchemaStatus, error) {
	have, err := s.tabTitles(ctx)
	if err != nil {
		return nil, err
	}
	var out []SchemaStatus
	for _, t := range Schema {
		st := SchemaStatus{Tab: t.Name, Exists: slices.Contains(have, t.Name)}
		if !st.Exists {
			st.MissingColumns = t.Columns
		} else {
			hdr, err := s.headerRow(ctx, t.Name)
			if err != nil {
				return nil, err
			}
			for _, c := range t.Columns {
				if !slices.Contains(hdr, c) {
					st.MissingColumns = append(st.MissingColumns, c)
				}
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// EnsureSchema creates missing tabs and adds missing header columns at the
// end of existing tabs. Seed rooms are added only if the Rooms tab is new,
// and seed pricing only if the Pricing tab is new. Existing data is never changed.
func (s *Store) EnsureSchema(ctx context.Context, seed []booking.Room, seedPricing []booking.PriceRule) error {
	status, err := s.CheckSchema(ctx)
	if err != nil {
		return err
	}
	var add []*sheets.Request
	for _, st := range status {
		if !st.Exists {
			add = append(add, &sheets.Request{AddSheet: &sheets.AddSheetRequest{
				Properties: &sheets.SheetProperties{Title: st.Tab},
			}})
		}
	}
	if len(add) > 0 {
		if _, err := s.api.Spreadsheets.BatchUpdate(s.spreadsheetID,
			&sheets.BatchUpdateSpreadsheetRequest{Requests: add}).Context(ctx).Do(); err != nil {
			return fmt.Errorf("add tabs: %w", err)
		}
	}

	for _, st := range status {
		if len(st.MissingColumns) == 0 {
			continue
		}
		start := 0
		if st.Exists {
			hdr, err := s.headerRow(ctx, st.Tab)
			if err != nil {
				return err
			}
			start = len(hdr)
		}
		rng := fmt.Sprintf("%s!%s1", st.Tab, columnLetter(start))
		rows := [][]any{toAny(st.MissingColumns)}
		if st.Tab == RoomsSheet && !st.Exists {
			for _, r := range seed {
				rows = append(rows, []any{r.ID, r.Name, r.RatePerHour, r.Active})
			}
		}
		if st.Tab == PricingSheet && !st.Exists {
			for _, r := range seedPricing {
				rows = append(rows, []any{string(r.DayType), r.Start.String(), r.End.String(), r.RatePerHour})
			}
		}
		if _, err := s.api.Spreadsheets.Values.Update(s.spreadsheetID, rng,
			&sheets.ValueRange{Values: rows}).ValueInputOption("RAW").Context(ctx).Do(); err != nil {
			return fmt.Errorf("write %s header: %w", st.Tab, err)
		}
	}
	s.invalidate()
	s.mu.Lock()
	s.headers = nil
	s.mu.Unlock()
	return nil
}

func (s *Store) tabTitles(ctx context.Context) ([]string, error) {
	ss, err := s.api.Spreadsheets.Get(s.spreadsheetID).Fields("sheets.properties.title").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("read spreadsheet: %w", err)
	}
	var names []string
	for _, sh := range ss.Sheets {
		names = append(names, sh.Properties.Title)
	}
	return names, nil
}

// TabNames returns the tab titles in the spreadsheet. It only reads.
func (s *Store) TabNames(ctx context.Context) ([]string, error) { return s.tabTitles(ctx) }

func (s *Store) headerRow(ctx context.Context, tab string) ([]string, error) {
	resp, err := s.api.Spreadsheets.Values.Get(s.spreadsheetID, tab+"!1:1").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("read %s header: %w", tab, err)
	}
	var out []string
	if len(resp.Values) > 0 {
		for _, h := range resp.Values[0] {
			out = append(out, strings.ToLower(strings.TrimSpace(fmt.Sprint(h))))
		}
	}
	return out, nil
}

// ---------- Reading ----------

func (s *Store) invalidate() {
	s.mu.Lock()
	s.snap = nil
	s.mu.Unlock()
}

// load reads Rooms, Bookings, Users, Devices and the Activity header in one API call,
// or returns the cached snapshot.
func (s *Store) load(ctx context.Context) (*snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap != nil && !booking.FreshRead(ctx) && time.Since(s.cachedAt) < cacheTTL {
		return s.snap, nil
	}

	resp, err := s.api.Spreadsheets.Values.BatchGet(s.spreadsheetID).
		Ranges(RoomsSheet, BookingsSheet, UsersSheet, ActivitySheet+"!1:1", DevicesSheet, PricingSheet).
		ValueRenderOption("UNFORMATTED_VALUE").
		DateTimeRenderOption("FORMATTED_STRING").
		Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("sheets read: %w (jalankan setup sheet jika tab baru belum ada)", err)
	}
	if len(resp.ValueRanges) != 6 {
		return nil, fmt.Errorf("sheets read: expected 6 ranges, got %d", len(resp.ValueRanges))
	}
	v := func(i int) [][]any { return resp.ValueRanges[i].Values }
	snap, err := s.parse(v(0), v(1), v(2), v(3), v(4), v(5))
	if err != nil {
		return nil, err
	}
	s.snap, s.cachedAt = snap, time.Now()
	s.headers = map[string]map[string]int{}
	for name, t := range snap.tabs {
		s.headers[name] = t.cols
	}
	return snap, nil
}

func (s *Store) parse(roomRows, bookingRows, userRows, activityHeader, deviceRows, pricingRows [][]any) (*snapshot, error) {
	snap := &snapshot{tabs: map[string]*table{}}
	newTable := func(name string, rows [][]any, want []string) (*table, error) {
		cols, err := headerIndex(name, rows, want)
		if err != nil {
			return nil, err
		}
		t := &table{cols: cols, rowOf: map[string]int{}, raw: map[string][]any{}}
		snap.tabs[name] = t
		return t, nil
	}
	remember := func(t *table, key string, i int, row []any) {
		k := rowKey(key)
		t.rowOf[k] = i + 2 // +1 for header, +1 for 1-based rows
		t.raw[k] = row
	}

	rt, err := newTable(RoomsSheet, roomRows, RoomColumns)
	if err != nil {
		return nil, err
	}
	for i, row := range roomRows[1:] {
		id := cellString(row, rt.cols["id"])
		if id == "" {
			continue
		}
		rate, err := cellInt(row, rt.cols["rate_per_hour"])
		if err != nil {
			return nil, fmt.Errorf("%s room %s: rate_per_hour: %w", RoomsSheet, id, err)
		}
		snap.rooms = append(snap.rooms, booking.Room{
			ID:          id,
			Name:        cellString(row, rt.cols["name"]),
			RatePerHour: rate,
			Active:      cellBool(row, rt.cols["active"]),
		})
		remember(rt, id, i, row)
	}

	bt, err := newTable(BookingsSheet, bookingRows, BookingColumns)
	if err != nil {
		return nil, err
	}
	for i, row := range bookingRows[1:] {
		id := cellString(row, bt.cols["id"])
		if id == "" {
			continue
		}
		b, err := s.parseBooking(row, bt.cols)
		if err != nil {
			return nil, fmt.Errorf("%s row %d (%s): %w", BookingsSheet, i+2, id, err)
		}
		snap.bookings = append(snap.bookings, b)
		remember(bt, id, i, row)
	}

	ut, err := newTable(UsersSheet, userRows, UserColumns)
	if err != nil {
		return nil, err
	}
	for i, row := range userRows[1:] {
		name := rowKey(cellString(row, ut.cols["username"]))
		if name == "" {
			continue
		}
		u := booking.User{
			Username: name,
			Name:     cellString(row, ut.cols["name"]),
			Role:     booking.Role(strings.ToLower(cellString(row, ut.cols["role"]))),
			PINHash:  cellString(row, ut.cols["pin_hash"]),
			Active:   cellBool(row, ut.cols["active"]),
		}
		// Bad timestamps in a user row should not lock everyone out.
		u.CreatedAt, _ = s.parseTime(cellString(row, ut.cols["created_at"]))
		u.UpdatedAt, _ = s.parseTime(cellString(row, ut.cols["updated_at"]))
		snap.users = append(snap.users, u)
		remember(ut, name, i, row)
	}

	if _, err := newTable(ActivitySheet, activityHeader, ActivityColumns); err != nil {
		return nil, err
	}

	dt, err := newTable(DevicesSheet, deviceRows, DeviceColumns)
	if err != nil {
		return nil, err
	}
	for i, row := range deviceRows[1:] {
		id := cellString(row, dt.cols["id"])
		if id == "" {
			continue
		}
		d := booking.Device{
			ID:           id,
			Name:         cellString(row, dt.cols["name"]),
			RoomID:       cellString(row, dt.cols["room_id"]),
			Status:       booking.DeviceStatus(cellString(row, dt.cols["status"])),
			TokenHash:    cellString(row, dt.cols["token_hash"]),
			PairCodeHash: cellString(row, dt.cols["pair_code_hash"]),
			CreatedBy:    cellString(row, dt.cols["created_by"]),
			RevokedBy:    cellString(row, dt.cols["revoked_by"]),
		}
		// A broken timestamp must not stop every TV; treat it as empty.
		d.PairExpires, _ = s.parseTime(cellString(row, dt.cols["pair_expires"]))
		d.CreatedAt, _ = s.parseTime(cellString(row, dt.cols["created_at"]))
		d.PairedAt, _ = s.parseTime(cellString(row, dt.cols["paired_at"]))
		snap.devices = append(snap.devices, d)
		remember(dt, id, i, row)
	}

	pt, err := newTable(PricingSheet, pricingRows, PricingColumns)
	if err != nil {
		return nil, err
	}
	for i, row := range pricingRows[1:] {
		day := strings.ToLower(cellString(row, pt.cols["day_type"]))
		if day == "" {
			continue
		}
		start, err1 := booking.ParseClock(cellString(row, pt.cols["start"]))
		end, err2 := booking.ParseClock(cellString(row, pt.cols["end"]))
		rate, err3 := cellInt(row, pt.cols["rate_per_hour"])
		if err := errors.Join(err1, err2, err3); err != nil {
			return nil, fmt.Errorf("%s row %d: %w", PricingSheet, i+2, err)
		}
		snap.pricing = append(snap.pricing, booking.PriceRule{
			DayType: booking.DayType(day), Start: start, End: end, RatePerHour: rate,
		})
	}
	return snap, nil
}

func (s *Store) parseBooking(row []any, c map[string]int) (booking.Booking, error) {
	b := booking.Booking{
		ID:               cellString(row, c["id"]),
		RoomID:           cellString(row, c["room_id"]),
		CustomerName:     cellString(row, c["customer_name"]),
		Phone:            cellString(row, c["phone"]),
		Status:           booking.Status(cellString(row, c["status"])),
		Notes:            cellString(row, c["notes"]),
		CreatedBy:        cellString(row, c["created_by"]),
		CheckedInBy:      cellString(row, c["checked_in_by"]),
		CheckedOutBy:     cellString(row, c["checked_out_by"]),
		CancelledBy:      cellString(row, c["cancelled_by"]),
		ConfirmedBy:      cellString(row, c["confirmed_by"]),
		Complimentary:    cellBool(row, c["complimentary"]),
		ComplimentReason: cellString(row, c["compliment_reason"]),
	}
	var err error
	if b.Start, err = s.parseTime(cellString(row, c["start"])); err != nil || b.Start.IsZero() {
		return b, fmt.Errorf("start: %v", orMissing(err))
	}
	if b.End, err = s.parseTime(cellString(row, c["end"])); err != nil || b.End.IsZero() {
		return b, fmt.Errorf("end: %v", orMissing(err))
	}
	if b.RatePerHour, err = cellInt(row, c["rate_per_hour"]); err != nil {
		return b, fmt.Errorf("rate_per_hour: %w", err)
	}
	if b.TotalPrice, err = cellInt(row, c["total_price"]); err != nil {
		return b, fmt.Errorf("total_price: %w", err)
	}
	for name, dst := range map[string]*time.Time{
		"checked_in_at": &b.CheckedInAt, "checked_out_at": &b.CheckedOutAt,
		"created_at": &b.CreatedAt, "updated_at": &b.UpdatedAt,
		"hold_until": &b.HoldUntil,
	} {
		if *dst, err = s.parseTime(cellString(row, c[name])); err != nil {
			return b, fmt.Errorf("%s: %w", name, err)
		}
	}
	return b, nil
}

// ---------- Values per tab ----------

func (s *Store) bookingValues(b booking.Booking) map[string]any {
	return map[string]any{
		"id":                b.ID,
		"room_id":           b.RoomID,
		"customer_name":     b.CustomerName,
		"phone":             b.Phone,
		"start":             s.formatTime(b.Start, minuteLayout),
		"end":               s.formatTime(b.End, minuteLayout),
		"duration_minutes":  b.DurationMinutes(),
		"status":            string(b.Status),
		"rate_per_hour":     b.RatePerHour,
		"total_price":       b.TotalPrice,
		"notes":             b.Notes,
		"checked_in_at":     s.formatTime(b.CheckedInAt, secondLayout),
		"checked_out_at":    s.formatTime(b.CheckedOutAt, secondLayout),
		"created_at":        s.formatTime(b.CreatedAt, secondLayout),
		"updated_at":        s.formatTime(b.UpdatedAt, secondLayout),
		"created_by":        b.CreatedBy,
		"checked_in_by":     b.CheckedInBy,
		"checked_out_by":    b.CheckedOutBy,
		"cancelled_by":      b.CancelledBy,
		"confirmed_by":      b.ConfirmedBy,
		"hold_until":        s.formatTime(b.HoldUntil, minuteLayout),
		"complimentary":     b.Complimentary,
		"compliment_reason": b.ComplimentReason,
	}
}

func (s *Store) deviceValues(d booking.Device) map[string]any {
	return map[string]any{
		"id":             d.ID,
		"name":           d.Name,
		"room_id":        d.RoomID,
		"status":         string(d.Status),
		"token_hash":     d.TokenHash,
		"pair_code_hash": d.PairCodeHash,
		"pair_expires":   s.formatTime(d.PairExpires, secondLayout),
		"created_by":     d.CreatedBy,
		"created_at":     s.formatTime(d.CreatedAt, secondLayout),
		"paired_at":      s.formatTime(d.PairedAt, secondLayout),
		"revoked_by":     d.RevokedBy,
	}
}

func roomValues(r booking.Room) map[string]any {
	return map[string]any{"id": r.ID, "name": r.Name, "rate_per_hour": r.RatePerHour, "active": r.Active}
}

func (s *Store) userValues(u booking.User) map[string]any {
	return map[string]any{
		"username":   u.Username,
		"name":       u.Name,
		"role":       string(u.Role),
		"pin_hash":   u.PINHash,
		"active":     u.Active,
		"created_at": s.formatTime(u.CreatedAt, secondLayout),
		"updated_at": s.formatTime(u.UpdatedAt, secondLayout),
	}
}

// ---------- Cell helpers ----------

func (s *Store) formatTime(t time.Time, layout string) string {
	if t.IsZero() {
		return ""
	}
	return t.In(s.loc).Format(layout)
}

func (s *Store) parseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{secondLayout, minuteLayout} {
		if t, err := time.ParseInLocation(layout, v, s.loc); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.In(s.loc), nil
	}
	return time.Time{}, fmt.Errorf("format waktu %q tidak dikenal, pakai YYYY-MM-DD HH:MM", v)
}

func headerIndex(sheet string, rows [][]any, want []string) (map[string]int, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("tab %s kosong: jalankan setup sheet dulu", sheet)
	}
	idx := map[string]int{}
	for i, h := range rows[0] {
		idx[strings.ToLower(strings.TrimSpace(fmt.Sprint(h)))] = i
	}
	var missing []string
	for _, w := range want {
		if _, ok := idx[w]; !ok {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("tab %s: kolom hilang: %s (jalankan setup sheet)", sheet, strings.Join(missing, ", "))
	}
	return idx, nil
}

func cellString(row []any, i int) string {
	if i < 0 || i >= len(row) || row[i] == nil {
		return ""
	}
	switch v := row[i].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func cellInt(row []any, i int) (int64, error) {
	if i >= 0 && i < len(row) {
		if f, ok := row[i].(float64); ok {
			return int64(f), nil
		}
	}
	s := cellString(row, i)
	if s == "" {
		return 0, nil
	}
	// Accept "150.000" or "150,000" typed by hand; prices have no decimals.
	s = strings.NewReplacer(".", "", ",", "", "Rp", "", " ", "").Replace(s)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bukan angka: %q", cellString(row, i))
	}
	return n, nil
}

func cellBool(row []any, i int) bool {
	if i >= 0 && i < len(row) {
		if b, ok := row[i].(bool); ok {
			return b
		}
	}
	switch strings.ToLower(cellString(row, i)) {
	case "true", "yes", "ya", "1", "aktif", "y":
		return true
	}
	return false
}

// columnLetter converts a 0-based column index to A, B, ..., Z, AA, ...
func columnLetter(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func orMissing(err error) any {
	if err != nil {
		return err
	}
	return "kosong"
}

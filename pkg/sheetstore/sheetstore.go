// Package sheetstore stores rooms and bookings in a Google Spreadsheet.
//
// The spreadsheet has two tabs. Row 1 of each tab is the header; columns are
// found by header name, so staff may reorder columns but must not rename them.
//
//	Rooms:    id | name | rate_per_hour | active
//	Bookings: id | room_id | customer_name | phone | start | end | duration_minutes |
//	          status | rate_per_hour | total_price | notes | checked_in_at |
//	          checked_out_at | created_at | updated_at
//
// Times are written as text in the business time zone ("2006-01-02 15:04"),
// so the sheet is easy to read and does not depend on the sheet's locale.
package sheetstore

import (
	"context"
	"fmt"
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
)

// Column headers, in the order used when a tab is created.
var (
	RoomColumns    = []string{"id", "name", "rate_per_hour", "active"}
	BookingColumns = []string{
		"id", "room_id", "customer_name", "phone", "start", "end", "duration_minutes",
		"status", "rate_per_hour", "total_price", "notes", "checked_in_at",
		"checked_out_at", "created_at", "updated_at",
	}
)

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
}

// snapshot is one read of both tabs.
type snapshot struct {
	rooms       []booking.Room
	bookings    []booking.Booking
	bookingCols map[string]int   // header name -> column index
	bookingRow  map[string]int   // booking ID -> 1-based sheet row
	bookingRaw  map[string][]any // booking ID -> cells as read
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

func (s *Store) ListRooms(ctx context.Context) ([]booking.Room, error) {
	snap, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return append([]booking.Room(nil), snap.rooms...), nil
}

func (s *Store) ListBookings(ctx context.Context) ([]booking.Booking, error) {
	snap, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return append([]booking.Booking(nil), snap.bookings...), nil
}

func (s *Store) AddBooking(ctx context.Context, b booking.Booking) error {
	snap, err := s.load(booking.WithFreshRead(ctx))
	if err != nil {
		return err
	}
	row := s.bookingRow(b, snap.bookingCols, nil)
	_, err = s.api.Spreadsheets.Values.Append(s.spreadsheetID, BookingsSheet+"!A1",
		&sheets.ValueRange{Values: [][]any{row}}).
		ValueInputOption("RAW").InsertDataOption("INSERT_ROWS").Context(ctx).Do()
	s.invalidate()
	if err != nil {
		return fmt.Errorf("sheets append booking: %w", err)
	}
	return nil
}

func (s *Store) UpdateBooking(ctx context.Context, b booking.Booking) error {
	// Read again so a row added or deleted by hand does not shift the target row.
	snap, err := s.load(booking.WithFreshRead(ctx))
	if err != nil {
		return err
	}
	rowNum, ok := snap.bookingRow[b.ID]
	if !ok {
		return booking.ErrNotFound
	}
	row := s.bookingRow(b, snap.bookingCols, snap.bookingRaw[b.ID])
	rng := fmt.Sprintf("%s!A%d:%s%d", BookingsSheet, rowNum, columnLetter(len(row)-1), rowNum)
	_, err = s.api.Spreadsheets.Values.Update(s.spreadsheetID, rng,
		&sheets.ValueRange{Values: [][]any{row}}).
		ValueInputOption("RAW").Context(ctx).Do()
	s.invalidate()
	if err != nil {
		return fmt.Errorf("sheets update booking: %w", err)
	}
	return nil
}

// TabNames returns the tab titles in the spreadsheet. It only reads.
func (s *Store) TabNames(ctx context.Context) ([]string, error) {
	ss, err := s.api.Spreadsheets.Get(s.spreadsheetID).Fields("properties.title,sheets.properties.title").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("read spreadsheet: %w", err)
	}
	names := make([]string, 0, len(ss.Sheets))
	for _, sh := range ss.Sheets {
		names = append(names, sh.Properties.Title)
	}
	return names, nil
}

// EnsureSchema creates missing tabs and header rows. If the Rooms tab is new
// and seed is not empty, the seed rooms are added. Existing data is never changed.
func (s *Store) EnsureSchema(ctx context.Context, seed []booking.Room) error {
	ss, err := s.api.Spreadsheets.Get(s.spreadsheetID).Fields("sheets.properties.title").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("read spreadsheet: %w", err)
	}
	have := map[string]bool{}
	for _, sh := range ss.Sheets {
		have[sh.Properties.Title] = true
	}

	var add []*sheets.Request
	for _, name := range []string{RoomsSheet, BookingsSheet} {
		if !have[name] {
			add = append(add, &sheets.Request{AddSheet: &sheets.AddSheetRequest{
				Properties: &sheets.SheetProperties{Title: name},
			}})
		}
	}
	if len(add) > 0 {
		if _, err := s.api.Spreadsheets.BatchUpdate(s.spreadsheetID,
			&sheets.BatchUpdateSpreadsheetRequest{Requests: add}).Context(ctx).Do(); err != nil {
			return fmt.Errorf("add tabs: %w", err)
		}
	}

	if !have[BookingsSheet] {
		if err := s.writeRows(ctx, BookingsSheet, [][]any{toAny(BookingColumns)}); err != nil {
			return err
		}
	}
	if !have[RoomsSheet] {
		rows := [][]any{toAny(RoomColumns)}
		for _, r := range seed {
			rows = append(rows, []any{r.ID, r.Name, r.RatePerHour, r.Active})
		}
		if err := s.writeRows(ctx, RoomsSheet, rows); err != nil {
			return err
		}
	}
	s.invalidate()
	return nil
}

func (s *Store) writeRows(ctx context.Context, sheet string, rows [][]any) error {
	_, err := s.api.Spreadsheets.Values.Update(s.spreadsheetID, sheet+"!A1",
		&sheets.ValueRange{Values: rows}).ValueInputOption("RAW").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("write %s: %w", sheet, err)
	}
	return nil
}

func (s *Store) invalidate() {
	s.mu.Lock()
	s.snap = nil
	s.mu.Unlock()
}

// load reads both tabs in one API call, or returns the cached snapshot.
func (s *Store) load(ctx context.Context) (*snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap != nil && !booking.FreshRead(ctx) && time.Since(s.cachedAt) < cacheTTL {
		return s.snap, nil
	}

	resp, err := s.api.Spreadsheets.Values.BatchGet(s.spreadsheetID).
		Ranges(RoomsSheet, BookingsSheet).
		ValueRenderOption("UNFORMATTED_VALUE").
		DateTimeRenderOption("FORMATTED_STRING").
		Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("sheets read: %w", err)
	}
	if len(resp.ValueRanges) != 2 {
		return nil, fmt.Errorf("sheets read: expected 2 ranges, got %d", len(resp.ValueRanges))
	}
	snap, err := s.parse(resp.ValueRanges[0].Values, resp.ValueRanges[1].Values)
	if err != nil {
		return nil, err
	}
	s.snap, s.cachedAt = snap, time.Now()
	return snap, nil
}

func (s *Store) parse(roomRows, bookingRows [][]any) (*snapshot, error) {
	snap := &snapshot{bookingRow: map[string]int{}, bookingRaw: map[string][]any{}}

	rc, err := headerIndex(RoomsSheet, roomRows, RoomColumns)
	if err != nil {
		return nil, err
	}
	for _, row := range roomRows[1:] {
		id := cellString(row, rc["id"])
		if id == "" {
			continue
		}
		rate, err := cellInt(row, rc["rate_per_hour"])
		if err != nil {
			return nil, fmt.Errorf("%s room %s: rate_per_hour: %w", RoomsSheet, id, err)
		}
		snap.rooms = append(snap.rooms, booking.Room{
			ID:          id,
			Name:        cellString(row, rc["name"]),
			RatePerHour: rate,
			Active:      cellBool(row, rc["active"]),
		})
	}

	bc, err := headerIndex(BookingsSheet, bookingRows, BookingColumns)
	if err != nil {
		return nil, err
	}
	snap.bookingCols = bc
	for i, row := range bookingRows[1:] {
		id := cellString(row, bc["id"])
		if id == "" {
			continue
		}
		b, err := s.parseBooking(row, bc)
		if err != nil {
			return nil, fmt.Errorf("%s row %d (%s): %w", BookingsSheet, i+2, id, err)
		}
		snap.bookings = append(snap.bookings, b)
		snap.bookingRow[id] = i + 2 // +1 for header, +1 for 1-based rows
		snap.bookingRaw[id] = row
	}
	return snap, nil
}

func (s *Store) parseBooking(row []any, c map[string]int) (booking.Booking, error) {
	b := booking.Booking{
		ID:           cellString(row, c["id"]),
		RoomID:       cellString(row, c["room_id"]),
		CustomerName: cellString(row, c["customer_name"]),
		Phone:        cellString(row, c["phone"]),
		Status:       booking.Status(cellString(row, c["status"])),
		Notes:        cellString(row, c["notes"]),
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
	} {
		if *dst, err = s.parseTime(cellString(row, c[name])); err != nil {
			return b, fmt.Errorf("%s: %w", name, err)
		}
	}
	return b, nil
}

// bookingRow converts a booking to a row in the sheet's column order.
// Cells in columns this program does not know keep their existing value.
func (s *Store) bookingRow(b booking.Booking, cols map[string]int, existing []any) []any {
	values := map[string]any{
		"id":               b.ID,
		"room_id":          b.RoomID,
		"customer_name":    b.CustomerName,
		"phone":            b.Phone,
		"start":            s.formatTime(b.Start, minuteLayout),
		"end":              s.formatTime(b.End, minuteLayout),
		"duration_minutes": b.DurationMinutes(),
		"status":           string(b.Status),
		"rate_per_hour":    b.RatePerHour,
		"total_price":      b.TotalPrice,
		"notes":            b.Notes,
		"checked_in_at":    s.formatTime(b.CheckedInAt, secondLayout),
		"checked_out_at":   s.formatTime(b.CheckedOutAt, secondLayout),
		"created_at":       s.formatTime(b.CreatedAt, secondLayout),
		"updated_at":       s.formatTime(b.UpdatedAt, secondLayout),
	}
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
		return nil, fmt.Errorf("tab %s: kolom hilang: %s", sheet, strings.Join(missing, ", "))
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

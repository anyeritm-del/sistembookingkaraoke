package httpapi

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"karaoke/pkg/booking"
)

// CSV downloads for accounting. Files are UTF-8 with a byte order mark so
// Excel shows names correctly, comma separated.

func (s *Server) exportBookings(w http.ResponseWriter, r *http.Request, u booking.User) {
	q := r.URL.Query()
	from, ok := s.parseDate(w, q.Get("from"), s.svc.Now())
	if !ok {
		return
	}
	to, ok := s.parseDate(w, q.Get("to"), from.AddDate(0, 0, 30))
	if !ok {
		return
	}
	list, err := s.svc.ExportBookings(r.Context(), u, booking.ListFilter{
		From: from, To: to, Status: q.Get("status"), Query: q.Get("q"), RoomID: q.Get("room"),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	rooms := s.roomNames(r)
	loc := s.svc.Location()
	ts := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.In(loc).Format("2006-01-02 15:04")
	}
	rows := [][]string{{
		"id", "tanggal", "mulai", "selesai", "durasi_menit", "kode_room", "room", "tamu", "hp",
		"status", "harga_per_jam", "total", "catatan", "dibuat_oleh", "dikonfirmasi_oleh",
		"checkin_oleh", "checkout_oleh", "dibatalkan_oleh", "checkin_at", "checkout_at", "dibuat_at",
		"compliment", "alasan_compliment", "nilai_normal", "menit_pakai", "menit_ditagih", "voucher",
	}}
	for _, b := range list {
		rows = append(rows, []string{
			b.ID, b.Start.In(loc).Format(time.DateOnly), b.Start.In(loc).Format("15:04"), b.End.In(loc).Format("15:04"),
			strconv.Itoa(b.DurationMinutes()), b.RoomID, rooms[b.RoomID], b.CustomerName, b.Phone,
			statusLabel(b), strconv.FormatInt(b.RatePerHour, 10), strconv.FormatInt(b.TotalPrice, 10), b.Notes,
			b.CreatedBy, b.ConfirmedBy, b.CheckedInBy, b.CheckedOutBy, b.CancelledBy,
			ts(b.CheckedInAt), ts(b.CheckedOutAt), ts(b.CreatedAt),
			yaTidak(b.Complimentary), b.ComplimentReason, strconv.FormatInt(b.NormalPrice(), 10),
			strconv.Itoa(b.UsedMinutes()), strconv.Itoa(b.ChargedMinutes()), b.VoucherNumber,
		})
	}
	name := fmt.Sprintf("booking_%s_%s.csv", from.Format("20060102"), to.Format("20060102"))
	writeCSV(w, name, rows)
}

func (s *Server) exportReport(w http.ResponseWriter, r *http.Request, u booking.User) {
	from, to, ok := s.reportRange(w, r)
	if !ok {
		return
	}
	rep, err := s.svc.ExportReport(r.Context(), u, from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	period := rep.From + " s/d " + rep.To
	itoa, ftoa := strconv.Itoa, func(n int64) string { return strconv.FormatInt(n, 10) }
	rows := [][]string{
		{"laporan", period},
		{},
		{"per room"},
		{"kode_room", "room", "booking", "menit", "pendapatan"},
	}
	for _, rr := range rep.Rooms {
		rows = append(rows, []string{rr.RoomID, rr.RoomName, itoa(rr.Bookings), itoa(rr.Minutes), ftoa(rr.Revenue)})
	}
	rows = append(rows, []string{"", "TOTAL", itoa(rep.Finished + rep.CheckedIn), itoa(rep.Minutes), ftoa(rep.Revenue)},
		[]string{}, []string{"per hari"}, []string{"tanggal", "booking", "menit", "pendapatan"})
	for _, d := range rep.Days {
		rows = append(rows, []string{d.Date, itoa(d.Bookings), itoa(d.Minutes), ftoa(d.Revenue)})
	}
	rows = append(rows,
		[]string{},
		[]string{"selesai", itoa(rep.Finished)},
		[]string{"sedang check-in", itoa(rep.CheckedIn)},
		[]string{"confirm belum check-in", itoa(rep.Booked)},
		[]string{"tentative", itoa(rep.Tentative)},
		[]string{"batal", itoa(rep.Cancelled)},
		[]string{"compliment (booking)", itoa(rep.Compliments)},
		[]string{"compliment (menit)", itoa(rep.ComplimentMinutes)},
		[]string{"compliment (nilai normal)", ftoa(rep.ComplimentValue)},
	)
	name := "laporan_" + strings.ReplaceAll(rep.From, "-", "")
	if rep.To != rep.From {
		name += "_" + strings.ReplaceAll(rep.To, "-", "")
	}
	writeCSV(w, name+".csv", rows)
}

func (s *Server) roomNames(r *http.Request) map[string]string {
	names := map[string]string{}
	if rooms, err := s.svc.Rooms(r.Context()); err == nil {
		for _, rm := range rooms {
			names[rm.ID] = rm.Name
		}
	}
	return names
}

func yaTidak(v bool) string {
	if v {
		return "ya"
	}
	return "tidak"
}

func statusLabel(b booking.Booking) string {
	switch b.Status {
	case booking.StatusBooked:
		return "confirm"
	case booking.StatusCheckedIn:
		return "check-in"
	case booking.StatusFinished:
		return "selesai"
	case booking.StatusCancelled:
		return "cancel"
	}
	return string(b.Status)
}

func writeCSV(w http.ResponseWriter, filename string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Write([]byte("\xEF\xBB\xBF")) // UTF-8 BOM for Excel
	cw := csv.NewWriter(w)
	for _, row := range rows {
		for i, cell := range row {
			row[i] = safeCell(cell)
		}
		cw.Write(row)
	}
	cw.Flush()
}

// safeCell stops spreadsheet apps from running a guest name such as
// "=HYPERLINK(...)" as a formula (CSV injection).
func safeCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

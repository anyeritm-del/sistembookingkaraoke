package booking

import (
	"context"
	"fmt"
	"time"
)

// ExportBookings returns the bookings for a download and records who
// downloaded what. Downloads leave the system, so they are audited.
func (s *Service) ExportBookings(ctx context.Context, actor User, f ListFilter) ([]Booking, error) {
	if !actor.Can(PermExport) {
		return nil, ErrForbidden
	}
	list, _, err := s.List(ctx, actor, f)
	if err != nil {
		return nil, err
	}
	status := f.Status
	if status == "" {
		status = "semua"
	}
	s.audit(ctx, actor, ActExport, "", "", fmt.Sprintf("daftar booking %s s/d %s, status %s, %d baris",
		f.From.In(s.loc).Format(time.DateOnly), f.To.In(s.loc).Format(time.DateOnly), status, len(list)))
	return list, nil
}

// ExportReport returns the daily report for a download and records it.
func (s *Service) ExportReport(ctx context.Context, actor User, day time.Time) (DailyReport, error) {
	if !actor.Can(PermExport) {
		return DailyReport{}, ErrForbidden
	}
	rep, err := s.Report(ctx, actor, day)
	if err != nil {
		return DailyReport{}, err
	}
	s.audit(ctx, actor, ActExport, "", "", "laporan harian "+rep.Date)
	return rep, nil
}

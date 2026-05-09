package reports

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"time"

	commandsv1 "medsage/proto/medsage/commands/v1"
	natsbus "medsage/report-service/nats"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pollInterval is how often the scheduler checks for due subscriptions.
// One minute is fine even for "hourly" cadences in the future; fast enough
// that no human notices the lag, slow enough that the DB load is trivial.
const pollInterval = time.Minute

// Scheduler delivers recurring reports by reading the report_subscriptions
// table, generating reports, and publishing SendEmail commands. It does not
// send email itself — that's the notifications-service consumer. If
// notifications-service is down, JetStream queues the command until it
// returns.
type Scheduler struct {
	pool      *pgxpool.Pool
	store     *Store
	publisher *natsbus.Publisher
}

func NewScheduler(pool *pgxpool.Pool, store *Store, publisher *natsbus.Publisher) *Scheduler {
	return &Scheduler{pool: pool, store: store, publisher: publisher}
}

func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()

	// Run an immediate tick on startup so a freshly-due subscription doesn't
	// wait a full poll interval after a deploy.
	s.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

type dueSubscription struct {
	ID             uuid.UUID
	DeviceID       uuid.UUID
	RecipientEmail string
	ReportType     string
	Cadence        string
}

func (s *Scheduler) tick(ctx context.Context) {
	const query = `
		SELECT id, device_id, recipient_email, report_type::text, cadence::text
		FROM report_subscriptions
		WHERE status = 'active' AND next_run_at <= NOW()
		ORDER BY next_run_at ASC
		LIMIT 100
	`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		slog.Error("Scheduler tick query failed", "error", err)
		return
	}
	defer rows.Close()

	var due []dueSubscription
	for rows.Next() {
		var sub dueSubscription
		if err := rows.Scan(&sub.ID, &sub.DeviceID, &sub.RecipientEmail, &sub.ReportType, &sub.Cadence); err != nil {
			slog.Error("Scheduler scan failed", "error", err)
			continue
		}
		due = append(due, sub)
	}

	for _, sub := range due {
		if err := s.process(ctx, sub); err != nil {
			slog.Error("Scheduler failed to process subscription",
				"subscription_id", sub.ID,
				"device_id", sub.DeviceID,
				"error", err,
			)
			// Leave next_run_at unchanged so the next tick retries.
			continue
		}

		if err := s.advance(ctx, sub); err != nil {
			slog.Error("Scheduler failed to advance subscription",
				"subscription_id", sub.ID,
				"error", err,
			)
		}
	}
}

func (s *Scheduler) process(ctx context.Context, sub dueSubscription) error {
	from, to := windowFor(sub.Cadence, time.Now().UTC())

	switch sub.ReportType {
	case "adherence":
		return s.processAdherence(ctx, sub, from, to)
	default:
		return fmt.Errorf("unsupported report_type %q", sub.ReportType)
	}
}

func (s *Scheduler) processAdherence(ctx context.Context, sub dueSubscription, from, to time.Time) error {
	report, err := s.store.GetAdherenceReport(ctx, sub.DeviceID, from, to)
	if err != nil {
		return fmt.Errorf("get adherence report: %w", err)
	}

	html, err := renderAdherenceHTML(report, sub.Cadence)
	if err != nil {
		return fmt.Errorf("render html: %w", err)
	}

	var csvBuf bytes.Buffer
	if err := WriteAdherenceCSV(&csvBuf, report); err != nil {
		return fmt.Errorf("render csv: %w", err)
	}
	filename := fmt.Sprintf("adherence_%s_%s_%s.csv",
		sub.DeviceID,
		from.Format("20060102"),
		to.AddDate(0, 0, -1).Format("20060102"),
	)

	cmd := &commandsv1.SendEmail{
		CommandId: uuid.NewString(),
		To:        sub.RecipientEmail,
		Subject:   fmt.Sprintf("Medsage %s adherence report", sub.Cadence),
		BodyHtml:  html,
		Source:    "report-service",
		SourceRef: sub.ID.String(),
		Attachments: []*commandsv1.EmailAttachment{
			{
				Filename:    filename,
				ContentType: "text/csv",
				Content:     csvBuf.Bytes(),
			},
		},
	}
	if err := s.publisher.PublishSendEmail(ctx, cmd); err != nil {
		return fmt.Errorf("publish SendEmail: %w", err)
	}

	slog.Info("Adherence report queued for delivery",
		"subscription_id", sub.ID,
		"device_id", sub.DeviceID,
		"to", sub.RecipientEmail,
		"command_id", cmd.CommandId,
	)
	return nil
}

func (s *Scheduler) advance(ctx context.Context, sub dueSubscription) error {
	interval, err := pgIntervalFor(sub.Cadence)
	if err != nil {
		return err
	}
	const query = `
		UPDATE report_subscriptions
		SET last_run_at = NOW(),
		    next_run_at = next_run_at + $2::interval,
		    updated_at  = NOW()
		WHERE id = $1
	`
	_, err = s.pool.Exec(ctx, query, sub.ID, interval)
	return err
}

// windowFor returns the [from, to) UTC range that a cadence covers, ending
// at the given "now". Rolling windows (last N days) are intentional — they
// match what a user expects from "weekly report sent every Monday" without
// needing per-recipient timezone math.
func windowFor(cadence string, now time.Time) (time.Time, time.Time) {
	switch cadence {
	case "weekly":
		return now.AddDate(0, 0, -7), now
	case "monthly":
		return now.AddDate(0, -1, 0), now
	default:
		return now.AddDate(0, 0, -7), now
	}
}

func pgIntervalFor(cadence string) (string, error) {
	switch cadence {
	case "weekly":
		return "7 days", nil
	case "monthly":
		return "1 month", nil
	default:
		return "", fmt.Errorf("unknown cadence %q", cadence)
	}
}

const adherenceEmailTemplate = `<!DOCTYPE html>
<html>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif; max-width: 640px; margin: 0 auto; padding: 24px; color: #1f2937;">
  <h2 style="margin-top: 0;">Medsage {{.Cadence}} adherence report</h2>
  <p>Reporting period: <strong>{{.FromLabel}} – {{.ToLabel}}</strong></p>

  <div style="background: #f3f4f6; border-radius: 8px; padding: 16px; margin: 16px 0;">
    <div style="font-size: 32px; font-weight: 600;">{{.AdherencePct}}</div>
    <div style="color: #6b7280;">overall adherence</div>
    <div style="margin-top: 12px; font-size: 14px;">
      Dispensed: <strong>{{.Report.TotalDispensed}}</strong> &nbsp;·&nbsp;
      Missed: <strong>{{.Report.TotalMissed}}</strong> &nbsp;·&nbsp;
      Confirmed: <strong>{{.Report.TotalConfirmed}}</strong>
    </div>
  </div>

  {{if .Report.Daily}}
  <h3>Daily breakdown</h3>
  <table style="border-collapse: collapse; width: 100%;">
    <thead>
      <tr style="text-align: left; border-bottom: 1px solid #e5e7eb;">
        <th style="padding: 8px;">Date</th>
        <th style="padding: 8px;">Dispensed</th>
        <th style="padding: 8px;">Missed</th>
        <th style="padding: 8px;">Confirmed</th>
      </tr>
    </thead>
    <tbody>
      {{range .Report.Daily}}
      <tr style="border-bottom: 1px solid #f3f4f6;">
        <td style="padding: 8px;">{{.Date}}</td>
        <td style="padding: 8px;">{{.Dispensed}}</td>
        <td style="padding: 8px;">{{.Missed}}</td>
        <td style="padding: 8px;">{{.Confirmed}}</td>
      </tr>
      {{end}}
    </tbody>
  </table>
  {{else}}
  <p style="color: #6b7280;">No medication events recorded in this period.</p>
  {{end}}

  <p style="margin-top: 32px; font-size: 12px; color: #9ca3af;">
    A CSV copy of this report is attached. Replies to this email aren't monitored.
  </p>
</body>
</html>`

var adherenceTmpl = template.Must(template.New("adherence_email").Parse(adherenceEmailTemplate))

func renderAdherenceHTML(report *AdherenceReport, cadence string) (string, error) {
	data := struct {
		Report       *AdherenceReport
		Cadence      string
		FromLabel    string
		ToLabel      string
		AdherencePct string
	}{
		Report:       report,
		Cadence:      cadence,
		FromLabel:    report.From.Format("Jan 2, 2006"),
		ToLabel:      report.To.AddDate(0, 0, -1).Format("Jan 2, 2006"),
		AdherencePct: fmt.Sprintf("%.0f%%", report.AdherenceRate*100),
	}

	var buf bytes.Buffer
	if err := adherenceTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

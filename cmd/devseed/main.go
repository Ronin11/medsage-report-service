// devseed builds a report you can actually look at.
//
// Reports read the daily_adherence rollup, which is normally filled by the
// streaming projector as MQTT events arrive — so on a dev box with no hardware
// there is nothing to report on, and the whole pipeline is invisible. This
// seeds plausible medication events, drives the *real* reconciler to roll them
// up, then renders the report three ways: the JSON the API serves, the CSV that
// gets attached to the email, and the HTML body a recipient actually receives.
//
//	go run ./cmd/devseed -device <uuid> -days 14 -adherence 0.85
//
// Deliberately writes events rather than rollup rows: reconciler.ReconcileDay
// recomputes daily_adherence from events, so this exercises the same code that
// heals drift in production instead of a shortcut that would hide bugs in it.
//
// Dev only. It writes to whatever database DATABASE_URL points at, and it
// deletes its own seeded events on rerun (matched on metadata.devseed) so the
// numbers do not accumulate across runs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"medsage/report-service/reports"
)

func main() {
	var (
		deviceFlag = flag.String("device", "", "device UUID to seed (required)")
		days       = flag.Int("days", 14, "days of history to generate, ending yesterday")
		adherence  = flag.Float64("adherence", 0.85, "roughly what fraction of doses are taken")
		dosesLow   = flag.Int("doses-min", 2, "minimum scheduled doses per day")
		dosesHigh  = flag.Int("doses-max", 3, "maximum scheduled doses per day")
		outDir     = flag.String("out", "tmp/reports", "directory for the rendered report")
		seed       = flag.Int64("seed", 1, "RNG seed, so a run is reproducible")
		keep       = flag.Bool("keep", false, "keep previously seeded events instead of replacing them")
	)
	flag.Parse()

	if *deviceFlag == "" {
		fail("-device is required (a UUID from the devices table)")
	}
	deviceID, err := uuid.Parse(*deviceFlag)
	if err != nil {
		fail("-device must be a UUID: %v", err)
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fail("DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fail("connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fail("ping: %v", err)
	}

	// The rollup has a foreign key to devices, so a typo'd UUID would other-
	// wise surface as an opaque constraint error after all the work.
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM devices WHERE id = $1)`, deviceID).Scan(&exists); err != nil {
		fail("device lookup: %v", err)
	}
	if !exists {
		fail("device %s is not in the devices table", deviceID)
	}

	// End yesterday: today is still accumulating, and a partial final day makes
	// a report look wrong for reasons that have nothing to do with the report.
	end := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -(*days - 1))

	if !*keep {
		tag, err := pool.Exec(ctx,
			`DELETE FROM events WHERE stream_id = $1 AND metadata ? 'devseed'`, deviceID)
		if err != nil {
			fail("clear previous seed: %v", err)
		}
		if n := tag.RowsAffected(); n > 0 {
			fmt.Printf("cleared %d previously seeded events\n", n)
		}
	}

	rng := rand.New(rand.NewSource(*seed))
	inserted := 0
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		doses := *dosesLow
		if *dosesHigh > *dosesLow {
			doses += rng.Intn(*dosesHigh - *dosesLow + 1)
		}
		for d := 0; d < doses; d++ {
			// Morning and evening-ish, so the CSV reads like a real schedule.
			at := day.Add(time.Duration(8+d*6)*time.Hour + time.Duration(rng.Intn(30))*time.Minute)

			// A dose is dispensed, then either confirmed or missed. Missing a
			// dose still dispenses it — the pill was released, nobody took it.
			insert(ctx, pool, deviceID, "medication_dispensed", at, map[string]any{"hour": at.Hour()})
			inserted++
			if rng.Float64() < *adherence {
				insert(ctx, pool, deviceID, "medication_confirmed", at.Add(3*time.Minute),
					map[string]any{"hour": at.Hour(), "delay_secs": 60 + rng.Intn(600)})
			} else {
				insert(ctx, pool, deviceID, "medication_missed", at.Add(10*time.Minute),
					map[string]any{"hour": at.Hour(), "minute": at.Minute(), "timeout_secs": 600})
			}
			inserted++
		}

		// The real reconciler, not a local copy of its SQL.
		if err := reports.NewReconciler(pool).ReconcileDay(ctx, day.Format("2006-01-02")); err != nil {
			fail("reconcile %s: %v", day.Format("2006-01-02"), err)
		}
	}
	fmt.Printf("seeded %d events across %d days for %s\n", inserted, *days, deviceID)

	// Read it back the way the API does: half-open range, so `to` is the day
	// after the last day we want included.
	report, err := reports.NewStore(pool).GetAdherenceReport(ctx, deviceID, start, end.AddDate(0, 0, 1))
	if err != nil {
		fail("get report: %v", err)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail("mkdir %s: %v", *outDir, err)
	}
	writeJSON(filepath.Join(*outDir, "adherence.json"), report)
	writeCSV(filepath.Join(*outDir, "adherence.csv"), report)
	writeHTML(filepath.Join(*outDir, "adherence-email.html"), report)

	fmt.Printf("\n  %-10s %9s %7s %10s\n", "DATE", "DISPENSED", "MISSED", "CONFIRMED")
	for _, row := range report.Daily {
		fmt.Printf("  %-10s %9d %7d %10d\n", row.Date, row.Dispensed, row.Missed, row.Confirmed)
	}
	fmt.Printf("\n  adherence: %.1f%%  (%d dispensed, %d missed, %d confirmed)\n",
		report.AdherenceRate*100, report.TotalDispensed, report.TotalMissed, report.TotalConfirmed)
	fmt.Printf("\nwrote %s/{adherence.json,adherence.csv,adherence-email.html}\n", *outDir)
	fmt.Printf("open the last one in a browser to see what a subscriber receives\n")
}

func insert(ctx context.Context, pool *pgxpool.Pool, deviceID uuid.UUID, eventType string, at time.Time, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		fail("marshal payload: %v", err)
	}
	// metadata.devseed is what makes a rerun idempotent, and marks these rows
	// as synthetic for anyone who finds them later.
	_, err = pool.Exec(ctx, `
		INSERT INTO events (id, stream_id, stream_type, event_type, event_version, payload, metadata, created_at)
		VALUES ($1, $2, 'Device', $3::event_type, 1, $4, '{"devseed": true, "fw_version": "0.0.0-devseed"}', $5)`,
		uuid.New(), deviceID, eventType, body, at)
	if err != nil {
		fail("insert %s: %v", eventType, err)
	}
}

func writeJSON(path string, report *reports.AdherenceReport) {
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fail("marshal report: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		fail("write %s: %v", path, err)
	}
}

func writeCSV(path string, report *reports.AdherenceReport) {
	f, err := os.Create(path)
	if err != nil {
		fail("create %s: %v", path, err)
	}
	defer f.Close()
	if err := reports.WriteAdherenceCSV(f, report); err != nil {
		fail("write csv: %v", err)
	}
}

func writeHTML(path string, report *reports.AdherenceReport) {
	// The same renderer the scheduler uses, so this is the actual email body
	// rather than a lookalike that can drift from it.
	html, err := reports.RenderAdherenceHTML(report, "weekly")
	if err != nil {
		fail("render html: %v", err)
	}
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		fail("write %s: %v", path, err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "devseed: "+format+"\n", args...)
	os.Exit(1)
}

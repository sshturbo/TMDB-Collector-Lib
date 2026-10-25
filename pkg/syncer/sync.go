// Package syncer applies TMDB's incremental change lists to the SQLite store.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sshturbo/TMDB-Collector-Lib/pkg/api"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/database"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

// Options selects a bounded date interval. A zero EndDate means today in UTC.
// A zero StartDate resumes from the saved checkpoint, or starts 14 days back.
type Options struct {
	MediaType         models.MediaType
	StartDate         time.Time
	EndDate           time.Time
	MaxPagesPerWindow int
}

// Failure identifies the window/page/item that prevented checkpoint advancement.
type Failure struct {
	StartDate string
	EndDate   string
	Page      int
	TMDBID    int
	Err       error
}

// Report separates changes observed from metadata successfully stored.
type Report struct {
	MediaType         models.MediaType
	StartDate         time.Time
	EndDate           time.Time
	CompletedThrough  time.Time
	IDsReceived       int
	ItemsUpdated      int
	DuplicatesSkipped int
	PagesProcessed    int
	WindowsCompleted  int
	Failures          []Failure
}

// SyncChangesContext fetches changed IDs in <=14-day windows, refreshes only those
// records, and advances the persisted checkpoint only after each full window commits.
// A failed window can be safely rerun because item upserts are idempotent.
func SyncChangesContext(ctx context.Context, client *api.TMDBClient, store *database.Database, options Options) (Report, error) {
	report := Report{MediaType: options.MediaType, Failures: []Failure{}}
	if client == nil || store == nil {
		return report, errors.New("TMDB client and database are required")
	}
	if options.MediaType != models.MediaTypeMovie && options.MediaType != models.MediaTypeTV {
		return report, fmt.Errorf("unsupported media type %q", options.MediaType)
	}
	if options.MaxPagesPerWindow < 0 {
		return report, errors.New("MaxPagesPerWindow must not be negative")
	}
	if options.MaxPagesPerWindow > api.MaxChangePages {
		return report, fmt.Errorf("MaxPagesPerWindow cannot exceed %d", api.MaxChangePages)
	}
	end := utcDate(options.EndDate)
	if options.EndDate.IsZero() {
		end = utcDate(time.Now())
	}
	if end.After(utcDate(time.Now())) {
		return report, errors.New("EndDate cannot be in the future")
	}
	start := utcDate(options.StartDate)
	if options.StartDate.IsZero() {
		checkpoint, exists, err := store.GetSyncCheckpoint(options.MediaType)
		if err != nil {
			return report, err
		}
		if exists {
			start, err = time.Parse("2006-01-02", checkpoint.CompletedThrough)
			if err != nil {
				return report, fmt.Errorf("parse stored checkpoint: %w", err)
			}
		} else {
			start = end.AddDate(0, 0, -(api.MaxChangeWindowDays - 1))
		}
	}
	if end.Before(start) {
		return report, errors.New("EndDate precedes StartDate")
	}
	report.StartDate, report.EndDate = start, end

	for !start.After(end) {
		windowEnd := start.AddDate(0, 0, api.MaxChangeWindowDays-1)
		if windowEnd.After(end) {
			windowEnd = end
		}
		windowStartString, windowEndString := dateString(start), dateString(windowEnd)
		if err := syncWindow(ctx, client, store, options.MediaType, start, windowEnd, options.MaxPagesPerWindow, &report); err != nil {
			report.Failures = append(report.Failures, Failure{StartDate: windowStartString, EndDate: windowEndString, Err: err})
			return report, err
		}
		if err := store.SaveSyncCheckpoint(database.SyncCheckpoint{MediaType: options.MediaType, CompletedThrough: windowEndString}); err != nil {
			report.Failures = append(report.Failures, Failure{StartDate: windowStartString, EndDate: windowEndString, Err: err})
			return report, fmt.Errorf("save %s checkpoint through %s: %w", options.MediaType, windowEndString, err)
		}
		report.WindowsCompleted++
		report.CompletedThrough = windowEnd
		if !windowEnd.Before(end) {
			break
		}
		// Revisit the boundary date on the next window/run: TMDB accepts dates,
		// not a time-of-day cursor, so the overlap prevents missing same-day edits.
		start = windowEnd
	}
	return report, nil
}

func syncWindow(ctx context.Context, client *api.TMDBClient, store *database.Database, mediaType models.MediaType, startDate, endDate time.Time, maxPages int, report *Report) error {
	seen := make(map[int]bool)
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var changes api.ChangedIDsPage
		var err error
		if mediaType == models.MediaTypeMovie {
			changes, err = client.GetMovieChangesContext(ctx, startDate, endDate, page)
		} else {
			changes, err = client.GetTVShowChangesContext(ctx, startDate, endDate, page)
		}
		report.PagesProcessed++
		if err != nil {
			report.Failures = append(report.Failures, Failure{StartDate: dateString(startDate), EndDate: dateString(endDate), Page: page, Err: err})
			return fmt.Errorf("fetch %s changes page %d: %w", mediaType, page, err)
		}
		report.IDsReceived += len(changes.IDs)
		ids := make([]int, 0, len(changes.IDs))
		for _, id := range changes.IDs {
			if seen[id] {
				report.DuplicatesSkipped++
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			if err := refreshChangedIDs(ctx, client, store, mediaType, ids, startDate, endDate, page, report); err != nil {
				return err
			}
		}
		if changes.TotalPages <= page || len(changes.IDs) == 0 {
			return nil
		}
		if maxPages > 0 && page >= maxPages {
			return fmt.Errorf("change interval has more than the configured %d pages", maxPages)
		}
	}
}

func refreshChangedIDs(ctx context.Context, client *api.TMDBClient, store *database.Database, mediaType models.MediaType, ids []int, startDate, endDate time.Time, page int, report *Report) error {
	if mediaType == models.MediaTypeMovie {
		movies := make([]models.Movie, 0, len(ids))
		for _, id := range ids {
			movie, err := client.GetMovieDetailsFreshContext(ctx, id)
			if err != nil {
				report.Failures = append(report.Failures, Failure{StartDate: dateString(startDate), EndDate: dateString(endDate), Page: page, TMDBID: id, Err: err})
				return fmt.Errorf("refresh movie %d: %w", id, err)
			}
			movies = append(movies, movie)
		}
		if err := store.SaveMoviesBulk(movies); err != nil {
			report.Failures = append(report.Failures, Failure{StartDate: dateString(startDate), EndDate: dateString(endDate), Page: page, Err: err})
			return fmt.Errorf("save movies from changes page %d: %w", page, err)
		}
		report.ItemsUpdated += len(movies)
		return nil
	}
	shows := make([]models.TVShow, 0, len(ids))
	for _, id := range ids {
		show, err := client.GetTVShowDetailsFreshContext(ctx, id)
		if err != nil {
			report.Failures = append(report.Failures, Failure{StartDate: dateString(startDate), EndDate: dateString(endDate), Page: page, TMDBID: id, Err: err})
			return fmt.Errorf("refresh TV show %d: %w", id, err)
		}
		shows = append(shows, show)
	}
	if err := store.SaveTVShowsBulk(shows); err != nil {
		report.Failures = append(report.Failures, Failure{StartDate: dateString(startDate), EndDate: dateString(endDate), Page: page, Err: err})
		return fmt.Errorf("save TV shows from changes page %d: %w", page, err)
	}
	report.ItemsUpdated += len(shows)
	return nil
}

func dateString(value time.Time) string { return utcDate(value).Format("2006-01-02") }

func utcDate(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	year, month, day := value.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

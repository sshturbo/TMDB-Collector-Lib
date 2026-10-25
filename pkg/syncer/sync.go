// Package syncer applies TMDB's incremental change lists to the SQLite store.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/api"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/database"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

// MissingRecordPolicy controls what happens when a changed ID's details return HTTP 404.
type MissingRecordPolicy string

const (
	// MissingRecordFail leaves the interval incomplete and retries it on the next run.
	MissingRecordFail MissingRecordPolicy = "fail"
	// MissingRecordSkip records the missing ID and permits checkpoint advancement without
	// deleting the local record. Callers can reprocess it later with an explicit StartDate.
	MissingRecordSkip MissingRecordPolicy = "skip"
)

// Options selects a bounded date interval. A zero EndDate means today in UTC.
// A zero StartDate resumes from the saved checkpoint, or starts 14 days back.
type Options struct {
	MediaType           models.MediaType
	StartDate           time.Time
	EndDate             time.Time
	MaxPagesPerWindow   int
	MissingRecordPolicy MissingRecordPolicy
}

// FailureStage identifies the stage that prevented a window from completing.
type FailureStage string

const (
	FailureStageLock       FailureStage = "sync_lock"
	FailureStageCheckpoint FailureStage = "checkpoint"
	FailureStageChanges    FailureStage = "changes_request"
	FailureStagePagination FailureStage = "pagination"
	FailureStageDetails    FailureStage = "details_request"
	FailureStagePersist    FailureStage = "persistence"
)

// Failure identifies one unresolved occurrence that prevented checkpoint advancement.
type Failure struct {
	StartDate  string
	EndDate    string
	Stage      FailureStage
	Page       int
	TMDBID     int
	HTTPStatus int
	Retryable  bool
	Err        error
}

// UnavailableRecord records a details lookup that returned HTTP 404. The local
// record is retained regardless of the selected MissingRecordPolicy.
type UnavailableRecord struct {
	StartDate  string
	EndDate    string
	Page       int
	TMDBID     int
	HTTPStatus int
	Reason     string
}

// PaginationError describes inconsistent or incomplete metadata from a changes page.
type PaginationError struct {
	Page   int
	Reason string
}

func (e *PaginationError) Error() string {
	return fmt.Sprintf("inconsistent TMDB changes pagination at page %d: %s", e.Page, e.Reason)
}

// Report separates successfully updated items, explicitly skipped 404 records,
// and failures that kept a changes window from being checkpointed.
type Report struct {
	MediaType          models.MediaType
	StartDate          time.Time
	EndDate            time.Time
	CompletedThrough   time.Time
	IDsReceived        int
	ItemsUpdated       int
	DuplicatesSkipped  int
	PagesProcessed     int
	WindowsCompleted   int
	UnavailableRecords []UnavailableRecord
	IgnoredUnavailable []UnavailableRecord
	Failures           []Failure
}

type operationFailure struct {
	failure Failure
	cause   error
}

func (e *operationFailure) Error() string {
	return fmt.Sprintf("%s: %v", e.failure.Stage, e.cause)
}

func (e *operationFailure) Unwrap() error { return e.cause }

// SyncChangesContext fetches changed IDs in <=14-day windows, refreshes only those
// records, and advances a per-media checkpoint only after each window is complete.
// Same-store synchronizations of one media type are serialized. A 404 blocks progress
// by default; MissingRecordSkip is an explicit, non-destructive resolution policy.
func SyncChangesContext(ctx context.Context, client *api.TMDBClient, store *database.Database, options Options) (Report, error) {
	report := Report{
		MediaType: options.MediaType, Failures: []Failure{},
		UnavailableRecords: []UnavailableRecord{}, IgnoredUnavailable: []UnavailableRecord{},
	}
	if ctx == nil {
		return report, errors.New("context is required")
	}
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
	if options.MissingRecordPolicy == "" {
		options.MissingRecordPolicy = MissingRecordFail
	}
	if options.MissingRecordPolicy != MissingRecordFail && options.MissingRecordPolicy != MissingRecordSkip {
		return report, fmt.Errorf("unsupported missing-record policy %q", options.MissingRecordPolicy)
	}
	end := utcDate(options.EndDate)
	if options.EndDate.IsZero() {
		end = utcDate(time.Now())
	}
	if end.After(utcDate(time.Now())) {
		return report, errors.New("EndDate cannot be in the future")
	}

	// Lock before reading the checkpoint so two goroutines sharing this Database
	// cannot race their starting cursor or advance it past an unfinished window.
	unlock, err := store.AcquireSyncLock(ctx, options.MediaType)
	if err != nil {
		recordFailure(&report, Failure{Stage: FailureStageLock, Err: err, Retryable: isRetryable(err)})
		return report, err
	}
	defer unlock()

	start := utcDate(options.StartDate)
	if options.StartDate.IsZero() {
		checkpoint, exists, err := store.GetSyncCheckpointContext(ctx, options.MediaType)
		if err != nil {
			failure := Failure{Stage: FailureStageCheckpoint, Err: err, Retryable: isRetryable(err)}
			recordFailure(&report, failure)
			return report, &operationFailure{failure: failure, cause: err}
		}
		if exists {
			start, err = time.Parse("2006-01-02", checkpoint.CompletedThrough)
			if err != nil {
				failure := Failure{Stage: FailureStageCheckpoint, Err: err}
				recordFailure(&report, failure)
				return report, &operationFailure{failure: failure, cause: err}
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
		if err := syncWindow(ctx, client, store, options.MediaType, start, windowEnd, options.MaxPagesPerWindow, options.MissingRecordPolicy, &report); err != nil {
			var failure *operationFailure
			if errors.As(err, &failure) {
				recordFailure(&report, failure.failure)
			} else {
				f := Failure{StartDate: windowStartString, EndDate: windowEndString, Stage: FailureStageChanges, Err: err, Retryable: isRetryable(err)}
				recordFailure(&report, f)
			}
			return report, err
		}
		if err := ctx.Err(); err != nil {
			failure := Failure{StartDate: windowStartString, EndDate: windowEndString, Stage: FailureStageCheckpoint, Err: err, Retryable: true}
			recordFailure(&report, failure)
			return report, &operationFailure{failure: failure, cause: err}
		}
		if err := store.SaveSyncCheckpointContext(ctx, database.SyncCheckpoint{MediaType: options.MediaType, CompletedThrough: windowEndString}); err != nil {
			failure := Failure{StartDate: windowStartString, EndDate: windowEndString, Stage: FailureStageCheckpoint, Err: err, Retryable: isRetryable(err)}
			recordFailure(&report, failure)
			return report, &operationFailure{failure: failure, cause: err}
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

func syncWindow(ctx context.Context, client *api.TMDBClient, store *database.Database, mediaType models.MediaType, startDate, endDate time.Time, maxPages int, missingPolicy MissingRecordPolicy, report *Report) error {
	seen := make(map[int]bool)
	expectedPages, expectedResults, receivedResults := -1, -1, 0
	pageLimit := maxPages
	if pageLimit == 0 {
		pageLimit = api.MaxChangePages
	}
	for page := 1; page <= pageLimit; page++ {
		if err := ctx.Err(); err != nil {
			return newOperationFailure(startDate, endDate, FailureStageChanges, page, 0, err)
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
			return newOperationFailure(startDate, endDate, FailureStageChanges, page, 0, err)
		}
		if err := validateChangesPage(changes, mediaType, page, expectedPages, expectedResults); err != nil {
			return newOperationFailure(startDate, endDate, FailureStagePagination, page, 0, err)
		}
		if expectedPages == -1 {
			expectedPages, expectedResults = changes.TotalPages, changes.TotalResults
		}
		if len(changes.IDs) == 0 {
			if page == 1 && changes.TotalPages == 1 && changes.TotalResults == 0 {
				return nil
			}
			return newOperationFailure(startDate, endDate, FailureStagePagination, page, 0,
				&PaginationError{Page: page, Reason: "empty page before all declared results were received"})
		}
		receivedResults += len(changes.IDs)
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
			if err := refreshChangedIDs(ctx, client, store, mediaType, ids, startDate, endDate, page, missingPolicy, report); err != nil {
				return err
			}
		}
		if page == changes.TotalPages {
			if receivedResults != changes.TotalResults {
				return newOperationFailure(startDate, endDate, FailureStagePagination, page, 0,
					&PaginationError{Page: page, Reason: fmt.Sprintf("received %d results but TMDB declared %d", receivedResults, changes.TotalResults)})
			}
			return nil
		}
		if page >= pageLimit {
			return newOperationFailure(startDate, endDate, FailureStagePagination, page, 0,
				&PaginationError{Page: page, Reason: fmt.Sprintf("configured limit %d leaves declared pages unprocessed", pageLimit)})
		}
	}
	return newOperationFailure(startDate, endDate, FailureStagePagination, pageLimit, 0,
		&PaginationError{Page: pageLimit, Reason: "TMDB maximum page limit reached before completion"})
}

func validateChangesPage(page api.ChangedIDsPage, mediaType models.MediaType, requestedPage, expectedPages, expectedResults int) error {
	if page.MediaType != mediaType {
		return &PaginationError{Page: requestedPage, Reason: fmt.Sprintf("media type %q does not match %q", page.MediaType, mediaType)}
	}
	if page.Page != requestedPage {
		return &PaginationError{Page: requestedPage, Reason: fmt.Sprintf("response page is %d", page.Page)}
	}
	if page.TotalPages < 1 || page.TotalPages > api.MaxChangePages {
		return &PaginationError{Page: requestedPage, Reason: fmt.Sprintf("total_pages must be between 1 and %d, got %d", api.MaxChangePages, page.TotalPages)}
	}
	if requestedPage > page.TotalPages {
		return &PaginationError{Page: requestedPage, Reason: fmt.Sprintf("requested page exceeds total_pages %d", page.TotalPages)}
	}
	if page.TotalResults < 0 {
		return &PaginationError{Page: requestedPage, Reason: fmt.Sprintf("total_results cannot be negative: %d", page.TotalResults)}
	}
	if expectedPages != -1 && page.TotalPages != expectedPages {
		return &PaginationError{Page: requestedPage, Reason: fmt.Sprintf("total_pages changed from %d to %d", expectedPages, page.TotalPages)}
	}
	if expectedResults != -1 && page.TotalResults != expectedResults {
		return &PaginationError{Page: requestedPage, Reason: fmt.Sprintf("total_results changed from %d to %d", expectedResults, page.TotalResults)}
	}
	if page.TotalResults == 0 && len(page.IDs) != 0 {
		return &PaginationError{Page: requestedPage, Reason: "page contains IDs while total_results is zero"}
	}
	return nil
}

func refreshChangedIDs(ctx context.Context, client *api.TMDBClient, store *database.Database, mediaType models.MediaType, ids []int, startDate, endDate time.Time, page int, missingPolicy MissingRecordPolicy, report *Report) error {
	if mediaType == models.MediaTypeMovie {
		movies := make([]models.Movie, 0, len(ids))
		for _, id := range ids {
			movie, err := client.GetMovieDetailsFreshContext(ctx, id)
			if err != nil {
				if isUnavailable404(err) {
					unavailable := unavailableRecord(startDate, endDate, page, id, err)
					report.UnavailableRecords = append(report.UnavailableRecords, unavailable)
					if missingPolicy == MissingRecordSkip {
						report.IgnoredUnavailable = append(report.IgnoredUnavailable, unavailable)
						continue
					}
				}
				return newOperationFailure(startDate, endDate, FailureStageDetails, page, id, err)
			}
			movies = append(movies, movie)
		}
		if len(movies) == 0 {
			return nil
		}
		if err := store.SaveMoviesBulk(movies); err != nil {
			return newOperationFailure(startDate, endDate, FailureStagePersist, page, 0, err)
		}
		report.ItemsUpdated += len(movies)
		return nil
	}
	shows := make([]models.TVShow, 0, len(ids))
	for _, id := range ids {
		show, err := client.GetTVShowDetailsFreshContext(ctx, id)
		if err != nil {
			if isUnavailable404(err) {
				unavailable := unavailableRecord(startDate, endDate, page, id, err)
				report.UnavailableRecords = append(report.UnavailableRecords, unavailable)
				if missingPolicy == MissingRecordSkip {
					report.IgnoredUnavailable = append(report.IgnoredUnavailable, unavailable)
					continue
				}
			}
			return newOperationFailure(startDate, endDate, FailureStageDetails, page, id, err)
		}
		shows = append(shows, show)
	}
	if len(shows) == 0 {
		return nil
	}
	if err := store.SaveTVShowsBulk(shows); err != nil {
		return newOperationFailure(startDate, endDate, FailureStagePersist, page, 0, err)
	}
	report.ItemsUpdated += len(shows)
	return nil
}

func isUnavailable404(err error) bool {
	var apiErr *api.TMDBClientError
	return errors.As(err, &apiErr) && apiErr.StatusCode == 404
}

func unavailableRecord(startDate, endDate time.Time, page, id int, err error) UnavailableRecord {
	var apiErr *api.TMDBClientError
	_ = errors.As(err, &apiErr)
	reason := err.Error()
	status := 404
	if apiErr != nil {
		status = apiErr.StatusCode
		if apiErr.Message != "" {
			reason = apiErr.Message
		}
	}
	return UnavailableRecord{StartDate: dateString(startDate), EndDate: dateString(endDate), Page: page, TMDBID: id, HTTPStatus: status, Reason: reason}
}

func newOperationFailure(startDate, endDate time.Time, stage FailureStage, page, id int, cause error) *operationFailure {
	var apiErr *api.TMDBClientError
	status := 0
	if errors.As(cause, &apiErr) {
		status = apiErr.StatusCode
	}
	failure := Failure{
		StartDate: dateString(startDate), EndDate: dateString(endDate), Stage: stage,
		Page: page, TMDBID: id, HTTPStatus: status, Retryable: isRetryable(cause), Err: cause,
	}
	return &operationFailure{failure: failure, cause: cause}
}

func recordFailure(report *Report, failure Failure) {
	for _, current := range report.Failures {
		if current.StartDate == failure.StartDate && current.EndDate == failure.EndDate && current.Stage == failure.Stage &&
			current.Page == failure.Page && current.TMDBID == failure.TMDBID && current.HTTPStatus == failure.HTTPStatus &&
			current.Err != nil && failure.Err != nil && current.Err.Error() == failure.Err.Error() {
			return
		}
	}
	report.Failures = append(report.Failures, failure)
}

func isRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var apiErr *api.TMDBClientError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 0 || apiErr.StatusCode == 429 || apiErr.StatusCode >= 500
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func dateString(value time.Time) string { return utcDate(value).Format("2006-01-02") }

func utcDate(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	year, month, day := value.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

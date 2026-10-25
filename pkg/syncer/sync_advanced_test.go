package syncer

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/api"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/database"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

func Test404PolicyIsExplicitAndNeverDeletesLocalRecords(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mediaType models.MediaType
		changes   string
		details   string
		seed      func(t *testing.T, storePath string)
		query     string
	}{
		{name: "movie", mediaType: models.MediaTypeMovie, changes: "/3/movie/changes", details: "/3/movie/99", query: "SELECT title FROM movies WHERE id=99"},
		{name: "tv", mediaType: models.MediaTypeTV, changes: "/3/tv/changes", details: "/3/tv/99", query: "SELECT name FROM tv_shows WHERE id=99"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.changes {
					_, _ = fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":1,"results":[{"id":99}]}`)
					return
				}
				if r.URL.Path == tc.details {
					http.Error(w, `{"status_code":34,"status_message":"record not found"}`, http.StatusNotFound)
					return
				}
				t.Errorf("unexpected path %s", r.URL.Path)
				http.NotFound(w, r)
			}))
			defer server.Close()

			path := filepath.Join(t.TempDir(), "missing-record.sqlite")
			store, err := database.NewDatabase(path + "?_busy_timeout=5000")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if tc.mediaType == models.MediaTypeMovie {
				if err := store.SaveMovie(&models.Movie{ID: 99, Title: "Keep movie"}); err != nil {
					t.Fatal(err)
				}
			} else if err := store.SaveTVShow(&models.TVShow{ID: 99, Name: "Keep series"}); err != nil {
				t.Fatal(err)
			}
			observer, err := sql.Open("sqlite3", path+"?_busy_timeout=5000")
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()

			date := time.Date(2024, 7, 3, 0, 0, 0, 0, time.UTC)
			options := Options{MediaType: tc.mediaType, StartDate: date, EndDate: date}
			failedReport, err := SyncChangesContext(context.Background(), makeClient(server), store, options)
			if err == nil || len(failedReport.UnavailableRecords) != 1 || len(failedReport.IgnoredUnavailable) != 0 || len(failedReport.Failures) != 1 {
				t.Fatalf("default policy did not hold the checkpoint for 404: report=%#v err=%v", failedReport, err)
			}
			failure := failedReport.Failures[0]
			if failure.Stage != FailureStageDetails || failure.TMDBID != 99 || failure.HTTPStatus != http.StatusNotFound || failure.Retryable {
				t.Fatalf("404 failure was not classified: %#v", failure)
			}
			if _, exists, err := store.GetSyncCheckpoint(tc.mediaType); err != nil || exists {
				t.Fatalf("unresolved 404 advanced checkpoint: exists=%v err=%v", exists, err)
			}

			options.MissingRecordPolicy = MissingRecordSkip
			skippedReport, err := SyncChangesContext(context.Background(), makeClient(server), store, options)
			if err != nil || skippedReport.WindowsCompleted != 1 || len(skippedReport.UnavailableRecords) != 1 || len(skippedReport.IgnoredUnavailable) != 1 || len(skippedReport.Failures) != 0 {
				t.Fatalf("explicit skip policy did not resolve the unavailable ID: report=%#v err=%v", skippedReport, err)
			}
			var stored string
			if err := observer.QueryRow(tc.query).Scan(&stored); err != nil {
				t.Fatalf("404 deleted existing local row: %v", err)
			}
			if stored != "Keep movie" && stored != "Keep series" {
				t.Fatalf("local row was modified by 404: %q", stored)
			}
		})
	}
}

func TestSyncClassifies429AndServerFailures(t *testing.T) {
	t.Run("429 retries then updates", func(t *testing.T) {
		var detailCalls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/3/movie/changes" {
				_, _ = fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":1,"results":[{"id":50}]}`)
				return
			}
			if detailCalls.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = fmt.Fprint(w, `{"status_code":25,"status_message":"rate limited"}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"id":50,"title":"Recovered"}`)
		}))
		defer server.Close()
		store := makeStore(t)
		date := time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)
		report, err := SyncChangesContext(context.Background(), makeClientWithOptions(server, api.ClientOptions{MaxRetries: 1, RetryBaseDelay: time.Millisecond}), store, Options{MediaType: models.MediaTypeMovie, StartDate: date, EndDate: date})
		if err != nil || report.ItemsUpdated != 1 || detailCalls.Load() != 2 {
			t.Fatalf("429 retry did not recover: report=%#v calls=%d err=%v", report, detailCalls.Load(), err)
		}
	})

	t.Run("500 blocks checkpoint and is retryable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/3/movie/changes" {
				_, _ = fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":1,"results":[{"id":51}]}`)
				return
			}
			http.Error(w, `{"status_code":11,"status_message":"server error"}`, http.StatusInternalServerError)
		}))
		defer server.Close()
		store := makeStore(t)
		date := time.Date(2024, 8, 2, 0, 0, 0, 0, time.UTC)
		report, err := SyncChangesContext(context.Background(), makeClient(server), store, Options{MediaType: models.MediaTypeMovie, StartDate: date, EndDate: date})
		if err == nil || len(report.Failures) != 1 || report.Failures[0].HTTPStatus != http.StatusInternalServerError || !report.Failures[0].Retryable {
			t.Fatalf("500 was not retained as a retryable failure: report=%#v err=%v", report, err)
		}
		if _, exists, err := store.GetSyncCheckpoint(models.MediaTypeMovie); err != nil || exists {
			t.Fatalf("500 advanced checkpoint: exists=%v err=%v", exists, err)
		}
	})
}

func TestSyncTimeoutBlocksCheckpointAndCanBeRetried(t *testing.T) {
	var detailCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/3/movie/changes" {
			_, _ = fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":1,"results":[{"id":52}]}`)
			return
		}
		if detailCalls.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		_, _ = fmt.Fprint(w, `{"id":52,"title":"Recovered after timeout"}`)
	}))
	defer server.Close()
	store := makeStore(t)
	date := time.Date(2024, 8, 3, 0, 0, 0, 0, time.UTC)
	options := Options{MediaType: models.MediaTypeMovie, StartDate: date, EndDate: date}
	client := makeClientWithOptions(server, api.ClientOptions{Timeout: 30 * time.Millisecond, MaxRetries: -1})
	report, err := SyncChangesContext(context.Background(), client, store, options)
	if err == nil || len(report.Failures) != 1 || !report.Failures[0].Retryable {
		t.Fatalf("timeout was not reported as retryable: report=%#v err=%v", report, err)
	}
	if _, exists, err := store.GetSyncCheckpoint(models.MediaTypeMovie); err != nil || exists {
		t.Fatalf("timeout advanced checkpoint: exists=%v err=%v", exists, err)
	}
	retry, err := SyncChangesContext(context.Background(), client, store, options)
	if err != nil || retry.ItemsUpdated != 1 || retry.WindowsCompleted != 1 {
		t.Fatalf("retry after timeout did not complete: report=%#v err=%v", retry, err)
	}
}

func TestSyncClassifiesSQLiteBusyAsRetryable(t *testing.T) {
	for _, code := range []sqlite3.ErrNo{sqlite3.ErrBusy, sqlite3.ErrLocked} {
		err := fmt.Errorf("persist sync window: %w", sqlite3.Error{Code: code})
		if !isRetryable(err) {
			t.Errorf("SQLite code %d should be retryable", code)
		}
	}
	if isRetryable(sqlite3.Error{Code: sqlite3.ErrConstraint}) {
		t.Fatal("SQLite constraint failure should not be classified as retryable")
	}
}

func TestSyncPaginationRejectsInconsistentAndIncompletePages(t *testing.T) {
	tests := []struct {
		name           string
		maxPages       int
		responses      map[string]string
		statuses       map[string]int
		wantErr        bool
		wantStage      FailureStage
		wantPage       int
		wantCalls      int32
		wantCheckpoint bool
	}{
		{name: "first empty single page is valid", responses: map[string]string{"1": `{"page":1,"total_pages":1,"total_results":0,"results":[]}`}, wantCalls: 1, wantCheckpoint: true},
		{name: "empty intermediate page is incomplete", responses: map[string]string{"1": `{"page":1,"total_pages":3,"total_results":2,"results":[{"id":60}]}`, "2": `{"page":2,"total_pages":3,"total_results":2,"results":[]}`}, wantErr: true, wantStage: FailureStagePagination, wantPage: 2, wantCalls: 2},
		{name: "response page mismatch", responses: map[string]string{"1": `{"page":2,"total_pages":2,"total_results":1,"results":[{"id":61}]}`}, wantErr: true, wantStage: FailureStagePagination, wantPage: 1, wantCalls: 1},
		{name: "zero total pages", responses: map[string]string{"1": `{"page":1,"total_pages":0,"total_results":0,"results":[]}`}, wantErr: true, wantStage: FailureStagePagination, wantPage: 1, wantCalls: 1},
		{name: "received result count exceeds declaration", responses: map[string]string{"1": `{"page":1,"total_pages":1,"total_results":1,"results":[{"id":60},{"id":60}]}`}, wantErr: true, wantStage: FailureStagePagination, wantPage: 1, wantCalls: 1},
		{name: "over TMDB page ceiling", responses: map[string]string{"1": `{"page":1,"total_pages":501,"total_results":50100,"results":[{"id":62}]}`}, wantErr: true, wantStage: FailureStagePagination, wantPage: 1, wantCalls: 1},
		{name: "configured page ceiling", maxPages: 1, responses: map[string]string{"1": `{"page":1,"total_pages":2,"total_results":2,"results":[{"id":63}]}`}, wantErr: true, wantStage: FailureStagePagination, wantPage: 1, wantCalls: 1},
		{name: "last page request failure", responses: map[string]string{"1": `{"page":1,"total_pages":2,"total_results":2,"results":[{"id":64}]}`}, statuses: map[string]int{"2": http.StatusServiceUnavailable}, wantErr: true, wantStage: FailureStageChanges, wantPage: 2, wantCalls: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/3/movie/64" || r.URL.Path == "/3/movie/63" || r.URL.Path == "/3/movie/60" {
					id := strings.TrimPrefix(r.URL.Path, "/3/movie/")
					_, _ = fmt.Fprintf(w, `{"id":%s,"title":"Stored %s"}`, id, id)
					return
				}
				page := r.URL.Query().Get("page")
				calls.Add(1)
				if status := tc.statuses[page]; status != 0 {
					http.Error(w, `{"status_message":"page failed"}`, status)
					return
				}
				body, ok := tc.responses[page]
				if !ok {
					t.Errorf("unexpected page request %s", page)
					http.NotFound(w, r)
					return
				}
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			store := makeStore(t)
			date := time.Date(2024, 9, 1, 0, 0, 0, 0, time.UTC)
			report, err := SyncChangesContext(context.Background(), makeClient(server), store, Options{MediaType: models.MediaTypeMovie, StartDate: date, EndDate: date, MaxPagesPerWindow: tc.maxPages})
			if tc.wantErr {
				if err == nil || len(report.Failures) != 1 || report.Failures[0].Stage != tc.wantStage || report.Failures[0].Page != tc.wantPage {
					t.Fatalf("incomplete pagination was not reported once: report=%#v err=%v", report, err)
				}
			} else if err != nil || len(report.Failures) != 0 {
				t.Fatalf("valid empty pagination failed: report=%#v err=%v", report, err)
			}
			if calls.Load() != tc.wantCalls {
				t.Fatalf("unexpected changes endpoint calls: %d want %d", calls.Load(), tc.wantCalls)
			}
			_, exists, checkpointErr := store.GetSyncCheckpoint(models.MediaTypeMovie)
			if checkpointErr != nil || exists != tc.wantCheckpoint {
				t.Fatalf("checkpoint state mismatch: exists=%v want=%v err=%v", exists, tc.wantCheckpoint, checkpointErr)
			}
		})
	}
}

func TestConcurrentSyncsSerializeCheckpointReads(t *testing.T) {
	firstRequest, releaseFirst := make(chan struct{}), make(chan struct{})
	var firstOnce sync.Once
	var startsMu sync.Mutex
	var starts []string
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startsMu.Lock()
		starts = append(starts, r.URL.Query().Get("start_date"))
		startsMu.Unlock()
		if calls.Add(1) == 1 {
			firstOnce.Do(func() { close(firstRequest) })
			<-releaseFirst
		}
		_, _ = fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":0,"results":[]}`)
	}))
	defer server.Close()
	store := makeStore(t)
	end := time.Date(2024, 10, 20, 0, 0, 0, 0, time.UTC)
	options := Options{MediaType: models.MediaTypeMovie, EndDate: end}
	startCalls := make(chan struct{})
	results := make(chan Report, 2)
	errorsOut := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-startCalls
			report, err := SyncChangesContext(context.Background(), makeClient(server), store, options)
			results <- report
			errorsOut <- err
		}()
	}
	close(startCalls)
	select {
	case <-firstRequest:
	case <-time.After(3 * time.Second):
		t.Fatal("first synchronization did not issue a changes request")
	}
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("second synchronization bypassed the per-media lock: requests=%d", calls.Load())
	}
	close(releaseFirst)
	reports := []Report{<-results, <-results}
	for i := 0; i < 2; i++ {
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
	gotStarts := []string{reports[0].StartDate.Format("2006-01-02"), reports[1].StartDate.Format("2006-01-02")}
	sort.Strings(gotStarts)
	if gotStarts[0] != "2024-10-07" || gotStarts[1] != "2024-10-20" {
		t.Fatalf("concurrent syncs read the same stale checkpoint: starts=%v", gotStarts)
	}
}

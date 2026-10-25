package syncer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sshturbo/TMDB-Collector-Lib/pkg/api"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/config"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/database"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

func makeClient(server *httptest.Server) *api.TMDBClient {
	return makeClientWithOptions(server, api.ClientOptions{MaxRetries: -1})
}

func makeClientWithOptions(server *httptest.Server, options api.ClientOptions) *api.TMDBClient {
	cfg := &config.Config{}
	cfg.TMDB.APIKey = "sync-test-key"
	cfg.TMDB.BaseURL = server.URL + "/3"
	return api.NewTMDBClientWithOptions(cfg, options)
}

func makeStore(t *testing.T) *database.Database {
	t.Helper()
	store, err := database.NewDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestSyncChangesDeduplicatesIDsAndStoresCheckpoint(t *testing.T) {
	var detailCalls atomic.Int32
	var requestedStarts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/movie/changes":
			requestedStarts = append(requestedStarts, r.URL.Query().Get("start_date"))
			page := r.URL.Query().Get("page")
			if page == "1" {
				_, _ = fmt.Fprint(w, `{"page":1,"total_pages":2,"total_results":3,"results":[{"id":10}]}`)
			} else {
				_, _ = fmt.Fprint(w, `{"page":2,"total_pages":2,"total_results":3,"results":[{"id":10},{"id":11}]}`)
			}
		case "/3/movie/10", "/3/movie/11":
			detailCalls.Add(1)
			id := strings.TrimPrefix(r.URL.Path, "/3/movie/")
			_, _ = fmt.Fprintf(w, `{"id":%s,"title":"Movie %s","vote_average":7.5,"popularity":22.1}`, id, id)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	store := makeStore(t)
	start := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	report, err := SyncChangesContext(ctx, makeClient(server), store, Options{
		MediaType: models.MediaTypeMovie, StartDate: start, EndDate: end,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.IDsReceived != 3 || report.ItemsUpdated != 2 || report.DuplicatesSkipped != 1 || report.PagesProcessed != 2 || report.WindowsCompleted != 1 {
		t.Fatalf("unexpected sync report: %#v", report)
	}
	checkpoint, exists, err := store.GetSyncCheckpoint(models.MediaTypeMovie)
	if err != nil || !exists || checkpoint.CompletedThrough != "2024-05-02" {
		t.Fatalf("checkpoint not stored after successful interval: %#v exists=%v err=%v", checkpoint, exists, err)
	}
	resumeReport, err := SyncChangesContext(context.Background(), makeClient(server), store, Options{
		MediaType: models.MediaTypeMovie, EndDate: end,
	})
	if err != nil || resumeReport.StartDate.Format("2006-01-02") != "2024-05-02" || requestedStarts[len(requestedStarts)-1] != "2024-05-02" {
		t.Fatalf("default sync did not resume from inclusive checkpoint: report=%#v starts=%v err=%v", resumeReport, requestedStarts, err)
	}
	if detailCalls.Load() != 4 {
		t.Fatalf("changed IDs were not deduplicated before details fetch on each pass: %d", detailCalls.Load())
	}
}

func TestSyncFailureDoesNotAdvanceCheckpointAndCanRetry(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/tv/changes":
			_, _ = fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":1,"results":[{"id":20}]}`)
		case "/3/tv/20":
			if fail.Load() {
				http.Error(w, `{"status_message":"temporary failure"}`, http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprint(w, `{"id":20,"name":"Series 20","first_air_date":"2020-01-01"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	store := makeStore(t)
	start := time.Date(2024, 6, 7, 0, 0, 0, 0, time.UTC)
	options := Options{MediaType: models.MediaTypeTV, StartDate: start, EndDate: start}
	if report, err := SyncChangesContext(context.Background(), makeClient(server), store, options); err == nil || len(report.Failures) == 0 {
		t.Fatalf("expected an item failure and report: report=%#v err=%v", report, err)
	}
	if _, exists, err := store.GetSyncCheckpoint(models.MediaTypeTV); err != nil || exists {
		t.Fatalf("failed interval advanced checkpoint: exists=%v err=%v", exists, err)
	}
	fail.Store(false)
	report, err := SyncChangesContext(context.Background(), makeClient(server), store, options)
	if err != nil || report.ItemsUpdated != 1 || report.WindowsCompleted != 1 {
		t.Fatalf("retry failed: report=%#v err=%v", report, err)
	}
	checkpoint, exists, err := store.GetSyncCheckpoint(models.MediaTypeTV)
	if err != nil || !exists || checkpoint.CompletedThrough != "2024-06-07" {
		t.Fatalf("checkpoint missing after successful retry: %#v exists=%v err=%v", checkpoint, exists, err)
	}
}

func TestSyncSplitsDateIntervalsWithOneDayOverlap(t *testing.T) {
	var intervals []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/movie/changes" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		intervals = append(intervals, r.URL.Query().Get("start_date")+":"+r.URL.Query().Get("end_date"))
		_, _ = fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":0,"results":[]}`)
	}))
	defer server.Close()
	store := makeStore(t)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 17)
	report, err := SyncChangesContext(context.Background(), makeClient(server), store, Options{
		MediaType: models.MediaTypeMovie, StartDate: start, EndDate: end,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2024-01-01:2024-01-14", "2024-01-14:2024-01-18"}
	if strings.Join(intervals, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected API date windows: got %v want %v", intervals, want)
	}
	if report.WindowsCompleted != 2 || report.CompletedThrough.Format("2006-01-02") != "2024-01-18" {
		t.Fatalf("windows/checkpoint boundary incorrect: %#v", report)
	}
}

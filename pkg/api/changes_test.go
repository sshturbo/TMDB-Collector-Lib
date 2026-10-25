package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

func TestGetMovieChangesValidatesInclusiveWindowAndParsesPage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/3/movie/changes" || r.URL.Query().Get("start_date") != "2024-01-01" ||
			r.URL.Query().Get("end_date") != "2024-01-14" || r.URL.Query().Get("page") != "2" {
			t.Errorf("unexpected changes request: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = fmt.Fprint(w, `{"page":2,"total_pages":3,"total_results":205,"results":[{"id":17},{"id":0},{"id":23}]}`)
	}))
	defer server.Close()
	client := testClient(server, ClientOptions{})
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	page, err := client.GetMovieChangesContext(context.Background(), start, start.AddDate(0, 0, 13), 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.MediaType != models.MediaTypeMovie || page.Page != 2 || page.TotalPages != 3 || page.TotalResults != 205 || len(page.IDs) != 2 || page.IDs[0] != 17 || page.IDs[1] != 23 {
		t.Fatalf("unexpected changes page: %#v", page)
	}
	if _, err := client.GetMovieChangesContext(context.Background(), start, start.AddDate(0, 0, 14), 1); err == nil || calls.Load() != 1 {
		t.Fatalf("15-day inclusive interval was not rejected before HTTP: calls=%d err=%v", calls.Load(), err)
	}
}

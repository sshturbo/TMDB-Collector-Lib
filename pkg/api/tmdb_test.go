package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sshturbo/TMDB-Collector-Lib/pkg/config"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

func testClient(server *httptest.Server, options ClientOptions) *TMDBClient {
	cfg := &config.Config{}
	cfg.TMDB.APIKey = "test-secret"
	cfg.TMDB.BaseURL = server.URL + "/3"
	cfg.TMDB.Language = "pt-BR"
	cfg.TMDB.ImageBaseURL = server.URL + "/images"
	if options.MaxRetries == 0 {
		options.MaxRetries = -1
	}
	return NewTMDBClientWithOptions(cfg, options)
}

func TestSearchMoviesUsesConfiguredLanguagePageYearAndEscapesQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/search/movie" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("language") != "pt-BR" || query.Get("page") != "3" || query.Get("primary_release_year") != "1999" {
			t.Errorf("unexpected query parameters: %v", query)
		}
		if query.Get("query") != "Amélie & Co" || query.Get("api_key") != "test-secret" {
			t.Errorf("query or key was corrupted: %v", query)
		}
		_, _ = w.Write([]byte(`{"page":3,"results":[{"id":4,"title":"O Fabuloso Destino de Amélie Poulain","original_title":"Le Fabuleux Destin d'Amélie Poulain","original_language":"fr","release_date":"2001-04-25","poster_path":"/p.jpg"}],"total_pages":9}`))
	}))
	defer server.Close()

	movies, err := testClient(server, ClientOptions{}).SearchMoviesByTitleContext(context.Background(), "Amélie & Co", 1999, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 1 || movies[0].TMDBID != 4 || movies[0].ReleaseYear != 2001 {
		t.Fatalf("unexpected movie result: %#v", movies)
	}
	if movies[0].PosterPath != server.URL+"/images/p.jpg" {
		t.Errorf("poster URL was not expanded: %q", movies[0].PosterPath)
	}
}

func TestSearchReturnsEmptySliceForNoResultsAndUsesPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("language") != "pt-BR" {
			t.Errorf("unexpected default page or language: %v", r.URL.Query())
		}
		_, _ = w.Write([]byte(`{"results":null}`))
	}))
	defer server.Close()

	movies, err := testClient(server, ClientOptions{}).SearchMovies("missing", 0)
	if err != nil {
		t.Fatal(err)
	}
	if movies == nil || len(movies) != 0 {
		t.Fatalf("expected a non-nil empty result, got %#v", movies)
	}
}

func TestSearchMoviePagesStopsAtEmptyPage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		calls.Add(1)
		if page == "1" {
			_, _ = w.Write([]byte(`{"results":[{"id":1,"title":"One"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	movies, err := testClient(server, ClientOptions{}).SearchMoviePagesContext(context.Background(), "One", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 1 || calls.Load() != 2 {
		t.Fatalf("pagination did not stop on empty results: got %d records, %d requests", len(movies), calls.Load())
	}
}

func TestGetMovieDetailsAndSearchTitlesPreserveLanguageAndOrigins(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/movie/361743" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if r.URL.Query().Get("language") != "pt-BR" || !strings.Contains(r.URL.Query().Get("append_to_response"), "external_ids") {
			t.Errorf("details request missing requested language or appended data: %v", r.URL.Query())
		}
		_, _ = w.Write([]byte(`{
			"id":361743,"title":"Ases Indomáveis","original_title":"Top Gun: Maverick","original_language":"en",
			"release_date":"2022-05-27","overview":"Sinopse em português",
			"external_ids":{"imdb_id":"tt1745960"},
			"translations":{"translations":[
				{"iso_639_1":"en","iso_3166_1":"US","data":{"title":"Top Gun: Maverick","overview":"English synopsis"}},
				{"iso_639_1":"pt","iso_3166_1":"BR","data":{"title":"Ases Indomáveis","overview":"Sinopse"}}]},
			"alternative_titles":{"titles":[{"iso_3166_1":"BR","title":"Ases Indomáveis 2"}]}
		}`))
	}))
	defer server.Close()

	metadata, err := testClient(server, ClientOptions{}).GetSearchTitles(models.MediaTypeMovie, 361743)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.TMDBID != 361743 || metadata.IMDbID != "tt1745960" || metadata.ReleaseYear != 2022 || metadata.OriginalLanguage != "en" {
		t.Fatalf("identity metadata missing: %#v", metadata)
	}
	if len(metadata.SearchTitles) != 3 {
		t.Fatalf("expected localized/original/alternative titles with duplicates removed, got %#v", metadata.SearchTitles)
	}
	if metadata.SearchTitles[0].Language != "pt-BR" || metadata.SearchTitles[1].Language != "en" {
		t.Errorf("localized and original language provenance missing: %#v", metadata.SearchTitles)
	}
	if metadata.SearchTitles[2].Title != "Ases Indomáveis 2" || metadata.SearchTitles[2].Country != "BR" {
		t.Errorf("alternative title provenance missing: %#v", metadata.SearchTitles)
	}
}

func TestGetDetailsFallsBackToEnglishTranslation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":10,"title":"Original","original_title":"Original","original_language":"ja","translations":{"translations":[{"iso_639_1":"en","iso_3166_1":"US","data":{"title":"English fallback","overview":"English synopsis"}}]}}`))
	}))
	defer server.Close()

	movie, err := testClient(server, ClientOptions{}).GetMovieDetails(10)
	if err != nil {
		t.Fatal(err)
	}
	if movie.Title != "English fallback" || movie.Overview != "English synopsis" {
		t.Fatalf("translation fallback failed: %#v", movie)
	}
}

func TestFindByIMDbIDReturnsMovieAndTVMatches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/find/tt1234567" || r.URL.Query().Get("external_source") != "imdb_id" {
			t.Errorf("unexpected IMDb find request: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"movie_results":[{"id":8,"title":"Film","release_date":"2020-01-01"}],"tv_results":[{"id":9,"name":"Show","first_air_date":"2019-01-01"}]}`))
	}))
	defer server.Close()

	matches, err := testClient(server, ClientOptions{}).FindByIMDbID("tt1234567")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].IMDbID != "tt1234567" || matches[1].IMDbID != "tt1234567" {
		t.Fatalf("unexpected IMDb matches: %#v", matches)
	}
}

func TestIdentifyMediaDoesNotAutoSelectAmbiguousResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[
			{"id":1,"title":"Dune","original_title":"Dune","release_date":"2021-10-22"},
			{"id":2,"title":"Dune","original_title":"Dune","release_date":"2021-10-22"}]}`))
	}))
	defer server.Close()

	result, err := testClient(server, ClientOptions{}).IdentifyMedia("Dune", models.MediaTypeMovie, 2021)
	if err != nil {
		t.Fatal(err)
	}
	if result.Conclusive || result.Candidate != nil || len(result.Candidates) != 2 {
		t.Fatalf("ambiguous results should not be selected automatically: %#v", result)
	}
}

func TestIdentifyMediaEnrichesConclusiveMatchWithIMDbID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/search/movie":
			_, _ = w.Write([]byte(`{"results":[{"id":11,"title":"Dune","original_title":"Dune","original_language":"en","release_date":"2021-10-22"}]}`))
		case "/3/movie/11":
			_, _ = w.Write([]byte(`{"id":11,"title":"Duna","original_title":"Dune","original_language":"en","release_date":"2021-10-22","external_ids":{"imdb_id":"tt1160419"}}`))
		default:
			t.Errorf("unexpected request %q", r.URL.Path)
		}
	}))
	defer server.Close()
	result, err := testClient(server, ClientOptions{}).IdentifyMedia("Dune", models.MediaTypeMovie, 2021)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Conclusive || result.Candidate == nil || result.Candidate.TMDBID != 11 || result.Candidate.IMDbID != "tt1160419" {
		t.Fatalf("conclusive match was not enriched: %#v", result)
	}
}

func Test429RetriesAndThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"status_code":25,"status_message":"rate limited"}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	client := testClient(server, ClientOptions{MaxRetries: 1, RetryBaseDelay: time.Millisecond})
	var response struct {
		Results []any `json:"results"`
	}
	if err := client.doJSON(context.Background(), "search/movie", url.Values{}, &response); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one retry; calls=%d", calls.Load())
	}
}

func TestServerErrorRetries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	client := testClient(server, ClientOptions{MaxRetries: 1, RetryBaseDelay: time.Millisecond})
	var response any
	if err := client.doJSON(context.Background(), "search/movie", url.Values{}, &response); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected a 5xx retry; calls=%d", calls.Load())
	}
}

func TestRateLimitErrorExposesRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"status_code":25,"status_message":"rate limited"}`))
	}))
	defer server.Close()
	client := testClient(server, ClientOptions{MaxRetries: -1})
	var response any
	err := client.doJSON(context.Background(), "search/movie", url.Values{}, &response)
	var apiErr *TMDBClientError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests || apiErr.RetryAfter != 2*time.Second {
		t.Fatalf("Retry-After was not preserved: %#v", err)
	}
}

func TestAPIErrorDoesNotExposeAPIKey(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status_code":7,"status_message":"invalid key test-secret"}`))
	}))
	defer server.Close()
	client := testClient(server, ClientOptions{MaxRetries: 3})
	var response any
	err := client.doJSON(context.Background(), "search/movie", url.Values{}, &response)
	if err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("error is absent or exposes API key: %v", err)
	}
	var apiErr *TMDBClientError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized || apiErr.TMDBStatusCode != 7 {
		t.Fatalf("structured API error unavailable: %#v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("authentication error should not be retried: %d requests", calls.Load())
	}
}

func TestClientFormattingDoesNotExposeAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	client := testClient(server, ClientOptions{})
	if formatted := fmt.Sprintf("%+v %#v", client, client); strings.Contains(formatted, "test-secret") {
		t.Fatalf("client formatting exposed API key: %s", formatted)
	}
}

func TestTimeoutAndExplicitCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(150 * time.Millisecond):
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer server.Close()
	client := testClient(server, ClientOptions{Timeout: 25 * time.Millisecond, MaxRetries: -1})
	var response any
	if err := client.doJSON(context.Background(), "search/movie", url.Values{}, &response); err == nil {
		t.Fatal("expected timeout")
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	err := client.doJSON(ctx, "search/movie", url.Values{}, &response)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestDiscoverDoesNotFetchTrailersUnlessConfigured(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/3/discover/movie" {
			t.Errorf("unexpected extra API call: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"results":[{"id":3,"title":"Film"}]}`))
	}))
	defer server.Close()
	movies, err := testClient(server, ClientOptions{}).DiscoverMovies(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 1 || calls.Load() != 1 {
		t.Fatalf("discover unexpectedly made additional trailer calls: len=%d calls=%d", len(movies), calls.Load())
	}
}

func TestCollectionReportsMetricsAndContinuesAfterFailedPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{"results":[{"id":1,"title":"One"}]}`))
		case "2":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status_message":"temporarily unavailable"}`))
		case "3":
			_, _ = w.Write([]byte(`{"results":[{"id":3,"title":"Three"}]}`))
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer server.Close()

	movies, report, err := testClient(server, ClientOptions{}).CollectMoviesContext(context.Background(), 3)
	if err == nil {
		t.Fatal("expected an aggregated page error")
	}
	if len(movies) != 2 || report.PagesRequested != 3 || report.PagesSucceeded != 2 || report.FailedPages != 1 || report.ItemsProcessed != 2 || report.Duration < 0 {
		t.Fatalf("collection metrics or partial results incorrect: movies=%d report=%#v", len(movies), report)
	}
	if len(report.Failures) != 1 || report.Failures[0].Page != 2 {
		t.Fatalf("failed page was not recorded: %#v", report.Failures)
	}
}

func TestCacheExpiresAndCanBeCleared(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, `{"results":[]}`)
	}))
	defer server.Close()
	client := testClient(server, ClientOptions{CacheTTL: 15 * time.Millisecond})
	for i := 0; i < 2; i++ {
		var response any
		if err := client.doJSON(context.Background(), "search/movie", url.Values{}, &response); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("response was not cached: calls=%d", calls.Load())
	}
	time.Sleep(20 * time.Millisecond)
	var expiredResponse any
	if err := client.doJSON(context.Background(), "search/movie", url.Values{}, &expiredResponse); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expired cached response was returned: calls=%d", calls.Load())
	}
	client.ClearCache()
	var response any
	if err := client.doJSON(context.Background(), "search/movie", url.Values{}, &response); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("ClearCache did not force a request: calls=%d", calls.Load())
	}
}

func TestConcurrentRequestsRespectClientLimit(t *testing.T) {
	var active, maximum atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client := testClient(server, ClientOptions{MaxConcurrentRequests: 2})
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			var response map[string]any
			if err := client.doJSON(context.Background(), "search/movie", url.Values{}, &response); err != nil {
				t.Errorf("request failed: %v", err)
			}
		}()
	}
	group.Wait()
	if maximum.Load() > 2 || maximum.Load() == 0 {
		t.Fatalf("observed concurrency %d with a limit of 2", maximum.Load())
	}
}

func TestTVSearchYearUsesFirstAirDateYearAndLanguage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("first_air_date_year") != "2018" || r.URL.Query().Get("language") != "pt-BR" {
			t.Errorf("unexpected TV search params: %v", r.URL.Query())
		}
		_, _ = w.Write([]byte(`{"results":[{"id":55,"name":"Série","original_name":"Show","first_air_date":"2018-01-02"}]}`))
	}))
	defer server.Close()
	shows, err := testClient(server, ClientOptions{}).SearchTVShowsByTitleContext(context.Background(), "Série", 2018, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(shows) != 1 || shows[0].ReleaseYear != 2018 {
		t.Fatalf("unexpected TV results: %#v", shows)
	}
}

func TestErrorTransportURLRedactsAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	client := testClient(server, ClientOptions{MaxRetries: -1})
	var response any
	err := client.doJSON(context.Background(), "search/movie", url.Values{}, &response)
	if err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("transport error leaked API key: %v", err)
	}
}

func TestAPIKeyIsURLQueryEncoded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "key with&symbols" {
			t.Errorf("API key was not encoded safely: %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	client := testClient(server, ClientOptions{})
	client.apiKey = "key with&symbols"
	var response any
	if err := client.doJSON(context.Background(), "search/movie", url.Values{}, &response); err != nil {
		t.Fatal(err)
	}
}

func TestExternalIDsEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/tv/12/external_ids" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(models.ExternalIDs{IMDbID: "tt7654321"})
	}))
	defer server.Close()
	ids, err := testClient(server, ClientOptions{}).GetTVShowExternalIDs(12)
	if err != nil || ids.IMDbID != "tt7654321" {
		t.Fatalf("unexpected external IDs: %#v, %v", ids, err)
	}
}

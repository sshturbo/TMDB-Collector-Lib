package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sshturbo/TMDB-Collector-Lib/internal/normalization"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/config"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

const (
	defaultBaseURL      = "https://api.themoviedb.org/3"
	defaultImageBaseURL = "https://image.tmdb.org/t/p/original"
	defaultLanguage     = "pt-BR"
	defaultFallback     = "en-US"
	defaultTimeout      = 15 * time.Second
	defaultConcurrency  = 5
	defaultRetries      = 2
	defaultResponseSize = 2 << 20
	defaultCacheEntries = 512
	maxCacheTTL         = 180 * 24 * time.Hour
)

// ClientOptions configures HTTP behavior. Zero values use conservative defaults;
// set MaxRetries to -1 to disable retries.
type ClientOptions struct {
	HTTPClient            *http.Client
	Timeout               time.Duration
	MaxConcurrentRequests int
	MaxRetries            int
	RetryBaseDelay        time.Duration
	MaxResponseBytes      int64
	CacheTTL              time.Duration
	MaxCacheEntries       int
}

// TMDBClientError describes an HTTP/API failure without exposing request credentials.
type TMDBClientError struct {
	StatusCode     int
	TMDBStatusCode int
	Message        string
	RetryAfter     time.Duration
	cause          error
}

func (e *TMDBClientError) Error() string {
	if e == nil {
		return "tmdb request failed"
	}
	if e.StatusCode != 0 {
		if e.Message != "" {
			return fmt.Sprintf("tmdb API returned HTTP %d: %s", e.StatusCode, e.Message)
		}
		return fmt.Sprintf("tmdb API returned HTTP %d", e.StatusCode)
	}
	if e.Message != "" {
		return "tmdb request failed: " + e.Message
	}
	return "tmdb request failed"
}

func (e *TMDBClientError) Unwrap() error { return e.cause }

type cacheEntry struct {
	body      []byte
	expiresAt time.Time
}

type TMDBClient struct {
	config           *config.Config
	client           *http.Client
	baseURL          string
	imageBaseURL     string
	apiKey           string
	language         string
	fallbackLanguage string
	includeTrailers  bool
	maxRetries       int
	retryBaseDelay   time.Duration
	maxResponseBytes int64
	cacheTTL         time.Duration
	maxCacheEntries  int
	semaphore        chan struct{}
	cacheMu          sync.Mutex
	cache            map[string]cacheEntry
}

type TMDBResponse struct {
	Page         int             `json:"page"`
	Results      json.RawMessage `json:"results"`
	TotalPages   int             `json:"total_pages"`
	TotalResults int             `json:"total_results"`
}

type VideoResponse struct {
	Results []struct {
		Key      string `json:"key"`
		Site     string `json:"site"`
		Type     string `json:"type"`
		Official bool   `json:"official"`
	} `json:"results"`
}

// NewTMDBClient preserves the original constructor and uses safe defaults.
func NewTMDBClient(cfg *config.Config) *TMDBClient {
	return NewTMDBClientWithOptions(cfg, ClientOptions{})
}

// NewTMDBClientWithOptions creates a reusable, concurrency-limited HTTP client.
func NewTMDBClientWithOptions(cfg *config.Config, options ClientOptions) *TMDBClient {
	if cfg == nil {
		cfg = &config.Config{}
	}
	configCopy := *cfg
	apiKey := configCopy.TMDB.APIKey
	configCopy.TMDB.APIKey = ""
	cfg = &configCopy
	timeout := options.Timeout
	if timeout <= 0 && cfg.TMDB.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TMDB.TimeoutSeconds) * time.Second
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	maxConcurrent := options.MaxConcurrentRequests
	if maxConcurrent <= 0 {
		maxConcurrent = cfg.Fetch.MaxConcurrentRequests
	}
	if maxConcurrent <= 0 {
		maxConcurrent = defaultConcurrency
	}

	maxRetries := options.MaxRetries
	if maxRetries == 0 {
		maxRetries = cfg.Fetch.MaxRetries
	}
	if maxRetries == 0 {
		maxRetries = defaultRetries
	}
	if maxRetries < 0 {
		maxRetries = 0
	}

	baseURL := strings.TrimSpace(cfg.TMDB.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	imageBaseURL := strings.TrimRight(strings.TrimSpace(cfg.TMDB.ImageBaseURL), "/")
	if imageBaseURL == "" {
		imageBaseURL = defaultImageBaseURL
	}
	language := strings.TrimSpace(cfg.TMDB.Language)
	if language == "" {
		language = defaultLanguage
	}
	fallbackLanguage := strings.TrimSpace(cfg.TMDB.FallbackLanguage)
	if fallbackLanguage == "" {
		fallbackLanguage = defaultFallback
	}

	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	} else {
		clonedClient := *client
		if options.Timeout > 0 || clonedClient.Timeout <= 0 {
			clonedClient.Timeout = timeout
		}
		client = &clonedClient
	}

	retryBaseDelay := options.RetryBaseDelay
	if retryBaseDelay <= 0 {
		retryBaseDelay = 200 * time.Millisecond
	}
	maxResponseBytes := options.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultResponseSize
	}
	cacheTTL := options.CacheTTL
	if cacheTTL < 0 {
		cacheTTL = 0
	} else if cacheTTL == 0 && cfg.Fetch.CacheTTLSeconds > 0 {
		cacheTTL = time.Duration(cfg.Fetch.CacheTTLSeconds) * time.Second
	}
	if cacheTTL > maxCacheTTL {
		cacheTTL = maxCacheTTL
	}
	maxCacheEntries := options.MaxCacheEntries
	if maxCacheEntries <= 0 {
		maxCacheEntries = defaultCacheEntries
	}

	return &TMDBClient{
		config: cfg, client: client, baseURL: baseURL, imageBaseURL: imageBaseURL,
		apiKey: apiKey, language: language, fallbackLanguage: fallbackLanguage,
		includeTrailers: cfg.Fetch.FetchTrailers, maxRetries: maxRetries,
		retryBaseDelay: retryBaseDelay, maxResponseBytes: maxResponseBytes, cacheTTL: cacheTTL,
		maxCacheEntries: maxCacheEntries,
		semaphore:       make(chan struct{}, maxConcurrent), cache: make(map[string]cacheEntry),
	}
}

// String returns a safe summary and deliberately excludes credentials.
func (c *TMDBClient) String() string {
	if c == nil {
		return "TMDBClient<nil>"
	}
	return fmt.Sprintf("TMDBClient{language:%q, fallbackLanguage:%q, maxConcurrentRequests:%d}", c.language, c.fallbackLanguage, cap(c.semaphore))
}

// GoString keeps %#v formatting safe for logs and diagnostics as well.
func (c *TMDBClient) GoString() string { return c.String() }

// ClearCache drops all cached successful responses.
func (c *TMDBClient) ClearCache() {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.cache = make(map[string]cacheEntry)
}

func (c *TMDBClient) endpoint(path string, query url.Values) (string, error) {
	base, err := url.Parse(c.baseURL + "/")
	if err != nil {
		return "", fmt.Errorf("invalid TMDB base URL: %w", err)
	}
	relative, err := url.Parse(strings.TrimLeft(path, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid TMDB endpoint: %w", err)
	}
	endpoint := base.ResolveReference(relative)
	values := endpoint.Query()
	for key, entries := range query {
		for _, value := range entries {
			values.Add(key, value)
		}
	}
	if c.apiKey != "" {
		values.Set("api_key", c.apiKey)
	}
	endpoint.RawQuery = values.Encode()
	return endpoint.String(), nil
}

func (c *TMDBClient) doJSON(ctx context.Context, path string, query url.Values, destination any) error {
	return c.doJSONWithCache(ctx, path, query, destination, true)
}

func (c *TMDBClient) doJSONFresh(ctx context.Context, path string, query url.Values, destination any) error {
	return c.doJSONWithCache(ctx, path, query, destination, false)
}

func (c *TMDBClient) doJSONWithCache(ctx context.Context, path string, query url.Values, destination any, useCache bool) error {
	requestURL, err := c.endpoint(path, query)
	if err != nil {
		return err
	}
	if useCache && c.cacheTTL > 0 {
		if body, ok := c.cached(requestURL); ok {
			if err := json.Unmarshal(body, destination); err != nil {
				return fmt.Errorf("decode cached TMDB response: %w", err)
			}
			return nil
		}
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		requestContext := ctx
		cancel := func() {}
		if c.client.Timeout <= 0 {
			requestContext, cancel = context.WithTimeout(ctx, defaultTimeout)
		}
		request, err := http.NewRequestWithContext(requestContext, http.MethodGet, requestURL, nil)
		if err != nil {
			cancel()
			return fmt.Errorf("create TMDB request: %w", err)
		}
		request.Header.Set("Accept", "application/json")

		select {
		case c.semaphore <- struct{}{}:
		case <-requestContext.Done():
			cancel()
			return requestContext.Err()
		}
		response, requestErr := c.client.Do(request)
		if requestErr != nil {
			<-c.semaphore
			cancel()
			lastErr = safeRequestError(requestErr, c.apiKey)
			if attempt < c.maxRetries && ctx.Err() == nil {
				if err := waitContext(ctx, c.retryDelay(attempt, 0)); err != nil {
					return err
				}
				continue
			}
			return &TMDBClientError{Message: lastErr.Error(), cause: lastErr}
		}

		body, readErr := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
		response.Body.Close()
		<-c.semaphore
		cancel()
		if readErr != nil {
			lastErr = fmt.Errorf("read TMDB response: %w", readErr)
			if attempt < c.maxRetries && ctx.Err() == nil {
				if err := waitContext(ctx, c.retryDelay(attempt, 0)); err != nil {
					return err
				}
				continue
			}
			return &TMDBClientError{Message: lastErr.Error(), cause: lastErr}
		}
		if int64(len(body)) > c.maxResponseBytes {
			return &TMDBClientError{Message: "TMDB response exceeded configured size limit"}
		}

		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			apiErr := parseAPIError(response.StatusCode, response.Header, body, c.apiKey)
			lastErr = apiErr
			if isTransientStatus(response.StatusCode) && attempt < c.maxRetries {
				if err := waitContext(ctx, c.retryDelay(attempt, apiErr.RetryAfter)); err != nil {
					return err
				}
				continue
			}
			return apiErr
		}
		if useCache && c.cacheTTL > 0 {
			c.storeCache(requestURL, body)
		}
		if err := json.Unmarshal(body, destination); err != nil {
			return fmt.Errorf("decode TMDB response: %w", err)
		}
		return nil
	}
	return lastErr
}

func safeRequestError(err error, apiKey string) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		parsed, parseErr := url.Parse(urlErr.URL)
		if parseErr == nil {
			query := parsed.Query()
			query.Set("api_key", "[REDACTED]")
			parsed.RawQuery = query.Encode()
			return &url.Error{Op: urlErr.Op, URL: parsed.String(), Err: urlErr.Err}
		}
	}
	message := err.Error()
	if apiKey != "" {
		message = strings.ReplaceAll(message, apiKey, "[REDACTED]")
	}
	return &safeCause{message: message, cause: err}
}

type safeCause struct {
	message string
	cause   error
}

func (e *safeCause) Error() string { return e.message }
func (e *safeCause) Unwrap() error { return e.cause }

func parseAPIError(status int, headers http.Header, body []byte, apiKey string) *TMDBClientError {
	var payload struct {
		StatusCode    int    `json:"status_code"`
		StatusMessage string `json:"status_message"`
	}
	_ = json.Unmarshal(body, &payload)
	message := strings.TrimSpace(payload.StatusMessage)
	if apiKey != "" {
		message = strings.ReplaceAll(message, apiKey, "[REDACTED]")
	}
	return &TMDBClientError{
		StatusCode: status, TMDBStatusCode: payload.StatusCode,
		Message: message, RetryAfter: parseRetryAfter(headers.Get("Retry-After")),
	}
}

func parseRetryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

func isTransientStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func (c *TMDBClient) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	delay := c.retryBaseDelay
	for i := 0; i < attempt && delay < 5*time.Second; i++ {
		delay *= 2
	}
	if delay > 5*time.Second {
		return 5 * time.Second
	}
	return delay
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *TMDBClient) cached(key string) ([]byte, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	entry, exists := c.cache[key]
	if !exists {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.cache, key)
		return nil, false
	}
	return append([]byte(nil), entry.body...), true
}

func (c *TMDBClient) storeCache(key string, body []byte) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	now := time.Now()
	for cacheKey, entry := range c.cache {
		if now.After(entry.expiresAt) {
			delete(c.cache, cacheKey)
		}
	}
	if _, exists := c.cache[key]; !exists && len(c.cache) >= c.maxCacheEntries {
		var earliestKey string
		var earliest time.Time
		for cacheKey, entry := range c.cache {
			if earliestKey == "" || entry.expiresAt.Before(earliest) {
				earliestKey, earliest = cacheKey, entry.expiresAt
			}
		}
		delete(c.cache, earliestKey)
	}
	c.cache[key] = cacheEntry{body: append([]byte(nil), body...), expiresAt: now.Add(c.cacheTTL)}
}

func (c *TMDBClient) languageQuery() url.Values {
	query := url.Values{}
	query.Set("language", c.language)
	return query
}

// SearchMovies preserves the original API and searches using the configured language.
func (c *TMDBClient) SearchMovies(query string, page int) ([]models.Movie, error) {
	return c.SearchMoviesContext(context.Background(), query, page)
}

func (c *TMDBClient) SearchMoviesContext(ctx context.Context, query string, page int) ([]models.Movie, error) {
	return c.SearchMoviesByTitleContext(ctx, query, 0, page)
}

// SearchMoviesByTitleContext adds the official primary_release_year search filter when year is non-zero.
func (c *TMDBClient) SearchMoviesByTitleContext(ctx context.Context, title string, year, page int) ([]models.Movie, error) {
	if page < 1 {
		page = 1
	}
	params := c.languageQuery()
	params.Set("query", title)
	params.Set("page", strconv.Itoa(page))
	if year > 0 {
		params.Set("primary_release_year", strconv.Itoa(year))
	}
	var response struct {
		Page         int            `json:"page"`
		Results      []models.Movie `json:"results"`
		TotalPages   int            `json:"total_pages"`
		TotalResults int            `json:"total_results"`
	}
	if err := c.doJSON(ctx, "search/movie", params, &response); err != nil {
		return nil, err
	}
	for i := range response.Results {
		prepareMovie(&response.Results[i], c.imageBaseURL)
	}
	if response.Results == nil {
		response.Results = []models.Movie{}
	}
	return response.Results, nil
}

// SearchMoviePagesContext collects consecutive pages up to maxPages and stops early
// when TMDB returns an empty page. maxPages is required to bound memory and requests.
func (c *TMDBClient) SearchMoviePagesContext(ctx context.Context, title string, year, maxPages int) ([]models.Movie, error) {
	results := make([]models.Movie, 0)
	if maxPages < 0 {
		return results, errors.New("maxPages must not be negative")
	}
	for page := 1; page <= maxPages; page++ {
		movies, err := c.SearchMoviesByTitleContext(ctx, title, year, page)
		if err != nil {
			return results, err
		}
		if len(movies) == 0 {
			break
		}
		results = append(results, movies...)
	}
	return results, nil
}

// SearchTVShows preserves the original API and searches using the configured language.
func (c *TMDBClient) SearchTVShows(query string, page int) ([]models.TVShow, error) {
	return c.SearchTVShowsContext(context.Background(), query, page)
}

func (c *TMDBClient) SearchTVShowsContext(ctx context.Context, query string, page int) ([]models.TVShow, error) {
	return c.SearchTVShowsByTitleContext(ctx, query, 0, page)
}

func (c *TMDBClient) SearchTVShowsByTitleContext(ctx context.Context, title string, year, page int) ([]models.TVShow, error) {
	if page < 1 {
		page = 1
	}
	params := c.languageQuery()
	params.Set("query", title)
	params.Set("page", strconv.Itoa(page))
	if year > 0 {
		params.Set("first_air_date_year", strconv.Itoa(year))
	}
	var response struct {
		Page         int             `json:"page"`
		Results      []models.TVShow `json:"results"`
		TotalPages   int             `json:"total_pages"`
		TotalResults int             `json:"total_results"`
	}
	if err := c.doJSON(ctx, "search/tv", params, &response); err != nil {
		return nil, err
	}
	for i := range response.Results {
		prepareTVShow(&response.Results[i], c.imageBaseURL)
	}
	if response.Results == nil {
		response.Results = []models.TVShow{}
	}
	return response.Results, nil
}

// SearchTVShowPagesContext collects consecutive pages up to maxPages.
func (c *TMDBClient) SearchTVShowPagesContext(ctx context.Context, title string, year, maxPages int) ([]models.TVShow, error) {
	results := make([]models.TVShow, 0)
	if maxPages < 0 {
		return results, errors.New("maxPages must not be negative")
	}
	for page := 1; page <= maxPages; page++ {
		shows, err := c.SearchTVShowsByTitleContext(ctx, title, year, page)
		if err != nil {
			return results, err
		}
		if len(shows) == 0 {
			break
		}
		results = append(results, shows...)
	}
	return results, nil
}

func (c *TMDBClient) DiscoverMovies(page int) ([]models.Movie, error) {
	return c.DiscoverMoviesContext(context.Background(), page)
}

func (c *TMDBClient) DiscoverMoviesContext(ctx context.Context, page int) ([]models.Movie, error) {
	if page < 1 {
		page = 1
	}
	params := c.languageQuery()
	params.Set("page", strconv.Itoa(page))
	params.Set("sort_by", configuredSort(c.config.Fetch.Sort.Movies))
	params.Set("include_adult", strconv.FormatBool(c.config.Fetch.IncludeAdult))
	params.Set("include_video", strconv.FormatBool(c.config.Fetch.IncludeVideo))
	if c.config.Fetch.MaxReleaseDate != "" {
		params.Set("release_date.lte", c.config.Fetch.MaxReleaseDate)
	}
	var response struct {
		Results []models.Movie `json:"results"`
	}
	if err := c.doJSON(ctx, "discover/movie", params, &response); err != nil {
		return nil, err
	}
	for i := range response.Results {
		prepareMovie(&response.Results[i], c.imageBaseURL)
	}
	if c.includeTrailers {
		c.fillMovieTrailers(ctx, response.Results)
	}
	if response.Results == nil {
		response.Results = []models.Movie{}
	}
	return response.Results, nil
}

func (c *TMDBClient) DiscoverTVShows(page int) ([]models.TVShow, error) {
	return c.DiscoverTVShowsContext(context.Background(), page)
}

func (c *TMDBClient) DiscoverTVShowsContext(ctx context.Context, page int) ([]models.TVShow, error) {
	if page < 1 {
		page = 1
	}
	params := c.languageQuery()
	params.Set("page", strconv.Itoa(page))
	params.Set("sort_by", configuredSort(c.config.Fetch.Sort.TVShows))
	params.Set("include_adult", strconv.FormatBool(c.config.Fetch.IncludeAdult))
	if c.config.Fetch.MaxReleaseDate != "" {
		params.Set("first_air_date.lte", c.config.Fetch.MaxReleaseDate)
	}
	var response struct {
		Results []models.TVShow `json:"results"`
	}
	if err := c.doJSON(ctx, "discover/tv", params, &response); err != nil {
		return nil, err
	}
	for i := range response.Results {
		prepareTVShow(&response.Results[i], c.imageBaseURL)
	}
	if c.includeTrailers {
		c.fillTVTrailers(ctx, response.Results)
	}
	if response.Results == nil {
		response.Results = []models.TVShow{}
	}
	return response.Results, nil
}

func (c *TMDBClient) fillMovieTrailers(ctx context.Context, movies []models.Movie) {
	c.fillWorkers(ctx, len(movies), func(index int) {
		trailer, err := c.GetMovieTrailerContext(ctx, movies[index].ID)
		if err == nil {
			movies[index].TrailerURL = trailer
		}
	})
}

func (c *TMDBClient) fillTVTrailers(ctx context.Context, shows []models.TVShow) {
	c.fillWorkers(ctx, len(shows), func(index int) {
		trailer, err := c.GetTVShowTrailerContext(ctx, shows[index].ID)
		if err == nil {
			shows[index].TrailerURL = trailer
		}
	})
}

func (c *TMDBClient) fillWorkers(ctx context.Context, count int, work func(int)) {
	workers := cap(c.semaphore)
	if workers > count {
		workers = count
	}
	if workers == 0 {
		return
	}
	jobs := make(chan int)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					continue
				}
				work(index)
			}
		}()
	}
	for index := 0; index < count; index++ {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			group.Wait()
			return
		}
	}
	close(jobs)
	group.Wait()
}

func (c *TMDBClient) GetMovieTrailer(movieID int) (string, error) {
	return c.GetMovieTrailerContext(context.Background(), movieID)
}

func (c *TMDBClient) GetMovieTrailerContext(ctx context.Context, movieID int) (string, error) {
	return c.getTrailerContext(ctx, fmt.Sprintf("movie/%d/videos", movieID))
}

func (c *TMDBClient) GetTVShowTrailer(showID int) (string, error) {
	return c.GetTVShowTrailerContext(context.Background(), showID)
}

func (c *TMDBClient) GetTVShowTrailerContext(ctx context.Context, showID int) (string, error) {
	return c.getTrailerContext(ctx, fmt.Sprintf("tv/%d/videos", showID))
}

func (c *TMDBClient) getTrailerContext(ctx context.Context, path string) (string, error) {
	getForLanguage := func(language string) (string, error) {
		params := url.Values{}
		params.Set("language", language)
		var videos VideoResponse
		if err := c.doJSON(ctx, path, params, &videos); err != nil {
			return "", err
		}
		return chooseTrailer(videos), nil
	}
	trailer, err := getForLanguage(c.language)
	if err != nil || trailer != "" || c.language == c.fallbackLanguage {
		return trailer, err
	}
	return getForLanguage(c.fallbackLanguage)
}

func chooseTrailer(videos VideoResponse) string {
	for _, video := range videos.Results {
		if video.Site == "YouTube" && video.Type == "Trailer" && video.Official && video.Key != "" {
			return "https://www.youtube.com/watch?v=" + url.QueryEscape(video.Key)
		}
	}
	for _, preferredType := range []string{"Trailer", "Teaser", "Clip"} {
		for _, video := range videos.Results {
			if video.Site == "YouTube" && video.Type == preferredType && video.Key != "" {
				return "https://www.youtube.com/watch?v=" + url.QueryEscape(video.Key)
			}
		}
	}
	return ""
}

func (c *TMDBClient) FetchMovieGenres() ([]models.Genre, error) {
	return c.FetchMovieGenresContext(context.Background())
}

func (c *TMDBClient) FetchMovieGenresContext(ctx context.Context) ([]models.Genre, error) {
	return c.fetchGenres(ctx, "genre/movie/list")
}

func (c *TMDBClient) FetchTVShowGenres() ([]models.Genre, error) {
	return c.FetchTVShowGenresContext(context.Background())
}

func (c *TMDBClient) FetchTVShowGenresContext(ctx context.Context) ([]models.Genre, error) {
	return c.fetchGenres(ctx, "genre/tv/list")
}

func (c *TMDBClient) fetchGenres(ctx context.Context, path string) ([]models.Genre, error) {
	var response struct {
		Genres []models.Genre `json:"genres"`
	}
	if err := c.doJSON(ctx, path, c.languageQuery(), &response); err != nil {
		return nil, err
	}
	if response.Genres == nil {
		response.Genres = []models.Genre{}
	}
	return response.Genres, nil
}

func configuredSort(sort config.SortConfig) string {
	field := strings.TrimSpace(sort.Field)
	direction := strings.TrimSpace(sort.Direction)
	if field == "" || direction == "" {
		return "popularity.desc"
	}
	return field + "." + direction
}

type translationPayload struct {
	Translations []struct {
		Language string `json:"iso_639_1"`
		Country  string `json:"iso_3166_1"`
		Data     struct {
			Title    string `json:"title"`
			Name     string `json:"name"`
			Overview string `json:"overview"`
		} `json:"data"`
	} `json:"translations"`
}

type alternativeTitlePayload struct {
	Titles []struct {
		Title   string `json:"title"`
		Name    string `json:"name"`
		Country string `json:"iso_3166_1"`
		Type    string `json:"type"`
	} `json:"titles"`
	Results []struct {
		Title   string `json:"title"`
		Name    string `json:"name"`
		Country string `json:"iso_3166_1"`
		Type    string `json:"type"`
	} `json:"results"`
}

type movieDetailsPayload struct {
	models.Movie
	ExternalIDs      models.ExternalIDs      `json:"external_ids"`
	Translations     translationPayload      `json:"translations"`
	AlternativeNames alternativeTitlePayload `json:"alternative_titles"`
}

type tvDetailsPayload struct {
	models.TVShow
	ExternalIDs      models.ExternalIDs      `json:"external_ids"`
	Translations     translationPayload      `json:"translations"`
	AlternativeNames alternativeTitlePayload `json:"alternative_titles"`
}

func (c *TMDBClient) GetMovieDetails(movieID int) (models.Movie, error) {
	return c.GetMovieDetailsContext(context.Background(), movieID)
}

func (c *TMDBClient) GetMovieDetailsContext(ctx context.Context, movieID int) (models.Movie, error) {
	var payload movieDetailsPayload
	if err := c.doJSON(ctx, fmt.Sprintf("movie/%d", movieID), detailQuery(c.language), &payload); err != nil {
		return models.Movie{}, err
	}
	movie := payload.Movie
	movie.ID = movieIDIfMissing(movie.ID, movieID)
	movie.TMDBID = movie.ID
	movie.IMDbID = payload.ExternalIDs.IMDbID
	movie.AlternativeTitles = movieAlternativeTitles(payload.AlternativeNames)
	localizedTitle, localizedOverview := "", ""
	fallbackTitle, fallbackOverview := "", ""
	for _, translated := range payload.Translations.Translations {
		title := strings.TrimSpace(translated.Data.Title)
		if title == "" {
			title = strings.TrimSpace(translated.Data.Name)
		}
		if title != "" {
			movie.AlternativeTitles = append(movie.AlternativeTitles, models.LocalizedTitle{
				Title: title, Language: translated.Language, Country: translated.Country, Source: "translation",
			})
		}
		if strings.EqualFold(translated.Language+"-"+translated.Country, c.language) {
			localizedTitle, localizedOverview = title, translated.Data.Overview
		}
		if strings.EqualFold(translated.Language+"-"+translated.Country, c.fallbackLanguage) {
			fallbackTitle, fallbackOverview = title, translated.Data.Overview
		}
	}
	if localizedTitle != "" {
		movie.Title = localizedTitle
	} else if movie.Title == "" || normalization.Title(movie.Title) == normalization.Title(movie.OriginalTitle) {
		movie.Title = fallbackTitle
		if movie.Title == "" {
			movie.Title = movie.OriginalTitle
		}
	}
	if localizedOverview != "" {
		movie.Overview = localizedOverview
	} else if movie.Overview == "" {
		movie.Overview = fallbackOverview
	}
	prepareMovie(&movie, c.imageBaseURL)
	return movie, nil
}

func (c *TMDBClient) GetTVShowDetails(tvID int) (models.TVShow, error) {
	return c.GetTVShowDetailsContext(context.Background(), tvID)
}

func (c *TMDBClient) GetTVShowDetailsContext(ctx context.Context, tvID int) (models.TVShow, error) {
	var payload tvDetailsPayload
	if err := c.doJSON(ctx, fmt.Sprintf("tv/%d", tvID), detailQuery(c.language), &payload); err != nil {
		return models.TVShow{}, err
	}
	show := payload.TVShow
	show.ID = movieIDIfMissing(show.ID, tvID)
	show.TMDBID = show.ID
	show.IMDbID = payload.ExternalIDs.IMDbID
	show.AlternativeTitles = tvAlternativeTitles(payload.AlternativeNames)
	localizedTitle, localizedOverview := "", ""
	fallbackTitle, fallbackOverview := "", ""
	for _, translated := range payload.Translations.Translations {
		title := strings.TrimSpace(translated.Data.Name)
		if title == "" {
			title = strings.TrimSpace(translated.Data.Title)
		}
		if title != "" {
			show.AlternativeTitles = append(show.AlternativeTitles, models.LocalizedTitle{
				Title: title, Language: translated.Language, Country: translated.Country, Source: "translation",
			})
		}
		if strings.EqualFold(translated.Language+"-"+translated.Country, c.language) {
			localizedTitle, localizedOverview = title, translated.Data.Overview
		}
		if strings.EqualFold(translated.Language+"-"+translated.Country, c.fallbackLanguage) {
			fallbackTitle, fallbackOverview = title, translated.Data.Overview
		}
	}
	if localizedTitle != "" {
		show.Name = localizedTitle
	} else if show.Name == "" || normalization.Title(show.Name) == normalization.Title(show.OriginalName) {
		show.Name = fallbackTitle
		if show.Name == "" {
			show.Name = show.OriginalName
		}
	}
	if localizedOverview != "" {
		show.Overview = localizedOverview
	} else if show.Overview == "" {
		show.Overview = fallbackOverview
	}
	prepareTVShow(&show, c.imageBaseURL)
	return show, nil
}

func detailQuery(language string) url.Values {
	params := url.Values{}
	params.Set("language", language)
	params.Set("append_to_response", "external_ids,translations,alternative_titles")
	return params
}

func movieIDIfMissing(id, requestedID int) int {
	if id == 0 {
		return requestedID
	}
	return id
}

func movieAlternativeTitles(payload alternativeTitlePayload) []models.LocalizedTitle {
	titles := make([]models.LocalizedTitle, 0, len(payload.Titles)+len(payload.Results))
	for _, title := range payload.Titles {
		name := title.Title
		if name == "" {
			name = title.Name
		}
		if name != "" {
			titles = append(titles, models.LocalizedTitle{Title: name, Country: title.Country, Source: "alternative"})
		}
	}
	return appendAlternativeResults(titles, payload.Results)
}

func tvAlternativeTitles(payload alternativeTitlePayload) []models.LocalizedTitle {
	titles := make([]models.LocalizedTitle, 0, len(payload.Titles)+len(payload.Results))
	for _, title := range payload.Titles {
		name := title.Name
		if name == "" {
			name = title.Title
		}
		if name != "" {
			titles = append(titles, models.LocalizedTitle{Title: name, Country: title.Country, Source: "alternative"})
		}
	}
	return appendAlternativeResults(titles, payload.Results)
}

func appendAlternativeResults(destination []models.LocalizedTitle, source []struct {
	Title   string `json:"title"`
	Name    string `json:"name"`
	Country string `json:"iso_3166_1"`
	Type    string `json:"type"`
}) []models.LocalizedTitle {
	for _, title := range source {
		name := title.Title
		if name == "" {
			name = title.Name
		}
		if name != "" {
			destination = append(destination, models.LocalizedTitle{Title: name, Country: title.Country, Source: "alternative"})
		}
	}
	return destination
}

func (c *TMDBClient) GetMovieExternalIDs(movieID int) (models.ExternalIDs, error) {
	return c.GetMovieExternalIDsContext(context.Background(), movieID)
}

func (c *TMDBClient) GetMovieExternalIDsContext(ctx context.Context, movieID int) (models.ExternalIDs, error) {
	var ids models.ExternalIDs
	err := c.doJSON(ctx, fmt.Sprintf("movie/%d/external_ids", movieID), url.Values{}, &ids)
	return ids, err
}

func (c *TMDBClient) GetTVShowExternalIDs(tvID int) (models.ExternalIDs, error) {
	return c.GetTVShowExternalIDsContext(context.Background(), tvID)
}

func (c *TMDBClient) GetTVShowExternalIDsContext(ctx context.Context, tvID int) (models.ExternalIDs, error) {
	var ids models.ExternalIDs
	err := c.doJSON(ctx, fmt.Sprintf("tv/%d/external_ids", tvID), url.Values{}, &ids)
	return ids, err
}

func (c *TMDBClient) GetSearchTitles(mediaType models.MediaType, tmdbID int) (models.SearchTitles, error) {
	return c.GetSearchTitlesContext(context.Background(), mediaType, tmdbID)
}

func (c *TMDBClient) GetSearchTitlesContext(ctx context.Context, mediaType models.MediaType, tmdbID int) (models.SearchTitles, error) {
	var result models.SearchTitles
	switch mediaType {
	case models.MediaTypeMovie:
		movie, err := c.GetMovieDetailsContext(ctx, tmdbID)
		if err != nil {
			return result, err
		}
		result = models.SearchTitles{TMDBID: movie.ID, IMDbID: movie.IMDbID, MediaType: mediaType, Title: movie.Title,
			OriginalTitle: movie.OriginalTitle, OriginalLanguage: movie.OriginalLanguage,
			ReleaseYear: movie.ReleaseYear, SearchTitles: titleSet(movie.Title, c.language, movie.OriginalTitle, movie.OriginalLanguage, movie.AlternativeTitles)}
	case models.MediaTypeTV:
		show, err := c.GetTVShowDetailsContext(ctx, tmdbID)
		if err != nil {
			return result, err
		}
		result = models.SearchTitles{TMDBID: show.ID, IMDbID: show.IMDbID, MediaType: mediaType, Title: show.Name,
			OriginalTitle: show.OriginalTitle, OriginalName: show.OriginalName, OriginalLanguage: show.OriginalLanguage,
			ReleaseYear: show.ReleaseYear, SearchTitles: titleSet(show.Name, c.language, show.OriginalName, show.OriginalLanguage, show.AlternativeTitles)}
	default:
		return result, fmt.Errorf("unsupported media type %q", mediaType)
	}
	return result, nil
}

func titleSet(current, currentLanguage, original, originalLanguage string, alternatives []models.LocalizedTitle) []models.LocalizedTitle {
	result := make([]models.LocalizedTitle, 0, len(alternatives)+2)
	seen := make(map[string]bool)
	add := func(title, language, source, country string) {
		normalized := normalization.Title(title)
		if normalized == "" || seen[normalized] {
			return
		}
		seen[normalized] = true
		result = append(result, models.LocalizedTitle{Title: strings.TrimSpace(title), Language: language, Source: source, Country: country})
	}
	add(current, currentLanguage, "localized", "")
	add(original, originalLanguage, "original", "")
	for _, title := range alternatives {
		add(title.Title, title.Language, title.Source, title.Country)
	}
	return result
}

func (c *TMDBClient) FindByIMDbID(imdbID string) ([]models.IdentificationCandidate, error) {
	return c.FindByIMDbIDContext(context.Background(), imdbID)
}

func (c *TMDBClient) FindByIMDbIDContext(ctx context.Context, imdbID string) ([]models.IdentificationCandidate, error) {
	imdbID = strings.TrimSpace(imdbID)
	if !isIMDbID(imdbID) {
		return nil, fmt.Errorf("invalid IMDb ID %q", imdbID)
	}
	params := c.languageQuery()
	params.Set("external_source", "imdb_id")
	var response struct {
		Movies []models.Movie  `json:"movie_results"`
		TV     []models.TVShow `json:"tv_results"`
	}
	if err := c.doJSON(ctx, "find/"+url.PathEscape(imdbID), params, &response); err != nil {
		return nil, err
	}
	candidates := make([]models.IdentificationCandidate, 0, len(response.Movies)+len(response.TV))
	for i := range response.Movies {
		movie := response.Movies[i]
		prepareMovie(&movie, c.imageBaseURL)
		candidate := candidateFromMovie(movie, 1)
		candidate.IMDbID = imdbID
		candidates = append(candidates, candidate)
	}
	for i := range response.TV {
		show := response.TV[i]
		prepareTVShow(&show, c.imageBaseURL)
		candidate := candidateFromTV(show, 1)
		candidate.IMDbID = imdbID
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func isIMDbID(id string) bool {
	if len(id) < 3 || !strings.HasPrefix(strings.ToLower(id), "tt") {
		return false
	}
	for _, character := range id[2:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (c *TMDBClient) IdentifyMedia(title string, mediaType models.MediaType, year int) (models.IdentificationResult, error) {
	return c.IdentifyMediaContext(context.Background(), title, mediaType, year)
}

func (c *TMDBClient) IdentifyMediaContext(ctx context.Context, title string, mediaType models.MediaType, year int) (models.IdentificationResult, error) {
	result := models.IdentificationResult{Query: title, MediaType: mediaType, Year: year, Candidates: []models.IdentificationCandidate{}}
	if normalization.Title(title) == "" {
		return result, fmt.Errorf("title must not be empty")
	}
	var candidates []models.IdentificationCandidate
	switch mediaType {
	case models.MediaTypeMovie:
		movies, err := c.SearchMoviesByTitleContext(ctx, title, year, 1)
		if err != nil {
			return result, err
		}
		for i := range movies {
			movie := movies[i]
			score := identificationScore(title, movie.Title, movie.OriginalTitle, movie.ReleaseYear, year)
			candidates = append(candidates, candidateFromMovie(movie, score))
		}
	case models.MediaTypeTV:
		shows, err := c.SearchTVShowsByTitleContext(ctx, title, year, 1)
		if err != nil {
			return result, err
		}
		for i := range shows {
			show := shows[i]
			score := identificationScore(title, show.Name, show.OriginalName, show.ReleaseYear, year)
			candidates = append(candidates, candidateFromTV(show, score))
		}
	default:
		return result, fmt.Errorf("unsupported media type %q", mediaType)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Confidence > candidates[j].Confidence })
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	result.Candidates = candidates
	if len(candidates) == 0 {
		return result, nil
	}
	margin := 1.0
	if len(candidates) > 1 {
		margin = candidates[0].Confidence - candidates[1].Confidence
	}
	threshold := 0.85
	if year > 0 {
		threshold = 0.82
	}
	if candidates[0].Confidence >= threshold && margin >= 0.08 {
		selected := candidates[0]
		switch mediaType {
		case models.MediaTypeMovie:
			movie, err := c.GetMovieDetailsContext(ctx, selected.TMDBID)
			if err != nil {
				return result, err
			}
			selected = candidateFromMovie(movie, selected.Confidence)
		case models.MediaTypeTV:
			show, err := c.GetTVShowDetailsContext(ctx, selected.TMDBID)
			if err != nil {
				return result, err
			}
			selected = candidateFromTV(show, selected.Confidence)
		}
		result.Candidates[0] = selected
		result.Conclusive = true
		result.Candidate = &selected
	}
	return result, nil
}

func identificationScore(query, localized, original string, foundYear, requestedYear int) float64 {
	titleScore := titleSimilarity(query, localized)
	if originalScore := titleSimilarity(query, original); originalScore > titleScore {
		titleScore = originalScore
	}
	if requestedYear == 0 {
		return titleScore
	}
	yearScore := 0.0
	if foundYear == requestedYear {
		yearScore = 1
	}
	return titleScore*0.8 + yearScore*0.2
}

func titleSimilarity(left, right string) float64 {
	left = normalization.Title(left)
	right = normalization.Title(right)
	if left == "" || right == "" {
		return 0
	}
	if left == right {
		return 1
	}
	leftRunes, rightRunes := []rune(left), []rune(right)
	maxLen := len(leftRunes)
	if len(rightRunes) > maxLen {
		maxLen = len(rightRunes)
	}
	editSimilarity := 1 - float64(levenshtein(leftRunes, rightRunes))/float64(maxLen)
	leftTokens, rightTokens := strings.Fields(left), strings.Fields(right)
	leftSet := make(map[string]bool, len(leftTokens))
	for _, token := range leftTokens {
		leftSet[token] = true
	}
	intersection := 0
	for _, token := range rightTokens {
		if leftSet[token] {
			intersection++
		}
	}
	union := len(leftSet)
	for _, token := range rightTokens {
		if !leftSet[token] {
			union++
		}
	}
	tokenSimilarity := float64(intersection) / float64(union)
	return editSimilarity*0.6 + tokenSimilarity*0.4
}

func levenshtein(left, right []rune) int {
	previous := make([]int, len(right)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, leftRune := range left {
		current := make([]int, len(right)+1)
		current[0] = i + 1
		for j, rightRune := range right {
			cost := 0
			if leftRune != rightRune {
				cost = 1
			}
			current[j+1] = min(current[j]+1, previous[j+1]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(right)]
}

func min(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func candidateFromMovie(movie models.Movie, confidence float64) models.IdentificationCandidate {
	return models.IdentificationCandidate{TMDBID: movie.ID, IMDbID: movie.IMDbID, MediaType: models.MediaTypeMovie,
		Title: movie.Title, OriginalTitle: movie.OriginalTitle, OriginalLanguage: movie.OriginalLanguage,
		ReleaseYear: movie.ReleaseYear, Confidence: confidence}
}

func candidateFromTV(show models.TVShow, confidence float64) models.IdentificationCandidate {
	return models.IdentificationCandidate{TMDBID: show.ID, IMDbID: show.IMDbID, MediaType: models.MediaTypeTV,
		Title: show.Name, OriginalTitle: show.OriginalTitle, OriginalName: show.OriginalName,
		OriginalLanguage: show.OriginalLanguage, ReleaseYear: show.ReleaseYear, Confidence: confidence}
}

func prepareMovie(movie *models.Movie, imageBaseURL string) {
	if movie.ID != 0 {
		movie.TMDBID = movie.ID
	}
	if movie.ReleaseYear == 0 {
		movie.ReleaseYear = yearFromDate(movie.ReleaseDate)
	}
	movie.PosterPath = absoluteImageURL(imageBaseURL, movie.PosterPath)
	movie.BackdropPath = absoluteImageURL(imageBaseURL, movie.BackdropPath)
}

func prepareTVShow(show *models.TVShow, imageBaseURL string) {
	if show.ID != 0 {
		show.TMDBID = show.ID
	}
	if show.ReleaseYear == 0 {
		show.ReleaseYear = yearFromDate(show.FirstAirDate)
	}
	show.PosterPath = absoluteImageURL(imageBaseURL, show.PosterPath)
	show.BackdropPath = absoluteImageURL(imageBaseURL, show.BackdropPath)
}

func absoluteImageURL(base, path string) string {
	path = strings.TrimSpace(path)
	if path == "" || strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
		return path
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func yearFromDate(value string) int {
	if len(value) < 4 {
		return 0
	}
	year, _ := strconv.Atoi(value[:4])
	return year
}

package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

// PageFailure records a page that failed after the HTTP retry policy was exhausted.
type PageFailure struct {
	Page  int
	Error error
}

// CollectionReport contains lightweight progress and recovery metrics for a paged collection.
type CollectionReport struct {
	StartedAt      time.Time
	FinishedAt     time.Time
	Duration       time.Duration
	PagesRequested int
	PagesSucceeded int
	FailedPages    int
	ItemsProcessed int
	Failures       []PageFailure
}

// CollectMoviesContext fetches consecutive discover pages, bounded by maxPages.
// If maxPages is zero, Fetch.NumPages is used. Failed pages are recorded and later
// pages are still attempted; partial results and an aggregated error are returned.
func (c *TMDBClient) CollectMoviesContext(ctx context.Context, maxPages int) ([]models.Movie, CollectionReport, error) {
	return collectPages(ctx, maxPages, c.config.Fetch.NumPages, func(ctx context.Context, page int) ([]models.Movie, error) {
		return c.DiscoverMoviesContext(ctx, page)
	})
}

// CollectTVShowsContext fetches consecutive discover pages, bounded by maxPages.
// If maxPages is zero, Fetch.NumPages is used. Failed pages are recorded and later
// pages are still attempted; partial results and an aggregated error are returned.
func (c *TMDBClient) CollectTVShowsContext(ctx context.Context, maxPages int) ([]models.TVShow, CollectionReport, error) {
	return collectPages(ctx, maxPages, c.config.Fetch.NumPages, func(ctx context.Context, page int) ([]models.TVShow, error) {
		return c.DiscoverTVShowsContext(ctx, page)
	})
}

// CollectMoviesProgressiveContext processes one page at a time and never retains
// previous pages. Callback errors stop the run and identify the page for retry.
func (c *TMDBClient) CollectMoviesProgressiveContext(ctx context.Context, maxPages int, process func(context.Context, int, []models.Movie) error) (CollectionReport, error) {
	return processPagesProgressively(ctx, maxPages, c.config.Fetch.NumPages,
		func(ctx context.Context, page int) ([]models.Movie, error) { return c.DiscoverMoviesContext(ctx, page) }, process)
}

// CollectTVShowsProgressiveContext processes one page at a time and never retains
// previous pages. Callback errors stop the run and identify the page for retry.
func (c *TMDBClient) CollectTVShowsProgressiveContext(ctx context.Context, maxPages int, process func(context.Context, int, []models.TVShow) error) (CollectionReport, error) {
	return processPagesProgressively(ctx, maxPages, c.config.Fetch.NumPages,
		func(ctx context.Context, page int) ([]models.TVShow, error) {
			return c.DiscoverTVShowsContext(ctx, page)
		}, process)
}

// ProcessMoviePagesContext replays only the supplied distinct pages, typically
// from a previous CollectionReport.Failures value.
func (c *TMDBClient) ProcessMoviePagesContext(ctx context.Context, pages []int, process func(context.Context, int, []models.Movie) error) (CollectionReport, error) {
	return processSpecificPages(ctx, pages,
		func(ctx context.Context, page int) ([]models.Movie, error) { return c.DiscoverMoviesContext(ctx, page) }, process)
}

// ProcessTVShowPagesContext replays only the supplied distinct pages.
func (c *TMDBClient) ProcessTVShowPagesContext(ctx context.Context, pages []int, process func(context.Context, int, []models.TVShow) error) (CollectionReport, error) {
	return processSpecificPages(ctx, pages,
		func(ctx context.Context, page int) ([]models.TVShow, error) {
			return c.DiscoverTVShowsContext(ctx, page)
		}, process)
}

func collectPages[T any](
	ctx context.Context,
	maxPages, configuredPages int,
	fetch func(context.Context, int) ([]T, error),
) ([]T, CollectionReport, error) {
	report := CollectionReport{StartedAt: time.Now().UTC(), Failures: []PageFailure{}}
	results := make([]T, 0)
	if maxPages < 0 {
		report.FinishedAt = time.Now().UTC()
		report.Duration = report.FinishedAt.Sub(report.StartedAt)
		return results, report, errors.New("maxPages must not be negative")
	}
	if maxPages > maxSearchPages || configuredPages > maxSearchPages {
		report.FinishedAt = time.Now().UTC()
		report.Duration = report.FinishedAt.Sub(report.StartedAt)
		return results, report, fmt.Errorf("maxPages cannot exceed %d", maxSearchPages)
	}
	if maxPages == 0 {
		maxPages = configuredPages
	}
	if maxPages <= 0 {
		report.FinishedAt = time.Now().UTC()
		report.Duration = report.FinishedAt.Sub(report.StartedAt)
		return results, report, errors.New("maxPages must be positive or set Fetch.NumPages")
	}

	var pageErrors []error
	for page := 1; page <= maxPages; page++ {
		if err := ctx.Err(); err != nil {
			report.FinishedAt = time.Now().UTC()
			report.Duration = report.FinishedAt.Sub(report.StartedAt)
			return results, report, errors.Join(errors.Join(pageErrors...), err)
		}
		report.PagesRequested++
		items, err := fetch(ctx, page)
		if err != nil {
			pageError := fmt.Errorf("page %d: %w", page, err)
			report.Failures = append(report.Failures, PageFailure{Page: page, Error: err})
			report.FailedPages++
			pageErrors = append(pageErrors, pageError)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		report.PagesSucceeded++
		if len(items) == 0 {
			break
		}
		results = append(results, items...)
		report.ItemsProcessed += len(items)
	}
	report.FinishedAt = time.Now().UTC()
	report.Duration = report.FinishedAt.Sub(report.StartedAt)
	return results, report, errors.Join(pageErrors...)
}

func processPagesProgressively[T any](
	ctx context.Context,
	maxPages, configuredPages int,
	fetch func(context.Context, int) ([]T, error),
	process func(context.Context, int, []T) error,
) (CollectionReport, error) {
	report := CollectionReport{StartedAt: time.Now().UTC(), Failures: []PageFailure{}}
	if process == nil {
		return finishReport(report), errors.New("page callback is nil")
	}
	if maxPages < 0 {
		return finishReport(report), errors.New("maxPages must not be negative")
	}
	if maxPages > maxSearchPages || configuredPages > maxSearchPages {
		return finishReport(report), fmt.Errorf("maxPages cannot exceed %d", maxSearchPages)
	}
	if maxPages == 0 {
		maxPages = configuredPages
	}
	if maxPages <= 0 {
		return finishReport(report), errors.New("maxPages must be positive or set Fetch.NumPages")
	}
	var failures []error
	for page := 1; page <= maxPages; page++ {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		report.PagesRequested++
		items, err := fetch(ctx, page)
		if err != nil {
			report.Failures = append(report.Failures, PageFailure{Page: page, Error: err})
			report.FailedPages++
			failures = append(failures, fmt.Errorf("page %d: %w", page, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if len(items) == 0 {
			report.PagesSucceeded++
			break
		}
		if err := process(ctx, page, items); err != nil {
			report.Failures = append(report.Failures, PageFailure{Page: page, Error: err})
			report.FailedPages++
			failures = append(failures, fmt.Errorf("process page %d: %w", page, err))
			break
		}
		report.PagesSucceeded++
		report.ItemsProcessed += len(items)
	}
	return finishReport(report), errors.Join(failures...)
}

func processSpecificPages[T any](
	ctx context.Context,
	pages []int,
	fetch func(context.Context, int) ([]T, error),
	process func(context.Context, int, []T) error,
) (CollectionReport, error) {
	report := CollectionReport{StartedAt: time.Now().UTC(), Failures: []PageFailure{}}
	if process == nil {
		return finishReport(report), errors.New("page callback is nil")
	}
	unique := make(map[int]bool, len(pages))
	ordered := make([]int, 0, len(pages))
	for _, page := range pages {
		if page < 1 {
			return finishReport(report), fmt.Errorf("page numbers must be positive: %d", page)
		}
		if page > maxSearchPages {
			return finishReport(report), fmt.Errorf("page cannot exceed %d", maxSearchPages)
		}
		if !unique[page] {
			unique[page] = true
			ordered = append(ordered, page)
		}
	}
	sort.Ints(ordered)
	var failures []error
	for _, page := range ordered {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		report.PagesRequested++
		items, err := fetch(ctx, page)
		if err == nil {
			err = process(ctx, page, items)
		}
		if err != nil {
			report.FailedPages++
			report.Failures = append(report.Failures, PageFailure{Page: page, Error: err})
			failures = append(failures, fmt.Errorf("page %d: %w", page, err))
			continue
		}
		report.PagesSucceeded++
		report.ItemsProcessed += len(items)
	}
	return finishReport(report), errors.Join(failures...)
}

func finishReport(report CollectionReport) CollectionReport {
	report.FinishedAt = time.Now().UTC()
	report.Duration = report.FinishedAt.Sub(report.StartedAt)
	return report
}

package api

import (
	"context"
	"errors"
	"fmt"
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

package api

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

const (
	MaxChangeWindowDays = 14
	MaxChangePages      = 500
)

// ChangedIDsPage contains TMDB IDs returned for one changes-list page.
type ChangedIDsPage struct {
	MediaType    models.MediaType `json:"media_type"`
	IDs          []int            `json:"ids"`
	Page         int              `json:"page"`
	TotalPages   int              `json:"total_pages"`
	TotalResults int              `json:"total_results"`
}

// GetMovieChangesContext lists movie IDs changed in the inclusive date interval.
// The official endpoint limits each interval to 14 days and returns 100 IDs/page.
func (c *TMDBClient) GetMovieChangesContext(ctx context.Context, startDate, endDate time.Time, page int) (ChangedIDsPage, error) {
	return c.getChangedIDsContext(ctx, models.MediaTypeMovie, "movie/changes", startDate, endDate, page)
}

// GetTVShowChangesContext lists TV series IDs changed in the inclusive date interval.
func (c *TMDBClient) GetTVShowChangesContext(ctx context.Context, startDate, endDate time.Time, page int) (ChangedIDsPage, error) {
	return c.getChangedIDsContext(ctx, models.MediaTypeTV, "tv/changes", startDate, endDate, page)
}

func (c *TMDBClient) getChangedIDsContext(ctx context.Context, mediaType models.MediaType, path string, startDate, endDate time.Time, page int) (ChangedIDsPage, error) {
	result := ChangedIDsPage{MediaType: mediaType}
	start, end, err := formatChangeWindow(startDate, endDate)
	if err != nil {
		return result, err
	}
	if page < 0 {
		return result, fmt.Errorf("page must be positive")
	}
	if page == 0 {
		page = 1
	}
	if page > MaxChangePages {
		return result, fmt.Errorf("page cannot exceed TMDB's %d-page limit", MaxChangePages)
	}
	params := url.Values{}
	params.Set("start_date", start)
	params.Set("end_date", end)
	params.Set("page", strconv.Itoa(page))
	var response struct {
		Page         int `json:"page"`
		TotalPages   int `json:"total_pages"`
		TotalResults int `json:"total_results"`
		Results      []struct {
			ID int `json:"id"`
		} `json:"results"`
	}
	if err := c.doJSONFresh(ctx, path, params, &response); err != nil {
		return result, err
	}
	result.Page = response.Page
	result.TotalPages = response.TotalPages
	result.TotalResults = response.TotalResults
	result.IDs = make([]int, 0, len(response.Results))
	for _, item := range response.Results {
		if item.ID > 0 {
			result.IDs = append(result.IDs, item.ID)
		}
	}
	return result, nil
}

func formatChangeWindow(startDate, endDate time.Time) (string, string, error) {
	if startDate.IsZero() || endDate.IsZero() {
		return "", "", fmt.Errorf("start and end dates are required")
	}
	startDate = utcDate(startDate)
	endDate = utcDate(endDate)
	if endDate.Before(startDate) {
		return "", "", fmt.Errorf("end date precedes start date")
	}
	days := int(endDate.Sub(startDate)/(24*time.Hour)) + 1
	if days > MaxChangeWindowDays {
		return "", "", fmt.Errorf("change window is %d days; maximum is %d", days, MaxChangeWindowDays)
	}
	return startDate.Format("2006-01-02"), endDate.Format("2006-01-02"), nil
}

func utcDate(value time.Time) time.Time {
	year, month, day := value.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

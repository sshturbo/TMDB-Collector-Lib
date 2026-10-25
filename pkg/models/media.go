package models

import "time"

// MediaType identifies the kind of content handled by TMDB.
type MediaType string

const (
	MediaTypeMovie MediaType = "movie"
	MediaTypeTV    MediaType = "tv"
)

// LocalizedTitle keeps the language and origin of a title used for search.
type LocalizedTitle struct {
	Title    string `json:"title"`
	Language string `json:"language,omitempty"`
	Country  string `json:"country,omitempty"`
	Source   string `json:"source,omitempty"`
}

// ExternalIDs contains optional identifiers provided by external catalogs.
type ExternalIDs struct {
	IMDbID string `json:"imdb_id,omitempty"`
}

type Movie struct {
	ID                        int              `json:"id"`
	TMDBID                    int              `json:"tmdb_id,omitempty"`
	Title                     string           `json:"title"`
	OriginalTitle             string           `json:"original_title,omitempty"`
	OriginalLanguage          string           `json:"original_language,omitempty"`
	IMDbID                    string           `json:"imdb_id,omitempty"`
	ReleaseYear               int              `json:"release_year,omitempty"`
	Overview                  string           `json:"overview"`
	ReleaseDate               string           `json:"release_date"`
	PosterPath                string           `json:"poster_path"`
	BackdropPath              string           `json:"backdrop_path"`
	VoteAverage               float64          `json:"vote_average"`
	TrailerURL                string           `json:"trailer_url"`
	Popularity                float64          `json:"popularity"`
	CreatedAt                 time.Time        `json:"created_at"`
	LastSyncedAt              time.Time        `json:"last_synced_at,omitempty"`
	GenreIDs                  []int            `json:"genre_ids"`
	AlternativeTitles         []LocalizedTitle `json:"alternative_titles,omitempty"`
	AlternativeTitlesComplete bool             `json:"-"`
	DetailsComplete           bool             `json:"-"`
}

type TVShow struct {
	ID                        int              `json:"id"`
	TMDBID                    int              `json:"tmdb_id,omitempty"`
	Name                      string           `json:"name"`
	OriginalName              string           `json:"original_name,omitempty"`
	OriginalTitle             string           `json:"original_title,omitempty"`
	OriginalLanguage          string           `json:"original_language,omitempty"`
	IMDbID                    string           `json:"imdb_id,omitempty"`
	ReleaseYear               int              `json:"release_year,omitempty"`
	Overview                  string           `json:"overview"`
	FirstAirDate              string           `json:"first_air_date"`
	PosterPath                string           `json:"poster_path"`
	BackdropPath              string           `json:"backdrop_path"`
	VoteAverage               float64          `json:"vote_average"`
	TrailerURL                string           `json:"trailer_url"`
	Popularity                float64          `json:"popularity"`
	CreatedAt                 time.Time        `json:"created_at"`
	LastSyncedAt              time.Time        `json:"last_synced_at,omitempty"`
	GenreIDs                  []int            `json:"genre_ids"`
	AlternativeTitles         []LocalizedTitle `json:"alternative_titles,omitempty"`
	AlternativeTitlesComplete bool             `json:"-"`
	DetailsComplete           bool             `json:"-"`
}

// SearchTitles is a normalized set of useful names for one TMDB record.
type SearchTitles struct {
	TMDBID           int              `json:"tmdb_id"`
	IMDbID           string           `json:"imdb_id,omitempty"`
	MediaType        MediaType        `json:"media_type"`
	Title            string           `json:"title"`
	OriginalTitle    string           `json:"original_title,omitempty"`
	OriginalName     string           `json:"original_name,omitempty"`
	OriginalLanguage string           `json:"original_language,omitempty"`
	ReleaseYear      int              `json:"release_year,omitempty"`
	SearchTitles     []LocalizedTitle `json:"search_titles"`
}

// IdentificationCandidate is one scored TMDB search result.
type IdentificationCandidate struct {
	TMDBID           int       `json:"tmdb_id"`
	IMDbID           string    `json:"imdb_id,omitempty"`
	MediaType        MediaType `json:"media_type"`
	Title            string    `json:"title"`
	OriginalTitle    string    `json:"original_title,omitempty"`
	OriginalName     string    `json:"original_name,omitempty"`
	OriginalLanguage string    `json:"original_language,omitempty"`
	ReleaseYear      int       `json:"release_year,omitempty"`
	Confidence       float64   `json:"confidence"`
}

// IdentificationResult is conclusive only when there is a clear top candidate.
type IdentificationResult struct {
	Query      string                    `json:"query"`
	MediaType  MediaType                 `json:"media_type"`
	Year       int                       `json:"year,omitempty"`
	Conclusive bool                      `json:"conclusive"`
	Candidate  *IdentificationCandidate  `json:"candidate,omitempty"`
	Candidates []IdentificationCandidate `json:"candidates"`
}

type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type MovieGenre struct {
	MovieID int
	GenreID int
}

type TVShowGenre struct {
	TVShowID int
	GenreID  int
}

package models

import "time"

type Movie struct {
	ID           int       `json:"id"`
	Title        string    `json:"title"`
	Overview     string    `json:"overview"`
	ReleaseDate  string    `json:"release_date"`
	PosterPath   string    `json:"poster_path"`
	BackdropPath string    `json:"backdrop_path"`
	VoteAverage  float64   `json:"vote_average"`
	TrailerURL   string    `json:"trailer_url"`
	Popularity   float64   `json:"popularity"`
	CreatedAt    time.Time `json:"created_at"`
	GenreIDs     []int     `json:"genre_ids"`
}

type TVShow struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	Overview     string    `json:"overview"`
	FirstAirDate string    `json:"first_air_date"`
	PosterPath   string    `json:"poster_path"`
	BackdropPath string    `json:"backdrop_path"`
	VoteAverage  float64   `json:"vote_average"`
	TrailerURL   string    `json:"trailer_url"`
	Popularity   float64   `json:"popularity"`
	CreatedAt    time.Time `json:"created_at"`
	GenreIDs     []int     `json:"genre_ids"`
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

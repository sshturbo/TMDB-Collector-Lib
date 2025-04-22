package database

import (
	"database/sql"
	"time"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"

	_ "github.com/mattn/go-sqlite3"
)

type Database struct {
	db *sql.DB
}

func NewDatabase(dbPath string) (*Database, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		return nil, err
	}

	return &Database{db: db}, nil
}

func (d *Database) SaveMovie(movie *models.Movie) error {
	query := `INSERT OR REPLACE INTO movies 
        (id, title, overview, release_date, poster_path, backdrop_path, vote_average, trailer_url, popularity, created_at) 
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	movie.CreatedAt = time.Now()
	_, err := d.db.Exec(query, movie.ID, movie.Title, movie.Overview,
		movie.ReleaseDate, movie.PosterPath, movie.BackdropPath, movie.VoteAverage, movie.TrailerURL,
		movie.Popularity, movie.CreatedAt)
	return err
}

func (d *Database) SaveTVShow(show *models.TVShow) error {
	query := `INSERT OR REPLACE INTO tv_shows 
        (id, name, overview, first_air_date, poster_path, backdrop_path, vote_average, trailer_url, popularity, created_at) 
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	show.CreatedAt = time.Now()
	_, err := d.db.Exec(query, show.ID, show.Name, show.Overview,
		show.FirstAirDate, show.PosterPath, show.BackdropPath, show.VoteAverage, show.TrailerURL,
		show.Popularity, show.CreatedAt)
	return err
}

func (d *Database) SaveMoviesBulk(movies []models.Movie) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO movies 
		(id, title, overview, release_date, poster_path, backdrop_path, vote_average, trailer_url, popularity, created_at) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	now := time.Now()
	for _, movie := range movies {
		movie.CreatedAt = now
		_, err := stmt.Exec(movie.ID, movie.Title, movie.Overview,
			movie.ReleaseDate, movie.PosterPath, movie.BackdropPath, movie.VoteAverage, movie.TrailerURL,
			movie.Popularity, movie.CreatedAt)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (d *Database) SaveTVShowsBulk(shows []models.TVShow) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO tv_shows 
		(id, name, overview, first_air_date, poster_path, backdrop_path, vote_average, trailer_url, popularity, created_at) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	now := time.Now()
	for _, show := range shows {
		show.CreatedAt = now
		_, err := stmt.Exec(show.ID, show.Name, show.Overview,
			show.FirstAirDate, show.PosterPath, show.BackdropPath, show.VoteAverage, show.TrailerURL,
			show.Popularity, show.CreatedAt)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (d *Database) SaveGenres(genres []models.Genre) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO genres (id, name) VALUES (?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, genre := range genres {
		_, err := stmt.Exec(genre.ID, genre.Name)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (d *Database) SaveMovieGenres(movieID int, genreIDs []int) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO movie_genres (movie_id, genre_id) VALUES (?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, genreID := range genreIDs {
		_, err := stmt.Exec(movieID, genreID)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (d *Database) SaveTVShowGenres(tvShowID int, genreIDs []int) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO tvshow_genres (tvshow_id, genre_id) VALUES (?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, genreID := range genreIDs {
		_, err := stmt.Exec(tvShowID, genreID)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (d *Database) SaveMovieGenresBulk(relations []models.MovieGenre) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO movie_genres (movie_id, genre_id) VALUES (?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, rel := range relations {
		_, err := stmt.Exec(rel.MovieID, rel.GenreID)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (d *Database) SaveTVShowGenresBulk(relations []models.TVShowGenre) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO tvshow_genres (tvshow_id, genre_id) VALUES (?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, rel := range relations {
		_, err := stmt.Exec(rel.TVShowID, rel.GenreID)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func NewDatabaseFromDB(db *sql.DB) *Database {
	return &Database{db: db}
}

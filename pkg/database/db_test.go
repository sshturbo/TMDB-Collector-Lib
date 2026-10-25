package database

import (
	"database/sql"
	"testing"
	"time"

	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

func TestMigrationPreservesExistingRowsAndRelationshipsOnUpsert(t *testing.T) {
	dbConn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer dbConn.Close()
	dbConn.SetMaxOpenConns(1)

	legacySchema := []string{
		`CREATE TABLE movies (id INTEGER PRIMARY KEY, title TEXT NOT NULL, overview TEXT, release_date TEXT, poster_path TEXT, backdrop_path TEXT, vote_average REAL, trailer_url TEXT, popularity REAL, created_at DATETIME)`,
		`CREATE TABLE tv_shows (id INTEGER PRIMARY KEY, name TEXT NOT NULL, overview TEXT, first_air_date TEXT, poster_path TEXT, backdrop_path TEXT, vote_average REAL, trailer_url TEXT, popularity REAL, created_at DATETIME)`,
		`CREATE TABLE genres (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE movie_genres (movie_id INTEGER, genre_id INTEGER, PRIMARY KEY (movie_id, genre_id))`,
		`CREATE TABLE tvshow_genres (tvshow_id INTEGER, genre_id INTEGER, PRIMARY KEY (tvshow_id, genre_id))`,
		`INSERT INTO movies (id,title,overview,release_date) VALUES (10,'Old title','Old overview','2000-01-01')`,
		`INSERT INTO genres (id,name) VALUES (2,'Drama')`,
		`INSERT INTO movie_genres (movie_id,genre_id) VALUES (10,2)`,
	}
	for _, statement := range legacySchema {
		if _, err := dbConn.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	db, err := NewDatabaseFromDBWithMigrations(dbConn)
	if err != nil {
		t.Fatal(err)
	}
	var oldTitle, oldOverview string
	if err := dbConn.QueryRow(`SELECT title, overview FROM movies WHERE id = 10`).Scan(&oldTitle, &oldOverview); err != nil {
		t.Fatal(err)
	}
	if oldTitle != "Old title" || oldOverview != "Old overview" {
		t.Fatalf("migration changed existing row: %q %q", oldTitle, oldOverview)
	}

	created := time.Date(2020, 2, 3, 4, 5, 6, 0, time.UTC)
	movie := models.Movie{
		ID: 10, Title: "Localized title", OriginalTitle: "Original title", OriginalLanguage: "en",
		IMDbID: "tt1234567", ReleaseYear: 2020, ReleaseDate: "2020-01-02", CreatedAt: created,
		AlternativeTitles: []models.LocalizedTitle{{Title: "Título alternativo", Language: "pt", Country: "BR", Source: "translation"}},
	}
	if err := db.SaveMovie(&movie); err != nil {
		t.Fatal(err)
	}
	var title, imdbID, originalTitle, language string
	var year int
	if err := dbConn.QueryRow(`SELECT title, imdb_id, original_title, original_language, release_year FROM movies WHERE id = 10`).Scan(&title, &imdbID, &originalTitle, &language, &year); err != nil {
		t.Fatal(err)
	}
	if title != "Localized title" || imdbID != "tt1234567" || originalTitle != "Original title" || language != "en" || year != 2020 {
		t.Fatalf("new metadata was not persisted: title=%q imdb=%q original=%q language=%q year=%d", title, imdbID, originalTitle, language, year)
	}
	var relations int
	if err := dbConn.QueryRow(`SELECT COUNT(*) FROM movie_genres WHERE movie_id = 10 AND genre_id = 2`).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if relations != 1 {
		t.Fatal("upsert removed an existing movie/genre relationship")
	}
	var alternative, alternativeLanguage string
	if err := dbConn.QueryRow(`SELECT title, language FROM media_titles WHERE media_type='movie' AND tmdb_id=10`).Scan(&alternative, &alternativeLanguage); err != nil {
		t.Fatal(err)
	}
	if alternative != "Título alternativo" || alternativeLanguage != "pt" {
		t.Fatalf("localized title metadata missing: %q %q", alternative, alternativeLanguage)
	}

	// Partial/search updates with omitted identifiers preserve richer metadata.
	if err := db.SaveMovie(&models.Movie{ID: 10, Title: "Updated search title"}); err != nil {
		t.Fatal(err)
	}
	if err := dbConn.QueryRow(`SELECT imdb_id, original_title FROM movies WHERE id=10`).Scan(&imdbID, &originalTitle); err != nil {
		t.Fatal(err)
	}
	if imdbID != "tt1234567" || originalTitle != "Original title" {
		t.Fatalf("partial update erased metadata: imdb=%q original=%q", imdbID, originalTitle)
	}
}

func TestMigrateIsIdempotentAndRelationSaveUpdatesAssociations(t *testing.T) {
	db, err := NewDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var foreignKeys int
	if err := db.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("SQLite foreign key enforcement is off: %d", foreignKeys)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMovie(&models.Movie{ID: 1, Title: "Film"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveGenres([]models.Genre{{ID: 1, Name: "Drama"}, {ID: 2, Name: "Comedy"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMovieGenres(1, []int{1, 2, 2}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMovieGenres(1, []int{2}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM movie_genres WHERE movie_id=1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("relationship update left stale/duplicate rows: %d", count)
	}
	if err := db.SaveTVShow(&models.TVShow{ID: 9, Name: "Show", OriginalName: "Original show", IMDbID: "tt8765432"}); err != nil {
		t.Fatal(err)
	}
	var storedIMDb, originalName string
	if err := db.db.QueryRow(`SELECT imdb_id, original_name FROM tv_shows WHERE id=9`).Scan(&storedIMDb, &originalName); err != nil {
		t.Fatal(err)
	}
	if storedIMDb != "tt8765432" || originalName != "Original show" {
		t.Fatalf("TV show metadata missing: %q %q", storedIMDb, originalName)
	}
}

func TestBulkSaveRollsBackOnInvalidItem(t *testing.T) {
	db, err := NewDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.SaveMoviesBulk([]models.Movie{{ID: 1, Title: "Valid"}, {Title: "Missing ID"}})
	if err == nil {
		t.Fatal("expected invalid bulk item to fail")
	}
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM movies`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("bulk transaction partially committed %d rows", count)
	}
}

func TestNewDatabaseFromDBDoesNotCloseCallerConnection(t *testing.T) {
	dbConn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer dbConn.Close()
	wrapped := NewDatabaseFromDB(dbConn)
	if err := wrapped.check(); err != nil {
		t.Fatal(err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dbConn.Ping(); err != nil {
		t.Fatalf("wrapper closed caller-owned DB: %v", err)
	}
}

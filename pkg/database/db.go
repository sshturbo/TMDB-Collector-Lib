package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

var ErrDatabaseNotInitialized = errors.New("database initialization failed; call Migrate and check the returned error")

// Database wraps a SQLite connection. Connections passed through NewDatabaseFromDB
// remain owned by the caller and are never closed by this wrapper.
type Database struct {
	db           *sql.DB
	owned        bool
	initMu       sync.RWMutex
	initialError error
}

// SyncCheckpoint records the last fully processed inclusive TMDB changes date.
type SyncCheckpoint struct {
	MediaType        models.MediaType
	CompletedThrough string
	UpdatedAt        time.Time
}

func NewDatabase(dbPath string) (*Database, error) {
	dsn := dbPath
	if !strings.Contains(dsn, "_foreign_keys=") {
		separator := "?"
		if strings.Contains(dsn, "?") {
			separator = "&"
		}
		dsn += separator + "_foreign_keys=on"
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	if dbPath == ":memory:" {
		// A plain SQLite in-memory database belongs to one connection.
		db.SetMaxOpenConns(1)
	}
	database := &Database{db: db, owned: true}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping SQLite database: %w", err)
	}
	if err := database.Migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return database, nil
}

// NewDatabaseFromDB retains the historical no-error signature. It migrates the
// SQLite schema immediately and reports initialization failures on later methods.
func NewDatabaseFromDB(db *sql.DB) *Database {
	database := &Database{db: db}
	database.initialError = database.Migrate()
	return database
}

// NewDatabaseFromDBWithMigrations is the error-returning form for new integrations.
func NewDatabaseFromDBWithMigrations(db *sql.DB) (*Database, error) {
	database := &Database{db: db}
	if err := database.Migrate(); err != nil {
		return nil, err
	}
	return database, nil
}

// Close closes only connections opened by NewDatabase.
func (d *Database) Close() error {
	if d == nil || d.db == nil || !d.owned {
		return nil
	}
	return d.db.Close()
}

// Migrate creates missing tables and adds new metadata columns without dropping data.
// The database package currently targets SQLite, matching the original SQL schema.
func (d *Database) Migrate() error {
	if d == nil || d.db == nil {
		return errors.New("database connection is nil")
	}
	d.initMu.Lock()
	defer d.initMu.Unlock()
	conn, err := d.db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("acquire SQLite connection for migration: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable SQLite foreign keys: %w", err)
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer tx.Rollback()

	statements := []string{
		`CREATE TABLE IF NOT EXISTS movies (
			id INTEGER PRIMARY KEY, title TEXT NOT NULL DEFAULT '', overview TEXT, release_date TEXT,
			poster_path TEXT, backdrop_path TEXT, vote_average REAL, trailer_url TEXT,
			popularity REAL, created_at DATETIME, original_title TEXT NOT NULL DEFAULT '',
			original_language TEXT NOT NULL DEFAULT '', imdb_id TEXT, release_year INTEGER NOT NULL DEFAULT 0,
			last_synced_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS tv_shows (
			id INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT '', overview TEXT, first_air_date TEXT,
			poster_path TEXT, backdrop_path TEXT, vote_average REAL, trailer_url TEXT,
			popularity REAL, created_at DATETIME, original_name TEXT NOT NULL DEFAULT '',
			original_title TEXT NOT NULL DEFAULT '', original_language TEXT NOT NULL DEFAULT '',
			imdb_id TEXT, release_year INTEGER NOT NULL DEFAULT 0, last_synced_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS genres (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS movie_genres (
			movie_id INTEGER NOT NULL, genre_id INTEGER NOT NULL, PRIMARY KEY (movie_id, genre_id),
			FOREIGN KEY (movie_id) REFERENCES movies(id) ON DELETE CASCADE,
			FOREIGN KEY (genre_id) REFERENCES genres(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS tvshow_genres (
			tvshow_id INTEGER NOT NULL, genre_id INTEGER NOT NULL, PRIMARY KEY (tvshow_id, genre_id),
			FOREIGN KEY (tvshow_id) REFERENCES tv_shows(id) ON DELETE CASCADE,
			FOREIGN KEY (genre_id) REFERENCES genres(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS media_titles (
			media_type TEXT NOT NULL, tmdb_id INTEGER NOT NULL, title TEXT NOT NULL,
			language TEXT NOT NULL DEFAULT '', country TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (media_type, tmdb_id, title, language, country, source)
		)`,
		`CREATE TABLE IF NOT EXISTS sync_checkpoints (
			media_type TEXT PRIMARY KEY, completed_through TEXT NOT NULL, updated_at DATETIME NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("create SQLite schema: %w", err)
		}
	}
	if err := ensureColumns(tx, "movies", map[string]string{
		"original_title": "TEXT NOT NULL DEFAULT ''", "original_language": "TEXT NOT NULL DEFAULT ''",
		"imdb_id": "TEXT", "release_year": "INTEGER NOT NULL DEFAULT 0", "last_synced_at": "DATETIME",
	}); err != nil {
		return err
	}
	if err := ensureColumns(tx, "tv_shows", map[string]string{
		"original_name": "TEXT NOT NULL DEFAULT ''", "original_title": "TEXT NOT NULL DEFAULT ''",
		"original_language": "TEXT NOT NULL DEFAULT ''", "imdb_id": "TEXT",
		"release_year": "INTEGER NOT NULL DEFAULT 0", "last_synced_at": "DATETIME",
	}); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE INDEX IF NOT EXISTS idx_movies_title ON movies(title)`,
		`CREATE INDEX IF NOT EXISTS idx_movies_imdb_id ON movies(imdb_id)`,
		`CREATE INDEX IF NOT EXISTS idx_movies_release_year ON movies(release_year)`,
		`CREATE INDEX IF NOT EXISTS idx_tv_shows_name ON tv_shows(name)`,
		`CREATE INDEX IF NOT EXISTS idx_tv_shows_imdb_id ON tv_shows(imdb_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tv_shows_release_year ON tv_shows(release_year)`,
		`CREATE INDEX IF NOT EXISTS idx_media_titles_title ON media_titles(title)`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("create SQLite index: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SQLite migration: %w", err)
	}
	d.initialError = nil
	return nil
}

func ensureColumns(tx *sql.Tx, table string, wanted map[string]string) error {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("inspect %s table: %w", table, err)
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("read %s table columns: %w", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read %s table columns: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close %s table metadata: %w", table, err)
	}
	for name, definition := range wanted {
		if columns[name] {
			continue
		}
		if _, err := tx.Exec("ALTER TABLE " + table + " ADD COLUMN " + name + " " + definition); err != nil {
			return fmt.Errorf("add %s.%s: %w", table, name, err)
		}
	}
	return nil
}

func (d *Database) check() error {
	if d == nil || d.db == nil {
		return errors.New("database connection is nil")
	}
	d.initMu.RLock()
	defer d.initMu.RUnlock()
	if d.initialError != nil {
		return fmt.Errorf("%w: %v", ErrDatabaseNotInitialized, d.initialError)
	}
	return nil
}

// beginTx enables SQLite foreign keys on the exact pooled connection that will
// execute the transaction. A PRAGMA on *sql.DB alone only configures one connection.
func (d *Database) beginTx() (*sql.Tx, func(), error) {
	conn, err := d.db.Conn(context.Background())
	if err != nil {
		return nil, nil, fmt.Errorf("acquire SQLite connection: %w", err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("enable SQLite foreign keys: %w", err)
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("begin SQLite transaction: %w", err)
	}
	return tx, func() { _ = conn.Close() }, nil
}

func (d *Database) SaveMovie(movie *models.Movie) error {
	if movie == nil {
		return errors.New("movie is nil")
	}
	if err := d.check(); err != nil {
		return err
	}
	tx, release, err := d.beginTx()
	if err != nil {
		return fmt.Errorf("begin save movie transaction: %w", err)
	}
	defer release()
	defer tx.Rollback()
	if err := saveMovieTx(tx, *movie); err != nil {
		return err
	}
	return tx.Commit()
}

func saveMovieTx(tx *sql.Tx, movie models.Movie) error {
	id, err := mediaID(movie.ID, movie.TMDBID)
	if err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("movie TMDB ID must be positive")
	}
	now := time.Now().UTC()
	if movie.CreatedAt.IsZero() {
		movie.CreatedAt = now
	}
	if movie.LastSyncedAt.IsZero() {
		movie.LastSyncedAt = now
	}
	if movie.ReleaseYear == 0 {
		movie.ReleaseYear = yearFromDate(movie.ReleaseDate)
	}
	_, err = tx.Exec(`INSERT INTO movies
		(id, title, overview, release_date, poster_path, backdrop_path, vote_average, trailer_url,
		 popularity, created_at, original_title, original_language, imdb_id, release_year, last_synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		 title=CASE WHEN excluded.title <> '' THEN excluded.title ELSE movies.title END,
		 overview=CASE WHEN excluded.overview <> '' THEN excluded.overview ELSE movies.overview END,
		 release_date=CASE WHEN excluded.release_date <> '' THEN excluded.release_date ELSE movies.release_date END,
		 poster_path=CASE WHEN excluded.poster_path <> '' THEN excluded.poster_path ELSE movies.poster_path END,
		 backdrop_path=CASE WHEN excluded.backdrop_path <> '' THEN excluded.backdrop_path ELSE movies.backdrop_path END,
		 vote_average=CASE WHEN excluded.vote_average <> 0 OR ? = 1 THEN excluded.vote_average ELSE movies.vote_average END,
		 trailer_url=CASE WHEN excluded.trailer_url <> '' THEN excluded.trailer_url ELSE movies.trailer_url END,
		 popularity=CASE WHEN excluded.popularity <> 0 OR ? = 1 THEN excluded.popularity ELSE movies.popularity END,
		 original_title=CASE WHEN excluded.original_title <> '' THEN excluded.original_title ELSE movies.original_title END,
		 original_language=CASE WHEN excluded.original_language <> '' THEN excluded.original_language ELSE movies.original_language END,
		 imdb_id=CASE WHEN excluded.imdb_id IS NOT NULL AND excluded.imdb_id <> '' THEN excluded.imdb_id ELSE movies.imdb_id END,
		 release_year=CASE WHEN excluded.release_year <> 0 THEN excluded.release_year ELSE movies.release_year END,
		 last_synced_at=excluded.last_synced_at`,
		id, movie.Title, movie.Overview, movie.ReleaseDate, movie.PosterPath, movie.BackdropPath,
		movie.VoteAverage, movie.TrailerURL, movie.Popularity, movie.CreatedAt, movie.OriginalTitle,
		movie.OriginalLanguage, nullIfEmpty(movie.IMDbID), movie.ReleaseYear, movie.LastSyncedAt, movie.DetailsComplete, movie.DetailsComplete)
	if err != nil {
		return fmt.Errorf("save movie %d: %w", id, err)
	}
	if movie.AlternativeTitlesComplete || len(movie.AlternativeTitles) > 0 {
		if err := saveTitlesTx(tx, models.MediaTypeMovie, id, movie.AlternativeTitles, movie.AlternativeTitlesComplete); err != nil {
			return err
		}
	}
	return nil
}

func (d *Database) SaveTVShow(show *models.TVShow) error {
	if show == nil {
		return errors.New("TV show is nil")
	}
	if err := d.check(); err != nil {
		return err
	}
	tx, release, err := d.beginTx()
	if err != nil {
		return fmt.Errorf("begin save TV show transaction: %w", err)
	}
	defer release()
	defer tx.Rollback()
	if err := saveTVShowTx(tx, *show); err != nil {
		return err
	}
	return tx.Commit()
}

func saveTVShowTx(tx *sql.Tx, show models.TVShow) error {
	id, err := mediaID(show.ID, show.TMDBID)
	if err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("TV show TMDB ID must be positive")
	}
	now := time.Now().UTC()
	if show.CreatedAt.IsZero() {
		show.CreatedAt = now
	}
	if show.LastSyncedAt.IsZero() {
		show.LastSyncedAt = now
	}
	if show.ReleaseYear == 0 {
		show.ReleaseYear = yearFromDate(show.FirstAirDate)
	}
	_, err = tx.Exec(`INSERT INTO tv_shows
		(id, name, overview, first_air_date, poster_path, backdrop_path, vote_average, trailer_url,
		 popularity, created_at, original_name, original_title, original_language, imdb_id, release_year, last_synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		 name=CASE WHEN excluded.name <> '' THEN excluded.name ELSE tv_shows.name END,
		 overview=CASE WHEN excluded.overview <> '' THEN excluded.overview ELSE tv_shows.overview END,
		 first_air_date=CASE WHEN excluded.first_air_date <> '' THEN excluded.first_air_date ELSE tv_shows.first_air_date END,
		 poster_path=CASE WHEN excluded.poster_path <> '' THEN excluded.poster_path ELSE tv_shows.poster_path END,
		 backdrop_path=CASE WHEN excluded.backdrop_path <> '' THEN excluded.backdrop_path ELSE tv_shows.backdrop_path END,
		 vote_average=CASE WHEN excluded.vote_average <> 0 OR ? = 1 THEN excluded.vote_average ELSE tv_shows.vote_average END,
		 trailer_url=CASE WHEN excluded.trailer_url <> '' THEN excluded.trailer_url ELSE tv_shows.trailer_url END,
		 popularity=CASE WHEN excluded.popularity <> 0 OR ? = 1 THEN excluded.popularity ELSE tv_shows.popularity END,
		 original_name=CASE WHEN excluded.original_name <> '' THEN excluded.original_name ELSE tv_shows.original_name END,
		 original_title=CASE WHEN excluded.original_title <> '' THEN excluded.original_title ELSE tv_shows.original_title END,
		 original_language=CASE WHEN excluded.original_language <> '' THEN excluded.original_language ELSE tv_shows.original_language END,
		 imdb_id=CASE WHEN excluded.imdb_id IS NOT NULL AND excluded.imdb_id <> '' THEN excluded.imdb_id ELSE tv_shows.imdb_id END,
		 release_year=CASE WHEN excluded.release_year <> 0 THEN excluded.release_year ELSE tv_shows.release_year END,
		 last_synced_at=excluded.last_synced_at`,
		id, show.Name, show.Overview, show.FirstAirDate, show.PosterPath, show.BackdropPath,
		show.VoteAverage, show.TrailerURL, show.Popularity, show.CreatedAt, show.OriginalName,
		show.OriginalTitle, show.OriginalLanguage, nullIfEmpty(show.IMDbID), show.ReleaseYear, show.LastSyncedAt, show.DetailsComplete, show.DetailsComplete)
	if err != nil {
		return fmt.Errorf("save TV show %d: %w", id, err)
	}
	if show.AlternativeTitlesComplete || len(show.AlternativeTitles) > 0 {
		if err := saveTitlesTx(tx, models.MediaTypeTV, id, show.AlternativeTitles, show.AlternativeTitlesComplete); err != nil {
			return err
		}
	}
	return nil
}

func (d *Database) SaveMoviesBulk(movies []models.Movie) error {
	if err := d.check(); err != nil {
		return err
	}
	return d.saveBulk(len(movies), func(tx *sql.Tx, index int) error { return saveMovieTx(tx, movies[index]) })
}

func (d *Database) SaveTVShowsBulk(shows []models.TVShow) error {
	if err := d.check(); err != nil {
		return err
	}
	return d.saveBulk(len(shows), func(tx *sql.Tx, index int) error { return saveTVShowTx(tx, shows[index]) })
}

func (d *Database) saveBulk(count int, save func(*sql.Tx, int) error) error {
	tx, release, err := d.beginTx()
	if err != nil {
		return fmt.Errorf("begin bulk save transaction: %w", err)
	}
	defer release()
	defer tx.Rollback()
	for index := 0; index < count; index++ {
		if err := save(tx, index); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit bulk save: %w", err)
	}
	return nil
}

func (d *Database) SaveGenres(genres []models.Genre) error {
	if err := d.check(); err != nil {
		return err
	}
	tx, release, err := d.beginTx()
	if err != nil {
		return fmt.Errorf("begin save genres transaction: %w", err)
	}
	defer release()
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO genres (id, name) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name`)
	if err != nil {
		return fmt.Errorf("prepare save genres: %w", err)
	}
	defer stmt.Close()
	for _, genre := range genres {
		if _, err := stmt.Exec(genre.ID, genre.Name); err != nil {
			return fmt.Errorf("save genre %d: %w", genre.ID, err)
		}
	}
	return tx.Commit()
}

func (d *Database) SaveMovieGenres(movieID int, genreIDs []int) error {
	return d.saveRelations("movie_genres", "movie_id", movieID, genreIDs)
}

func (d *Database) SaveTVShowGenres(tvShowID int, genreIDs []int) error {
	return d.saveRelations("tvshow_genres", "tvshow_id", tvShowID, genreIDs)
}

func (d *Database) saveRelations(table, mediaColumn string, mediaID int, genreIDs []int) error {
	if err := d.check(); err != nil {
		return err
	}
	tx, release, err := d.beginTx()
	if err != nil {
		return fmt.Errorf("begin save media genres transaction: %w", err)
	}
	defer release()
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM "+table+" WHERE "+mediaColumn+" = ?", mediaID); err != nil {
		return fmt.Errorf("replace %s relations for %d: %w", table, mediaID, err)
	}
	stmt, err := tx.Prepare("INSERT OR IGNORE INTO " + table + " (" + mediaColumn + ", genre_id) VALUES (?, ?)")
	if err != nil {
		return fmt.Errorf("prepare %s relations: %w", table, err)
	}
	defer stmt.Close()
	for _, genreID := range genreIDs {
		if _, err := stmt.Exec(mediaID, genreID); err != nil {
			return fmt.Errorf("save %s relation %d/%d: %w", table, mediaID, genreID, err)
		}
	}
	return tx.Commit()
}

func (d *Database) SaveMovieGenresBulk(relations []models.MovieGenre) error {
	if err := d.check(); err != nil {
		return err
	}
	return d.saveRelationBulk("movie_genres", "movie_id", len(relations), func(index int) (int, int) {
		return relations[index].MovieID, relations[index].GenreID
	})
}

func (d *Database) SaveTVShowGenresBulk(relations []models.TVShowGenre) error {
	if err := d.check(); err != nil {
		return err
	}
	return d.saveRelationBulk("tvshow_genres", "tvshow_id", len(relations), func(index int) (int, int) {
		return relations[index].TVShowID, relations[index].GenreID
	})
}

// GetSyncCheckpoint returns the persisted inclusive change date for one media type.
func (d *Database) GetSyncCheckpoint(mediaType models.MediaType) (SyncCheckpoint, bool, error) {
	if err := validateSyncMediaType(mediaType); err != nil {
		return SyncCheckpoint{}, false, err
	}
	if err := d.check(); err != nil {
		return SyncCheckpoint{}, false, err
	}
	checkpoint := SyncCheckpoint{MediaType: mediaType}
	err := d.db.QueryRowContext(context.Background(), `SELECT completed_through, updated_at
		FROM sync_checkpoints WHERE media_type = ?`, mediaType).Scan(&checkpoint.CompletedThrough, &checkpoint.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncCheckpoint{}, false, nil
	}
	if err != nil {
		return SyncCheckpoint{}, false, fmt.Errorf("load %s sync checkpoint: %w", mediaType, err)
	}
	return checkpoint, true, nil
}

// SaveSyncCheckpoint advances a checkpoint monotonically; it never moves backward.
func (d *Database) SaveSyncCheckpoint(checkpoint SyncCheckpoint) error {
	if err := validateSyncMediaType(checkpoint.MediaType); err != nil {
		return err
	}
	if _, err := time.Parse("2006-01-02", checkpoint.CompletedThrough); err != nil {
		return fmt.Errorf("invalid checkpoint date %q: %w", checkpoint.CompletedThrough, err)
	}
	if err := d.check(); err != nil {
		return err
	}
	if checkpoint.UpdatedAt.IsZero() {
		checkpoint.UpdatedAt = time.Now().UTC()
	}
	tx, release, err := d.beginTx()
	if err != nil {
		return err
	}
	defer release()
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO sync_checkpoints (media_type, completed_through, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(media_type) DO UPDATE SET
		completed_through=CASE WHEN excluded.completed_through > sync_checkpoints.completed_through
			THEN excluded.completed_through ELSE sync_checkpoints.completed_through END,
		updated_at=excluded.updated_at`, checkpoint.MediaType, checkpoint.CompletedThrough, checkpoint.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save %s sync checkpoint: %w", checkpoint.MediaType, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s sync checkpoint: %w", checkpoint.MediaType, err)
	}
	return nil
}

func validateSyncMediaType(mediaType models.MediaType) error {
	if mediaType != models.MediaTypeMovie && mediaType != models.MediaTypeTV {
		return fmt.Errorf("unsupported sync media type %q", mediaType)
	}
	return nil
}

func (d *Database) saveRelationBulk(table, mediaColumn string, count int, at func(int) (int, int)) error {
	tx, release, err := d.beginTx()
	if err != nil {
		return fmt.Errorf("begin bulk relationship transaction: %w", err)
	}
	defer release()
	defer tx.Rollback()
	stmt, err := tx.Prepare("INSERT OR IGNORE INTO " + table + " (" + mediaColumn + ", genre_id) VALUES (?, ?)")
	if err != nil {
		return fmt.Errorf("prepare bulk %s relations: %w", table, err)
	}
	defer stmt.Close()
	for index := 0; index < count; index++ {
		mediaID, genreID := at(index)
		if _, err := stmt.Exec(mediaID, genreID); err != nil {
			return fmt.Errorf("save bulk %s relation %d/%d: %w", table, mediaID, genreID, err)
		}
	}
	return tx.Commit()
}

func saveTitlesTx(tx *sql.Tx, mediaType models.MediaType, tmdbID int, titles []models.LocalizedTitle, replace bool) error {
	if replace {
		if _, err := tx.Exec(`DELETE FROM media_titles WHERE media_type = ? AND tmdb_id = ?`, mediaType, tmdbID); err != nil {
			return fmt.Errorf("replace titles for %s %d: %w", mediaType, tmdbID, err)
		}
	}
	stmt, err := tx.Prepare(`INSERT INTO media_titles
		(media_type, tmdb_id, title, language, country, source) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(media_type, tmdb_id, title, language, country, source) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("prepare title persistence: %w", err)
	}
	defer stmt.Close()
	for _, title := range titles {
		if strings.TrimSpace(title.Title) == "" {
			continue
		}
		if _, err := stmt.Exec(mediaType, tmdbID, strings.TrimSpace(title.Title), title.Language, title.Country, title.Source); err != nil {
			return fmt.Errorf("save localized title for %s %d: %w", mediaType, tmdbID, err)
		}
	}
	return nil
}

func mediaID(id, tmdbID int) (int, error) {
	if id != 0 && tmdbID != 0 && id != tmdbID {
		return 0, fmt.Errorf("ID %d conflicts with TMDBID %d", id, tmdbID)
	}
	if id == 0 {
		return tmdbID, nil
	}
	return id, nil
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func yearFromDate(value string) int {
	if len(value) < 4 {
		return 0
	}
	var year int
	_, _ = fmt.Sscanf(value[:4], "%d", &year)
	return year
}

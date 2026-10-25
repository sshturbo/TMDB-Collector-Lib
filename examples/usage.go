package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/sshturbo/TMDB-Collector-Lib/pkg/api"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/config"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/database"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	file, err := os.Open("config.json")
	if err != nil {
		return fmt.Errorf("open config.json: %w", err)
	}
	defer file.Close()

	var cfg config.Config
	if err := json.NewDecoder(file).Decode(&cfg); err != nil {
		return fmt.Errorf("decode config.json: %w", err)
	}
	if key := os.Getenv("TMDB_API_KEY"); key != "" {
		cfg.TMDB.APIKey = key
	}
	if cfg.TMDB.APIKey == "" {
		return errors.New("set TMDB_API_KEY or configure tmdb.api_key")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := api.NewTMDBClient(&cfg)

	store, err := database.NewDatabase("media.db")
	if err != nil {
		return err
	}
	defer store.Close()

	genres, err := client.FetchMovieGenresContext(ctx)
	if err != nil {
		return err
	}
	if err := store.SaveGenres(genres); err != nil {
		return err
	}

	movies, err := client.DiscoverMoviesContext(ctx, 1)
	if err != nil {
		return err
	}
	if err := store.SaveMoviesBulk(movies); err != nil {
		return err
	}

	relations := make([]models.MovieGenre, 0)
	for _, movie := range movies {
		for _, genreID := range movie.GenreIDs {
			relations = append(relations, models.MovieGenre{MovieID: movie.ID, GenreID: genreID})
		}
	}
	if err := store.SaveMovieGenresBulk(relations); err != nil {
		return err
	}

	fmt.Printf("Saved %d movies and %d genres\n", len(movies), len(genres))
	return nil
}

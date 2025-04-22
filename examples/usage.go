package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"tmdb-collector/pkg/api"
	"tmdb-collector/pkg/config"
	"tmdb-collector/pkg/database"

	_ "github.com/mattn/go-sqlite3"
)

func main() {
	// Lê a configuração de um arquivo config.json
	f, err := os.Open("config.json")
	if err != nil {
		log.Fatal("Erro ao abrir config.json:", err)
	}
	defer f.Close()
	var cfg config.Config
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		log.Fatal("Erro ao decodificar config.json:", err)
	}

	// Inicializa o cliente TMDB
	tmdbClient := api.NewTMDBClient(&cfg)

	// Abre o banco de dados já existente
	dbConn, err := sql.Open("sqlite3", "meubanco.db")
	if err != nil {
		log.Fatal("Erro ao conectar ao banco de dados:", err)
	}
	defer dbConn.Close()

	// Usa o banco já aberto na biblioteca
	db := database.NewDatabaseFromDB(dbConn)

	// Buscar e salvar gêneros de filmes
	movieGenres, err := tmdbClient.FetchMovieGenres()
	if err != nil {
		log.Fatalf("Erro ao buscar gêneros de filmes: %v\n", err)
	}
	if err := db.SaveGenres(movieGenres); err != nil {
		log.Fatalf("Erro ao salvar gêneros de filmes: %v\n", err)
	}

	// Buscar filmes da primeira página
	movies, err := tmdbClient.DiscoverMovies(1)
	if err != nil {
		log.Fatalf("Erro ao buscar filmes: %v\n", err)
	}
	if len(movies) > 0 {
		err := db.SaveMoviesBulk(movies)
		if err != nil {
			log.Printf("Erro ao salvar filmes em lote: %v\n", err)
		} else {
			fmt.Printf("%d filmes salvos!\n", len(movies))
		}
	}
}

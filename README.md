# TMDB-Collector-Lib

Esta biblioteca permite buscar filmes, séries, gêneros e trailers da API do TMDB e salvar em um banco relacional de forma eficiente.

## Compatibilidade com bancos de dados

Você pode usar **qualquer banco de dados suportado pelo Go (`database/sql`)**, como SQLite, PostgreSQL, MySQL, etc. Basta:

- Instalar/importar o driver do banco desejado (exemplos abaixo)
- Criar as tabelas com a estrutura documentada neste README (adaptando a sintaxe se necessário)
- Passar a conexão já aberta (`*sql.DB`) para a biblioteca

**Exemplos de drivers:**

- SQLite: `_ "github.com/mattn/go-sqlite3"`
- PostgreSQL: `_ "github.com/lib/pq"`
- MySQL: `_ "github.com/go-sql-driver/mysql"`

> **Atenção:** O SQL de criação das tabelas pode precisar de pequenas adaptações para cada banco (tipos, auto-incremento, etc). Veja a documentação do seu banco para detalhes.

A biblioteca **não cria nem gerencia o banco**: isso é responsabilidade do usuário.

## Estrutura do banco de dados

**Atenção:** A biblioteca não cria as tabelas automaticamente. Você deve criar o banco e as tabelas antes de usar.

Exemplo de SQL para criar as tabelas necessárias:

```sql
CREATE TABLE movies (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    overview TEXT,
    release_date TEXT,
    poster_path TEXT,
    backdrop_path TEXT,
    vote_average REAL,
    trailer_url TEXT,
    popularity REAL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE tv_shows (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    overview TEXT,
    first_air_date TEXT,
    poster_path TEXT,
    backdrop_path TEXT,
    vote_average REAL,
    trailer_url TEXT,
    popularity REAL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE genres (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL
);

CREATE TABLE movie_genres (
    movie_id INTEGER,
    genre_id INTEGER,
    PRIMARY KEY (movie_id, genre_id),
    FOREIGN KEY (movie_id) REFERENCES movies(id) ON DELETE CASCADE,
    FOREIGN KEY (genre_id) REFERENCES genres(id) ON DELETE CASCADE
);

CREATE TABLE tvshow_genres (
    tvshow_id INTEGER,
    genre_id INTEGER,
    PRIMARY KEY (tvshow_id, genre_id),
    FOREIGN KEY (tvshow_id) REFERENCES tv_shows(id) ON DELETE CASCADE,
    FOREIGN KEY (genre_id) REFERENCES genres(id) ON DELETE CASCADE
);
```

## Exemplos de Uso

### Configuração Inicial

Primeiro, crie um arquivo `config.json`:

```json
{
    "tmdb": {
        "api_key": "sua_api_key_aqui",
        "base_url": "https://api.themoviedb.org/3",
        "image_base_url": "https://image.tmdb.org/t/p/original",
        "language": "pt-BR"
    },
    "database": {
        "filename": "media.db"
    },
    "fetch": {
        "num_pages": 500,
        "include_adult": false,
        "include_video": false,
        "max_release_date": "",
        "sort": {
            "movies": {
                "field": "popularity",
                "direction": "desc"
            },
            "tv_shows": {
                "field": "popularity",
                "direction": "desc"
            }
        }
    }
}
```

### Populando o Banco de Dados

Exemplo de como usar a biblioteca para popular seu banco de dados com filmes e séries:

```go
package main

import (
    "encoding/json"
    "log"
    "os"
    
    tmdbapi "github.com/sshturbo/TMDB-Collector-Lib/pkg/api"
    tmdbconfig "github.com/sshturbo/TMDB-Collector-Lib/pkg/config"
    tmdbdb "github.com/sshturbo/TMDB-Collector-Lib/pkg/database"
)

func main() {
    // 1. Carregar configuração
    f, err := os.Open("config.json")
    if err != nil {
        log.Fatalf("Erro ao abrir config.json: %v", err)
    }
    defer f.Close()

    var cfg tmdbconfig.Config
    if err := json.NewDecoder(f).Decode(&cfg); err != nil {
        log.Fatalf("Erro ao decodificar config.json: %v", err)
    }

    // 2. Inicializar banco de dados e cliente TMDB
    db, err := tmdbdb.NewDatabase("media.db")
    if err != nil {
        log.Fatalf("Erro ao conectar ao banco: %v", err)
    }
    
    tmdb := tmdbapi.NewTMDBClient(&cfg)

    // 3. Buscar e salvar gêneros
    log.Println("Buscando gêneros de filmes...")
    movieGenres, err := tmdb.FetchMovieGenres()
    if err != nil {
        log.Fatalf("Erro ao buscar gêneros de filmes: %v", err)
    }
    if err := db.SaveGenres(movieGenres); err != nil {
        log.Fatalf("Erro ao salvar gêneros de filmes: %v", err)
    }

    log.Println("Buscando gêneros de séries...")
    tvGenres, err := tmdb.FetchTVShowGenres()
    if err != nil {
        log.Fatalf("Erro ao buscar gêneros de séries: %v", err)
    }
    if err := db.SaveGenres(tvGenres); err != nil {
        log.Fatalf("Erro ao salvar gêneros de séries: %v", err)
    }

    // 4. Buscar e salvar filmes
    log.Printf("Buscando filmes (páginas: %d)...", cfg.Fetch.NumPages)
    for page := 1; page <= cfg.Fetch.NumPages; page++ {
        movies, err := tmdb.DiscoverMovies(page)
        if err != nil {
            log.Printf("Erro ao buscar filmes (página %d): %v", page, err)
            continue
        }
        
        if err := db.SaveMoviesBulk(movies); err != nil {
            log.Printf("Erro ao salvar filmes (página %d): %v", page, err)
            continue
        }

        // Salvar gêneros dos filmes
        for _, movie := range movies {
            if err := db.SaveMovieGenres(movie.ID, movie.GenreIDs); err != nil {
                log.Printf("Erro ao salvar gêneros do filme %d: %v", movie.ID, err)
            }
        }
        
        log.Printf("Página %d: %d filmes processados", page, len(movies))
    }

    // 5. Buscar e salvar séries
    log.Printf("Buscando séries (páginas: %d)...", cfg.Fetch.NumPages)
    for page := 1; page <= cfg.Fetch.NumPages; page++ {
        shows, err := tmdb.DiscoverTVShows(page)
        if err != nil {
            log.Printf("Erro ao buscar séries (página %d): %v", page, err)
            continue
        }
        
        if err := db.SaveTVShowsBulk(shows); err != nil {
            log.Printf("Erro ao salvar séries (página %d): %v", page, err)
            continue
        }

        // Salvar gêneros das séries
        for _, show := range shows {
            if err := db.SaveTVShowGenres(show.ID, show.GenreIDs); err != nil {
                log.Printf("Erro ao salvar gêneros da série %d: %v", show.ID, err)
            }
        }
        
        log.Printf("Página %d: %d séries processadas", page, len(shows))
    }

    log.Println("Importação concluída!")
}
```

Este exemplo demonstra:

- Como carregar a configuração do arquivo JSON
- Como inicializar o cliente TMDB e o banco de dados
- Como buscar e salvar gêneros
- Como buscar e salvar filmes e séries com seus respectivos gêneros
- Como usar paginação para buscar todos os itens desejados
- Como tratar erros durante o processo

**Nota**: Certifique-se de:

1. Ter sua API key do TMDB configurada no `config.json`
2. Ajustar `num_pages` de acordo com sua necessidade
3. Configurar o campo `sort` para ordenar os resultados como desejado

> **Nota:** A biblioteca só precisa da struct preenchida. O usuário pode ler de arquivo, variáveis de ambiente, etc.

## Como trabalhar com Gêneros (Importante)

Para trabalhar corretamente com os gêneros de filmes e séries, é necessário seguir uma ordem específica:

1. **Carregar os Gêneros Primeiro**:

   - Os gêneros NÃO são carregados automaticamente quando você busca filmes ou séries
   - Você DEVE chamar explicitamente as funções:
     - `FetchMovieGenres()` para gêneros de filmes
     - `FetchTVShowGenres()` para gêneros de séries

2. **Ordem Correta de Chamadas**:

   ```go
   // 1. Primeiro carrega e salva os gêneros
   movieGenres, _ := tmdbClient.FetchMovieGenres()
   db.SaveGenres(movieGenres)

   tvGenres, _ := tmdbClient.FetchTVShowGenres()
   db.SaveGenres(tvGenres)

   // 2. Depois carrega filmes e séries
   movies, _ := tmdbClient.DiscoverMovies(page)
   shows, _ := tmdbClient.DiscoverTVShows(page)
   ```

3. **Por que é necessário?**
   - Os filmes e séries vêm apenas com os IDs dos gêneros (`GenreIDs`)
   - Os nomes dos gêneros precisam ser carregados separadamente
   - Os gêneros raramente mudam no TMDB, então é mais eficiente carregá-los uma vez só
   - Evita requisições desnecessárias à API

4. **Relacionamentos no Banco**:
   - Os gêneros são salvos na tabela `genres`
   - Os relacionamentos são salvos em:
     - `movie_genres` para filmes
     - `tvshow_genres` para séries

## Funcionalidades

- Busca filmes, séries, gêneros e trailers do TMDB
- Salva dados em lote no SQLite (ou outro banco relacional)
- Relaciona filmes/séries com gêneros
- Busca trailers em múltiplos idiomas e tipos

## Estrutura dos pacotes

- `pkg/api`: Cliente TMDB
- `pkg/database`: Operações de banco de dados
- `pkg/models`: Modelos de dados
- `pkg/config`: Configuração

## Licença

MIT

# TMDB Collector Lib

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

## Exemplo de config.json

```json
{
  "tmdb": {
    "api_key": "SUA_API_KEY_AQUI",
    "base_url": "https://api.themoviedb.org/3",
    "image_base_url": "https://image.tmdb.org/t/p/original",
    "language": "pt-BR"
  },
  "fetch": {
    "num_pages": 1,
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

## Exemplo de uso

Veja o exemplo em `examples/usage.go`:

```go
import (
    "database/sql"
    "encoding/json"
    "os"
    _ "github.com/mattn/go-sqlite3"
    "tmdb-collector/pkg/api"
    "tmdb-collector/pkg/config"
    "tmdb-collector/pkg/database"
)

func main() {
    // Lê a configuração de um arquivo config.json
    f, _ := os.Open("config.json")
    defer f.Close()
    var cfg config.Config
    json.NewDecoder(f).Decode(&cfg)

    dbConn, _ := sql.Open("sqlite3", "meubanco.db")
    db := database.NewDatabaseFromDB(dbConn)
    tmdb := api.NewTMDBClient(&cfg)
    movies, _ := tmdb.DiscoverMovies(1)
    db.SaveMoviesBulk(movies)
}
```

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

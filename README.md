# TMDB-Collector-Lib

Cliente Go para pesquisar filmes e séries no TMDB, recuperar metadados localizados e IDs externos e persistir os resultados em SQLite. O pacote não depende de Gin, IPTVflix, IMDb comercial ou provedores de mídia.

## Requisitos

- Go 1.24 ou superior.
- Uma chave de API TMDB v3.
- SQLite para usar `pkg/database`.

## Instalação e credenciais

```bash
go get github.com/sshturbo/TMDB-Collector-Lib@v1.1.0
export TMDB_API_KEY="sua-chave"
```

Não salve chaves reais em `config.json`, no código-fonte ou em logs. A biblioteca recebe a chave pela configuração e remove seu valor das mensagens de erro HTTP que ela produz. Para ambientes que usam um token TMDB v4, configure um `RoundTripper` próprio que adicione o cabeçalho `Authorization: Bearer ...` e não configure `APIKey`.

## Configuração e cliente HTTP

```go
cfg := &config.Config{}
cfg.TMDB.APIKey = os.Getenv("TMDB_API_KEY")
cfg.TMDB.BaseURL = "https://api.themoviedb.org/3"
cfg.TMDB.ImageBaseURL = "https://image.tmdb.org/t/p/original"
cfg.TMDB.Language = "pt-BR"
cfg.TMDB.FallbackLanguage = "en-US"
cfg.TMDB.TimeoutSeconds = 15
cfg.Fetch.MaxConcurrentRequests = 5
cfg.Fetch.MaxRetries = 2
cfg.Fetch.CacheTTLSeconds = 0 // cache desativado

client := api.NewTMDBClient(cfg)
```

`NewTMDBClient` preserva a assinatura anterior. Para injetar um cliente HTTP ou configurar o comportamento diretamente:

```go
client := api.NewTMDBClientWithOptions(cfg, api.ClientOptions{
    Timeout:               15 * time.Second,
    MaxConcurrentRequests: 5,
    MaxRetries:            2,
    RetryBaseDelay:        200 * time.Millisecond,
    CacheTTL:              30 * time.Minute,
})
```

Os valores padrão são `pt-BR`, fallback `en-US`, timeout de 15 segundos, cinco requisições simultâneas por cliente e duas tentativas adicionais para falhas de transporte, HTTP 429 e HTTP 5xx. Use `MaxRetries: -1` para desativar retries. Respostas de sucesso podem ser armazenadas em cache com TTL (limitado a 180 dias); entradas expiradas não são retornadas e `ClearCache()` força uma nova consulta. O cache tem limite de entradas para controlar o uso de memória. Configure `CacheTTL: -1` para desativar o cache mesmo que ele esteja habilitado na configuração.

As variantes `...Context` aceitam `context.Context` para timeout e cancelamento. As funções antigas continuam disponíveis e usam `context.Background()`.

## Pesquisa de filmes e séries

O TMDB recebe o idioma configurado em buscas e detalhes. A busca textual do TMDB considera nomes originais, traduzidos e alternativos; o parâmetro de ano é enviado quando informado. Para coletar mais de uma página, limite explicitamente o número de chamadas:

```go
ctx := context.Background()

movies, err := client.SearchMoviePagesContext(ctx, "O Fabuloso Destino de Amélie Poulain", 2001, 3)
if err != nil {
    log.Fatal(err)
}

shows, err := client.SearchTVShowPagesContext(ctx, "Ruptura", 2022, 2)
if err != nil {
    log.Fatal(err)
}
```

Também permanecem disponíveis `SearchMovies(query, page)`, `SearchTVShows(query, page)`, `DiscoverMovies(page)` e `DiscoverTVShows(page)`. Para evitar dezenas de chamadas ocultas por página, descobertas não consultam trailers por padrão. Se um consumidor precisar desse comportamento, configure `cfg.Fetch.FetchTrailers = true`; as chamadas permanecem limitadas pelo semáforo compartilhado do cliente. Para um trailer pontual, use `GetMovieTrailer(id)` ou `GetTVShowTrailer(id)`.

Para percorrer páginas de descoberta com métricas e recuperação por página, use `CollectMoviesContext(ctx, maxPages)` ou `CollectTVShowsContext`. `maxPages` limita chamadas e memória. Uma página com falha é reportada em `CollectionReport`; a coleta tenta as páginas seguintes e retorna resultados parciais junto com um erro agregado. O chamador pode persistir os itens em lote e repetir as páginas com falha depois.

## Detalhes, traduções e IDs IMDb

```go
movie, err := client.GetMovieDetailsContext(ctx, 361743)
if err != nil {
    log.Fatal(err)
}
fmt.Println(movie.Title, movie.OriginalTitle, movie.OriginalLanguage, movie.ReleaseYear, movie.IMDbID)

ids, err := client.GetMovieExternalIDsContext(ctx, movie.ID)
if err != nil {
    log.Fatal(err)
}
fmt.Println(ids.IMDbID)

searchTitles, err := client.GetSearchTitlesContext(ctx, models.MediaTypeMovie, movie.ID)
if err != nil {
    log.Fatal(err)
}
for _, title := range searchTitles.SearchTitles {
    fmt.Printf("%s (%s; %s; %s)\n", title.Title, title.Language, title.Country, title.Source)
}
```

`GetMovieDetails` e `GetTVShowDetails` retornam a localização solicitada, título original, idioma original, ano, IMDb ID quando disponível e títulos alternativos/traduzidos com proveniência. Quando falta um campo localizado, o cliente tenta o fallback `en-US`, depois o título original. `GetMovieExternalIDs`, `GetTVShowExternalIDs` e `FindByIMDbID` estão disponíveis com e sem contexto. IDs externos são opcionais. O idioma dos metadados não indica o idioma das faixas de áudio ou legendas de um vídeo.

## Identificação de conteúdo

```go
result, err := client.IdentifyMediaContext(ctx, "O Poderoso Chefão", models.MediaTypeMovie, 1972)
if err != nil {
    log.Fatal(err)
}
if result.Conclusive {
    fmt.Printf("TMDB ID: %d, confiança: %.2f\n", result.Candidate.TMDBID, result.Candidate.Confidence)
} else {
    fmt.Printf("Identificação ambígua; revisar %d candidatos\n", len(result.Candidates))
}
```

A pontuação usa a maior similaridade normalizada entre o título consultado e o título localizado/original. A normalização remove acentos, converte pontuação e espaços repetidos em uma forma comparável e preserva letras e números. Com ano, o score é `0,8 × similaridade do título + 0,2 × correspondência exata do ano`; sem ano, usa apenas a similaridade. A identificação só é conclusiva acima do limiar (`0,82` com ano; `0,85` sem ano) e quando o primeiro candidato supera o segundo por pelo menos `0,08`. Resultados ambíguos são retornados para revisão e nunca selecionados automaticamente.

## Banco de dados e migrações

`pkg/database` oferece persistência SQLite. Os nomes e as operações SQL são específicos do SQLite; PostgreSQL e MySQL não são compatíveis nesta versão.

```go
db, err := database.NewDatabase("media.db") // cria tabelas e aplica migrações
if err != nil {
    log.Fatal(err)
}
defer db.Close()

if err := db.SaveMovie(&movie); err != nil {
    log.Fatal(err)
}
if err := db.SaveMoviesBulk(movies); err != nil {
    log.Fatal(err)
}
```

Para uma conexão já aberta, o chamador mantém a propriedade e fecha `*sql.DB`:

```go
dbConn, err := sql.Open("sqlite3", "media.db")
if err != nil {
    log.Fatal(err)
}
defer dbConn.Close()

store, err := database.NewDatabaseFromDBWithMigrations(dbConn)
if err != nil {
    log.Fatal(err)
}
```

`NewDatabaseFromDB(*sql.DB)` continua disponível para compatibilidade e também tenta migrar; como sua assinatura histórica não retorna erro, prefira a variante `WithMigrations` para receber falhas de migração diretamente. `Migrate()` pode ser chamada novamente: é idempotente e não apaga as tabelas ou dados antigos.

Ao abrir a conexão SQLite por conta própria, use o parâmetro do driver `_foreign_keys=on` na DSN se quiser que o SQLite valide as chaves estrangeiras em todas as conexões do pool.

As tabelas guardam os campos originais/localizados, IMDb ID opcional, ano e data da última sincronização. Os títulos traduzidos e alternativos ficam em `media_titles`. O identificador TMDB continua armazenado na coluna `id` existente. Upserts usam `ON CONFLICT DO UPDATE`, preservam IDs/metadados existentes quando uma atualização parcial não os informa e não apagam relações de gêneros. `SaveMovieGenres` e `SaveTVShowGenres` atualizam o conjunto de gêneros para aquele registro; as operações em lote são transacionais. `last_synced_at` registra quando a biblioteca salvou o item, mas não é uma prova de que o TMDB mudou ou permaneceu igual. O cache reaproveita somente a mesma resposta HTTP até o TTL; para uma leitura obrigatoriamente fresca, use `ClearCache()` antes da coleta.

## Erros

Erros HTTP são do tipo `*api.TMDBClientError`, com `StatusCode`, `TMDBStatusCode`, `Message` e `RetryAfter`. Use `errors.As` para inspecionar status; erros de transporte e decodificação preservam a causa com `Unwrap`. O texto de erro não inclui a chave de API nem o corpo bruto da resposta.

```go
var apiErr *api.TMDBClientError
if errors.As(err, &apiErr) {
    log.Printf("TMDB HTTP %d: %s", apiErr.StatusCode, apiErr.Message)
}
```

## Atribuição e termos do TMDB

O uso dos dados e imagens TMDB exige seguir os termos atuais do TMDB e atribuir a fonte na aplicação que exibe esses metadados. Os termos publicados exigem o logo TMDB e o aviso: “This [website, program, service, application, product] uses TMDB and the TMDB APIs but is not endorsed, certified, or otherwise approved by TMDB.” A aplicação deve usar um logo aprovado e não sugerir endosso. O uso comercial exige acordo escrito com TMDB; confirme os requisitos aplicáveis antes de distribuir uma aplicação.

- [API Terms of Use](https://www.themoviedb.org/api-terms-of-use)
- [TMDB FAQ: atribuição e uso da API](https://developer.themoviedb.org/docs/faq)
- [Logos e atribuição](https://www.themoviedb.org/about/logos-attribution)

Esta biblioteca é distribuída sob MIT; essa licença não substitui os termos aplicáveis aos dados, imagens, marcas ou serviços do TMDB.

## Atualização do IPTVflix

Nenhum arquivo do IPTVflix foi alterado. Quando a versão `v1.1.0` estiver publicada, a atualização da dependência será:

```bash
go get github.com/sshturbo/TMDB-Collector-Lib@v1.1.0
go mod tidy
```

As assinaturas públicas existentes (`NewTMDBClient`, buscas, descobertas, gêneros, trailers, `NewDatabaseFromDB` e salvamentos em lote) foram mantidas. O IPTVflix pode migrar aos poucos para as variantes com contexto e para `NewDatabaseFromDBWithMigrations`; não precisa adaptar as chamadas atuais. A descoberta deixa de buscar trailers automaticamente por padrão para reduzir chamadas; se o IPTVflix dependia desse preenchimento, deve definir `cfg.Fetch.FetchTrailers = true` ou solicitar o trailer explicitamente para os itens necessários.

## Desenvolvimento

```bash
go test ./...
go test -race ./...
go vet ./...
```

Os testes HTTP usam `httptest` e não acessam o serviço real do TMDB.

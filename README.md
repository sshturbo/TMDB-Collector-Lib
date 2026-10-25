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

As variantes `...Context` aceitam `context.Context` para timeout e cancelamento. As funções antigas continuam disponíveis e usam `context.Background()`. Requisições HTTP idênticas em andamento compartilham a resposta, mas cada chamador aguarda com seu próprio contexto: cancelar um chamador não cancela os demais. O pedido compartilhado é cancelado quando todos os chamadores saem; cada tentativa HTTP continua limitada pelo timeout do cliente.

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

Para percorrer páginas de descoberta com métricas e recuperação por página, use `CollectMoviesContext(ctx, maxPages)` ou `CollectTVShowsContext`. Essas funções mantêm os resultados acumulados por compatibilidade. `maxPages` limita a coleta a até 500 páginas.

Para processar lotes sem manter o catálogo inteiro em memória, use a API progressiva:

```go
report, err := client.CollectMoviesProgressiveContext(ctx, 10,
    func(ctx context.Context, page int, batch []models.Movie) error {
        return store.SaveMoviesBulk(batch)
    })
```

`CollectTVShowsProgressiveContext` tem a mesma forma. O callback recebe uma página por vez; sua memória de trabalho fica limitada ao lote atual se ele não guardar os slices recebidos. Falhas de busca ficam em `report.Failures` e a coleta tenta as próximas páginas. Um erro do callback interrompe a execução. Reprocesse páginas específicas com `ProcessMoviePagesContext(ctx, pages, callback)` ou `ProcessTVShowPagesContext`; as páginas são deduplicadas e ordenadas. O relatório inclui duração, páginas solicitadas/concluídas, falhas e itens processados. Cancelamento é propagado pelo contexto e não desfaz páginas já persistidas.

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

`GetMovieDetails` e `GetTVShowDetails` retornam a localização solicitada, título original, idioma original, ano, IMDb ID quando disponível e títulos alternativos/traduzidos com proveniência. A deduplicação dos metadados considera título normalizado, idioma, país e origem, então duas traduções com o mesmo nome podem ser preservadas. `pt-BR` aparece primeiro, seguido do título original e dos demais títulos em ordem determinística. `UniqueSearchQueries(searchTitles.SearchTitles)` remove consultas repetidas sem alterar os metadados apresentados. Quando falta um campo localizado, o cliente tenta o fallback `en-US`, depois o título original. `GetMovieDetailsFreshContext` e `GetTVShowDetailsFreshContext` ignoram respostas em cache para fluxos que precisam buscar o estado remoto atual. IDs externos são opcionais. O idioma dos metadados não indica o idioma das faixas de áudio ou legendas de um vídeo.

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

A busca padrão verifica até três páginas e para quando o melhor candidato atinge confiança e margem suficientes. Se a pesquisa filtrada por ano não der um resultado confiável, tenta novamente sem filtro de ano. Candidatos são únicos por tipo e TMDB ID; detalhes são consultados apenas para até dois candidatos relevantes. O limite configurável é de 20 páginas e cinco consultas de detalhes. Até 50 títulos alternativos por candidato entram no cálculo; títulos e consultas são limitados a 512 pontos de código Unicode.

```go
result, err := client.IdentifyMediaWithOptionsContext(ctx, "Duna", models.MediaTypeMovie, 2021,
    api.IdentifyMediaOptions{
        MaxPages: 5,
        ConfidenceThreshold: 0.9,
        MinimumConfidenceMargin: 0.12,
        MaxDetailLookups: 3,
    })
```

A pontuação usa a maior similaridade normalizada entre a consulta e os títulos localizado, original e alternativo/traduzido obtidos nos detalhes. A normalização remove acentos, converte pontuação e espaços repetidos em uma forma comparável e preserva letras e números. Com ano, o score é `0,8 × similaridade do título + 0,2 × correspondência exata do ano`; sem ano, usa apenas a similaridade. Os limiares padrão são `0,82` com ano e `0,85` sem ano, com margem de `0,08`. Resultados abaixo do limiar ou ambíguos são retornados para revisão e nunca selecionados automaticamente. Detalhes são enriquecidos sob demanda; títulos alternativos que só aparecem nos detalhes podem elevar o score, mas não eliminam a necessidade de revisar empates.

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

Quando recebe `*sql.DB`, a biblioteca habilita `PRAGMA foreign_keys=ON` na conexão exata que executa cada migração e transação, inclusive em pools com várias conexões. A biblioteca só suporta SQL compatível com SQLite; o driver `mattn/go-sqlite3` é o único incluído e validado pelo projeto. Um teste adicional com `modernc.org/sqlite` foi adiado para não adicionar um segundo driver apenas para testes; outros drivers SQLite não têm compatibilidade automatizada garantida.

As tabelas guardam os campos originais/localizados, IMDb ID opcional, ano e data da última sincronização. O identificador TMDB continua armazenado na coluna `id` existente. Se `ID` e `TMDBID` forem preenchidos juntos, precisam corresponder; conflitos são rejeitados para evitar gravar o registro sob o ID errado. Upserts usam `ON CONFLICT DO UPDATE`, preservam IDs/metadados existentes quando uma atualização parcial não os informa, não substituem notas/popularidade por zero e não apagam relações de gêneros. Objetos retornados pelos endpoints de detalhes são marcados como completos: um zero explícito de nota ou popularidade pode atualizar um valor antigo. Detalhes completos substituem a lista de títulos alternativos em uma transação; atualizações parciais preservam títulos existentes. `SaveMovieGenres` e `SaveTVShowGenres` atualizam o conjunto de gêneros para aquele registro; as operações em lote são transacionais. `last_synced_at` registra quando a biblioteca salvou o item, mas não é uma prova de que o TMDB mudou ou permaneceu igual.

## Sincronização incremental

O pacote `pkg/syncer` usa os endpoints oficiais de mudanças de filmes e séries para atualizar somente os IDs retornados:

```go
report, err := syncer.SyncChangesContext(ctx, client, store, syncer.Options{
    MediaType: models.MediaTypeMovie,
    // StartDate omitido: retoma do checkpoint ou consulta os últimos 14 dias.
    // EndDate omitido: usa a data atual UTC.
    MaxPagesPerWindow: 100,
})
if err != nil {
    log.Printf("sync parcial: %v (%d ocorrências)", err, len(report.Failures))
}
fmt.Printf("%d itens, %d páginas, checkpoint %s\n", report.ItemsUpdated, report.PagesProcessed, report.CompletedThrough)
```

`MediaTypeTV` sincroniza séries. Os intervalos de datas são inclusivos e limitados a 14 dias por consulta, com um dia de sobreposição entre janelas para não perder mudanças na fronteira. A paginação valida a página retornada, `total_pages` e `total_results`; uma página vazia só conclui uma janela quando é a primeira e declara zero resultados. A biblioteca não consulta além de 500 páginas. Se `MaxPagesPerWindow` impor um limite menor que o total declarado, a janela falha sem avançar o checkpoint.

Mudanças e detalhes ignoram o cache HTTP. Os checkpoints são persistidos em `sync_checkpoints`, separados por tipo de mídia, e representam o último intervalo concluído; `last_synced_at` continua sendo apenas o horário de gravação local. Um checkpoint avança depois que todas as páginas forem validadas e cada ID tiver sido atualizado ou resolvido pela política de indisponíveis. HTTP 404 em detalhes é registrado em `Report.UnavailableRecords`; por padrão, é uma falha não resolvida e impede o avanço. 429, 5xx, timeout, cancelamento e falhas de persistência também impedem o avanço e ficam em `Report.Failures`, com fase, página/ID, status HTTP e indicação de retry. Nenhum desses erros remove dados locais.

Se a aplicação decidir explicitamente que um ID 404 pode ser ignorado para aquela execução, use `MissingRecordSkip`:

```go
report, err := syncer.SyncChangesContext(ctx, client, store, syncer.Options{
    MediaType: models.MediaTypeMovie,
    MissingRecordPolicy: syncer.MissingRecordSkip,
})
// Itens em report.IgnoredUnavailable foram reportados e mantidos no SQLite.
```

Esse modo permite concluir a janela, mas não agenda novas tentativas automaticamente; para reprocessar IDs ignorados, execute depois com um `StartDate` explícito que inclua a data registrada. No modo padrão, a janela incompleta é repetida após reinicialização. Páginas anteriores podem já ter sido aplicadas, mas os upserts são idempotentes. Sincronizações do mesmo tipo de mídia são serializadas dentro de uma instância de `Database`; não há coordenação entre processos diferentes ou wrappers distintos do mesmo arquivo SQLite, então execute um sincronizador por arquivo de banco. O relatório separa IDs recebidos, duplicados ignorados, itens atualizados, 404 não disponíveis/ignorados, páginas, janelas e falhas.

O TMDB documenta endpoints de mudanças e janelas de no máximo 14 dias: [tracking de mudanças](https://developer.themoviedb.org/docs/tracking-content-changes), [filmes](https://developer.themoviedb.org/reference/changes-movie-list) e [séries](https://developer.themoviedb.org/reference/changes-tv-list). A API fornece datas, não um cursor horário; por isso a sobreposição pode reprocessar IDs na fronteira.

## Erros

Erros HTTP são do tipo `*api.TMDBClientError`, com `StatusCode`, `TMDBStatusCode`, `Message` e `RetryAfter`. Use `errors.As` para inspecionar status; erros de transporte preservam `context.Canceled` e `context.DeadlineExceeded` via `errors.Is`, sem reexpor causas brutas com URLs ou credenciais. O texto do erro não inclui a chave de API nem o corpo bruto da resposta. Requisições idênticas simultâneas compartilham a chamada em andamento; cache e respostas localizadas continuam separados pela URL completa, incluindo idioma e parâmetros. O tamanho máximo padrão da resposta é 2 MiB. `BaseURL` deve ser uma URL HTTP(S) absoluta sem credenciais embutidas; em produção, use HTTPS e trate o host configurado como confiável, pois a chave v3 é enviada como parâmetro de query. O timeout vale por tentativa HTTP; use um deadline no `context.Context` para limitar também todas as tentativas e esperas de retry.

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

Esta biblioteca é distribuída sob MIT; essa licença não substitui os termos aplicáveis aos dados, imagens, marcas ou serviços do TMDB. Temporadas e episódios ainda não fazem parte do modelo persistido; a ampliação foi adiada para manter o escopo incremental e a compatibilidade.

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
go build ./...
```

Os testes HTTP usam `httptest` e não acessam o serviço real do TMDB.

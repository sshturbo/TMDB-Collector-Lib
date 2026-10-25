package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sshturbo/TMDB-Collector-Lib/pkg/config"
	"github.com/sshturbo/TMDB-Collector-Lib/pkg/models"
)

func BenchmarkCollectionAccumulatingVsProgressive(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		_, _ = fmt.Fprintf(w, `{"results":[{"id":%s,"title":"Movie 01"`, page)
		for i := 1; i < 20; i++ {
			_, _ = fmt.Fprintf(w, `},{"id":%s,"title":"Movie %02d"`, page+fmt.Sprint(i), i+1)
		}
		_, _ = fmt.Fprint(w, `}]}`)
	}))
	defer server.Close()
	cfg := &config.Config{}
	cfg.TMDB.BaseURL = server.URL + "/3"
	client := NewTMDBClientWithOptions(cfg, ClientOptions{MaxRetries: -1, CacheTTL: -1})
	b.ReportAllocs()

	b.Run("accumulating", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, _, err := client.CollectMoviesContext(context.Background(), 10); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("progressive", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := client.CollectMoviesProgressiveContext(context.Background(), 10, func(context.Context, int, []models.Movie) error { return nil }); err != nil {
				b.Fatal(err)
			}
		}
	})
}

package api

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"sync"
	"tmdb-collector/pkg/config"
	"tmdb-collector/pkg/models"
)

type TMDBClient struct {
	config *config.Config
	client *http.Client
}

type TMDBResponse struct {
	Page         int             `json:"page"`
	Results      json.RawMessage `json:"results"`
	TotalPages   int             `json:"total_pages"`
	TotalResults int             `json:"total_results"`
}

type VideoResponse struct {
	Results []struct {
		Key      string `json:"key"`
		Site     string `json:"site"`
		Type     string `json:"type"`
		Official bool   `json:"official"`
	} `json:"results"`
}

func NewTMDBClient(cfg *config.Config) *TMDBClient {
	return &TMDBClient{
		config: cfg,
		client: &http.Client{},
	}
}

func (c *TMDBClient) SearchMovies(query string, page int) ([]models.Movie, error) {
	url := fmt.Sprintf("%s/search/movie?api_key=%s&query=%s&page=%d", c.config.TMDB.BaseURL, c.config.TMDB.APIKey, url.QueryEscape(query), page)

	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("erro na requisição: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler resposta: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("erro na API (status %d): %s", resp.StatusCode, string(body))
	}

	var response TMDBResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("erro ao decodificar resposta: %v", err)
	}

	var movies []models.Movie
	if err := json.Unmarshal(response.Results, &movies); err != nil {
		return nil, fmt.Errorf("erro ao decodificar filmes: %v", err)
	}

	for i := range movies {
		if movies[i].PosterPath != "" {
			movies[i].PosterPath = c.config.TMDB.ImageBaseURL + movies[i].PosterPath
		}
	}

	return movies, nil
}

func (c *TMDBClient) SearchTVShows(query string, page int) ([]models.TVShow, error) {
	url := fmt.Sprintf("%s/search/tv?api_key=%s&query=%s&page=%d", c.config.TMDB.BaseURL, c.config.TMDB.APIKey, url.QueryEscape(query), page)

	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("erro na requisição: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler resposta: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("erro na API (status %d): %s", resp.StatusCode, string(body))
	}

	var response TMDBResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("erro ao decodificar resposta: %v", err)
	}

	var shows []models.TVShow
	if err := json.Unmarshal(response.Results, &shows); err != nil {
		return nil, fmt.Errorf("erro ao decodificar séries: %v", err)
	}

	for i := range shows {
		if shows[i].PosterPath != "" {
			shows[i].PosterPath = c.config.TMDB.ImageBaseURL + shows[i].PosterPath
		}
	}

	return shows, nil
}

func (c *TMDBClient) DiscoverMovies(page int) ([]models.Movie, error) {
	url := fmt.Sprintf("%s/discover/movie?api_key=%s&page=%d&sort_by=%s.%s&include_adult=%v&include_video=%v&language=%s&release_date.lte=%s",
		c.config.TMDB.BaseURL,
		c.config.TMDB.APIKey,
		page,
		c.config.Fetch.Sort.Movies.Field,
		c.config.Fetch.Sort.Movies.Direction,
		c.config.Fetch.IncludeAdult,
		c.config.Fetch.IncludeVideo,
		c.config.TMDB.Language,
		c.config.Fetch.MaxReleaseDate)

	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("erro na requisição: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler resposta: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("erro na API (status %d): %s", resp.StatusCode, string(body))
	}

	var response TMDBResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("erro ao decodificar resposta: %v", err)
	}

	var movies []models.Movie
	if err := json.Unmarshal(response.Results, &movies); err != nil {
		return nil, fmt.Errorf("erro ao decodificar filmes: %v", err)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 10)

	for i := range movies {
		if movies[i].PosterPath != "" {
			movies[i].PosterPath = c.config.TMDB.ImageBaseURL + movies[i].PosterPath
		}
		if movies[i].BackdropPath != "" {
			movies[i].BackdropPath = c.config.TMDB.ImageBaseURL + movies[i].BackdropPath
		}
		wg.Add(1)
		go func(m *models.Movie) {
			defer wg.Done()
			sem <- struct{}{}
			if trailer, err := c.GetMovieTrailer(m.ID); err == nil {
				m.TrailerURL = trailer
			}
			<-sem
		}(&movies[i])
	}
	wg.Wait()

	return movies, nil
}

func (c *TMDBClient) DiscoverTVShows(page int) ([]models.TVShow, error) {
	url := fmt.Sprintf("%s/discover/tv?api_key=%s&page=%d&sort_by=%s.%s&include_adult=%v&language=%s&first_air_date.lte=%s",
		c.config.TMDB.BaseURL,
		c.config.TMDB.APIKey,
		page,
		c.config.Fetch.Sort.TVShows.Field,
		c.config.Fetch.Sort.TVShows.Direction,
		c.config.Fetch.IncludeAdult,
		c.config.TMDB.Language,
		c.config.Fetch.MaxReleaseDate)

	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("erro na requisição: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler resposta: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("erro na API (status %d): %s", resp.StatusCode, string(body))
	}

	var response TMDBResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("erro ao decodificar resposta: %v", err)
	}

	var shows []models.TVShow
	if err := json.Unmarshal(response.Results, &shows); err != nil {
		return nil, fmt.Errorf("erro ao decodificar séries: %v", err)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 10)

	for i := range shows {
		if shows[i].PosterPath != "" {
			shows[i].PosterPath = c.config.TMDB.ImageBaseURL + shows[i].PosterPath
		}
		if shows[i].BackdropPath != "" {
			shows[i].BackdropPath = c.config.TMDB.ImageBaseURL + shows[i].BackdropPath
		}
		wg.Add(1)
		go func(s *models.TVShow) {
			defer wg.Done()
			sem <- struct{}{}
			if trailer, err := c.GetTVShowTrailer(s.ID); err == nil {
				s.TrailerURL = trailer
			}
			<-sem
		}(&shows[i])
	}
	wg.Wait()

	return shows, nil
}

func (c *TMDBClient) GetMovieTrailer(movieID int) (string, error) {
	getTrailer := func(lang string) (string, error) {
		url := fmt.Sprintf("%s/movie/%d/videos?api_key=%s&language=%s",
			c.config.TMDB.BaseURL,
			movieID,
			c.config.TMDB.APIKey,
			lang)

		resp, err := c.client.Get(url)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("erro na API (status %d): %s", resp.StatusCode, string(body))
		}

		var videos VideoResponse
		if err := json.Unmarshal(body, &videos); err != nil {
			return "", err
		}

		for _, video := range videos.Results {
			if video.Site == "YouTube" && video.Type == "Trailer" && video.Official {
				return fmt.Sprintf("https://www.youtube.com/watch?v=%s", video.Key), nil
			}
		}

		for _, video := range videos.Results {
			if video.Site == "YouTube" && video.Type == "Trailer" {
				return fmt.Sprintf("https://www.youtube.com/watch?v=%s", video.Key), nil
			}
		}

		for _, video := range videos.Results {
			if video.Site == "YouTube" && video.Type == "Teaser" {
				return fmt.Sprintf("https://www.youtube.com/watch?v=%s", video.Key), nil
			}
		}

		for _, video := range videos.Results {
			if video.Site == "YouTube" && video.Type == "Clip" {
				return fmt.Sprintf("https://www.youtube.com/watch?v=%s", video.Key), nil
			}
		}
		return "", nil
	}

	trailer, err := getTrailer(c.config.TMDB.Language)
	if trailer != "" {
		return trailer, nil
	}
	if c.config.TMDB.Language != "en-US" {
		trailer, err = getTrailer("en-US")
		if trailer != "" {
			return trailer, nil
		}
	}
	log.Printf("Nenhum trailer encontrado para o filme %d", movieID)
	return "", err
}

func (c *TMDBClient) GetTVShowTrailer(showID int) (string, error) {
	getTrailer := func(lang string) (string, error) {
		url := fmt.Sprintf("%s/tv/%d/videos?api_key=%s&language=%s",
			c.config.TMDB.BaseURL,
			showID,
			c.config.TMDB.APIKey,
			lang)

		resp, err := c.client.Get(url)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("erro na API (status %d): %s", resp.StatusCode, string(body))
		}

		var videos VideoResponse
		if err := json.Unmarshal(body, &videos); err != nil {
			return "", err
		}

		for _, video := range videos.Results {
			if video.Site == "YouTube" && video.Type == "Trailer" && video.Official {
				return fmt.Sprintf("https://www.youtube.com/watch?v=%s", video.Key), nil
			}
		}
		for _, video := range videos.Results {
			if video.Site == "YouTube" && video.Type == "Trailer" {
				return fmt.Sprintf("https://www.youtube.com/watch?v=%s", video.Key), nil
			}
		}
		for _, video := range videos.Results {
			if video.Site == "YouTube" && video.Type == "Teaser" {
				return fmt.Sprintf("https://www.youtube.com/watch?v=%s", video.Key), nil
			}
		}
		for _, video := range videos.Results {
			if video.Site == "YouTube" && video.Type == "Clip" {
				return fmt.Sprintf("https://www.youtube.com/watch?v=%s", video.Key), nil
			}
		}
		return "", nil
	}

	trailer, err := getTrailer(c.config.TMDB.Language)
	if trailer != "" {
		return trailer, nil
	}
	if c.config.TMDB.Language != "en-US" {
		trailer, err = getTrailer("en-US")
		if trailer != "" {
			return trailer, nil
		}
	}
	log.Printf("Nenhum trailer encontrado para a série %d", showID)
	return "", err
}

func (c *TMDBClient) FetchMovieGenres() ([]models.Genre, error) {
	url := fmt.Sprintf("%s/genre/movie/list?api_key=%s&language=%s",
		c.config.TMDB.BaseURL,
		c.config.TMDB.APIKey,
		c.config.TMDB.Language)
	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("erro na requisição de gêneros de filmes: %v", err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler resposta: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("erro na API (status %d): %s", resp.StatusCode, string(body))
	}
	var result struct {
		Genres []models.Genre `json:"genres"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("erro ao decodificar gêneros: %v", err)
	}
	return result.Genres, nil
}

func (c *TMDBClient) FetchTVShowGenres() ([]models.Genre, error) {
	url := fmt.Sprintf("%s/genre/tv/list?api_key=%s&language=%s",
		c.config.TMDB.BaseURL,
		c.config.TMDB.APIKey,
		c.config.TMDB.Language)
	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("erro na requisição de gêneros de séries: %v", err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler resposta: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("erro na API (status %d): %s", resp.StatusCode, string(body))
	}
	var result struct {
		Genres []models.Genre `json:"genres"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("erro ao decodificar gêneros: %v", err)
	}
	return result.Genres, nil
}

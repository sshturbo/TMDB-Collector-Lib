package config

type SortConfig struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

type Config struct {
	TMDB struct {
		APIKey       string `json:"api_key"`
		BaseURL      string `json:"base_url"`
		ImageBaseURL string `json:"image_base_url"`
		Language     string `json:"language"`
	} `json:"tmdb"`
	Fetch struct {
		NumPages       int    `json:"num_pages"`
		IncludeAdult   bool   `json:"include_adult"`
		IncludeVideo   bool   `json:"include_video"`
		MaxReleaseDate string `json:"max_release_date"`
		Sort           struct {
			Movies  SortConfig `json:"movies"`
			TVShows SortConfig `json:"tv_shows"`
		} `json:"sort"`
	} `json:"fetch"`
}

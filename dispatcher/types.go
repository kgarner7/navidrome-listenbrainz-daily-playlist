package dispatcher

type JobType string

const (
	FetchPatches   JobType = "fetch-patches"
	GenerateJams   JobType = "generate-jams"
	ImportPlaylist JobType = "import-playlist"
)

type generationJob struct {
	Name        string `json:"name"`
	TrackAge    int    `json:"trackAge"`
	ArtistLimit int    `json:"artistLimit"`
}

type importJob struct {
	Name  string `json:"name"`
	LbzId string `json:"lbzId"`
}

type source struct {
	SourcePatch  string `json:"sourcePatch"`
	PlaylistName string `json:"playlistName"`
}

type patchJob struct {
	Sources []source `json:"sources"`
}

type Job struct {
	JobType     JobType        `json:"jobType"`
	Username    string         `json:"username"`
	LbzUsername string         `json:"lbzUsername"`
	LbzToken    string         `json:"string"`
	Ratings     map[int32]bool `json:"ratings"`
	Fallback    int            `json:"fallback"`

	Generate *generationJob `json:"generate,omitempty"`
	Import   *importJob     `json:"import,omitempty"`
	Patch    *patchJob      `json:"patch,omitempty"`
}

type userConfig struct {
	GeneratePlaylist             bool     `json:"generatePlaylist"`
	GeneratedPlaylist            string   `json:"generatedPlaylist"`
	GeneratedPlaylistTrackAge    int      `json:"generatedPlaylistTrackAge"`
	GeneratedPlaylistArtistLimit int      `json:"generatedPlaylistArtistLimit"`
	NDUsername                   string   `json:"username"`
	LbzUsername                  string   `json:"lbzUsername"`
	LbzToken                     string   `json:"lbzToken"`
	Ratings                      []string `json:"ratings,omitempty"`
	Sources                      []source `json:"sources"`
}

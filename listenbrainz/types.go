package listenbrainz

import "time"

type lbzError struct {
	Code  int    `json:"code"`
	Error string `json:"error"`
}

type lbzPlaylistResponse struct {
	Message       string            `json:"message"`
	Status        string            `json:"status"`
	Valid         bool              `json:"valid"`
	UserName      string            `json:"user_name"`
	PlaylistCount int               `json:"playlist_count"`
	Playlists     []overallPlaylist `json:"playlists,omitempty"`
	Playlist      *LbzPlaylist      `json:"playlist,omitempty"`
}

type overallPlaylist struct {
	Playlist LbzPlaylist `json:"playlist"`
}

type LbzPlaylist struct {
	Creator    string       `json:"creator"`
	Date       time.Time    `json:"date"`
	Identifier string       `json:"identifier"`
	Title      string       `json:"title"`
	Extension  plsExtension `json:"extension"`
	Tracks     []lbTrack    `json:"track"`
}

type plsExtension struct {
	Extension playlistExtension `json:"https://musicbrainz.org/doc/jspf#playlist"`
}

type playlistExtension struct {
	AdditionalMetadata additionalMeta `json:"additional_metadata"`
}

type additionalMeta struct {
	AlgorithmMetadata algoMeta `json:"algorithm_metadata"`
}

type algoMeta struct {
	SourcePatch string `json:"source_patch"`
}

type lbTrack struct {
	Creator    string         `json:"creator"`
	Extension  trackExtension `json:"extension"`
	Identifier []string       `json:"identifier"`
	Title      string         `json:"title"`
}

type trackExtension struct {
	Track trackExtensionTrack `json:"https://musicbrainz.org/doc/jspf#track"`
}

type trackExtensionTrack struct {
	AdditionalMetadata trackAdditionalMetadata `json:"additional_metadata"`
}

type trackAdditionalMetadata struct {
	Artists []Artist `json:"artists"`
}

type Artist struct {
	MBID string `json:"artist_mbid"`
}

type LbzRecommendations struct {
	Code    int                   `json:"code"`
	Error   string                `json:"error"`
	Payload RecommendationPayload `json:"payload"`
}

type RecommendationPayload struct {
	Count       int             `json:"count"`
	LastUpdated int64           `json:"last_updated"`
	MBIDs       []RecordingMBID `json:"mbids"`
}

type RecordingMBID struct {
	RecordingMBID string `json:"recording_mbid"`
}

type lbzMetadataLookup struct {
	Artist    artistCredit      `json:"artist"`
	Recording extendedRecording `json:"recording"`
}

type artistCredit struct {
	Artists []extendedArtist `json:"artists"`
}

type extendedArtist struct {
	ArtistMbid string `json:"artist_mbid"`
	Name       string `json:"name"`
}

type extendedRecording struct {
	Name string `json:"name"`
}

type recLookup struct {
	RecordingMbids []string `json:"recording_mbids"`
	Inc            string   `json:"inc"`
}

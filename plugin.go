//go:build wasip1

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/extism/go-pdk"
	"github.com/microcosm-cc/bluemonday"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/server/subsonic/responses"
)

const (
	lbzEndpoint    = "https://api.listenbrainz.org/1"
	peridiocSyncId = "listenbrainz"
	userAgent      = "NavidromePlaylistImporter/0.2"
)

type source struct {
	SourcePatch  string `json:"sourcePatch"`
	PlaylistName string `json:"playlistName"`
}

type userConfig struct {
	NDUsername  string   `json:"username"`
	LbzUsername string   `json:"lbzUsername"`
	Ratings     []string `json:"ratings,omitempty"`
	Sources     []source `json:"sources"`
}

type BrainzPlaylistPlugin struct {
	artistMbidToId map[string]string
}

type listenBrainzResponse struct {
	Code          int               `json:"code"`
	Message       string            `json:"message"`
	Error         string            `json:"error"`
	Status        string            `json:"status"`
	Valid         bool              `json:"valid"`
	UserName      string            `json:"user_name"`
	PlaylistCount int               `json:"playlist_count"`
	Playlists     []overallPlaylist `json:"playlists,omitempty"`
	Playlist      *lbPlaylist       `json:"playlist,omitempty"`
}

type overallPlaylist struct {
	Playlist lbPlaylist `json:"playlist"`
}

type lbPlaylist struct {
	Annotation string       `json:"annotation"`
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
	LastModified       time.Time      `json:"last_modified_at"`
	Public             bool           `json:"public"`
}

type additionalMeta struct {
	AlgorithmMetadata algoMeta `json:"algorithm_metadata"`
}

type algoMeta struct {
	SourcePatch string `json:"source_patch"`
}

type lbTrack struct {
	Creator   string `json:"creator"`
	Extension struct {
		Track struct {
			AdditionalMetadata struct {
				Artists []struct {
					MBID string `json:"artist_mbid"`
				} `json:"artists"`
			} `json:"additional_metadata"`
		} `json:"https://musicbrainz.org/doc/jspf#track"`
	} `json:"extension"`
	Identifier []string `json:"identifier"`
	Title      string   `json:"title"`
}

func getIdentifier(url string) string {
	split := strings.Split(url, "/")
	return split[len(split)-1]
}

func (b *BrainzPlaylistPlugin) getPlaylists(lbzUsername string) ([]overallPlaylist, error) {
	req := pdk.NewHTTPRequest(pdk.MethodGet, fmt.Sprintf("%s/user/%s/playlists/createdfor", lbzEndpoint, lbzUsername))
	req.SetHeader("Accept", "application/json")
	req.SetHeader("User-Agent", userAgent)
	resp := req.Send()

	var result listenBrainzResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("Failed to decode JSON: %v", err)
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("ListenBrainz HTTP Error. Code: %d, Error: %s", result.Code, result.Error)
	}

	return result.Playlists, nil
}

func (b *BrainzPlaylistPlugin) getPlaylist(id string) (*lbPlaylist, error) {
	req := pdk.NewHTTPRequest(pdk.MethodGet, fmt.Sprintf("%s/playlist/%s", lbzEndpoint, id))
	req.SetHeader("Accept", "application/json")
	req.SetHeader("User-Agent", userAgent)
	resp := req.Send()

	var result listenBrainzResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("Failed to decode JSON: %v", err)
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("ListenBrainz HTTP Error. Code: %d, Error: %s", result.Code, result.Error)
	}

	if result.Playlist == nil {
		return nil, fmt.Errorf("Nothing parsed for playlist %s", id)
	}

	return result.Playlist, nil
}

func (b *BrainzPlaylistPlugin) makeSubsonicRequest(endpoint, subsonicUser string, params *url.Values) (*responses.JsonWrapper, bool) {
	subsonicResp, err := host.SubsonicAPICall(fmt.Sprintf("/rest/%s?u=%s&%s", endpoint, subsonicUser, params.Encode()))

	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("An error occurred %s: %v", subsonicUser, err))
		return nil, false
	}

	var decoded responses.JsonWrapper
	if err := json.Unmarshal([]byte(subsonicResp), &decoded); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("A deserialization error occurred %s: %s", subsonicUser, err))
		return nil, false
	}

	if decoded.Subsonic.Status != "ok" {
		pdk.Log(pdk.LogError, fmt.Sprintf("Subsonic status is not ok: (%d)%s", decoded.Subsonic.Error.Code, decoded.Subsonic.Error.Message))
		return nil, false
	}

	return &decoded, true
}

func (b *BrainzPlaylistPlugin) findExistingPlaylist(resp *responses.JsonWrapper, playlistName string) *responses.Playlist {
	if len(resp.Subsonic.Playlists.Playlist) > 0 {
		for _, playlist := range resp.Subsonic.Playlists.Playlist {
			if playlist.Name == playlistName {
				return &playlist
			}
		}
	}

	return nil
}

func (b *BrainzPlaylistPlugin) findArtistIdByMbid(
	subsonicUser string,
	mbid string,
) string {
	existing, ok := b.artistMbidToId[mbid]
	if ok {
		return existing
	}

	artistParams := url.Values{
		"artistCount": []string{"1"},
		"albumCount":  []string{"0"},
		"songCount":   []string{"0"},
		"query":       []string{mbid},
	}

	resp, ok := b.makeSubsonicRequest("search3", subsonicUser, &artistParams)
	if !ok {
		return ""
	}

	var id string

	if len(resp.Subsonic.SearchResult3.Artist) > 0 {
		id = resp.Subsonic.SearchResult3.Artist[0].Id
	} else {
		id = ""
	}

	b.artistMbidToId[mbid] = id
	return id
}

func (b *BrainzPlaylistPlugin) fallbackLookup(
	subsonicUser string,
	track *lbTrack,
	fallbackCount int,
) *responses.Child {
	artistIds := map[string]bool{}

	for _, artist := range track.Extension.Track.AdditionalMetadata.Artists {
		id := b.findArtistIdByMbid(subsonicUser, artist.MBID)
		if id == "" {
			return nil
		}

		artistIds[id] = true
	}

	trackParams := url.Values{
		"artistCount": []string{"0"},
		"albumCount":  []string{"0"},
		"songCount":   []string{strconv.Itoa(fallbackCount)}, // I do not know what number is reasonable for multiple matches. Maybe I'll make it configurable
		"query":       []string{track.Title},
	}

	resp, ok := b.makeSubsonicRequest("search3", subsonicUser, &trackParams)
	if !ok {
		return nil
	}

	for _, subsonicTrack := range resp.Subsonic.SearchResult3.Song {
		if subsonicTrack.Title == track.Title && len(artistIds) == len(subsonicTrack.Artists) {
			missing := false

			for _, artist := range subsonicTrack.Artists {
				found := artistIds[artist.Id]

				if !found {
					missing = true
				}
			}

			if !missing {
				return &subsonicTrack
			}
		}
	}

	return nil
}

func (b *BrainzPlaylistPlugin) importPlaylist(
	source *source,
	subsonicUser string,
	playlists []overallPlaylist,
	rating map[int32]bool,
	fallbackCount int,
) {
	var id string
	var err error
	var listenBrainzPlaylist *lbPlaylist

	for _, plsMetadata := range playlists {
		if plsMetadata.Playlist.Extension.Extension.AdditionalMetadata.AlgorithmMetadata.SourcePatch == source.SourcePatch {
			id = getIdentifier(plsMetadata.Playlist.Identifier)

			listenBrainzPlaylist, err = b.getPlaylist(id)
			break
		}
	}

	if err != nil {
		err = errors.Join(err)
		pdk.Log(pdk.LogError, fmt.Sprintf("Failed to fetch playlist %s for user %s: %v", id, subsonicUser, err))
		return
	} else if listenBrainzPlaylist == nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Failed to get daily jams playlist for user %s", subsonicUser))
		return
	}

	songIds := []string{}

	trackParams := url.Values{
		"artistCount": []string{"0"},
		"albumCount":  []string{"0"},
		"songCount":   []string{"1"},
	}

	missing := []string{}
	excluded := []string{}

	for _, track := range listenBrainzPlaylist.Tracks {
		mbid := getIdentifier(track.Identifier[0])
		trackParams.Set("query", mbid)

		resp, ok := b.makeSubsonicRequest("search3", subsonicUser, &trackParams)
		if !ok {
			continue
		}

		var song *responses.Child

		if len(resp.Subsonic.SearchResult3.Song) > 0 {
			song = &resp.Subsonic.SearchResult3.Song[0]
		} else {
			song = b.fallbackLookup(subsonicUser, &track, fallbackCount)
		}

		if song != nil {
			if rating[song.UserRating] {
				songIds = append(songIds, song.Id)
			} else {
				excluded = append(excluded, fmt.Sprintf("%s by %s", track.Title, track.Creator))
			}
		} else {
			missing = append(missing, fmt.Sprintf("%s by %s", track.Title, track.Creator))
		}
	}

	resp, ok := b.makeSubsonicRequest("getPlaylists", subsonicUser, &url.Values{})
	if !ok {
		return
	}

	existingPlaylist := b.findExistingPlaylist(resp, source.PlaylistName)
	if len(songIds) != 0 || existingPlaylist != nil {
		createPlaylistParams := url.Values{
			"songId": songIds,
		}

		if existingPlaylist != nil {
			createPlaylistParams.Add("playlistId", existingPlaylist.Id)
		} else {
			createPlaylistParams.Add("name", source.PlaylistName)
		}

		resp, ok = b.makeSubsonicRequest("createPlaylist", subsonicUser, &createPlaylistParams)
		if !ok {
			pdk.Log(pdk.LogError, fmt.Sprintf("failed to create playlist %s", source.PlaylistName))
			return
		}

		if existingPlaylist == nil && resp.Subsonic.Playlist != nil {
			existingPlaylist = &responses.Playlist{
				Id:        resp.Subsonic.Playlist.Id,
				SongCount: int32(len(songIds)),
			}
		}
	}

	comment := fmt.Sprintf("%s&nbsp;\n%s", listenBrainzPlaylist.Annotation, listenBrainzPlaylist.Identifier)

	if len(missing) > 0 {
		comment += fmt.Sprintf("&nbsp;\nTracks not matched by MBID: %s", strings.Join(missing, ", "))
	}

	if len(excluded) > 0 {
		comment += fmt.Sprintf("&nbsp;\nTracks excluded by rating rule: %s", strings.Join(excluded, ", "))
	}

	if existingPlaylist.Comment != comment {
		policy := bluemonday.StrictPolicy()
		sanitized := html.UnescapeString(policy.Sanitize(comment))

		updatePlaylistParams := url.Values{
			"playlistId": []string{existingPlaylist.Id},
			"comment":    []string{sanitized},
		}

		if len(songIds) == 0 {
			for i := range existingPlaylist.SongCount {
				updatePlaylistParams.Add("songIndexToRemove", strconv.Itoa(int(i)))
			}
			pdk.Log(pdk.LogInfo, fmt.Sprintf("No matching files found for playlist %s", source.PlaylistName))
		}

		_, ok = b.makeSubsonicRequest("updatePlaylist", subsonicUser, &updatePlaylistParams)
		if !ok {
			return
		}
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Successfully processed playlist %s for user %s", source.PlaylistName, subsonicUser))
}

func (b *BrainzPlaylistPlugin) updatePlaylists(users []userConfig, fallbackCount int) {
	b.artistMbidToId = map[string]string{}

	for _, userData := range users {
		ratings := map[int32]bool{}

		if userData.Ratings != nil {
			for _, rating := range userData.Ratings {
				ratingInt, err := strconv.ParseInt(rating, 10, 32)
				if err != nil {
					continue
				}

				if ratingInt >= 0 && ratingInt <= 5 {
					ratings[int32(ratingInt)] = true
				}
			}
		}

		if len(ratings) == 0 {
			ratings = map[int32]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true}
		}

		playlists, err := b.getPlaylists(userData.LbzUsername)
		if err != nil {
			pdk.Log(pdk.LogError, fmt.Sprintf("Failed to fetch playlists for user %s: %v", userData.NDUsername, err))
			continue
		}

		for _, source := range userData.Sources {
			b.importPlaylist(&source, userData.NDUsername, playlists, ratings, fallbackCount)
		}
	}
}

func getConfig() ([]userConfig, int, error) {
	users, ok := pdk.GetConfig("users")
	if !ok {
		return nil, 0, errors.New("missing required 'users' configuration")
	}

	userMapping := []userConfig{}
	err := json.Unmarshal([]byte(users), &userMapping)
	if err != nil {
		return nil, 0, fmt.Errorf("Invalid user mapping: %s. Should be a mapping of Navidrome users to ListenBrainz usernames", users)
	}

	fallback, ok := pdk.GetConfig("fallbackCount")
	fallbackCount := 15

	if ok {
		value, err := strconv.Atoi(fallback)
		if err != nil {
			return nil, 0, errors.New("`fallbackCount` is not a valid number")
		}

		if value < 1 || value > 500 {
			return nil, 0, errors.New("`fallbackCount` must be between 1 and 500 (inclusive)")
		}

		fallbackCount = value
	}

	return userMapping, fallbackCount, nil
}

func (b *BrainzPlaylistPlugin) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	userMapping, fallbackCount, err := getConfig()
	if err != nil {
		return err
	}

	b.updatePlaylists(userMapping, fallbackCount)
	return nil
}

func (b *BrainzPlaylistPlugin) initialFetch(
	users []userConfig,
	fallbackCount int,
) error {
	nowTs := time.Now()

	missing := false
	olderThanThreeHours := false

userLoop:
	for _, user := range users {
		playlistResp, ok := b.makeSubsonicRequest("getPlaylists", user.NDUsername, &url.Values{})
		if !ok {
			return errors.New("Failed to fetch playlists on initial fetch")
		}

		for _, source := range user.Sources {
			pls := b.findExistingPlaylist(playlistResp, source.PlaylistName)

			if pls == nil {
				missing = true
				break userLoop
			}

			if nowTs.Sub(pls.Changed) > 3*time.Hour {
				olderThanThreeHours = true
				break userLoop
			}
		}
	}

	if missing || olderThanThreeHours {
		pdk.Log(pdk.LogInfo, "Missing or outdated playlists, fetching on initial sync")
		b.updatePlaylists(users, fallbackCount)
	} else {
		pdk.Log(pdk.LogInfo, "No missing/outdated playlists, not fetching on startup")
	}

	return nil
}

func (b *BrainzPlaylistPlugin) OnInit() error {
	schedule, ok := pdk.GetConfig("schedule")
	if !ok {
		schedule = "@every 24h"
	}

	userMapping, fallbackCount, err := getConfig()
	if err != nil {
		return err
	}

	_, err = host.SchedulerScheduleRecurring(schedule, "playlist-fetch", peridiocSyncId)
	if err != nil {
		return fmt.Errorf("Failed to schedule playlist sync. Is your schedule a valid cron expression? %v", err)
	}

	checkOnStartup, ok := pdk.GetConfig("checkOnStartup")

	if !ok || checkOnStartup != "false" {
		err := b.initialFetch(userMapping, fallbackCount)
		if err != nil {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("Failed to do initial sync. Proceeding anyway %w", err))
		}
	}

	pdk.Log(pdk.LogInfo, "init success")

	return nil
}

func main() {}

func init() {
	lifecycle.Register(&BrainzPlaylistPlugin{})
	scheduler.Register(&BrainzPlaylistPlugin{})
}

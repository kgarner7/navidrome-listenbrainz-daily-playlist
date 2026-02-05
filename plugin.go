//go:build wasip1

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/extism/go-pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/server/subsonic/responses"
)

const (
	lbzEndpoint    = "https://api.listenbrainz.org/1"
	peridiocSyncId = "listenbrainz"
	userAgent      = "NavidromePlaylistImporter/3.0"
	initialFetchId = "initial-fetch"
)

var (
	allowedSchedules = map[string]bool{
		"@every 12h": true, "@every 24h": true, "@every 48h": true, "@weekly": true, "@monthly": true,
	}
)

type source struct {
	SourcePatch  string `json:"sourcePatch"`
	PlaylistName string `json:"playlistName"`
}

type userConfig struct {
	GeneratePlaylist  bool     `json:"generatePlaylist"`
	GeneratedPlaylist string   `json:"generatedPlaylist"`
	NDUsername        string   `json:"username"`
	LbzUsername       string   `json:"lbzUsername"`
	Ratings           []string `json:"ratings,omitempty"`
	Sources           []source `json:"sources"`
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

func processRatelimit(resp *pdk.HTTPResponse) {
	headers := resp.Headers()

	remaining, remOk := headers["x-ratelimit-remaining"]
	resetIn, resetOk := headers["x-ratelimit-reset-in"]

	if remOk && resetOk {
		pdk.Log(pdk.LogTrace, fmt.Sprintf("ListenBrainz ratelimit check: Remaining=%s, Reset in=%s seconds", remaining, resetIn))

		remInt, err := strconv.Atoi(remaining)
		if err != nil {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("Rate limit remaining is not a valid number: %s", remaining))
			return
		}

		resetInt, err := strconv.Atoi(resetIn)
		if err != nil {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("Reset in is not a valid number: %s", resetIn))
			return
		}

		// Have a buffer for rate limit, in case some other application comes in at the same time
		// From my experience, the rate limit is 30 requests / 10 seconds
		if remInt <= 5 {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("Approaching rate limit, delaying further processing for %d seconds", resetInt))
			time.Sleep(time.Duration(resetInt))
		}
	}
}

func (b *BrainzPlaylistPlugin) getPlaylists(lbzUsername string) ([]overallPlaylist, error) {
	req := pdk.NewHTTPRequest(pdk.MethodGet, fmt.Sprintf("%s/user/%s/playlists/createdfor", lbzEndpoint, lbzUsername))
	req.SetHeader("Accept", "application/json")
	req.SetHeader("User-Agent", userAgent)
	resp := req.Send()

	processRatelimit(&resp)

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

	processRatelimit(&resp)

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
		pdk.Log(pdk.LogDebug, fmt.Sprintf("Artist found by mbid: %s", resp.Subsonic.SearchResult3.Artist[0].Name))
	} else {
		id = ""
		pdk.Log(pdk.LogDebug, fmt.Sprintf("Artist not found by mbid: %s", mbid))
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
		"songCount":   []string{strconv.Itoa(fallbackCount)},
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

type lbzRecommendations struct {
	Code    int    `json:"code"`
	Error   string `json:"error"`
	Payload struct {
		Count       int    `json:"count"`
		Entity      string `json:"entity"`
		LastUpdated int64  `json:"last_updated"`
		MBIDs       []struct {
			LatestListenedAt string  `json:"string"`
			Score            float64 `json:"score"`
			RecordingMBID    string  `json:"recording_mbid"`
		} `json:"mbids"`
	} `json:"payload"`
}

func (b *BrainzPlaylistPlugin) updateSubsonicPlaylist(subsonicUser, playlistName, comment string, songIds []string) error {
	subsonicResp, ok := b.makeSubsonicRequest("getPlaylists", subsonicUser, &url.Values{"username": []string{subsonicUser}})
	if !ok {
		return errors.New("Failed to fetch subsonic playlists for user " + subsonicUser)
	}

	existingPlaylist := b.findExistingPlaylist(subsonicResp, playlistName)
	createPlaylistParams := url.Values{"songId": songIds}

	if existingPlaylist != nil {
		createPlaylistParams.Add("playlistId", existingPlaylist.Id)
	} else {
		createPlaylistParams.Add("name", playlistName)
	}

	subsonicResp, ok = b.makeSubsonicRequest("createPlaylist", subsonicUser, &createPlaylistParams)
	if !ok {
		return fmt.Errorf("failed to create playlist %s", playlistName)
	}

	if subsonicResp.Subsonic.Playlist != nil && subsonicResp.Subsonic.Playlist.Comment != comment {
		updatePlaylistParams := url.Values{
			"playlistId": []string{subsonicResp.Subsonic.Playlist.Id},
			"comment":    []string{comment},
		}

		_, ok = b.makeSubsonicRequest("updatePlaylist", subsonicUser, &updatePlaylistParams)
		if !ok {
			return fmt.Errorf("Failed to update playlist %s for %s", playlistName, subsonicUser)
		}
	}

	return nil
}

func (b *BrainzPlaylistPlugin) createJams(
	subsonicUser, lbzUsername, playlistName string,
	minLastPlayedDays, preferredMinPerArtist int,
	rating map[int32]bool,
) error {
	req := pdk.NewHTTPRequest(pdk.MethodGet, fmt.Sprintf("%s/cf/recommendation/user/%s/recording?count=1000", lbzEndpoint, lbzUsername))
	req.SetHeader("Accept", "application/json")
	req.SetHeader("User-Agent", userAgent)
	resp := req.Send()

	processRatelimit(&resp)

	recommendations := lbzRecommendations{}
	err := json.Unmarshal(resp.Body(), &recommendations)
	if err != nil {
		return err
	}

	if recommendations.Code != 0 {
		return fmt.Errorf("ListenBrainz HTTP Error. Code: %d, Error: %s", recommendations.Code, recommendations.Error)
	}

	if recommendations.Payload.Count == 0 || len(recommendations.Payload.MBIDs) == 0 {
		return fmt.Errorf("No recommendations found for user %s", lbzUsername)
	}

	now := time.Now()

	trackParams := url.Values{
		"artistCount": []string{"0"},
		"albumCount":  []string{"0"},
		"songCount":   []string{"1"},
	}

	allowedSongs := []*responses.Child{}
	notPlayed := []*responses.Child{}

	excludedCount := 0
	missingCount := 0
	recentCount := 0

	for _, mbid := range recommendations.Payload.MBIDs {
		trackParams.Set("query", mbid.RecordingMBID)

		resp, ok := b.makeSubsonicRequest("search3", subsonicUser, &trackParams)
		if !ok {
			continue
		}

		if len(resp.Subsonic.SearchResult3.Song) == 0 {
			missingCount += 1
			pdk.Log(pdk.LogTrace, fmt.Sprintf("Could not find track `%s` by mbid", mbid.RecordingMBID))
			continue
		}

		song := &resp.Subsonic.SearchResult3.Song[0]
		if !rating[song.UserRating] {
			excludedCount += 1
			pdk.Log(pdk.LogTrace, fmt.Sprintf("Excluding track `%s` per rating", song.Title))
			continue
		}

		if song.Played == nil {
			notPlayed = append(notPlayed, song)
			continue
		}

		if now.Sub(*song.Played).Hours() < float64(minLastPlayedDays*24) {
			recentCount += 1
			pdk.Log(pdk.LogTrace, fmt.Sprintf("Excluding track `%s` for being played recently", song.Title))
			continue
		}

		allowedSongs = append(allowedSongs, song)
	}

	if len(allowedSongs) < 50 {
		unlistenedCount := min(50-len(allowedSongs), len(notPlayed))
		allowedSongs = append(allowedSongs, notPlayed[0:unlistenedCount]...)
	}

	songIds := []string{}

	if len(allowedSongs) < 50 {
		for _, song := range allowedSongs {
			songIds = append(songIds, song.Id)
		}
	} else {
		artistCredits := map[string]int{}

		workingSet := allowedSongs
		nextSet := []*responses.Child{}

		for len(songIds) < 50 {
		outer:
			for _, song := range workingSet {
				for _, artist := range song.Artists {
					count := artistCredits[artist.Id]
					if count >= preferredMinPerArtist {
						nextSet = append(nextSet, song)
						continue outer
					}
				}

				songIds = append(songIds, song.Id)
				if len(songIds) == 50 {
					break outer
				}

				for _, artist := range song.Artists {
					artistCredits[artist.Id] += 1
				}
			}

			workingSet = nextSet
			nextSet = []*responses.Child{}

			preferredMinPerArtist += 1
		}
	}

	comment := fmt.Sprintf(
		"Jams generated on %s from %d recommendations.\nMissing matches by MBID: %d\nExcluded by rating: %d\nExcluded for being recent: %d",
		now.Format(time.RFC1123), recommendations.Payload.Count, missingCount, excludedCount, recentCount,
	)

	return b.updateSubsonicPlaylist(subsonicUser, playlistName, comment, songIds)
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

	pdk.Log(pdk.LogDebug, fmt.Sprintf("Importing playlist `%s`", listenBrainzPlaylist.Title))

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

	resp, ok := b.makeSubsonicRequest("getPlaylists", subsonicUser, &url.Values{"username": []string{subsonicUser}})
	if !ok {
		pdk.Log(pdk.LogError, "Failed to fetch subsonic playlists for user "+subsonicUser)
		return
	}

	existingPlaylist := b.findExistingPlaylist(resp, source.PlaylistName)
	createPlaylistParams := url.Values{"songId": songIds}

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

	comment := fmt.Sprintf("Generated from source %s\n%s\nUpdated on: %s", source.SourcePatch, listenBrainzPlaylist.Identifier, listenBrainzPlaylist.Date)

	if len(missing) > 0 {
		comment += fmt.Sprintf("\nTracks not matched by track MBID or track name + artist MBIDs: %s", strings.Join(missing, ", "))
	}

	if len(excluded) > 0 {
		comment += fmt.Sprintf("\nTracks excluded by rating rule: %s", strings.Join(excluded, ", "))
	}

	// There are two cases where the existing playlist should be updated: the comment needs updating
	// and (for whatever reason), the current playlist has no matching tracks, but the existing one does
	if existingPlaylist != nil && (existingPlaylist.Comment != comment ||
		(len(songIds) == 0 && existingPlaylist.SongCount != 0)) {

		// If the current song count is empty, empty the playlist. This can't be done with createPlaylist
		if len(songIds) == 0 {
			comment += "\nNo matches were found for ListenBrainz playlist. Playlist content refers to prior playlist"
			pdk.Log(pdk.LogWarn, fmt.Sprintf("No matching files found for playlist %s", source.PlaylistName))
		}

		updatePlaylistParams := url.Values{
			"playlistId": []string{existingPlaylist.Id},
			"comment":    []string{comment},
		}

		_, ok = b.makeSubsonicRequest("updatePlaylist", subsonicUser, &updatePlaylistParams)
		if !ok {
			pdk.Log(pdk.LogError, fmt.Sprintf("Failed to update playlist %s for %s", source.PlaylistName, subsonicUser))
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

		if userData.GeneratePlaylist && userData.GeneratedPlaylist != "" {
			err := b.createJams(userData.NDUsername, userData.LbzUsername, userData.GeneratedPlaylist, 60, 2, ratings)
			if err != nil {
				pdk.Log(pdk.LogError, fmt.Sprintf("Failed to generate playlist `%s` for user `%s` locally: %v", userData.GeneratedPlaylist, userData.NDUsername, err))
			} else {
				pdk.Log(pdk.LogInfo, fmt.Sprintf("Successfully generated playlist `%s` for user `%s`", userData.GeneratedPlaylist, userData.NDUsername))
			}
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

func (b *BrainzPlaylistPlugin) initialFetch(
	users []userConfig,
	fallbackCount int,
) error {
	nowTs := time.Now()

	missing := []string{}
	olderThanThreeHours := []string{}

userLoop:
	for _, user := range users {
		playlistResp, ok := b.makeSubsonicRequest("getPlaylists", user.NDUsername, &url.Values{})
		if !ok {
			return errors.New("Failed to fetch playlists on initial fetch")
		}

		for _, source := range user.Sources {
			pls := b.findExistingPlaylist(playlistResp, source.PlaylistName)

			if pls == nil {
				missing = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, source.PlaylistName))
				break userLoop
			}

			if nowTs.Sub(pls.Changed) > 3*time.Hour {
				olderThanThreeHours = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, source.PlaylistName))
				break userLoop
			}
		}

		if user.GeneratePlaylist && user.GeneratedPlaylist != "" {
			pls := b.findExistingPlaylist(playlistResp, user.GeneratedPlaylist)
			if pls == nil {
				missing = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, user.GeneratedPlaylist))
				break
			}

			if nowTs.Sub(pls.Changed) > 3*time.Hour {
				olderThanThreeHours = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, user.GeneratedPlaylist))
				break userLoop
			}
		}
	}

	if len(missing) > 0 || len(olderThanThreeHours) > 0 {
		pdk.Log(pdk.LogInfo,
			fmt.Sprintf("Missing or outdated playlists, fetching on initial sync. Missing: %v, Outdated: %v",
				missing,
				olderThanThreeHours,
			))
		b.updatePlaylists(users, fallbackCount)
	} else {
		pdk.Log(pdk.LogInfo, "No missing/outdated playlists, not fetching on startup")
	}

	return nil
}

func (b *BrainzPlaylistPlugin) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	userMapping, fallbackCount, err := getConfig()
	if err != nil {
		return err
	}

	if req.Payload == initialFetchId {
		b.initialFetch(userMapping, fallbackCount)
	} else {
		b.updatePlaylists(userMapping, fallbackCount)
	}

	return nil
}

func (b *BrainzPlaylistPlugin) OnInit() error {
	schedule, ok := pdk.GetConfig("schedule")
	if !ok {
		schedule = "@every 24h"
	}

	_, ok = allowedSchedules[schedule]
	if !ok {
		return fmt.Errorf("%s is not an allowed sync schedule", schedule)
	}

	_, _, err := getConfig()
	if err != nil {
		return err
	}

	_, err = host.SchedulerScheduleRecurring(schedule, "playlist-fetch", peridiocSyncId)
	if err != nil {
		return fmt.Errorf("Failed to schedule playlist sync. Is your schedule a valid cron expression? %v", err)
	}

	checkOnStartup, ok := pdk.GetConfig("checkOnStartup")

	if !ok || checkOnStartup != "false" {
		_, err := host.SchedulerScheduleOneTime(1, initialFetchId, "initialFetchId")
		if err != nil {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("Failed to do initial sync. Proceeding anyway %v", err))
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

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"listenbrainz-daily-playlist/listenbrainz"
	"math/rand"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/server/subsonic/responses"
)

const (
	initialFetchId = "initial-fetch"
	delayedSync    = "delayed-sync"
	dailyCron      = "daily-cron"
)

type source struct {
	SourcePatch  string `json:"sourcePatch"`
	PlaylistName string `json:"playlistName"`
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

type BrainzPlaylistPlugin struct {
	artistMbidToId map[string]string
	fallbackCount  int
}

func getIdentifier(url string) string {
	split := strings.Split(url, "/")
	return split[len(split)-1]
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

func findExistingPlaylist(resp *responses.JsonWrapper, playlistName string) *responses.Playlist {
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

func (b *BrainzPlaylistPlugin) updateSubsonicPlaylist(subsonicUser, playlistName, comment string, songIds []string) error {
	subsonicResp, ok := b.makeSubsonicRequest("getPlaylists", subsonicUser, &url.Values{"username": []string{subsonicUser}})
	if !ok {
		return errors.New("Failed to fetch subsonic playlists for user " + subsonicUser)
	}

	existingPlaylist := findExistingPlaylist(subsonicResp, playlistName)
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
	userData *userConfig,
	rating map[int32]bool,
) error {
	now := time.Now()

	recommendations, err := listenbrainz.GetRecommendations(userData.LbzUsername, userData.LbzToken)
	if err != nil {
		return err
	}

	mbids := make([]string, len(recommendations.Payload.MBIDs))
	for idx, recording := range recommendations.Payload.MBIDs {
		mbids[idx] = recording.RecordingMBID
	}

	metadata, err := listenbrainz.LookupRecordings(mbids, userData.LbzToken)
	if err != nil {
		return err
	}

	allowedSongs := []*responses.Child{}
	notPlayed := []*responses.Child{}

	missing := []string{}
	excluded := []string{}
	recentCount := 0

	for _, mbid := range mbids {
		recordingMetadata, ok := metadata[mbid]
		if !ok {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("Warning: track with mbid %s not found in metadata lookup. Skipping", mbid))
			continue
		}

		artistMbids := make([]string, len(recordingMetadata.Artist.Artists))
		for idx, artist := range recordingMetadata.Artist.Artists {
			artistMbids[idx] = artist.ArtistMbid
		}

		song := b.lookupTrack(userData.NDUsername, recordingMetadata.Recording.Name, mbid, artistMbids)
		if song == nil {
			missing = append(missing, recordingMetadata.Recording.Name)
			continue
		}

		if !rating[song.UserRating] {
			excluded = append(excluded, recordingMetadata.Recording.Name)
			continue
		}

		if song.Played == nil {
			notPlayed = append(notPlayed, song)
			continue
		}

		if now.Sub(*song.Played).Hours() < float64(userData.GeneratedPlaylistTrackAge*24) {
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

	if userData.GeneratedPlaylistArtistLimit == 0 {
		for _, song := range allowedSongs[:min(len(allowedSongs), 50)] {
			songIds = append(songIds, song.Id)
		}
	} else {
		artistCredits := map[string]int{}

	outer:
		for _, song := range allowedSongs {
			for _, artist := range song.Artists {
				count := artistCredits[artist.Id]
				if count >= userData.GeneratedPlaylistArtistLimit {
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
	}

	recsUpdated := time.Unix(recommendations.Payload.LastUpdated, recommendations.Payload.LastUpdated).Format(time.RFC1123)

	comment := fmt.Sprintf(
		"Jams generated on %s with %d recommendations generated on %s."+
			"\nExcluded by rating rules: %s\nTracks not found in library: %s\nExcluded for being recent: %d",
		now.Format(time.RFC1123), len(mbids), recsUpdated,
		strings.Join(excluded, ", "),
		strings.Join(missing, ", "),
		recentCount,
	)

	return b.updateSubsonicPlaylist(userData.NDUsername, userData.GeneratedPlaylist, comment, songIds)
}

func (b *BrainzPlaylistPlugin) lookupTrack(
	subsonicUser, title, mbid string,
	artistMbids []string,
) *responses.Child {
	trackParams := url.Values{
		"artistCount": []string{"0"},
		"albumCount":  []string{"0"},
		"songCount":   []string{"1"},
		"query":       []string{mbid},
	}

	resp, ok := b.makeSubsonicRequest("search3", subsonicUser, &trackParams)
	if !ok {
		return nil
	}

	var song *responses.Child

	if len(resp.Subsonic.SearchResult3.Song) > 0 {
		song = &resp.Subsonic.SearchResult3.Song[0]
	} else {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("Could not find track by MBID: %s", mbid))
		artistIds := map[string]bool{}

		for _, artist := range artistMbids {
			id := b.findArtistIdByMbid(subsonicUser, artist)
			if id == "" {
				return nil
			}

			artistIds[id] = true
		}

		trackParams = url.Values{
			"artistCount": []string{"0"},
			"albumCount":  []string{"0"},
			"songCount":   []string{strconv.Itoa(b.fallbackCount)},
			"query":       []string{title},
		}

		resp, ok := b.makeSubsonicRequest("search3", subsonicUser, &trackParams)
		if !ok {
			return nil
		}

		for _, subsonicTrack := range resp.Subsonic.SearchResult3.Song {
			if subsonicTrack.Title == title && len(artistIds) == len(subsonicTrack.Artists) {
				missing := false

				for _, artist := range subsonicTrack.Artists {
					found := artistIds[artist.Id]

					if !found {
						missing = true
					}
				}

				if !missing {
					song = &subsonicTrack
					break
				}
			}
		}

		if song == nil {
			pdk.Log(pdk.LogDebug, fmt.Sprintf("Could not find song by matching title and artist mbids: %s, %v", title, artistMbids))
			return nil
		}
	}

	return song
}

func (b *BrainzPlaylistPlugin) importPlaylist(
	source *source,
	subsonicUser, lbzToken string,
	playlists []*listenbrainz.LbzPlaylist,
	rating map[int32]bool,
) {
	var playlistId string
	var err error
	var listenBrainzPlaylist *listenbrainz.LbzPlaylist

	for _, plsMetadata := range playlists {
		if plsMetadata.Extension.Extension.AdditionalMetadata.AlgorithmMetadata.SourcePatch == source.SourcePatch {
			playlistId = getIdentifier(plsMetadata.Identifier)
			listenBrainzPlaylist, err = listenbrainz.GetPlaylist(playlistId, lbzToken)
			break
		}
	}

	if err != nil {
		err = errors.Join(err)
		pdk.Log(pdk.LogError, fmt.Sprintf("Failed to fetch playlist %s for user %s: %v", playlistId, subsonicUser, err))
		return
	} else if listenBrainzPlaylist == nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Failed to get daily jams playlist for user %s", subsonicUser))
		return
	}

	songIds := []string{}
	missing := []string{}
	excluded := []string{}

	pdk.Log(pdk.LogDebug, fmt.Sprintf("Importing playlist `%s`", listenBrainzPlaylist.Title))

	for _, track := range listenBrainzPlaylist.Tracks {
		mbid := getIdentifier(track.Identifier[0])
		artistMbids := make([]string, len(track.Extension.Track.AdditionalMetadata.Artists))
		for idx, artist := range track.Extension.Track.AdditionalMetadata.Artists {
			artistMbids[idx] = artist.MBID
		}
		song := b.lookupTrack(subsonicUser, track.Title, mbid, artistMbids)

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

	existingPlaylist := findExistingPlaylist(resp, source.PlaylistName)
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

func (b *BrainzPlaylistPlugin) updatePlaylists(users []userConfig) {
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

		playlists, err := listenbrainz.GetCreatedForPlaylists(userData.LbzUsername, userData.LbzToken)
		if err != nil {
			pdk.Log(pdk.LogError, fmt.Sprintf("Failed to fetch playlists for user %s: %v", userData.NDUsername, err))
			continue
		}

		for _, source := range userData.Sources {
			b.importPlaylist(&source, userData.NDUsername, userData.LbzToken, playlists, ratings)
		}

		if userData.GeneratePlaylist && userData.GeneratedPlaylist != "" {
			err := b.createJams(&userData, ratings)
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

func (b *BrainzPlaylistPlugin) initialFetch(users []userConfig) error {
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
			pls := findExistingPlaylist(playlistResp, source.PlaylistName)

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
			pls := findExistingPlaylist(playlistResp, user.GeneratedPlaylist)
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
		b.updatePlaylists(users)
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

	b.fallbackCount = fallbackCount

	switch req.Payload {
	case dailyCron:
		delay := rand.Int31n(3600)
		pdk.Log(pdk.LogInfo, fmt.Sprintf("Delaying fetch by %d seconds", delay))
		host.SchedulerScheduleOneTime(delay, delayedSync, delayedSync)
	case delayedSync:
		b.updatePlaylists(userMapping)
	case initialFetchId:
		b.initialFetch(userMapping)
	}

	return nil
}

func (b *BrainzPlaylistPlugin) OnInit() error {
	schedule, ok := pdk.GetConfig("schedule")
	if !ok {
		schedule = "8"
	}

	schedInt, err := strconv.Atoi(schedule)
	if err != nil {
		return fmt.Errorf("Invalid schedule %s: %v", schedule, err)
	}

	if schedInt < 0 || schedInt > 23 {
		return fmt.Errorf("Schedule is not a valid hour (between [0, 23], inclusive): %d", schedInt)
	}

	_, _, err = getConfig()
	if err != nil {
		return err
	}

	_, err = host.SchedulerScheduleRecurring(fmt.Sprintf("0 %d * * *", schedInt), dailyCron, dailyCron)
	if err != nil {
		return fmt.Errorf("Failed to schedule playlist sync. Is your schedule a valid cron expression? %v", err)
	}

	checkOnStartup, ok := pdk.GetConfig("checkOnStartup")

	if !ok || checkOnStartup != "false" {
		_, err := host.SchedulerScheduleOneTime(1, initialFetchId, initialFetchId)
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

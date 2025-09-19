//go:build wasip1

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/navidrome/navidrome/plugins/api"
	"github.com/navidrome/navidrome/plugins/host/config"
	"github.com/navidrome/navidrome/plugins/host/http"
	"github.com/navidrome/navidrome/plugins/host/scheduler"
	"github.com/navidrome/navidrome/plugins/host/subsonicapi"
	"github.com/navidrome/navidrome/server/subsonic/responses"
)

const (
	lbzEndpoint    = "https://api.listenbrainz.org/1"
	peridiocSyncId = "listenbrainz"
)

var (
	client = http.NewHttpService()

	// SubsonicAPIService instance for making API calls
	subsonicService = subsonicapi.NewSubsonicAPIService()
)

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

func (b *BrainzPlaylistPlugin) getPlaylists(ctx context.Context, user string) ([]overallPlaylist, error) {
	req := &http.HttpRequest{
		Url: fmt.Sprintf("%s/user/%s/playlists/createdfor", lbzEndpoint, user),
		Headers: map[string]string{
			"Accept":     "application/json",
			"User-Agent": "NavidromePlaylistImporter/0.1",
		},
	}

	resp, err := client.Get(ctx, req)
	if err != nil {
		return nil, err
	}

	var result listenBrainzResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("Failed to decode JSON: %v", err)
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("ListenBrainz HTTP Error. Code: %d, Error: %s", result.Code, result.Error)
	}

	return result.Playlists, nil
}

func (b *BrainzPlaylistPlugin) getPlaylist(ctx context.Context, id string) (*lbPlaylist, error) {
	req := &http.HttpRequest{
		Url: fmt.Sprintf("%s/playlist/%s", lbzEndpoint, id),
		Headers: map[string]string{
			"Accept":     "application/json",
			"User-Agent": "NavidromePlaylistImporter/0.1",
		},
	}

	resp, err := client.Get(ctx, req)
	if err != nil {
		return nil, err
	}

	var result listenBrainzResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
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

func (b *BrainzPlaylistPlugin) makeSubsonicRequest(ctx context.Context, endpoint, subsonicUser string, params *url.Values) (*responses.JsonWrapper, bool) {
	subsonicResp, err := subsonicService.Call(ctx, &subsonicapi.CallRequest{
		Url: fmt.Sprintf("/rest/%s?u=%s&%s", endpoint, subsonicUser, params.Encode()),
	})

	if err != nil {
		log.Printf("An error occurred %s: %v", subsonicUser, err)
		return nil, false
	}

	if subsonicResp.Error != "" {
		log.Printf("A Subsonic error occurred for user %s: %s", subsonicUser, subsonicResp.Error)
		return nil, false
	}

	var decoded responses.JsonWrapper
	if err := json.Unmarshal([]byte(subsonicResp.Json), &decoded); err != nil {
		log.Printf("A deserialization error occurred %s: %s", subsonicUser, err)
		return nil, false
	}

	if decoded.Subsonic.Status != "ok" {
		log.Printf("Subsonic status is not ok: (%d)%s", decoded.Subsonic.Error.Code, decoded.Subsonic.Error.Message)
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
	ctx context.Context,
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

	resp, ok := b.makeSubsonicRequest(ctx, "search3", subsonicUser, &artistParams)
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
	ctx context.Context,
	subsonicUser string,
	track *lbTrack,
	fallbackCount string,
) *responses.Child {
	artistIds := map[string]bool{}

	for _, artist := range track.Extension.Track.AdditionalMetadata.Artists {
		id := b.findArtistIdByMbid(ctx, subsonicUser, artist.MBID)
		if id == "" {
			return nil
		}

		artistIds[id] = true
	}

	trackParams := url.Values{
		"artistCount": []string{"0"},
		"albumCount":  []string{"0"},
		"songCount":   []string{fallbackCount}, // I do not know what number is reasonable for multiple matches. Maybe I'll make it configurable
		"query":       []string{track.Title},
	}

	resp, ok := b.makeSubsonicRequest(ctx, "search3", subsonicUser, &trackParams)
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
	ctx context.Context,
	source string,
	playlistName string,
	subsonicUser string,
	playlists []overallPlaylist,
	rating map[int32]bool,
	fallbackCount string,
) {
	var id string
	var err error
	var listenBrainzPlaylist *lbPlaylist

	for _, plsMetadata := range playlists {
		if plsMetadata.Playlist.Extension.Extension.AdditionalMetadata.AlgorithmMetadata.SourcePatch == source {
			id = getIdentifier(plsMetadata.Playlist.Identifier)

			listenBrainzPlaylist, err = b.getPlaylist(ctx, id)
			break
		}
	}

	if err != nil {
		err = errors.Join(err)
		log.Printf("Failed to fetch playlist %s for user %s: %v", id, subsonicUser, err)
		return
	} else if listenBrainzPlaylist == nil {
		log.Printf("Failed to get daily jams playlist for user %s", subsonicUser)
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

		resp, ok := b.makeSubsonicRequest(ctx, "search3", subsonicUser, &trackParams)
		if !ok {
			continue
		}

		var song *responses.Child

		if len(resp.Subsonic.SearchResult3.Song) > 0 {
			song = &resp.Subsonic.SearchResult3.Song[0]
		} else {
			song = b.fallbackLookup(ctx, subsonicUser, &track, fallbackCount)
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

	resp, ok := b.makeSubsonicRequest(ctx, "getPlaylists", subsonicUser, &url.Values{})
	if !ok {
		return
	}

	existingPlaylist := b.findExistingPlaylist(resp, playlistName)
	if len(songIds) != 0 || existingPlaylist != nil {
		createPlaylistParams := url.Values{
			"songId": songIds,
		}

		if existingPlaylist != nil {
			createPlaylistParams.Add("playlistId", existingPlaylist.Id)
		} else {
			createPlaylistParams.Add("name", playlistName)
		}

		_, ok = b.makeSubsonicRequest(ctx, "createPlaylist", subsonicUser, &createPlaylistParams)
		if !ok {
			return
		}
	}

	comment := fmt.Sprintf("%s&nbsp;\n%s", listenBrainzPlaylist.Annotation, listenBrainzPlaylist.Identifier)

	if len(missing) > 0 {
		comment += fmt.Sprintf("&nbsp;\nTracks not matched by MBID: %s", strings.Join(missing, ", "))
	}

	if len(excluded) > 0 {
		comment += fmt.Sprintf("&nbsp;\nTracks excluded by rating rule: %s", strings.Join(excluded, ", "))
	}

	if existingPlaylist != nil && existingPlaylist.Comment != comment {
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
			log.Printf("No matching files found for playlist %s", existingPlaylist.Name)
		}

		_, ok = b.makeSubsonicRequest(ctx, "updatePlaylist", subsonicUser, &updatePlaylistParams)
		if !ok {
			return
		}
	}

	log.Printf("Successfully processed playlist %s for user %s", playlistName, subsonicUser)
}

func (b *BrainzPlaylistPlugin) updatePlaylists(ctx context.Context, conf map[string]string) (*api.SchedulerCallbackResponse, error) {
	b.artistMbidToId = map[string]string{}

	delimiter := conf["split"]
	if delimiter == "" {
		delimiter = ";"
	}

	subsonicUsers := strings.Split(conf["users"], delimiter)
	sources := strings.Split(conf["sources"], delimiter)

	var fallbackCount string
	if conf["fallbackcount"] != "" {
		fallbackCount = conf["fallbackcount"]
	} else {
		fallbackCount = "15"
	}

	for idx := range subsonicUsers {
		allowedRatings := conf[fmt.Sprintf("rating[%d]", idx)]
		var ratings map[int32]bool

		if allowedRatings != "" {
			ratings = map[int32]bool{}
			for item := range strings.SplitSeq(allowedRatings, ",") {
				rating, _ := strconv.Atoi(item)
				ratings[int32(rating)] = true
			}
		} else {
			ratings = map[int32]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true}
		}

		lbzUser := conf[fmt.Sprintf("users[%d]", idx)]

		playlists, err := b.getPlaylists(ctx, lbzUser)
		if err != nil {
			log.Printf("Failed to fetch playlists for user %s: %v", lbzUser, err)
			continue
		}

		for sourceIdx, source := range sources {
			plsName := conf[fmt.Sprintf("sources[%d]", sourceIdx)]
			b.importPlaylist(ctx, source, plsName, subsonicUsers[idx], playlists, ratings, fallbackCount)
		}
	}

	return &api.SchedulerCallbackResponse{}, nil
}

func (b *BrainzPlaylistPlugin) OnSchedulerCallback(ctx context.Context, req *api.SchedulerCallbackRequest) (*api.SchedulerCallbackResponse, error) {
	configService := config.NewConfigService()

	configResp, err := configService.GetPluginConfig(ctx, &config.GetPluginConfigRequest{})
	if err != nil {
		log.Printf("Failed to get plugin configuration: %v", err)
		return &api.SchedulerCallbackResponse{Error: fmt.Sprintf("Config error: %v", err)}, nil
	}

	return b.updatePlaylists(ctx, configResp.Config)
}

func (b *BrainzPlaylistPlugin) initialFetch(
	ctx context.Context,
	sched scheduler.SchedulerService,
	conf map[string]string,
	users []string,
	sources []string,
) error {
	now, err := sched.TimeNow(ctx, &scheduler.TimeNowRequest{})
	if err != nil {
		return err
	}

	nowTs, err := time.Parse(time.RFC3339Nano, now.Rfc3339Nano)
	if err != nil {
		return err
	}

	missing := false
	olderThanThreeHours := false

userLoop:
	for _, user := range users {
		playlistResp, ok := b.makeSubsonicRequest(ctx, "getPlaylists", user, &url.Values{})
		if !ok {
			return errors.New("Failed to fetch playlists on initial fetch")
		}

		for _, source := range sources {
			pls := b.findExistingPlaylist(playlistResp, source)

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
		log.Println("Missing or outdated playlists, fetching on initial sync")

		playlistResp, err := b.updatePlaylists(ctx, conf)
		if err != nil {
			return err
		}

		if playlistResp.Error != "" {
			return errors.New(playlistResp.Error)
		}
	} else {
		log.Println("No missing/outdated playlists, not fetching on startup")
	}

	return nil
}

func (b *BrainzPlaylistPlugin) OnInit(ctx context.Context, req *api.InitRequest) (*api.InitResponse, error) {
	conf := req.Config

	schedule, ok := conf["schedule"]
	if !ok {
		schedule = "@every 24h"
	}

	delimiter := conf["split"]
	if delimiter == "" {
		delimiter = ";"
	}

	usersString := conf["users"]
	if usersString == "" {
		log.Printf("Missing required 'users' configuration")
		return &api.InitResponse{Error: "Missing required 'users' configuration"}, nil
	}

	usersSplit := strings.Split(usersString, delimiter)
	userOk := true

	for idx := range usersSplit {
		lbzUser := conf[fmt.Sprintf("users[%d]", idx)]

		if lbzUser == "" {
			userOk = false
			log.Printf("User %s is missing a ListenBrainz username", usersSplit[idx])
		}
	}

	if !userOk {
		return &api.InitResponse{Error: "One or more users is missing a corresponding ListenBrainz username"}, nil
	}

	sourcesString := conf["sources"]
	if sourcesString == "" {
		log.Printf("Missing required 'sources' configuration")
		return &api.InitResponse{Error: "Missing required 'sources' configuration"}, nil
	}

	sourcesSplit := strings.Split(sourcesString, delimiter)
	sourcesOk := true
	sourceNames := make([]string, len(sourcesSplit))

	ratingOk := true

	for idx := range sourcesSplit {
		mappedName := conf[fmt.Sprintf("sources[%d]", idx)]

		if mappedName == "" {
			sourcesOk = false
			log.Printf("Source %s is missing a playlist name", sourcesSplit[idx])
		} else {
			sourceNames[idx] = mappedName
		}

		rating := conf[fmt.Sprintf("rating[%d]", idx)]
		if rating != "" {
			for item := range strings.SplitSeq(rating, ",") {
				_, err := strconv.Atoi(item)
				if err != nil {
					ratingOk = false
				}
			}
		}
	}

	if !sourcesOk {
		return &api.InitResponse{Error: "One or more sources is missing a corresponding playlist name"}, nil
	}

	if !ratingOk {
		return &api.InitResponse{Error: "One or more users has misconfigured `rating`. This must be a comma-separated list of ratings"}, nil
	}

	if conf["fallbackcount"] != "" {
		value, err := strconv.Atoi(conf["fallbackcount"])
		if err != nil {
			return &api.InitResponse{Error: "`FallbackCount` is not a valid number"}, nil
		}

		if value < 1 || value > 500 {
			return &api.InitResponse{Error: "`FallbackCount` must be between 1 and 500 (inclusive)"}, nil
		}
	}

	// SchedulerService instance for scheduling tasks.
	schedService := scheduler.NewSchedulerService()

	_, err := schedService.ScheduleRecurring(ctx, &scheduler.ScheduleRecurringRequest{
		CronExpression: schedule,
		ScheduleId:     peridiocSyncId,
	})

	if err != nil {
		msg := fmt.Sprintf("Failed to schedule playlist sync. Is your schedule a valid cron expression? %v", err)
		log.Println(msg)
		return &api.InitResponse{Error: msg}, nil
	}

	if conf["checkonstartup"] != "false" {
		err := b.initialFetch(ctx, schedService, conf, usersSplit, sourceNames)
		if err != nil {
			log.Println("Failed to do initial sync. Proceeding anyway %w", err)
		}
	}

	log.Println("init success")

	return &api.InitResponse{}, nil
}

func main() {}

func init() {
	// Configure logging: No timestamps, no source file/line, prepend
	log.SetFlags(0)
	log.SetPrefix("[LBZ Playlist Fetcher]")

	api.RegisterLifecycleManagement(&BrainzPlaylistPlugin{})
	api.RegisterSchedulerCallback(&BrainzPlaylistPlugin{})
}

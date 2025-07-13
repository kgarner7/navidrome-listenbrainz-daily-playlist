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

	// ConfigService instance for accessing plugin configuration.
	configService = config.NewConfigService()

	// SchedulerService instance for scheduling tasks.
	schedService = scheduler.NewSchedulerService()
)

type BrainzPlaylistPlugin struct{}

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
	Collaborators      []string       `json:"collaborators"`
	CreatedFor         string         `json:"created_for"`
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
	Creator    string   `json:"creator"`
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

func (b *BrainzPlaylistPlugin) makeSubsonicRequest(ctx context.Context, endpoint, subsonicUser string, params url.Values) (*responses.JsonWrapper, bool) {
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

	return &decoded, true
}

func (b *BrainzPlaylistPlugin) importPlaylist(ctx context.Context, source, playlistName, subsonicUser string, playlists []overallPlaylist) {
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

	params := url.Values{
		"artistCount": []string{"0"},
		"albumCount":  []string{"0"},
		"songCount":   []string{"1"},
	}

	for _, track := range listenBrainzPlaylist.Tracks {
		mbid := getIdentifier(track.Identifier[0])
		params.Set("query", mbid)

		resp, ok := b.makeSubsonicRequest(ctx, "search3", subsonicUser, params)
		if !ok {
			continue
		}

		if len(resp.Subsonic.SearchResult3.Song) > 0 {
			songIds = append(songIds, resp.Subsonic.SearchResult3.Song[0].Id)
		}
	}

	resp, ok := b.makeSubsonicRequest(ctx, "getPlaylists", subsonicUser, url.Values{})
	if !ok {
		return
	}

	var existingPlaylist *responses.Playlist = nil

	if len(resp.Subsonic.Playlists.Playlist) > 0 {
		for _, playlist := range resp.Subsonic.Playlists.Playlist {
			if playlist.Name == playlistName {
				existingPlaylist = &playlist
				break
			}
		}
	}

	createPlaylistParams := url.Values{
		"songId": songIds,
	}

	if existingPlaylist != nil {
		createPlaylistParams.Add("playlistId", existingPlaylist.Id)
	} else {
		createPlaylistParams.Add("name", playlistName)
	}

	_, ok = b.makeSubsonicRequest(ctx, "createPlaylist", subsonicUser, createPlaylistParams)
	if !ok {
		return
	}

	comment := fmt.Sprintf("%s&nbsp;%s", listenBrainzPlaylist.Annotation, listenBrainzPlaylist.Identifier)

	if existingPlaylist != nil && existingPlaylist.Comment != comment {
		policy := bluemonday.StrictPolicy()
		sanitized := html.UnescapeString(policy.Sanitize(comment))
		updatePlaylistParams := url.Values{
			"playlistId": []string{existingPlaylist.Id},
			"comment":    []string{sanitized},
		}

		_, ok = b.makeSubsonicRequest(ctx, "updatePlaylist", subsonicUser, updatePlaylistParams)
		if !ok {
			return
		}
	}

	log.Printf("Successfully processed playlist for user %s", subsonicUser)
}

func (b *BrainzPlaylistPlugin) OnSchedulerCallback(ctx context.Context, req *api.SchedulerCallbackRequest) (*api.SchedulerCallbackResponse, error) {
	configResp, err := configService.GetPluginConfig(ctx, &config.GetPluginConfigRequest{})
	if err != nil {
		log.Printf("Failed to get plugin configuration: %v", err)
		return &api.SchedulerCallbackResponse{Error: fmt.Sprintf("Config error: %v", err)}, nil
	}

	conf := configResp.Config

	delimiter := conf["split"]
	if delimiter == "" {
		delimiter = ";"
	}

	playlistName := conf["playlistname"]
	if playlistName == "" {
		playlistName = "ListenBrainz Daily Jams"
	}

	subsonicUsers := strings.Split(conf["users"], delimiter)
	sources := strings.Split(conf["sources"], delimiter)

	for idx := range subsonicUsers {
		lbzUser := conf[fmt.Sprintf("users[%d]", idx)]

		playlist, err := b.getPlaylists(ctx, lbzUser)
		if err != nil {
			err = errors.Join(err)
			log.Printf("Failed to fetch playlists for user %s: %v", lbzUser, err)
			continue
		}

		for sourceIdx, source := range sources {
			plsName := conf[fmt.Sprintf("sources[%d]", sourceIdx)]
			b.importPlaylist(ctx, source, plsName, subsonicUsers[idx], playlist)

		}
	}

	return &api.SchedulerCallbackResponse{}, nil
}

func (b *BrainzPlaylistPlugin) OnInit(ctx context.Context, req *api.InitRequest) (*api.InitResponse, error) {
	configResp, err := configService.GetPluginConfig(ctx, &config.GetPluginConfigRequest{})
	if err != nil {
		log.Printf("Failed to get plugin configuration: %v", err)
		return &api.InitResponse{Error: fmt.Sprintf("Config error: %v", err)}, nil
	}

	conf := configResp.Config

	schedule, ok := conf["schedule"]
	if !ok {
		log.Printf("Missing required 'schedule' configuration")
		return &api.InitResponse{Error: "Missing required 'schedule' configuration"}, nil
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

	split := strings.Split(usersString, delimiter)
	userOk := true

	for idx := range split {
		lbzUser := conf[fmt.Sprintf("users[%d]", idx)]

		if lbzUser == "" {
			userOk = false
			log.Printf("User %s is missing a ListenBrainz username", split[idx])
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

	split = strings.Split(sourcesString, delimiter)
	sourcesOk := true

	for idx := range split {
		mappedName := conf[fmt.Sprintf("sources[%d]", idx)]

		if mappedName == "" {
			sourcesOk = false
			log.Printf("Source %s is missing a playlist name", split[idx])
		}
	}

	if !sourcesOk {
		return &api.InitResponse{Error: "One or more sources is missing a corresponding playlist name"}, nil
	}

	_, err = schedService.ScheduleRecurring(ctx, &scheduler.ScheduleRecurringRequest{
		CronExpression: schedule,
		ScheduleId:     peridiocSyncId,
	})

	if err != nil {
		msg := fmt.Sprintf("Failed to schedule playlist sync. Is your schedule a valid cron expression? %v", err)
		log.Println(msg)
		return &api.InitResponse{Error: msg}, nil
	}

	return &api.InitResponse{}, nil
}

func main() {}

func init() {
	// Configure logging: No timestamps, no source file/line, prepend [Discord]
	log.SetFlags(0)
	log.SetPrefix("[ListenBrainz Daily Playlist Fetcher]")

	api.RegisterLifecycleManagement(&BrainzPlaylistPlugin{})
	api.RegisterSchedulerCallback(&BrainzPlaylistPlugin{})
}

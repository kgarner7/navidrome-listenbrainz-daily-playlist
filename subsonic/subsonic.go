package subsonic

import (
	"encoding/json"
	"fmt"
	"listenbrainz-daily-playlist/retry"
	"net/url"
	"strconv"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/server/subsonic/responses"
)

type SubsonicHandler struct {
	artistMbidToId map[string]string
	fallbackCount  int
}

func NewSubsonicHandler(fallbackCount int) *SubsonicHandler {
	return &SubsonicHandler{
		artistMbidToId: make(map[string]string),
		fallbackCount:  fallbackCount,
	}
}

func Call(endpoint, subsonicUser string, params *url.Values) (*responses.JsonWrapper, *retry.Error) {
	url := fmt.Sprintf("/rest/%s?u=%s", endpoint, subsonicUser)
	if params != nil {
		url += "&" + params.Encode()
	}

	subsonicResp, err := host.SubsonicAPICall(url)

	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("An unrecoverable Subsonic error occurred %s: %v", subsonicUser, err))
		return nil, &retry.Error{
			Error:     err,
			Retryable: false,
		}
	}

	var decoded responses.JsonWrapper
	if err := json.Unmarshal([]byte(subsonicResp), &decoded); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("A deserialization error occurred %s: %s", subsonicUser, err))
		return nil, &retry.Error{
			Error:     err,
			Retryable: false,
		}
	}

	if decoded.Subsonic.Status != "ok" {
		err := fmt.Errorf("Subsonic status is not ok: (%d) %s", decoded.Subsonic.Error.Code, decoded.Subsonic.Error.Message)

		pdk.Log(pdk.LogError, err.Error())
		return nil, &retry.Error{
			Error: err,
			// 0 is an internal server error. This is the only error that we will consider as recoverable

			Retryable: decoded.Subsonic.Error.Code == 0,
		}
	}

	return &decoded, nil
}

// LookupTrack will ignore errors that occur
func (s *SubsonicHandler) LookupTrack(
	subsonicUser, title, mbid string,
	artistMbids []string,
) *responses.Child {

	trackParams := url.Values{
		"artistCount": []string{"0"},
		"albumCount":  []string{"0"},
		"songCount":   []string{"1"},
		"query":       []string{mbid},
	}

	resp, err := Call("search3", subsonicUser, &trackParams)
	if err != nil {
		return nil
	}

	var song *responses.Child

	if len(resp.Subsonic.SearchResult3.Song) > 0 {
		song = &resp.Subsonic.SearchResult3.Song[0]
	} else {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("Could not find track by MBID: %s", mbid))
		artistIds := map[string]bool{}

		for _, artist := range artistMbids {
			id := s.findArtistIdByMbid(subsonicUser, artist)
			if id == "" {
				return nil
			}

			artistIds[id] = true
		}

		trackParams = url.Values{
			"artistCount": []string{"0"},
			"albumCount":  []string{"0"},
			"songCount":   []string{strconv.Itoa(s.fallbackCount)},
			"query":       []string{title},
		}

		resp, err := Call("search3", subsonicUser, &trackParams)
		if err != nil {
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

func (s *SubsonicHandler) findArtistIdByMbid(
	subsonicUser string,
	mbid string,
) string {
	existing, ok := s.artistMbidToId[mbid]
	if ok {
		return existing
	}

	artistParams := url.Values{
		"artistCount": []string{"1"},
		"albumCount":  []string{"0"},
		"songCount":   []string{"0"},
		"query":       []string{mbid},
	}

	resp, err := Call("search3", subsonicUser, &artistParams)
	if err != nil {
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

	s.artistMbidToId[mbid] = id
	return id
}

func FindExistingPlaylist(resp *responses.JsonWrapper, playlistName string) *responses.Playlist {
	if len(resp.Subsonic.Playlists.Playlist) > 0 {
		for _, playlist := range resp.Subsonic.Playlists.Playlist {
			if playlist.Name == playlistName {
				return &playlist
			}
		}
	}

	return nil
}

func UpdatePlaylist(subsonicUser, playlistName, comment string, songIds []string) *retry.Error {
	subsonicResp, err := Call("getPlaylists", subsonicUser, &url.Values{"username": []string{subsonicUser}})
	if err != nil {
		return err
	}

	existingPlaylist := FindExistingPlaylist(subsonicResp, playlistName)
	createPlaylistParams := url.Values{"songId": songIds}

	if existingPlaylist != nil {
		createPlaylistParams.Add("playlistId", existingPlaylist.Id)
	} else {
		createPlaylistParams.Add("name", playlistName)
	}

	subsonicResp, err = Call("createPlaylist", subsonicUser, &createPlaylistParams)
	if err != nil {
		return err
	}

	if subsonicResp.Subsonic.Playlist != nil && subsonicResp.Subsonic.Playlist.Comment != comment {
		updatePlaylistParams := url.Values{
			"playlistId": []string{subsonicResp.Subsonic.Playlist.Id},
			"comment":    []string{comment},
		}

		_, err = Call("updatePlaylist", subsonicUser, &updatePlaylistParams)
		if err != nil {
			return err
		}
	}

	return nil
}

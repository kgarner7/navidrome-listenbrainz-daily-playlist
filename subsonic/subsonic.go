package subsonic

import (
	"encoding/json"
	"fmt"
	"listenbrainz-daily-playlist/retry"
	"net/url"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
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

func Call(endpoint, subsonicUser string, params *url.Values) (*JsonWrapper, *retry.Error) {
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

	var decoded JsonWrapper
	if err := json.Unmarshal([]byte(subsonicResp), &decoded); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("A deserialization error occurred %s: %s", subsonicUser, err))
		return nil, &retry.Error{
			Error:     err,
			Retryable: false,
		}
	}

	if decoded.Subsonic.Status != "ok" {
		err := fmt.Errorf("subsonic status is not ok: (%d) %s", decoded.Subsonic.Error.Code, decoded.Subsonic.Error.Message)

		pdk.Log(pdk.LogError, err.Error())
		return nil, &retry.Error{
			Error: err,
			// 0 is an internal server error. This is the only error that we will consider as recoverable
			Retryable: decoded.Subsonic.Error.Code == 0,
		}
	}

	return &decoded, nil
}

func FindExistingPlaylist(resp *JsonWrapper, playlistName string) *Playlist {
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

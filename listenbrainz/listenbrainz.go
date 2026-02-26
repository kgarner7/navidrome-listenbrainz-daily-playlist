package listenbrainz

import (
	"encoding/json"
	"fmt"
	"listenbrainz-daily-playlist/sleep"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

const (
	lbzEndpoint = "https://api.listenbrainz.org/1"
	userAgent   = "NavidromePlaylistImporter/4.0.3"
)

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
			sleep.Sleep(time.Duration(resetInt) * time.Second)
		}
	}
}

func makeLbzGet(endpoint, token string) pdk.HTTPResponse {
	req := pdk.NewHTTPRequest(pdk.MethodGet, endpoint)
	req.SetHeader("Accept", "application/json")
	req.SetHeader("User-Agent", userAgent)
	if token != "" {
		req.SetHeader("Authorization", "Token "+token)
	}

	resp := req.Send()

	processRatelimit(&resp)

	return resp
}

func GetPlaylist(id, lbzToken string) (*LbzPlaylist, error) {
	resp := makeLbzGet(fmt.Sprintf("%s/playlist/%s", lbzEndpoint, id), lbzToken)

	var result lbzPlaylistResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("ListenBrainz HTTP Error. Code: %d, Error: %s", result.Code, result.Error)
	}

	if result.Playlist == nil {
		return nil, fmt.Errorf("Nothing parsed for playlist %s", id)
	}

	return result.Playlist, nil
}

func GetCreatedForPlaylists(lbzUsername, lbzToken string) ([]*LbzPlaylist, error) {
	resp := makeLbzGet(fmt.Sprintf("%s/user/%s/playlists/createdfor", lbzEndpoint, lbzUsername), lbzToken)

	var result lbzPlaylistResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("ListenBrainz HTTP Error. Code: %d, Error: %s", result.Code, result.Error)
	}

	playlists := make([]*LbzPlaylist, len(result.Playlists))
	for idx, playlist := range result.Playlists {
		playlists[idx] = &playlist.Playlist
	}

	return playlists, nil
}

func GetRecommendations(lbzUsername, lbzToken string) (*LbzRecommendations, error) {
	resp := makeLbzGet(fmt.Sprintf("%s/cf/recommendation/user/%s/recording?count=1000", lbzEndpoint, lbzUsername), lbzToken)

	recommendations := LbzRecommendations{}
	err := json.Unmarshal(resp.Body(), &recommendations)
	if err != nil {
		return nil, err
	}

	if recommendations.Code != 0 {
		return nil, fmt.Errorf("ListenBrainz HTTP Error. Code: %d, Error: %s", recommendations.Code, recommendations.Error)
	}

	if len(recommendations.Payload.MBIDs) == 0 {
		return nil, fmt.Errorf("No recommendations found for user %s", lbzUsername)
	}

	return &recommendations, nil
}

func LookupRecordings(mbids []string, lbzToken string) (map[string]lbzMetadataLookup, error) {
	req := pdk.NewHTTPRequest(pdk.MethodPost, lbzEndpoint+"/metadata/recording")
	req.SetHeader("Accept", "application/json")
	req.SetHeader("Content-Type", "application/json")
	req.SetHeader("User-Agent", userAgent)

	if lbzToken != "" {
		req.SetHeader("Authorization", "Token "+lbzToken)
	}

	payload := recLookup{RecordingMbids: mbids, Inc: "artist"}
	payloadBytes, _ := json.Marshal(payload)
	req.SetBody(payloadBytes)

	resp := req.Send()
	processRatelimit(&resp)

	var metadata map[string]lbzMetadataLookup

	body := resp.Body()

	err := json.Unmarshal(body, &metadata)
	if err != nil {
		var lbzError LbzError
		err := json.Unmarshal(body, &lbzError)
		if err != nil {
			return nil, err
		}

		return nil, fmt.Errorf("ListenBrainz HTTP Error. Code: %d, Error: %s", lbzError.Code, lbzError.Error)
	}

	return metadata, nil
}

func GetIdentifier(url string) string {
	split := strings.Split(url, "/")
	return split[len(split)-1]
}

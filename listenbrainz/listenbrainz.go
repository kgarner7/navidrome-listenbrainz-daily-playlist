package listenbrainz

import (
	"encoding/json"
	"errors"
	"fmt"
	"listenbrainz-daily-playlist/retry"
	"listenbrainz-daily-playlist/sleep"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

const (
	lbzEndpoint = "https://api.listenbrainz.org/1"
	userAgent   = "NavidromePlaylistImporter/4.0.2"
)

func processRatelimit(resp *host.HTTPResponse) {
	remaining, remOk := resp.Headers["x-ratelimit-remaining"]
	resetIn, resetOk := resp.Headers["x-ratelimit-reset-in"]

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

func processHttpResponse(resp *host.HTTPResponse, err error) *retry.Error {
	if err != nil {
		retryable := strings.HasSuffix(err.Error(), ": connection reset by peer")

		return &retry.Error{
			Error:     err,
			Retryable: retryable,
		}
	}

	processRatelimit(resp)

	if resp.StatusCode != 200 && resp.StatusCode != 429 {
		var error lbzError
		if err := json.Unmarshal(resp.Body, &error); err != nil {
			return &retry.Error{
				Error:     err,
				Retryable: false,
			}
		}

		return &retry.Error{
			Error:     fmt.Errorf("ListenBrainz HTTP Error. Code: %d, Error: %s", error.Code, error.Error),
			Retryable: false,
		}
	}

	if resp.StatusCode == 429 {
		return &retry.Error{
			Error:     errors.New("ListenBrainz rate limit hit"),
			Retryable: true,
		}
	}

	return nil
}

func makeLbzGet(endpoint, token string) (*host.HTTPResponse, *retry.Error) {
	headers := map[string]string{
		"Accept":     "application/json",
		"User-Agent": userAgent,
	}

	if token != "" {
		headers["Authorization"] = "Token " + token
	}

	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:    "GET",
		URL:       endpoint,
		Headers:   headers,
		TimeoutMs: 10000,
	})

	retry := processHttpResponse(resp, err)
	if retry != nil {
		return nil, retry
	}

	return resp, nil
}

func GetPlaylist(id, lbzToken string) (*LbzPlaylist, *retry.Error) {
	resp, err := makeLbzGet(fmt.Sprintf("%s/playlist/%s", lbzEndpoint, id), lbzToken)
	if err != nil {
		return nil, err
	}

	var result lbzPlaylistResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, &retry.Error{Error: err, Retryable: false}
	}

	if result.Playlist == nil {
		return nil, &retry.Error{Error: fmt.Errorf("Nothing parsed for playlist %s", id), Retryable: false}
	}

	return result.Playlist, nil
}

func GetCreatedForPlaylists(lbzUsername, lbzToken string) ([]*LbzPlaylist, *retry.Error) {
	resp, err := makeLbzGet(fmt.Sprintf("%s/user/%s/playlists/createdfor", lbzEndpoint, lbzUsername), lbzToken)
	if err != nil {
		return nil, err
	}

	var result lbzPlaylistResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, &retry.Error{Error: err, Retryable: false}
	}

	playlists := make([]*LbzPlaylist, len(result.Playlists))
	for idx, playlist := range result.Playlists {
		playlists[idx] = &playlist.Playlist
	}

	return playlists, nil
}

func GetRecommendations(lbzUsername, lbzToken string) (*LbzRecommendations, *retry.Error) {
	resp, err := makeLbzGet(fmt.Sprintf("%s/cf/recommendation/user/%s/recording?count=1000", lbzEndpoint, lbzUsername), lbzToken)
	if err != nil {
		return nil, err
	}
	recommendations := LbzRecommendations{}
	jsonErr := json.Unmarshal(resp.Body, &recommendations)
	if jsonErr != nil {
		return nil, &retry.Error{Error: jsonErr, Retryable: false}
	}

	if len(recommendations.Payload.MBIDs) == 0 {
		return nil, &retry.Error{Error: fmt.Errorf("No recommendations found for user %s", lbzUsername), Retryable: false}
	}

	return &recommendations, nil
}

func LookupRecordings(mbids []string, token string) (map[string]lbzMetadataLookup, *retry.Error) {
	headers := map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/json",
		"User-Agent":   userAgent,
	}

	if token != "" {
		headers["Authorization"] = "Token " + token
	}

	payload := recLookup{RecordingMbids: mbids, Inc: "artist"}
	payloadBytes, _ := json.Marshal(payload)

	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:    "POST",
		URL:       lbzEndpoint + "/metadata/recording",
		Headers:   headers,
		TimeoutMs: 10000,
		Body:      payloadBytes,
	})

	retryErr := processHttpResponse(resp, err)
	if retryErr != nil {
		return nil, retryErr
	}

	var metadata map[string]lbzMetadataLookup
	err = json.Unmarshal(resp.Body, &metadata)
	if err != nil {
		return nil, &retry.Error{Error: err, Retryable: false}
	}

	return metadata, nil
}

func GetIdentifier(url string) string {
	split := strings.Split(url, "/")
	return split[len(split)-1]
}

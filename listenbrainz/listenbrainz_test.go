//go:build !wasip1

package listenbrainz

import (
	"encoding/json"
	"errors"
	"fmt"
	"listenbrainz-daily-playlist/retry"
	"listenbrainz-daily-playlist/sleep"
	"os"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("ListenBrainz endpoints", func() {
	const (
		CONNECTION_RESET = "read tcp 8.8.8.8:60000->142.132.240.1:443: read: connection reset by peer"
		EMPTY_UUID       = "00000000-0000-0000-0000-000000000000"
	)

	var sleepDuration *time.Duration

	mockSleep := func(d time.Duration) {
		sleepDuration = &d
	}

	makeRequest := func(url, token string, body []byte) host.HTTPRequest {
		headers := map[string]string{
			"Accept":     "application/json",
			"User-Agent": userAgent,
		}

		if token != "" {
			headers["Authorization"] = "Token " + token
		}

		request := host.HTTPRequest{
			URL:       url,
			Headers:   headers,
			TimeoutMs: 10000,
		}

		if body == nil {
			request.Method = "GET"
		} else {
			request.Method = "POST"
			request.Body = body
			request.Headers["Content-Type"] = "application/json"
		}

		return request
	}

	createResponse := func(code int, path string, err error, rateLimited bool) (*host.HTTPResponse, error) {
		if err != nil {
			return nil, err
		}
		f, err := os.ReadFile("testdata/" + path + ".json")
		Expect(err).To(BeNil())

		resp := host.HTTPResponse{StatusCode: int32(code), Body: f}
		if rateLimited {
			resp.Headers = map[string]string{"x-ratelimit-remaining": "1", "x-ratelimit-reset-in": "5"}
		} else {
			resp.Headers = map[string]string{"x-ratelimit-remaining": "29", "x-ratelimit-reset-in": "10"}
		}

		return &resp, nil
	}

	BeforeEach(func() {
		oldSleep := sleep.Sleep
		sleep.Sleep = mockSleep

		sleepDuration = nil
		pdk.ResetMock()
		pdk.PDKMock.Calls = nil
		pdk.PDKMock.ExpectedCalls = nil
		host.HTTPMock.Calls = nil
		host.HTTPMock.ExpectedCalls = nil
		pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()

		DeferCleanup(func() {
			sleep.Sleep = oldSleep
		})
	})

	setupResponse := func(request host.HTTPRequest, code int, dataPath string, err error, rateLimited bool) {
		host.HTTPMock.On("Send", request).Return(createResponse(code, dataPath, err, rateLimited))
	}

	makeErr := func(message string, retryable bool) *retry.Error {
		return &retry.Error{Error: errors.New(message), Retryable: retryable}
	}

	validateResponse := func(expected, output any, expectedErr *retry.Error, actualErr *retry.Error, rateLimited bool) {
		Expect(expected).To(BeComparableTo(output))
		if expectedErr == nil {
			Expect(actualErr).To(BeNil())
		} else {
			Expect(actualErr.Error.Error()).To(Equal(expectedErr.Error.Error()))
		}
		if rateLimited {
			duration := 5 * time.Second
			Expect(sleepDuration).To(BeComparableTo(&duration))
		} else {
			Expect(sleepDuration).To(BeNil())
		}

		expectedCalls := []mock.Arguments{}
		for _, call := range pdk.PDKMock.ExpectedCalls {
			if call.Method != "Log" {
				expectedCalls = append(expectedCalls, call.Arguments)
			}
		}

		actualCalls := []mock.Arguments{}
		for _, call := range pdk.PDKMock.Calls {
			if call.Method != "Log" {
				actualCalls = append(actualCalls, call.Arguments)
			}
		}

		Expect(expectedCalls).To(Equal(actualCalls))
	}

	Describe("GetPlaylist", func() {
		createTrack := func(title string, artist string, mbid string, artistMbids []string) lbTrack {
			artists := make([]Artist, len(artistMbids))
			for idx, artist := range artistMbids {
				artists[idx].MBID = artist
			}

			return lbTrack{
				Creator: artist,
				Extension: trackExtension{
					Track: trackExtensionTrack{
						AdditionalMetadata: trackAdditionalMetadata{
							Artists: artists,
						},
					},
				},
				Identifier: []string{"https://musicbrainz.org/recording/" + mbid},
				Title:      title,
			}
		}

		DescribeTable("requests",
			func(
				id string, token string,
				code int, dataPath string, err error, rateLimited bool,
				expectedPlaylist *LbzPlaylist, expectedErr *retry.Error,
			) {
				url := lbzEndpoint + "/playlist/" + id
				request := makeRequest(url, token, nil)
				setupResponse(request, code, dataPath, err, rateLimited)
				actualPlaylist, actualErr := GetPlaylist(id, token)
				validateResponse(expectedPlaylist, actualPlaylist, expectedErr, actualErr, rateLimited)
			},
			Entry(
				"Handle bad playlist id", "1234", "",
				400, "getPlaylist.error", nil, false,
				nil, makeErr("ListenBrainz HTTP Error. Code: 400, Error: Provided playlist ID is invalid.", false),
			),
			Entry(
				"Handle bad token error", "1234", "1234",
				401, "invalidToken", nil, false,
				nil, makeErr("ListenBrainz HTTP Error. Code: 401, Error: Invalid authorization token.", false),
			),
			Entry(
				"Handle malformed json", "1234", "",
				200, "malformed", nil, false,
				nil, makeErr("unexpected end of JSON input", false),
			),
			Entry(
				"Retries on connection reset error", "1234", "",
				0, "", errors.New(CONNECTION_RESET), false,
				nil, makeErr(CONNECTION_RESET, true),
			),
			Entry(
				"Does not retry on some other arbitrary http error", "1234", "",
				0, "", errors.New("fake error"), false,
				nil, makeErr("fake error", false),
			),
			Entry(
				"Error and rate limiter", "1234",
				"", 400, "getPlaylist.error", nil, true,
				nil, makeErr("ListenBrainz HTTP Error. Code: 400, Error: Provided playlist ID is invalid.", false),
			),
			Entry(
				"Handle a real (private) playlist",
				EMPTY_UUID, EMPTY_UUID,
				200, "getPlaylist.success", nil, true,
				&LbzPlaylist{
					Creator:    "test",
					Identifier: "https://listenbrainz.org/playlist/00000000-0000-0000-0000-000000000000",
					Tracks: []lbTrack{
						createTrack("world.execute(me);", "Mili", "9980309d-3480-4e7e-89ce-fce971a452be", []string{"d2a92ee2-27ce-4e71-bfc5-12e34fe8ef56"}),
					},
					Title: "test",
				}, nil,
			),
		)
	})

	Describe("GetCreatedForPlaylists", func() {
		DescribeTable("requests",
			func(
				user, token string,
				code int, dataPath string, err error, rateLimited bool,
				expectedPlaylists []*LbzPlaylist, expectedErr *retry.Error,
			) {

				url := fmt.Sprintf("%s/user/%s/playlists/createdfor", lbzEndpoint, user)
				request := makeRequest(url, token, nil)
				setupResponse(request, code, dataPath, err, rateLimited)
				actualPlaylists, actualErr := GetCreatedForPlaylists(user, token)
				validateResponse(expectedPlaylists, actualPlaylists, expectedErr, actualErr, rateLimited)
			},
			Entry(
				"Request with error", "a", "",
				404, "createdFor.noUser", nil, false,
				nil, makeErr("ListenBrainz HTTP Error. Code: 404, Error: Cannot find user: a", false),
			),
			Entry(
				"Handle malformed json with rate limit", "a", "",
				200, "malformed", nil, false,
				nil, makeErr("unexpected end of JSON input", false),
			),
			Entry(
				"Retries on connection reset error", "1234", "",
				0, "", errors.New(CONNECTION_RESET), false,
				nil, makeErr(CONNECTION_RESET, true),
			),
			Entry(
				"Does not retry on some other arbitrary http error", "1234", "",
				0, "", errors.New("fake error"), false,
				nil, makeErr("fake error", false),
			),
			Entry(
				"Successfully fetch playlists", "test", EMPTY_UUID,
				200, "createdFor.success", nil, true,
				[]*LbzPlaylist{
					{
						Creator: "listenbrainz",
						Date:    time.Date(2026, 02, 23, 12, 0, 0, 0, time.UTC),
						Extension: plsExtension{
							Extension: playlistExtension{
								AdditionalMetadata: additionalMeta{
									AlgorithmMetadata: algoMeta{
										SourcePatch: "weekly-exploration",
									},
								},
							},
						},
						Identifier: "https://listenbrainz.org/playlist/00000000-0000-0000-0000-000000000000",
						Title:      "Weekly Exploration for test, week of 2026-02-23 Mon",
						Tracks:     []lbTrack{},
					},
				}, nil,
			),
		)
	})

	Describe("GetRecommendations", func() {
		DescribeTable("requests",
			func(
				user, token string,
				code int, dataPath string, err error, rateLimited bool,
				expectedRecommendations *LbzRecommendations, expectedErr *retry.Error,
			) {
				url := fmt.Sprintf("%s/cf/recommendation/user/%s/recording?count=1000", lbzEndpoint, user)
				request := makeRequest(url, token, nil)
				setupResponse(request, code, dataPath, err, rateLimited)
				actualRecommendations, actualErr := GetRecommendations(user, token)
				validateResponse(expectedRecommendations, actualRecommendations, expectedErr, actualErr, rateLimited)
			},
			Entry(
				"Request with error", "a", "",
				404, "getRecommendations.error", nil, false,
				nil, makeErr("ListenBrainz HTTP Error. Code: 404, Error: The requested URL was not found on the server. If you entered the URL manually please check your spelling and try again.", false),
			),
			Entry(
				"Handle malformed json with rateLimit", "a", "",
				200, "malformed", nil, true,
				nil, makeErr("unexpected end of JSON input", false),
			),
			Entry(
				"Retries on connection reset error", "1234", "",
				0, "", errors.New(CONNECTION_RESET), false,
				nil, makeErr(CONNECTION_RESET, true),
			),
			Entry(
				"Does not retry on some other arbitrary http error", "1234", "",
				0, "", errors.New("fake error"), false,
				nil, makeErr("fake error", false),
			),
			Entry(
				"Handle valid response", "test", EMPTY_UUID,
				200, "getRecommendations.success", nil, true,
				&LbzRecommendations{
					Payload: RecommendationPayload{Count: 1, LastUpdated: 1771845555, MBIDs: []RecordingMBID{{RecordingMBID: "00000000-0000-0000-0000-000000000000"}}},
				}, nil,
			),
			Entry(
				"Handle empty", "test", EMPTY_UUID,
				200, "getRecommendations.emptyCount", nil, false,
				nil, makeErr("No recommendations found for user test", false),
			),
		)

		It("handles a bad lookup error", func() {
			url := lbzEndpoint + "/cf/recommendation/user/a/recording?count=1000"
			request := makeRequest(url, "", nil)

			f, err := os.ReadFile("testdata/badMetadataLookup.html")
			Expect(err).To(BeNil())

			resp := host.HTTPResponse{StatusCode: int32(415), Body: f, Headers: map[string]string{"x-ratelimit-remaining": "1", "x-ratelimit-reset-in": "5"}}

			host.HTTPMock.On("Send", request).Return(&resp, nil)
			actualRecommendations, actualErr := GetRecommendations("a", "")
			Expect(actualRecommendations).To(BeNil())
			Expect(actualErr).ToNot(BeNil())
			Expect(actualErr.Error).To(MatchError("invalid character '<' looking for beginning of value"))
			Expect(actualErr.Retryable).To(BeFalse())
		})
	})

	Describe("LookupRecordings", func() {
		url := lbzEndpoint + "/metadata/recording"

		DescribeTable("requests",
			func(
				mbids []string, token string,
				code int, dataPath string, err error, rateLimited bool,
				expected map[string]lbzMetadataLookup, expectedErr *retry.Error,
			) {
				payload := recLookup{RecordingMbids: mbids, Inc: "artist"}
				payloadBytes, jsonErr := json.Marshal(payload)
				Expect(jsonErr).To(BeNil())

				request := makeRequest(url, token, payloadBytes)
				setupResponse(request, code, dataPath, err, rateLimited)
				actualRecordings, actualErr := LookupRecordings(mbids, token)
				validateResponse(expected, actualRecordings, expectedErr, actualErr, rateLimited)
			},
			Entry(
				"Handles HTTP Error", []string{"1"}, "",
				400, "metadata.error", nil, false,
				nil, makeErr("ListenBrainz HTTP Error. Code: 400, Error: recording_mbid 1 is not valid.", false),
			),
			Entry(
				"Handle malformed json with rateLimit", []string{"1"}, "",
				200, "malformed", nil, true,
				nil, makeErr("unexpected end of JSON input", false),
			),
			Entry(
				"Retries on connection reset error", []string{"1"}, "",
				0, "", errors.New(CONNECTION_RESET), false,
				nil, makeErr(CONNECTION_RESET, true),
			),
			Entry(
				"Does not retry on some other arbitrary http error", []string{"1"}, "",
				0, "", errors.New("fake error"), false,
				nil, makeErr("fake error", false),
			),
			Entry(
				"Handles valid response", []string{"9980309d-3480-4e7e-89ce-fce971a452be"}, EMPTY_UUID,
				200, "lookupMetadata.success", nil, true,
				map[string]lbzMetadataLookup{
					"9980309d-3480-4e7e-89ce-fce971a452be": lbzMetadataLookup{
						Artist: artistCredit{
							Artists: []extendedArtist{
								{ArtistMbid: "d2a92ee2-27ce-4e71-bfc5-12e34fe8ef56", Name: "Mili"},
							},
						},
						Recording: extendedRecording{
							Name: "world.execute(me);",
						},
					},
				}, nil,
			),
		)
	})
})

//go:build !wasip1

package listenbrainz

import (
	"fmt"
	"listenbrainz-daily-playlist/sleep"
	"os"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("ListenBrainz endpoints", func() {
	const EMPTY_UUID = "00000000-0000-0000-0000-000000000000"

	var sleepDuration *time.Duration

	mockSleep := func(d time.Duration) {
		sleepDuration = &d
	}

	makeRequest := func(token string) *pdk.HTTPRequest {
		request := pdk.HTTPRequest{}
		request.SetHeader("Accept", "application/json")
		request.SetHeader("User-Agent", userAgent)
		if token != "" {
			request.SetHeader("Authorization", "Token "+token)
		}

		return &request
	}

	createResponse := func(code int, path string) pdk.HTTPResponse {
		f, _ := os.ReadFile("testdata/" + path + ".json")
		headers := map[string]string{"x-ratelimit-remaining": "29", "x-ratelimit-reset-in": "10"}
		return pdk.NewStubHTTPResponse(uint16(code), headers, f)
	}

	createRateLimitedResponse := func(code int, path string) pdk.HTTPResponse {
		f, _ := os.ReadFile("testdata/" + path + ".json")
		headers := map[string]string{"x-ratelimit-remaining": "1", "x-ratelimit-reset-in": "5"}
		return pdk.NewStubHTTPResponse(uint16(code), headers, f)
	}

	BeforeEach(func() {
		oldSleep := sleep.Sleep
		sleep.Sleep = mockSleep

		sleepDuration = nil
		pdk.ResetMock()
		pdk.PDKMock.Calls = nil
		pdk.PDKMock.ExpectedCalls = nil
		pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()

		DeferCleanup(func() {
			sleep.Sleep = oldSleep
		})
	})

	setupResponse := func(request *pdk.HTTPRequest, code int, dataPath string, rateLimited bool) {
		if rateLimited {
			pdk.PDKMock.On("Send", request).Return(createRateLimitedResponse(code, dataPath))
		} else {
			pdk.PDKMock.On("Send", request).Return(createResponse(code, dataPath))
		}
	}

	validateResponse := func(expected, output any, err error, errMatch types.GomegaMatcher, rateLimited bool) {
		Expect(expected).To(BeComparableTo(output))
		Expect(err).To(errMatch)
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
			func(id string, token string, code int, dataPath string, expected *LbzPlaylist, errMatch types.GomegaMatcher, rateLimited bool) {
				request := makeRequest(token)
				url := lbzEndpoint + "/playlist/" + id
				pdk.PDKMock.On("NewHTTPRequest", pdk.MethodGet, url).Return(request)

				setupResponse(request, code, dataPath, rateLimited)
				playlist, err := GetPlaylist(id, token)
				validateResponse(expected, playlist, err, errMatch, rateLimited)
			},
			Entry(
				"Handle bad playlist id", "1234", "", 400, "getPlaylist.error", nil, MatchError("ListenBrainz HTTP Error. Code: 400, Error: Provided playlist ID is invalid."), false,
			),
			Entry(
				"Handle bad token error", "1234", "1234", 401, "invalidToken", nil,
				MatchError("ListenBrainz HTTP Error. Code: 401, Error: Invalid authorization token."),
				false,
			),
			Entry(
				"Handle malformed json", "1234", "", 200, "malformed", nil,
				MatchError("unexpected end of JSON input"), false,
			),
			Entry(
				"Error and rate limiter", "1234", "", 400, "getPlaylist.error", nil, MatchError("ListenBrainz HTTP Error. Code: 400, Error: Provided playlist ID is invalid."), true,
			),
			Entry(
				"Handle a real (private) playlist", EMPTY_UUID, EMPTY_UUID, 200, "getPlaylist.success", &LbzPlaylist{
					Creator:    "test",
					Identifier: "https://listenbrainz.org/playlist/00000000-0000-0000-0000-000000000000",
					Tracks: []lbTrack{
						createTrack("world.execute(me);", "Mili", "9980309d-3480-4e7e-89ce-fce971a452be", []string{"d2a92ee2-27ce-4e71-bfc5-12e34fe8ef56"}),
					},
					Title: "test",
				}, BeNil(), true,
			),
		)
	})

	Describe("GetCreatedForPlaylists", func() {
		DescribeTable("requests",
			func(user, token string, code int, dataPath string, expected []*LbzPlaylist, errMatch types.GomegaMatcher, rateLimited bool) {
				request := makeRequest(token)
				url := fmt.Sprintf("%s/user/%s/playlists/createdfor", lbzEndpoint, user)
				pdk.PDKMock.On("NewHTTPRequest", pdk.MethodGet, url).Return(request)

				setupResponse(request, code, dataPath, rateLimited)
				playlists, err := GetCreatedForPlaylists(user, token)
				validateResponse(expected, playlists, err, errMatch, rateLimited)
			},
			Entry(
				"Request with error", "a", "", 404, "createdFor.noUser", nil,
				MatchError("ListenBrainz HTTP Error. Code: 404, Error: Cannot find user: a"), false,
			),
			Entry(
				"Handle malformed json with rate limit", "a", "", 200, "malformed", nil,
				MatchError("unexpected end of JSON input"), false,
			),
			Entry(
				"Successfully fetch playlists", "test", EMPTY_UUID, 200, "createdFor.success",
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
				},
				BeNil(), true,
			),
		)
	})

	Describe("GetRecommendations", func() {
		DescribeTable("requests",
			func(user, token string, code int, dataPath string, expected *LbzRecommendations, errMatch types.GomegaMatcher, rateLimited bool) {
				request := makeRequest(token)
				url := fmt.Sprintf("%s/cf/recommendation/user/%s/recording?count=1000", lbzEndpoint, user)
				pdk.PDKMock.On("NewHTTPRequest", pdk.MethodGet, url).Return(request)

				setupResponse(request, code, dataPath, rateLimited)
				output, err := GetRecommendations(user, token)
				validateResponse(expected, output, err, errMatch, rateLimited)
			},
			Entry(
				"Request with error", "a", "", 404, "getRecommendations.error", nil,
				MatchError("ListenBrainz HTTP Error. Code: 404, Error: The requested URL was not found on the server. If you entered the URL manually please check your spelling and try again."), false,
			),
			Entry(
				"Handle malformed json with rateLimit", "a", "", 200, "malformed", nil,
				MatchError("unexpected end of JSON input"), true,
			),
			Entry(
				"Handle valid response", "test", EMPTY_UUID, 200, "getRecommendations.success", &LbzRecommendations{
					Payload: RecommendationPayload{Count: 1, LastUpdated: 1771845555, MBIDs: []RecordingMBID{{RecordingMBID: "00000000-0000-0000-0000-000000000000"}}},
				}, BeNil(), true,
			),
			Entry(
				"Handle empty", "test", EMPTY_UUID, 200, "getRecommendations.emptyCount", nil, MatchError("No recommendations found for user test"), false,
			),
		)
	})

	Describe("LookupRecordings", func() {
		url := lbzEndpoint + "/metadata/recording"

		DescribeTable("requests",
			func(mbids []string, token string, code int, dataPath string, expected map[string]lbzMetadataLookup, errMatch types.GomegaMatcher, rateLimited bool) {
				request := makeRequest(token)
				pdk.PDKMock.On("NewHTTPRequest", pdk.MethodPost, url).Return(request)

				setupResponse(request, code, dataPath, rateLimited)
				playlists, err := LookupRecordings(mbids, token)
				validateResponse(expected, playlists, err, errMatch, rateLimited)
			},
			Entry(
				"Handles HTTP Error", []string{"1"}, "", 400, "metadata.error", nil,
				MatchError("ListenBrainz HTTP Error. Code: 400, Error: recording_mbid 1 is not valid."), false,
			),
			Entry(
				"Handle malformed json with rateLimit", []string{"1"}, "", 200, "malformed", nil,
				MatchError("unexpected end of JSON input"), true,
			),
			Entry(
				"Handles valid response", []string{"9980309d-3480-4e7e-89ce-fce971a452be"}, EMPTY_UUID, 200, "lookupMetadata.success",
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
				}, BeNil(), true,
			),
		)
	})
})

//go:build !wasip1

package listenbrainz

import (
	"encoding/json"
	"errors"
	"fmt"
	"listenbrainz-daily-playlist/retry"
	"listenbrainz-daily-playlist/sleep"
	"listenbrainz-daily-playlist/testdata"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("ListenBrainz endpoints", func() {
	const (
		EMPTY_UUID = "00000000-0000-0000-0000-000000000000"
	)

	var (
		CONNECTION_RESET = errors.New("read tcp 8.8.8.8:60000->142.132.240.1:443: read: connection reset by peer")
	)

	var sleepDuration *time.Duration

	mockSleep := func(d time.Duration) {
		sleepDuration = &d
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
		host.HTTPMock.On("Send", request).Return(testdata.MakeLbzResponse(code, dataPath+".json", err, rateLimited))
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
		DescribeTable("requests",
			func(
				id string, token string,
				code int, dataPath string, err error, rateLimited bool,
				expectedPlaylist *LbzPlaylist, expectedErr *retry.Error,
			) {
				url := lbzEndpoint + "/playlist/" + id
				request := testdata.MakeLbzRequest(url, token, nil)
				setupResponse(request, code, dataPath, err, rateLimited)
				actualPlaylist, actualErr := GetPlaylist(id, token)
				validateResponse(expectedPlaylist, actualPlaylist, expectedErr, actualErr, rateLimited)
			},
			Entry(
				"Handle bad playlist id", "1234", "",
				400, "getPlaylist.error", nil, false,
				nil, retry.FatalError("ListenBrainz HTTP Error. Code: 400, Error: Provided playlist ID is invalid."),
			),
			Entry(
				"Handle bad token error", "1234", "1234",
				401, "invalidToken", nil, false,
				nil, retry.FatalError("ListenBrainz HTTP Error. Code: 401, Error: Invalid authorization token."),
			),
			Entry(
				"Handle malformed json", "1234", "",
				200, "malformed", nil, false,
				nil, retry.FatalError("unexpected end of JSON input"),
			),
			Entry(
				"Retries on connection reset error", "1234", "",
				0, "", CONNECTION_RESET, false,
				nil, retry.TempError(CONNECTION_RESET),
			),
			Entry(
				"Does not retry on some other arbitrary http error", "1234", "",
				0, "", errors.New("fake error"), false,
				nil, retry.FatalError("fake error"),
			),
			Entry(
				"Error and rate limiter", "1234",
				"", 400, "getPlaylist.error", nil, true,
				nil, retry.FatalError("ListenBrainz HTTP Error. Code: 400, Error: Provided playlist ID is invalid."),
			),
			Entry(
				"Handle a real (private) playlist",
				EMPTY_UUID, EMPTY_UUID,
				200, "getPlaylist.success", nil, true,
				&LbzPlaylist{
					Creator:    "test",
					Identifier: "https://listenbrainz.org/playlist/00000000-0000-0000-0000-000000000000",
					Tracks: []lbTrack{
						{
							Album:    "Miracle Milk",
							Creator:  "Mili",
							Duration: 211912,
							Extension: trackExtension{
								Track: trackExtensionTrack{
									AdditionalMetadata: trackAdditionalMetadata{
										Artists: []Artist{
											{
												ArtistCreditName: "Mili",
												MBID:             "d2a92ee2-27ce-4e71-bfc5-12e34fe8ef56",
											},
										},
									},
								},
							},
							Identifier: []string{"https://musicbrainz.org/recording/9980309d-3480-4e7e-89ce-fce971a452be"},
							Title:      "world.execute(me);",
						},
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
				request := testdata.MakeLbzRequest(url, token, nil)
				setupResponse(request, code, dataPath, err, rateLimited)
				actualPlaylists, actualErr := GetCreatedForPlaylists(user, token)
				validateResponse(expectedPlaylists, actualPlaylists, expectedErr, actualErr, rateLimited)
			},
			Entry(
				"Request with error", "a", "",
				404, "createdFor.noUser", nil, false,
				nil, retry.FatalError("ListenBrainz HTTP Error. Code: 404, Error: Cannot find user: a"),
			),
			Entry(
				"Handle malformed json with rate limit", "a", "",
				200, "malformed", nil, false,
				nil, retry.FatalError("unexpected end of JSON input"),
			),
			Entry(
				"Retries on connection reset error", "1234", "",
				0, "", CONNECTION_RESET, false,
				nil, retry.TempError(CONNECTION_RESET),
			),
			Entry(
				"Does not retry on some other arbitrary http error", "1234", "",
				0, "", errors.New("fake error"), false,
				nil, retry.FatalError("fake error"),
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
				request := testdata.MakeLbzRequest(url, token, nil)
				setupResponse(request, code, dataPath, err, rateLimited)
				actualRecommendations, actualErr := GetRecommendations(user, token)
				validateResponse(expectedRecommendations, actualRecommendations, expectedErr, actualErr, rateLimited)
			},
			Entry(
				"Request with error", "a", "",
				404, "getRecommendations.error", nil, false,
				nil, retry.FatalError("ListenBrainz HTTP Error. Code: 404, Error: The requested URL was not found on the server. If you entered the URL manually please check your spelling and try again."),
			),
			Entry(
				"Handle malformed json with rateLimit", "a", "",
				200, "malformed", nil, true,
				nil, retry.FatalError("unexpected end of JSON input"),
			),
			Entry(
				"Retries on connection reset error", "1234", "",
				0, "", CONNECTION_RESET, false,
				nil, retry.TempError(CONNECTION_RESET),
			),
			Entry(
				"Does not retry on some other arbitrary http error", "1234", "",
				0, "", errors.New("fake error"), false,
				nil, retry.FatalError("fake error"),
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
				nil, retry.FatalError("no recommendations found for user test"),
			),
		)

		It("handles a bad lookup error", func() {
			url := lbzEndpoint + "/cf/recommendation/user/a/recording?count=1000"
			request := testdata.MakeLbzRequest(url, "", nil)

			resp, _ := testdata.MakeLbzResponse(415, "badMetadataLookup.html", nil, true)

			host.HTTPMock.On("Send", request).Return(resp, nil)
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
				payload := recLookup{RecordingMbids: mbids, Inc: "artist release"}
				payloadBytes, jsonErr := json.Marshal(payload)
				Expect(jsonErr).To(BeNil())

				request := testdata.MakeLbzRequest(url, token, payloadBytes)
				setupResponse(request, code, dataPath, err, rateLimited)
				actualRecordings, actualErr := LookupRecordings(mbids, token)
				validateResponse(expected, actualRecordings, expectedErr, actualErr, rateLimited)
			},
			Entry(
				"Handles HTTP Error", []string{"1"}, "",
				400, "metadata.error", nil, false,
				nil, retry.FatalError("ListenBrainz HTTP Error. Code: 400, Error: recording_mbid 1 is not valid."),
			),
			Entry(
				"Handle malformed json with rateLimit", []string{"1"}, "",
				200, "malformed", nil, true,
				nil, retry.FatalError("unexpected end of JSON input"),
			),
			Entry(
				"Retries on connection reset error", []string{"1"}, "",
				0, "", CONNECTION_RESET, false,
				nil, retry.TempError(CONNECTION_RESET),
			),
			Entry(
				"Does not retry on some other arbitrary http error", []string{"1"}, "",
				0, "", errors.New("fake error"), false,
				nil, retry.FatalError("fake error"),
			),
			Entry(
				"Handles valid response", []string{"9980309d-3480-4e7e-89ce-fce971a452be"}, EMPTY_UUID,
				200, "lookupMetadata.success", nil, true,
				map[string]lbzMetadataLookup{
					"9980309d-3480-4e7e-89ce-fce971a452be": {
						Artist: artistCredit{
							Artists: []extendedArtist{
								{ArtistMbid: "d2a92ee2-27ce-4e71-bfc5-12e34fe8ef56", Name: "Mili"},
							},
						},
						Recording: extendedRecording{
							ISRCs:  []string{"TCJPE1657482"},
							Length: 211912,
							Name:   "world.execute(me);",
						},
						Release: extendedRelease{
							MBID: "38a8f6e1-0e34-4418-a89d-78240a367408",
							Name: "Miracle Milk",
						},
					},
				}, nil,
			),
		)
	})
})

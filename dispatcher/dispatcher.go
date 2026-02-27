package dispatcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"listenbrainz-daily-playlist/listenbrainz"
	"listenbrainz-daily-playlist/subsonic"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/server/subsonic/responses"
)

const JOB_DURATION = int32(30)

func (j *Job) GetDuration() int32 {
	switch j.JobType {
	case FetchPatches:
		if j.Patch != nil {
			return JOB_DURATION * int32(1+len(j.Patch.Sources))
		}

		return JOB_DURATION
	default:
		return JOB_DURATION
	}
}

func (j *Job) Dispatch() error {
	switch j.JobType {
	case FetchPatches:
		return j.dispatchSourceFetching()
	case GenerateJams:
		return j.dispatchGenerate()
	case ImportPlaylist:
		return j.dispatchImport()
	default:
		return fmt.Errorf("unexpected job %s", j.JobType)
	}
}

func (j *Job) dispatchSourceFetching() error {
	if j.Patch == nil {
		return errors.New("attempting to dispatch patch fetch without patch")
	}

	playlists, err := listenbrainz.GetCreatedForPlaylists(j.LbzUsername, j.LbzToken)
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Failed to fetch playlists for user %s: %v", j.Username, err))
		return err
	}

	for idx, source := range j.Patch.Sources {
		playlistId := ""

		for _, plsMetadata := range playlists {
			if plsMetadata.Extension.Extension.AdditionalMetadata.AlgorithmMetadata.SourcePatch == source.SourcePatch {
				playlistId = listenbrainz.GetIdentifier(plsMetadata.Identifier)
				break
			}
		}

		if playlistId == "" {
			newErr := fmt.Errorf("No playlist for ListenBrainz user `%s` found with algorithm/source patch `%s`", j.LbzUsername, source.SourcePatch)
			err = errors.Join(err, newErr)
			pdk.Log(pdk.LogError, newErr.Error())
			continue
		}

		newJob := Job{
			JobType:     ImportPlaylist,
			Username:    j.Username,
			LbzUsername: j.LbzUsername,
			LbzToken:    j.LbzToken,
			Ratings:     j.Ratings,
			Fallback:    j.Fallback,
			Import: &importJob{
				Name:  source.PlaylistName,
				LbzId: playlistId,
			},
		}

		payload, serializeErr := json.Marshal(newJob)
		if serializeErr != nil {
			pdk.Log(pdk.LogError, fmt.Sprintf("Error serializing import job: %v", serializeErr))
			err = errors.Join(err, serializeErr)
			continue
		}

		_, scheduleErr := host.SchedulerScheduleOneTime(JOB_DURATION*int32(idx+1), string(payload), "")
		if scheduleErr != nil {
			return scheduleErr
		}
	}

	return err
}

func (j *Job) dispatchGenerate() error {
	if j.Generate == nil {
		return errors.New("attempting to call generate job without generate payload")
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Generating playlist `%s` for user %s", j.Generate.Name, j.Username))

	now := time.Now()

	recommendations, err := listenbrainz.GetRecommendations(j.LbzUsername, j.LbzToken)
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Unable to fetch recommendations for user %s: %v", j.Username, err))
		return err
	}

	mbids := make([]string, len(recommendations.Payload.MBIDs))
	for idx, recording := range recommendations.Payload.MBIDs {
		mbids[idx] = recording.RecordingMBID
	}

	metadata, err := listenbrainz.LookupRecordings(mbids, j.LbzToken)
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Unable to lookup %d recordings for user %s: %v", len(mbids), j.Username, err))
		return err
	}

	allowedSongs := []*responses.Child{}
	notPlayed := []*responses.Child{}

	missing := []string{}
	excluded := []string{}
	recentCount := 0

	subsonicHandler := subsonic.NewSubsonicHandler(j.Fallback)

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

		song := subsonicHandler.LookupTrack(j.Username, recordingMetadata.Recording.Name, mbid, artistMbids)
		if song == nil {
			missing = append(missing, recordingMetadata.Recording.Name)
			continue
		}

		if !j.Ratings[song.UserRating] {
			excluded = append(excluded, recordingMetadata.Recording.Name)
			continue
		}

		if song.Played == nil {
			notPlayed = append(notPlayed, song)
			continue
		}

		if now.Sub(*song.Played).Hours() < float64(j.Generate.TrackAge*24) {
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

	if j.Generate.ArtistLimit == 0 {
		for _, song := range allowedSongs[:min(len(allowedSongs), 50)] {
			songIds = append(songIds, song.Id)
		}
	} else {
		artistCredits := map[string]int{}

	outer:
		for _, song := range allowedSongs {
			for _, artist := range song.Artists {
				count := artistCredits[artist.Id]
				if count >= j.Generate.ArtistLimit {
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

	err = subsonic.UpdatePlaylist(j.Username, j.Generate.Name, comment, songIds)
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Unable to import playlist `%s` for user %s: %v", j.Generate.Name, j.Username, err))
		return err
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Successfully generated playlist `%s` for user %s", j.Generate.Name, j.Username))
	return nil
}

func (j *Job) dispatchImport() error {
	if j.Import == nil {
		return errors.New("attempting to call import job without import payload")
	}

	playlist, err := listenbrainz.GetPlaylist(j.Import.LbzId, j.LbzToken)
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Unable to import playlist %s: %v", j.Import.LbzId, err))
		return err
	}

	songIds := []string{}
	missing := []string{}
	excluded := []string{}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Importing playlist `%s`", playlist.Title))

	subsonicHandler := subsonic.NewSubsonicHandler(j.Fallback)

	for _, track := range playlist.Tracks {
		mbid := listenbrainz.GetIdentifier(track.Identifier[0])
		artistMbids := make([]string, len(track.Extension.Track.AdditionalMetadata.Artists))
		for idx, artist := range track.Extension.Track.AdditionalMetadata.Artists {
			artistMbids[idx] = artist.MBID
		}
		song := subsonicHandler.LookupTrack(j.Username, track.Title, mbid, artistMbids)

		if song != nil {
			if j.Ratings[song.UserRating] {
				songIds = append(songIds, song.Id)
			} else {
				excluded = append(excluded, fmt.Sprintf("%s by %s", track.Title, track.Creator))
			}
		} else {
			missing = append(missing, fmt.Sprintf("%s by %s", track.Title, track.Creator))
		}
	}

	name := j.Import.Name

	if len(songIds) == 0 {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("No matching files found for playlist %s. Refusing to create/update", name))
		return nil
	}

	comment := fmt.Sprintf("Imported from playlist %s\nUpdated on: %s", playlist.Identifier, playlist.Date)

	if len(missing) > 0 {
		comment += fmt.Sprintf("\nTracks not matched by track MBID or track name + artist MBIDs: %s", strings.Join(missing, ", "))
	}

	if len(excluded) > 0 {
		comment += fmt.Sprintf("\nTracks excluded by rating rule: %s", strings.Join(excluded, ", "))
	}

	err = subsonic.UpdatePlaylist(j.Username, name, comment, songIds)

	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Failed to import playlist `%s` for user %s: %v", name, j.Username, err))
		return err
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Successfully processed playlist `%s` for user %s", name, j.Username))
	return nil
}

func GetConfig() ([]userConfig, int, error) {
	users, ok := pdk.GetConfig("users")
	if !ok {
		return nil, 0, errors.New("missing required 'users' configuration")
	}

	userMapping := []userConfig{}
	err := json.Unmarshal([]byte(users), &userMapping)
	if err != nil {
		return nil, 0, fmt.Errorf("Invalid user mapping: %s. Should be a mapping of Navidrome users to ListenBrainz usernames", users)
	}

	for _, user := range userMapping {
		names := map[string]bool{}

		if user.NDUsername == "" || user.LbzUsername == "" {
			return nil, 0, errors.New("user must have a Navidrome username and ListenBrainz username")
		}

		if len(user.Sources) > 0 {
			for _, source := range user.Sources {
				_, existing := names[source.PlaylistName]
				if existing {
					return nil, 0, fmt.Errorf("duplicate playlist name found: %s", source.PlaylistName)
				}

				names[source.PlaylistName] = true
			}
		}

		if user.GeneratePlaylist && user.GeneratedPlaylist != "" {
			_, existing := names[user.GeneratedPlaylist]
			if existing {
				return nil, 0, fmt.Errorf("duplicate playlist name found: %s", user.GeneratedPlaylist)
			}

			names[user.GeneratedPlaylist] = true
		}

		if len(user.Playlists) > 0 {
			for _, playlist := range user.Playlists {
				_, existing := names[playlist.Name]
				if existing {
					return nil, 0, fmt.Errorf("duplicate playlist name found: %s", playlist.Name)
				}
				names[playlist.Name] = true
			}
		}
	}

	fallback, ok := pdk.GetConfig("fallbackCount")
	fallbackCount := 15

	if ok {
		value, err := strconv.Atoi(fallback)
		if err != nil {
			return nil, 0, errors.New("fallbackCount is not a valid number")
		}

		if value < 1 || value > 500 {
			return nil, 0, errors.New("fallbackCount must be between 1 and 500 (inclusive)")
		}

		fallbackCount = value
	}

	return userMapping, fallbackCount, nil
}

func parseRatings(ratingString []string) map[int32]bool {
	ratings := map[int32]bool{}

	for _, rating := range ratingString {
		ratingInt, err := strconv.ParseInt(rating, 10, 32)
		if err != nil {
			continue
		}

		if ratingInt >= 0 && ratingInt <= 5 {
			ratings[int32(ratingInt)] = true
		}
	}

	if len(ratings) == 0 {
		ratings = map[int32]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true}
	}

	return ratings
}

func InitialFetch() error {
	users, fallbackCount, err := GetConfig()
	if err != nil {
		return err
	}

	nowTs := time.Now()

	missing := []string{}
	olderThanThreeHours := []string{}

	jobs := []Job{}

	for _, user := range users {
		playlistResp, ok := subsonic.Call("getPlaylists", user.NDUsername, &url.Values{"username": []string{user.NDUsername}})
		if !ok {
			return errors.New("Failed to fetch playlists on initial fetch")
		}

		fetchedSources := []source{}
		rating := parseRatings(user.Ratings)

		if len(user.Sources) > 0 {
			for _, source := range user.Sources {
				pls := subsonic.FindExistingPlaylist(playlistResp, source.PlaylistName)

				if pls == nil {
					missing = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, source.PlaylistName))
					fetchedSources = append(fetchedSources, source)
					continue
				}

				if nowTs.Sub(pls.Changed) > 3*time.Hour {
					olderThanThreeHours = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, source.PlaylistName))
					fetchedSources = append(fetchedSources, source)
				}
			}
		}

		if len(fetchedSources) > 0 {
			jobs = append(jobs, Job{
				JobType:     FetchPatches,
				Username:    user.NDUsername,
				LbzUsername: user.LbzUsername,
				LbzToken:    user.LbzToken,
				Ratings:     rating,
				Fallback:    fallbackCount,
				Patch: &patchJob{
					Sources: fetchedSources,
				},
			})
		}

		if user.GeneratePlaylist && user.GeneratedPlaylist != "" {
			pls := subsonic.FindExistingPlaylist(playlistResp, user.GeneratedPlaylist)
			shouldGenerate := false

			if pls == nil {
				missing = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, user.GeneratedPlaylist))
				shouldGenerate = true
			} else if nowTs.Sub(pls.Changed) > 3*time.Hour {
				olderThanThreeHours = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, user.GeneratedPlaylist))
				shouldGenerate = true
			}

			if shouldGenerate {
				jobs = append(jobs, Job{
					JobType:     GenerateJams,
					Username:    user.NDUsername,
					LbzUsername: user.LbzUsername,
					LbzToken:    user.LbzToken,
					Ratings:     rating,
					Fallback:    fallbackCount,
					Generate: &generationJob{
						Name:        user.GeneratedPlaylist,
						ArtistLimit: user.GeneratedPlaylistArtistLimit,
						TrackAge:    user.GeneratedPlaylistTrackAge,
					},
				})
			}
		}

		if len(user.Playlists) > 0 {
			for _, item := range user.Playlists {
				pls := subsonic.FindExistingPlaylist(playlistResp, item.Name)
				shouldImport := false

				if pls == nil {
					missing = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, item.Name))
					shouldImport = true
				} else if !item.OneTime && nowTs.Sub(pls.Changed) > 3*time.Hour {
					olderThanThreeHours = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, user.GeneratedPlaylist))
					shouldImport = true
				}

				if shouldImport {
					jobs = append(jobs, Job{
						JobType:     ImportPlaylist,
						Username:    user.NDUsername,
						LbzUsername: user.LbzUsername,
						LbzToken:    user.LbzToken,
						Ratings:     rating,
						Fallback:    fallbackCount,
						Import: &importJob{
							Name:  item.Name,
							LbzId: item.LbzId,
						},
					})
				}
			}
		}
	}

	if len(jobs) > 0 {
		delay := int32(1)
		pdk.Log(pdk.LogInfo,
			fmt.Sprintf("Missing or outdated playlists, fetching on initial sync. Missing: %v, Outdated: %v",
				missing,
				olderThanThreeHours,
			))

		for _, job := range jobs {
			payload, err := json.Marshal(job)
			if err != nil {
				return err
			}

			_, err = host.SchedulerScheduleOneTime(delay, string(payload), "")
			if err != nil {
				return err
			}

			delay += job.GetDuration()
		}
	} else {
		pdk.Log(pdk.LogInfo, "No missing/outdated playlists, not fetching")
	}

	return nil
}

package main

import (
	"encoding/json"
	"fmt"
	"listenbrainz-daily-playlist/dispatcher"
	"math/rand"
	"strconv"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
)

const (
	fetch     = "fetch"
	dailyCron = "daily-cron"
)

type brainzPlaylistPlugin struct{}

func (b *brainzPlaylistPlugin) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	switch req.Payload {
	case dailyCron:
		delay := rand.Int31n(3600)
		pdk.Log(pdk.LogInfo, fmt.Sprintf("Delaying fetch by %d seconds", delay))
		_, err := host.SchedulerScheduleOneTime(delay, fetch, fetch)
		return err
	case fetch:
		return dispatcher.InitialFetch()
	default:
		return fmt.Errorf("unexpected scheduler task %s", req.Payload)
	}
}

func (b *brainzPlaylistPlugin) OnTaskExecute(req taskworker.TaskExecuteRequest) (string, error) {
	var job dispatcher.Job
	err := json.Unmarshal(req.Payload, &job)

	if err != nil {
		msg := fmt.Sprintf("unable to deserialize callback to a valid job: %v\n%s", err, req.Payload)
		pdk.Log(pdk.LogError, msg)
		return msg, nil
	}

	pdk.Log(pdk.LogTrace, "Dispatching job: "+string(req.Payload))
	unrecoverableError, retryError := job.Dispatch()

	if unrecoverableError != "" {
		pdk.Log(pdk.LogError, "An unrecoverable error occurred: "+unrecoverableError)
	}

	return unrecoverableError, retryError
}

func (b *brainzPlaylistPlugin) OnInit() error {
	schedule, ok := pdk.GetConfig("schedule")
	if !ok {
		schedule = "8"
	}

	schedInt, err := strconv.Atoi(schedule)
	if err != nil {
		return fmt.Errorf("Invalid schedule %s: %v", schedule, err)
	}

	err = dispatcher.CreateQueue()
	if err != nil {
		return fmt.Errorf("Unable to create task queue: %v", err)
	}

	if schedInt < 0 || schedInt > 23 {
		return fmt.Errorf("Schedule is not a valid hour (between [0, 23], inclusive): %d", schedInt)
	}

	_, _, err = dispatcher.GetConfig()
	if err != nil {
		return err
	}

	_, err = host.SchedulerScheduleRecurring(fmt.Sprintf("0 %d * * *", schedInt), dailyCron, dailyCron)
	if err != nil {
		return fmt.Errorf("Failed to schedule playlist sync. Is your schedule a valid cron expression? %v", err)
	}

	checkOnStartup, ok := pdk.GetConfig("checkOnStartup")

	if !ok || checkOnStartup != "false" {
		_, err := host.SchedulerScheduleOneTime(1, fetch, fetch)
		if err != nil {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("Failed to do initial sync. Proceeding anyway %v", err))
		}
	}

	pdk.Log(pdk.LogInfo, "init success")
	return nil
}

func main() {}

func init() {
	lifecycle.Register(&brainzPlaylistPlugin{})
	scheduler.Register(&brainzPlaylistPlugin{})
	taskworker.Register(&brainzPlaylistPlugin{})
}

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/yeboahd24/cronwatch/internal/channel"
)

// notifyTimeout bounds all sends together, below hookTimeout, so a slow
// service fails the hook with its own error instead of the hook timing out.
const notifyTimeout = 25 * time.Second

func notifyCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("notify", "cronwatch notify [--test] URL...",
		"Send a job's alert to chat and push services. Use it as the hook:\n\n"+
			"  --on-failure 'cronwatch notify https://ntfy.sh/TOPIC'\n\n"+
			"It reads the CRONWATCH_* variables a hook gets. The URL's host picks the service:\n"+
			"  https://ntfy.sh/TOPIC                                      ntfy\n"+
			"  https://hooks.slack.com/services/...                       Slack\n"+
			"  https://discord.com/api/webhooks/...                       Discord\n"+
			"  https://api.telegram.org/botTOKEN/sendMessage?chat_id=ID   Telegram\n"+
			"  any other http(s) URL                                      JSON webhook\n"+
			"For a self-hosted service, prefix its name: ntfy+https://ntfy.example.com/TOPIC,\n"+
			"slack+https://mattermost.example.com/hooks/KEY. Every URL is tried; it fails if any send fails.")
	test := fs.Bool("test", false, "send a test message instead of reading the hook's variables")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("notify needs at least one URL")
	}
	dests := make([]channel.Destination, fs.NArg())
	for i, raw := range fs.Args() {
		d, err := channel.Parse(raw)
		if err != nil {
			return fmt.Errorf("URL %d: %w", i+1, err)
		}
		dests[i] = d
	}
	var msg channel.Message
	if *test {
		msg = channel.Message{Event: "test"}
		msg.Host, _ = os.Hostname()
	} else {
		var err error
		if msg, err = messageFromEnv(os.Getenv); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	client := &http.Client{}
	errs := make([]error, len(dests))
	var wg sync.WaitGroup
	for i, d := range dests {
		wg.Go(func() { errs[i] = channel.Send(ctx, client, d, msg) })
	}
	wg.Wait()
	for i, d := range dests {
		if errs[i] == nil {
			fmt.Fprintf(stdout, "sent to %s\n", d)
		}
	}
	return errors.Join(errs...)
}

// messageFromEnv builds the alert from the variables a hook gets.
func messageFromEnv(getenv func(string) string) (channel.Message, error) {
	m := channel.Message{
		Event:     getenv("CRONWATCH_EVENT"),
		JobName:   getenv("CRONWATCH_JOB_NAME"),
		JobSlug:   getenv("CRONWATCH_JOB_SLUG"),
		Host:      getenv("CRONWATCH_HOST"),
		Status:    getenv("CRONWATCH_STATUS"),
		Reason:    getenv("CRONWATCH_REASON"),
		LastError: getenv("CRONWATCH_LAST_ERROR"),
		RunID:     getenv("CRONWATCH_RUN_ID"),
	}
	if m.Event == "" || m.JobName == "" {
		return m, errors.New("CRONWATCH_EVENT is not set: run notify as an --on-failure or --on-recover hook, or pass --test")
	}
	if v := getenv("CRONWATCH_EXIT_CODE"); v != "" {
		if code, err := strconv.Atoi(v); err == nil {
			m.ExitCode = &code
		}
	}
	for _, f := range []struct {
		dst **time.Time
		env string
	}{{&m.StartedAt, "CRONWATCH_STARTED_AT"}, {&m.ExpectedAt, "CRONWATCH_EXPECTED_AT"}} {
		if t, err := time.Parse(time.RFC3339, getenv(f.env)); err == nil {
			*f.dst = &t
		}
	}
	return m, nil
}

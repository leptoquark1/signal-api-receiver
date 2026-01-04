package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/urfave/cli/v3"
	"golang.org/x/sync/errgroup"

	"github.com/leptoquark1/signal-api-receiver/pkg/localerror"
	"github.com/leptoquark1/signal-api-receiver/pkg/mqtt"
	"github.com/leptoquark1/signal-api-receiver/pkg/receiver"
	"github.com/leptoquark1/signal-api-receiver/pkg/server"
)

// https://regex101.com/r/sxO3RG/1
var accountRegex = regexp.MustCompile(`^\+[0-9]+$`)

func makeRandomClientID() string {
	parts := []string{"signal-api-receiver"}

	netInterfaces, err := net.Interfaces()
	if err == nil {
		parts = append(parts, strings.ReplaceAll(netInterfaces[0].HardwareAddr.String(), ":", ""))
	}

	return strings.Join(parts, "-")
}

func serveCommand() *cli.Command {
	randomClientID := makeRandomClientID()

	return &cli.Command{
		Name:    "serve",
		Aliases: []string{"s"},
		Usage:   "start the signal-api-receiver HTTP server",
		Action:  serveAction(),
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name: "record-message-type",
				Usage: fmt.Sprintf(
					"Which message types to record? Valid message types: %v",
					receiver.AllMessageTypes(),
				),
				Value: []string{
					receiver.MessageTypeDataMessage.String(),
				},
				Validator: func(mts []string) error {
					for _, mt := range mts {
						_, err := receiver.ParseMessageType(mt)
						if err != nil {
							return localerror.MessageTypeParseFormatError(mt, err)
						}
					}

					return nil
				},
			},
			&cli.BoolFlag{
				Name:    "repeat-last-message",
				Usage:   "Repeat the last message if there are no new messages (applies to /receive/pop)",
				Sources: cli.EnvVars("REPEAT_LAST_MESSAGE"),
			},
			&cli.StringFlag{
				Name:     "signal-account",
				Usage:    "The account number for signal",
				Sources:  cli.EnvVars("SIGNAL_ACCOUNT"),
				Required: true,
				Validator: func(a string) error {
					if !accountRegex.MatchString(a) {
						return localerror.InvalidSignalAccountError()
					}

					return nil
				},
			},
			&cli.StringFlag{
				Name:     "signal-api-url",
				Usage:    "The URL of the Signal api including the scheme. e.g wss://signal-api.example.com",
				Sources:  cli.EnvVars("SIGNAL_API_URL"),
				Required: true,
				Validator: func(u string) error {
					uri, err := url.Parse(u)
					if err != nil {
						return err
					}

					if uri.Scheme == "" {
						return localerror.ErrSchemeMissing
					}

					return nil
				},
			},
			&cli.StringFlag{
				Name:    "server-addr",
				Usage:   "The address of the server",
				Sources: cli.EnvVars("SERVER_ADDR"),
				Value:   ":8105",
			},
			&cli.StringFlag{
				Name:    "mqtt-server",
				Usage:   "MQTT Server Host and Port",
				Sources: cli.EnvVars("MQTT_PASSWORD"),
			},
			&cli.StringFlag{
				Name:    "mqtt-client-id",
				Usage:   "MQTT Client ID",
				Sources: cli.EnvVars("MQTT_CLIENT_ID"),
				Value:   randomClientID,
			},
			&cli.StringFlag{
				Name:    "mqtt-user",
				Usage:   "MQTT Username",
				Sources: cli.EnvVars("MQTT_USER"),
			},
			&cli.StringFlag{
				Name:    "mqtt-password",
				Usage:   "MQTT Password",
				Sources: cli.EnvVars("MQTT_PASSWORD"),
			},
			&cli.StringFlag{
				Name:    "mqtt-topic-prefix",
				Usage:   "MQTT Topic Prefix. {topic-prefix}/message",
				Sources: cli.EnvVars("MQTT_TOPIC_PREFIX"),
				Value:   "signal-api-receiver",
			},
			&cli.IntFlag{
				Name:    "mqtt-qos",
				Usage:   "MQTT Quality of Service (QoS) value",
				Sources: cli.EnvVars("MQTT_QOS"),
				Value:   1,
				Validator: func(q int) error {
					allowedValues := []int{1, 2, 3}

					if !slices.Contains(allowedValues, q) {
						return localerror.MqttQosValueNotAllowedError(q, []int{0, 1, 2})
					}

					return nil
				},
			},
		},
	}
}

func serveAction() cli.ActionFunc {
	return func(ctx context.Context, cmd *cli.Command) error {
		logger := zerolog.Ctx(ctx).With().Str("cmd", "serve").Logger()

		ctx = logger.WithContext(ctx)

		ctx, cancel := context.WithCancel(ctx)

		g, ctx := errgroup.WithContext(ctx)
		defer func() {
			if err := g.Wait(); err != nil {
				logger.Error().Err(err).Msg("error returned from g.Wait()")
			}
		}()

		// NOTE: Reminder that defer statements run last to first so the first
		// thing that happens here is the context is canceled which triggers the
		// errgroup 'g' to start exiting.
		defer cancel()

		g.Go(func() error {
			return autoMaxProcs(ctx, 30*time.Second, logger)
		})

		signalAPIURL := cmd.String("signal-api-url")

		uri, err := url.Parse(signalAPIURL)
		if err != nil {
			return localerror.SignalURLParseError(signalAPIURL, err)
		}

		uri = uri.JoinPath(fmt.Sprintf("/v1/receive/%s", cmd.String("signal-account")))

		logger.Info().
			Str("signal-api-url", uri.String()).
			Msg("the fully qualified signal-api URL was computed")

		sarc, err := receiver.New(ctx, uri, cmd.StringSlice("record-message-type")...)
		if err != nil {
			return localerror.ReceiverCreateError(err)
		}

		if cmd.IsSet("mqtt-server") {
			err := mqtt.Init(
				ctx,
				cmd.String("mqtt-server"),
				cmd.String("mqtt-client-id"),
				cmd.String("mqtt-user"),
				cmd.String("mqtt-password"),
				cmd.String("mqtt-topic-prefix"),
				cmd.Int("mqtt-qos"),
			)
			if err != nil {
				return localerror.MqttInitError(err)
			}
		}

		srv := server.New(ctx, sarc, cmd.Bool("repeat-last-message"))

		server := &http.Server{
			Addr:              cmd.String("server-addr"),
			Handler:           srv,
			ReadHeaderTimeout: 10 * time.Second,
		}

		logger.Info().
			Str("server-addr", cmd.String("server-addr")).
			Msg("Server started")

		if err := server.ListenAndServe(); err != nil {
			return localerror.HTTPListenerStartError(err)
		}

		return nil
	}
}

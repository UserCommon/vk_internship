package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"
	"unicode"

	"github.com/mattermost/mattermost-server/v6/model"
	"github.com/rs/zerolog"
	"github.com/tarantool/go-tarantool/v2"
	_ "github.com/tarantool/go-tarantool/v2/datetime"
	_ "github.com/tarantool/go-tarantool/v2/decimal"
	_ "github.com/tarantool/go-tarantool/v2/uuid"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)

	defer cancel()
	dialer := tarantool.NetDialer{
		Address: os.Getenv("TARANTOOL_SERVER"),
		// Address:  "localhost:3301",
		User:     "voting_bot",
		Password: "123321",
	}
	opts := tarantool.Opts{
		Timeout: time.Second,
	}

	conn, err := tarantool.Connect(ctx, dialer, opts)
	if err != nil {
		fmt.Println("Connection refused:", err)
		return
	}

	app := &application{
		logger: zerolog.New(
			zerolog.ConsoleWriter{
				Out:        os.Stdout,
				TimeFormat: time.RFC822,
			},
		).With().Timestamp().Logger(),
		TarantoolConnection: conn,
	}

	app.config = loadConfig()
	app.logger.Info().Str("config", fmt.Sprint(app.config)).Msg("")

	setupGracefulShutdown(app)

	// Create a new mattermost client.
	app.mattermostClient = model.NewAPIv4Client(app.config.mattermostServer.String())

	// Login.
	app.mattermostClient.SetToken(app.config.mattermostToken)

	if user, resp, err := app.mattermostClient.GetUser("me", ""); err != nil {
		app.logger.Fatal().Err(err).Msg("Could not log in")
	} else {
		app.logger.Debug().Interface("user", user).Interface("resp", resp).Msg("")
		app.logger.Info().Msg("Logged in to mattermost")
		app.mattermostUser = user
	}

	// Find and save the bot's team to app struct.
	if team, resp, err := app.mattermostClient.GetTeamByName(app.config.mattermostTeamName, ""); err != nil {
		app.logger.Fatal().Err(err).Msg("Could not find team. Is this bot a member ?")
	} else {
		app.logger.Debug().Interface("team", team).Interface("resp", resp).Msg("")
		app.mattermostTeam = team
	}

	// Find and save the talking channel to app struct.
	if channel, resp, err := app.mattermostClient.GetChannelByName(
		app.config.mattermostChannel, app.mattermostTeam.Id, "",
	); err != nil {
		app.logger.Fatal().Err(err).Msg("Could not find channel. Is this bot added to that channel ?")
	} else {
		app.logger.Debug().Interface("channel", channel).Interface("resp", resp).Msg("")
		app.mattermostChannel = channel
	}

	// Send a message (new post).
	sendMsgToTalkingChannel(app, "Hi! I am a bot.", "")

	// Listen to live events coming in via websocket.
	listenToEvents(app)
}

func setupGracefulShutdown(app *application) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for range c {
			if app.mattermostWebSocketClient != nil {
				app.logger.Info().Msg("Closing websocket connection")
				app.mattermostWebSocketClient.Close()
			}
			app.logger.Info().Msg("Shutting down")
			os.Exit(0)
		}
	}()
}

func sendMsgToTalkingChannel(app *application, msg string, replyToId string) {
	// Note that replyToId should be empty for a new post.
	// All replies in a thread should reply to root.

	post := &model.Post{}
	post.ChannelId = app.mattermostChannel.Id
	post.Message = msg

	post.RootId = replyToId

	if _, _, err := app.mattermostClient.CreatePost(post); err != nil {
		app.logger.Error().Err(err).Str("RootID", replyToId).Msg("Failed to create post")
	}
}

func listenToEvents(app *application) {
	var err error
	failCount := 0
	for {
		app.mattermostWebSocketClient, err = model.NewWebSocketClient4(
			fmt.Sprintf("ws://%s", app.config.mattermostServer.Host+app.config.mattermostServer.Path),
			app.mattermostClient.AuthToken,
		)
		if err != nil {
			app.logger.Warn().Err(err).Msg("Mattermost websocket disconnected, retrying")
			failCount += 1
			// TODO: backoff based on failCount and sleep for a while.
			continue
		}
		app.logger.Info().Msg("Mattermost websocket connected")

		app.mattermostWebSocketClient.Listen()

		for event := range app.mattermostWebSocketClient.EventChannel {
			// Launch new goroutine for handling the actual event.
			// If required, you can limit the number of events beng processed at a time.
			go handleWebSocketEvent(app, event)
		}
	}
}

func handleWebSocketEvent(app *application, event *model.WebSocketEvent) {

	// Ignore other channels.
	if event.GetBroadcast().ChannelId != app.mattermostChannel.Id {
		return
	}

	// Ignore other types of events.
	if event.EventType() != model.WebsocketEventPosted {
		return
	}

	// Since this event is a post, unmarshal it to (*model.Post)
	post := &model.Post{}
	err := json.Unmarshal([]byte(event.GetData()["post"].(string)), &post)
	if err != nil {
		app.logger.Error().Err(err).Msg("Could not cast event to *model.Post")
	}

	// Ignore messages sent by this bot itself.
	if post.UserId == app.mattermostUser.Id {
		return
	}

	// Handle however you want.
	handlePost(app, post)
}

func handlePost(app *application, post *model.Post) {
	app.logger.Debug().Str("message", post.Message).Msg("")
	app.logger.Debug().Interface("post", post).Msg("")

	if strings.HasPrefix(post.Message, "/vote") {
		args := strings.Fields(post.Message)
		if len(args) < 2 {
			sendHelp(app, post.Id)
			return
		}

		switch args[1] {
		case "create":
			handleCreatePoll(app, post, args[2:])
		case "vote":
			handleVote(app, post, args[2:])
		case "results":
			handleResults(app, post, args[2:])
		case "close":
			handleClosePoll(app, post, args[2:])
		case "delete":
			handleDeletePoll(app, post, args[2:])
		default:
			sendHelp(app, post.Id)
		}
	}
}

func parseCommandArgs(input string) []string {
	var args []string
	var buffer bytes.Buffer
	inQuotes := false

	for _, r := range input {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			buffer.WriteRune(r)
		case unicode.IsSpace(r) && !inQuotes:
			if buffer.Len() > 0 {
				args = append(args, strings.Trim(buffer.String(), "\""))
				buffer.Reset()
			}
		default:
			buffer.WriteRune(r)
		}
	}

	if buffer.Len() > 0 {
		args = append(args, strings.Trim(buffer.String(), "\""))
	}

	return args
}

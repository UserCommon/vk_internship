package main

import (
	"github.com/mattermost/mattermost-server/v6/model"
	"github.com/rs/zerolog"
	"github.com/tarantool/go-tarantool/v2"
)

// application struct to hold the dependencies for our bot
type application struct {
	config                    config
	logger                    zerolog.Logger
	mattermostClient          *model.Client4
	mattermostWebSocketClient *model.WebSocketClient
	mattermostUser            *model.User
	mattermostChannel         *model.Channel
	mattermostTeam            *model.Team
	TarantoolConnection       *tarantool.Connection
}

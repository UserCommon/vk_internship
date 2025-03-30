package main

import (
	"time"
	"fmt"
	"strings"
	"math/rand"

	"github.com/mattermost/mattermost-server/v6/model"
	"github.com/tarantool/go-tarantool/v2"
	_ "github.com/tarantool/go-tarantool/v2/datetime"
	_ "github.com/tarantool/go-tarantool/v2/decimal"
	_ "github.com/tarantool/go-tarantool/v2/uuid"

)

// Structure that describe Poll type
type Poll struct {
	ID string
	Question string
	Options map[string]int
	Votes map[string]string
	Creator string
	Active bool
	CreatedAt uint64
}

func generatePollID() string {
    // Генерация UUID версии 4
    uuid := make([]byte, 16)
    _, err := rand.Read(uuid)
    if err != nil {
        panic("Failed to generate UUID")
    }
    
    uuid[6] = (uuid[6] & 0x0f) | 0x40 // Version 4
    uuid[8] = (uuid[8] & 0x3f) | 0x80 // Variant is 10
    
    return fmt.Sprintf("%x-%x-%x-%x-%x", 
        uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])
}


func handleCreatePoll(app *application, post *model.Post, args []string) {
    if len(args) < 2 {
        sendHelp(app, post.Id)
        return
    }

    question := args[0]
    options := make(map[string]int)
    for _, opt := range args[1:] {
        options[opt] = 0
    }

    poll := Poll{
        ID:        generatePollID(),
        Question:  question,
        Options:   options,
        Votes:     make(map[string]string),
        Creator:   post.UserId,
        Active:    true,
        CreatedAt: uint64(time.Now().Unix()),
    }

    req := tarantool.NewInsertRequest("polls").
			Tuple([]interface{}{
				generatePollID(),
				question,
				options,       // Теперь это map
				make(map[string]string), // votes
				post.UserId,
				true,
				time.Now().Unix(),
			})

    _, err := app.TarantoolConnection.Do(req).Get()
    if err != nil {
        app.logger.Error().Err(err).Msg("Failed to create poll")
        sendMsgToTalkingChannel(app, "❌ Failed to create poll", post.Id)
        return
    }

    response := fmt.Sprintf("🗳️ New poll created!\nID: `%s`\nQuestion: %s\nOptions: %v",
        poll.ID, poll.Question, getOptionsList(poll.Options))
    sendMsgToTalkingChannel(app, response, post.Id)
}

func handleVote(app *application, post *model.Post, args []string) {
    if len(args) < 2 {
        sendHelp(app, post.Id)
        return
    }

    pollID := args[0]
    choice := strings.Join(args[1:], " ")

		// Log the voting attempt
    app.logger.Info().
        Str("poll_id", pollID).
        Str("user_id", post.UserId).
        Str("choice", choice).
        Msg("Vote attempt")

    tuple, err := getPoll(app.TarantoolConnection, pollID)
		if err != nil {
        app.logger.Error().
            Err(err).
            Str("poll_id", pollID).
            Msg("Poll lookup failed")
        
        // Try to suggest active polls
        if activePolls, err := getActivePolls(app); err == nil {
            sendMsgToTalkingChannel(app, 
                fmt.Sprintf("❌ Poll not found. Active polls:\n%s", activePolls), 
                post.Id)
        } else {
            sendMsgToTalkingChannel(app, "❌ Poll not found", post.Id)
        }
        return
    }

		// 2. Check if poll is active
    active, ok := tuple[5].(bool)
    if !ok {
        app.logger.Error().
            Interface("active_field", tuple[5]).
            Msg("Invalid active field type")
        sendMsgToTalkingChannel(app, "❌ Poll data corrupted", post.Id)
        return
    }

    if !active {
        sendMsgToTalkingChannel(app, "❌ This poll is closed", post.Id)
        return
    }

		// 3. Verify the option exists
    options, ok := tuple[2].(map[interface{}]interface{})
    if !ok {
        app.logger.Error().
            Interface("options_field", tuple[2]).
            Msg("Invalid options field type")
        sendMsgToTalkingChannel(app, "❌ Poll data corrupted", post.Id)
        return
    }

    if _, exists := options[choice]; !exists {
        validOptions := make([]string, 0, len(options))
        for opt := range options {
            validOptions = append(validOptions, fmt.Sprintf("- %s", opt))
        }
        sendMsgToTalkingChannel(app, 
            fmt.Sprintf("❌ Invalid option. Valid options:\n%s", strings.Join(validOptions, "\n")), 
            post.Id)
        return
    }

		// 4. Prepare update operations
		ops := tarantool.NewOperations().
						Add(3, []interface{}{"=", choice, 1}).
						Assign(4, []interface{}{post.UserId, choice})

		// 5. Execute update
    updateReq := tarantool.NewUpdateRequest("polls").
        Index("primary").
        Key(tarantool.StringKey{pollID}).
        Operations(ops)

    _, err = app.TarantoolConnection.Do(updateReq).Get()
    if err != nil {
        app.logger.Error().Err(err).Msg("Failed to process vote")
        sendMsgToTalkingChannel(app, "❌ Failed to process your vote", post.Id)
        return
    }

		// 6. Confirm success
    sendMsgToTalkingChannel(app, fmt.Sprintf(
        "✅ Vote for '%s' recorded! Current results:\n%s", 
        choice, 
        formatPollResults(tuple)), 
        post.Id)
}

func handleResults(app *application, post *model.Post, args []string) {
    if len(args) < 1 {
        sendHelp(app, post.Id)
        return
    }

    pollID := args[0]
		tuple, err := getPoll(app.TarantoolConnection, pollID) 
    if err != nil {
        sendMsgToTalkingChannel(app, "❌ Poll not found", post.Id)
        return
    }

    options, ok := tuple[2].(map[interface{}]interface{})
    if !ok {
        sendMsgToTalkingChannel(app, "❌ Failed to parse options", post.Id)
        return
    }

    votes, ok := tuple[3].(map[interface{}]interface{})
    if !ok {
        sendMsgToTalkingChannel(app, "❌ Failed to parse votes", post.Id)
        return
    }

    var result strings.Builder
    result.WriteString(fmt.Sprintf("📊 Results for poll *%s*\n", tuple[1].(string)))

    for opt, count := range options {
        result.WriteString(fmt.Sprintf("- %s: %v votes\n", opt, count))
    }

    result.WriteString(fmt.Sprintf("\nTotal voters: %d", len(votes)))
    sendMsgToTalkingChannel(app, result.String(), post.Id)
}


func handleClosePoll(app *application, post *model.Post, args []string) {
    if len(args) < 1 {
        sendHelp(app, post.Id)
        return
    }

    pollID := args[0]
    tuple, err := getPoll(app.TarantoolConnection, pollID)
    if err != nil {
        sendMsgToTalkingChannel(app, "❌ Poll not found", post.Id)
        return
    }

    if creator, ok := tuple[4].(string); !ok || creator != post.UserId {
        sendMsgToTalkingChannel(app, "❌ Only poll creator can close it", post.Id)
        return
    }

		ops := tarantool.NewOperations().
			Assign(6, false)

		updateReq := tarantool.NewUpdateRequest("polls").
			Index("primary").
			Key(tarantool.StringKey{pollID}).
			Operations(ops)

    _, err = app.TarantoolConnection.Do(updateReq).Get()
    if err != nil {
        app.logger.Error().Err(err).Msg("Failed to close poll")
        sendMsgToTalkingChannel(app, "❌ Failed to close poll", post.Id)
        return
    }

    sendMsgToTalkingChannel(app, "✅ Poll closed!", post.Id)
}

func handleDeletePoll(app *application, post *model.Post, args []string) {
    if len(args) < 1 {
        sendHelp(app, post.Id)
        return
    }

    pollID := args[0]
    tuple, err := getPoll(app.TarantoolConnection, pollID)
    if err != nil {
        sendMsgToTalkingChannel(app, "❌ Poll not found", post.Id)
        return
    }

    if creator, ok := tuple[4].(string); !ok || creator != post.UserId {
        sendMsgToTalkingChannel(app, "❌ Only poll creator can delete it", post.Id)
        return
    }

    deleteReq := tarantool.NewDeleteRequest("polls").
        Index("primary").
        Key(tarantool.StringKey{pollID})

    _, err = app.TarantoolConnection.Do(deleteReq).Get()
    if err != nil {
        app.logger.Error().Err(err).Msg("Failed to delete poll")
        sendMsgToTalkingChannel(app, "❌ Failed to delete poll", post.Id)
        return
    }

    sendMsgToTalkingChannel(app, "✅ Poll deleted!", post.Id)
}

func getPoll(conn *tarantool.Connection, pollID string) ([]interface{}, error) {
    req := tarantool.NewSelectRequest("polls").
        Index("primary").
        Key(tarantool.StringKey{pollID}).
        Limit(1)

    data, err := conn.Do(req).Get()
    if err != nil {
        return nil, fmt.Errorf("database error: %v", err)
    }

    if len(data) == 0 {
        return nil, fmt.Errorf("poll not found")
    }

    tuple, ok := data[0].([]interface{})
    if !ok || len(tuple) < 7 {
        return nil, fmt.Errorf("invalid poll format")
    }

    return tuple, nil
}

func getActivePolls(app *application) (string, error) {
    resp, err := app.TarantoolConnection.Do(
        tarantool.NewSelectRequest("polls").
            Index("primary").
            Iterator(tarantool.IterAll).
            Limit(10),
    ).Get()

    if err != nil {
        return "", fmt.Errorf("failed to fetch polls: %w", err)
    }

    var activePolls []string
    for _, item := range resp {
        tuple, ok := item.([]interface{})
        if !ok || len(tuple) < 6 {
            continue
        }

        if active, ok := tuple[5].(bool); ok && active {
            id, _ := tuple[0].(string)
            question, _ := tuple[1].(string)
            activePolls = append(activePolls, fmt.Sprintf("- %s: %s", id, question))
        }
    }

    if len(activePolls) == 0 {
        return "No active polls available", nil
    }

    return strings.Join(activePolls, "\n"), nil
}

func formatPollResults(tuple []interface{}) string {
    options, ok := tuple[2].(map[interface{}]interface{})
    if !ok {
        return "Unable to display results"
    }

    var results []string
    for opt, count := range options {
        results = append(results, fmt.Sprintf("- %s: %v votes", opt, count))
    }

    return strings.Join(results, "\n")
}


func getOptionsList(options map[string]int) string {
	var list []string
	for opt := range options {
		list = append(list, fmt.Sprintf("`%s`", opt))
	}
	return strings.Join(list, ", ")
}


func sendHelp(app *application, replyToId string) {
	help := `📝 **Available commands:**
/vote create [question] [option1] [option2]... - Create new poll
/vote vote [pollID] [option] - Vote in a poll
/vote results [pollID] - Show poll results
/vote close [pollID] - Close a poll (creator only)
/vote delete [pollID] - Delete a poll (creator only)`

	sendMsgToTalkingChannel(app, help, replyToId)
}

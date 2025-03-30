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

		id := generatePollID()

    poll := Poll{
        ID:        id,
        Question:  question,
        Options:   options,
        Votes:     make(map[string]string),
        Creator:   post.UserId,
        Active:    true,
        CreatedAt: uint64(time.Now().Unix()),
    }

    req := tarantool.NewInsertRequest("polls").
			Tuple([]interface{}{
				id,
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

    app.logger.Info().
        Str("poll_id", pollID).
        Str("user_id", post.UserId).
        Str("choice", choice).
        Msg("Vote attempt")

    tuple, err := getPoll(app.TarantoolConnection, pollID)
		app.logger.Debug().Msgf("Raw options from Tarantool: %+v (type: %T)", tuple[2], tuple[2])
    if err != nil {
        app.logger.Error().Err(err).Str("poll_id", pollID).Msg("Poll lookup failed")
        
        if activePolls, err := getActivePolls(app); err == nil {
            sendMsgToTalkingChannel(app, fmt.Sprintf("❌ Poll not found. Active polls:\n%s", activePolls), post.Id)
        } else {
            sendMsgToTalkingChannel(app, "❌ Poll not found", post.Id)
        }
        return
    }

    // Проверяем, активен ли опрос
    active, ok := tuple[5].(bool)
    if !ok || !active {
        sendMsgToTalkingChannel(app, "❌ This poll is closed", post.Id)
        return
    }

		rawOptions, ok := tuple[2].(map[interface{}]interface{})
		if !ok {
				app.logger.Error().Msgf("Unexpected format for options: %+v", tuple[2])
				return
		}

		options := make(map[string]int)

		for k, v := range rawOptions {
				key, keyOk := k.(string)
				
				var value int
				switch vTyped := v.(type) {
				case int8:
						value = int(vTyped)
				case int16:
						value = int(vTyped)
				case int32:
						value = int(vTyped)
				case int64:
						value = int(vTyped)
				case uint8:
						value = int(vTyped)
				case uint16:
						value = int(vTyped)
				case uint32:
						value = int(vTyped)
				case uint64:
						value = int(vTyped)
				default:
						app.logger.Error().Msgf("Unexpected type in options: %T", v)
						continue
				}

				if keyOk {
						options[key] = value
				} else {
						app.logger.Error().Msgf("Invalid key-value pair in options: %v -> %v", k, v)
				}
		}

		app.logger.Debug().Msgf("Raw options from Tarantool: %+v (type: %T)", rawOptions, rawOptions)
		for k, v := range rawOptions {
				app.logger.Debug().Msgf("Key: %v (type: %T), Value: %v (type: %T)", k, k, v, v)
		}

		app.logger.Debug().Interface("parsed_options", options).Msg("Parsed poll options")

    if len(options) == 0 {
        sendMsgToTalkingChannel(app, "❌ No valid options found in poll", post.Id)
        return
    }

    if _, exists := options[choice]; !exists {
        validOptions := make([]string, 0, len(options))
        for opt := range options {
            validOptions = append(validOptions, fmt.Sprintf("- %s", opt))
        }
        sendMsgToTalkingChannel(app, fmt.Sprintf("❌ Invalid option. Valid options:\n%s", strings.Join(validOptions, "\n")), post.Id)
        return
    }

    // Обновляем результат голосования
    // ops := tarantool.NewOperations().
    //     Add(2, []interface{}{choice, 1}).
    //     Assign(3, []interface{}{post.UserId, choice})
    //

		options[choice] += 1
		ops := tarantool.NewOperations().
				// Add(2, []interface{}{choice, options[choice] + 1}). // Увеличиваем голос
				Assign(2, options)  // Обновляем количество голосов для выбранного пункта

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

    sendMsgToTalkingChannel(app, fmt.Sprintf("✅ Vote for '%s' recorded!", choice), post.Id)
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

    // Обновляем поле 'active' на false, чтобы закрыть опрос
    ops := tarantool.NewOperations().
        Assign(5, false)  // Поле 5 - это 'active' (судя по схеме)

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

package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"strconv"
	"strings"

	"github.com/1egoman/slick/frontend"
	"github.com/1egoman/slick/gateway"
	"github.com/1egoman/slick/gateway/slack"
	"github.com/1egoman/slick/version"

	"github.com/atotto/clipboard"
	"github.com/skratchdot/open-golang/open"
)

type CommandType int

const (
	NATIVE CommandType = iota
	SLACK
)

type Command struct {
	Name         string
	Description  string
	Type         CommandType
	Permutations []string
	Arguments    string
	Handler      func(args []string, state *State) error
}

var COMMANDS = []Command{
	//
	// SPECIAL CASES
	// `Quit` needs access to the `quit` channel to close the app and `Require` needs access to a
	// reference to `term` to pass to `ParseScript`. Since these are "special cases", they don't
	// have handlers and are taken care of separately in `OnCommandExecuted` in keyboard_events.go.
	//
	{
		Name:         "Quit",
		Type:         NATIVE,
		Description:  "Quits slick.",
		Permutations: []string{"quit", "q"},
		/* NO HANDLER, SPECIAL CASE */
	},
	{
		Name:         "Require",
		Type:         NATIVE,
		Description:  "Run a lua file.",
		Arguments:    "<path to lua file>",
		Permutations: []string{"require", "req", "source"},
		/* NO HANDLER, SPECIAL CASE */
	},

	//
	// CONNECT TO A TEAM
	//
	{
		Name:         "Connect",
		Type:         NATIVE,
		Description:  "Connect to a given team",
		Arguments:    "[team name] <token>",
		Permutations: []string{"connect", "con"},
		Handler: func(args []string, state *State) error {
			var name string
			var token string
			if len(args) == 2 { // /connect token-here
				token = args[1]
			} else if len(args) == 3 { // /connect "team name" token-here
				token = args[2]
				name = args[1]
			} else {
				return errors.New("Please use more arguments. /connect [team name] <token-here>")
			}

			// Add the connection.
			var connection gateway.Connection
			if len(name) > 0 {
				connection = gatewaySlack.NewWithName(name, token)
				// If there's any saved data, add that to the connection
				ApplySaveToConnection(name, &connection)
			} else {
				connection = gatewaySlack.New(token)
			}

			// Store the connection
			state.Connections = append(state.Connections, connection)
			state.SetActiveConnection(len(state.Connections) - 1)

			// Initialize the connection
			err := connection.Connect()
			log.Printf("Connection response: %s", err)
			if err != nil {
				return errors.New(fmt.Sprintf("Error in connecting: %s", err))
			}

			// Force-refresh to pull in latest data from the gateway
			go func() {
				err := connection.Refresh(true)
				if err != nil {
					state.Status.Errorf("Error refreshing connection: %s", err)
				}
			}()

			return nil
		},
	},
	{
		Name:         "Disconnect",
		Type:         NATIVE,
		Description:  "Connect to a given team. If no index is specified, then use the active connection.",
		Arguments:    "[connection index]",
		Permutations: []string{"disconnect", "dis"},
		Handler: func(args []string, state *State) error {
			var index int
			if len(args) == 1 { // /disconnect
				index = state.ActiveConnectionIndex()
			} else if len(args) == 2 { // /disconnect index
				// Convert the index from strint to int
				i, err := strconv.ParseInt(args[1], 10, 0)
				if err != nil {
					return errors.New("Error disconnecting: " + err.Error())
				}
				index = int(i)
				if index < 0 && index > len(state.Connections) {
					return errors.New("No such connection at that index.")
				}
			} else {
				return errors.New("Please use more arguments. /disconnect [team index]")
			}

			log.Println("Closing connection with index", index)

			// Gracefully close the conenctino prior to removing
			state.Connections[index].Disconnect()

			// Remove connection from the pool
			state.Connections = append(state.Connections[:index], state.Connections[index+1:]...)
			state.SetActiveConnection(len(state.Connections) - 1)
			return nil
		},
	},
	{
		Name:         "Reconnect",
		Type:         NATIVE,
		Description:  "Given a team that has moved into a failure state, reconnect to the server.",
		Arguments:    "[connection index]",
		Permutations: []string{"reconnect", "recon"},
		Handler: func(args []string, state *State) error {
			var index int
			if len(args) == 1 { // /reconnect
				index = state.ActiveConnectionIndex()
			} else if len(args) == 2 { // /reconnect [index]
				// Convert the index from strint to int
				i, err := strconv.ParseInt(args[1], 10, 0)
				if err != nil {
					return errors.New("Error disconnecting: " + err.Error())
				}
				index = int(i)
				if index < 0 && index > len(state.Connections) {
					return errors.New("No such connection at that index.")
				}
			} else {
				return errors.New("Please use more arguments. /reconnect [connection index]")
			}

			// try to reconnect
			err := state.Connections[index].Connect()
			log.Printf("Reconnection response: %s", err)
			if err != nil {
				return errors.New(fmt.Sprintf("Error in reconnecting (connect): %s", err))
			}

			// then, refresh the connection
			err = state.Connections[index].Refresh(true)
			log.Printf("Refresh response: %s", err)
			if err != nil {
				state.Offline = false // Currently offline, since we couldn't refresh the channel
				return errors.New(fmt.Sprintf("Error in reconnecting (refresh): %s", err))
			} else {
				state.Offline = false
			}

			return nil
		},
	},
	{
		Name:         "Version",
		Type:         NATIVE,
		Description:  "Show the current version of slick",
		Arguments:    "",
		Permutations: []string{"version"},
		Handler: func(args []string, state *State) error {
			state.Status.Printf("Slick version %s", version.Version())
			return nil
		},
	},

	//
	// POSTS
	//
	{
		Name:         "Post",
		Type:         NATIVE,
		Description:  "Make a post in the current channel with the contents of the specified file.",
		Arguments:    "<post file> [post name]",
		Permutations: []string{"post", "p"},
		Handler: func(args []string, state *State) error {
			var postTitle string
			var postPath string
			if len(args) == 3 { // /post "post content" "post title"
				postPath = args[1]
				postTitle = args[2]
			} else if len(args) == 2 { // /post "post content"
				postPath = args[1]
			} else {
				return errors.New("Please use more arguments. /post path/to/post.txt [\"post title\"]")
			}

			// Read post from filesystem
			postContent, err := ioutil.ReadFile(postPath)
			if err != nil {
				return errors.New(fmt.Sprintf("Couldn't read file %s: %s", postPath, err.Error()))
			}

			if state.ActiveConnection() == nil {
				return errors.New("No active connection!")
			}

			// Make the post
			if err = state.ActiveConnection().PostBinary(postTitle, postPath, []byte(postContent)); err != nil {
				return err
			}

			return nil
		},
	},
	{
		Name:         "PostInline",
		Type:         NATIVE,
		Description:  "Make a post in the current channel using the given text.",
		Arguments:    "<post content> [post name]",
		Permutations: []string{"postinline", "pin"},
		Handler: func(args []string, state *State) error {
			var postTitle string
			var postContent string
			if len(args) == 3 { // /postinline "post content" "post title"
				postContent = args[1]
				postTitle = args[2]
			} else if len(args) == 2 { // /postinline "post content"
				postContent = args[1]
			} else {
				return errors.New("Please use more arguments. /postinline \"post content\" [\"post title\"]")
			}

			if state.ActiveConnection() == nil {
				return errors.New("No active connection!")
			}

			// Make the post
			if err := state.ActiveConnection().PostText(postTitle, postContent); err != nil {
				return err
			}

			return nil
		},
	},
	{
		Name:         "Upload",
		Type:         NATIVE,
		Description:  "Upload a file to a channel.",
		Arguments:    "<file path> [file name]",
		Permutations: []string{"upload", "up"},
		Handler: func(args []string, state *State) error {
			var title string
			var uploadPath string
			if len(args) == 2 { // /upload /path/to/file.xt
				uploadPath = args[1]
			} else if len(args) == 3 { // /upload /path/to/file.xt "file-title.png"
				uploadPath = args[1]
				title = args[2]
			} else {
				return errors.New("Please use more arguments. /upload path/to/file.png [file name]")
			}

			// Read post from filesystem
			uploadContent, err := ioutil.ReadFile(uploadPath)
			if err != nil {
				return errors.New(fmt.Sprintf("Couldn't read file %s: %s", uploadPath, err.Error()))
			}

			if state.ActiveConnection() == nil {
				return errors.New("No active connection!")
			}

			// Make the post
			if err = state.ActiveConnection().PostBinary(title, uploadPath, uploadContent); err != nil {
				return err
			}

			return nil
		},
	},

	//
	// SET CONFIGURATION OPTIONS
	//
	{
		Name:         "Set",
		Type:         NATIVE,
		Description:  "Sets a configuration option",
		Arguments:    "<option name> [option value]",
		Permutations: []string{"set"},
		Handler: func(args []string, state *State) error {
			if len(args) == 3 { // set foo bar
				state.Configuration[args[1]] = args[2]
				return nil
			} else if len(args) == 2 { // set foo
				delete(state.Configuration, args[1])
				return nil
			} else {
				return errors.New("Please use more arguments. /set foo bar")
			}
		},
	},

	//
	// MESSAGE ACTIONS
	//

	{
		Name:         "Reaction",
		Type:         NATIVE,
		Description:  "Post a reaction to the selected message",
		Arguments:    "<reaction name>",
		Permutations: []string{"reaction", "react", "r"},
		Handler: func(args []string, state *State) error {
			selectedMessageIndex := len(state.ActiveConnection().MessageHistory()) - 1 - state.SelectedMessageIndex
			selectedMessage := state.ActiveConnection().MessageHistory()[selectedMessageIndex]

			if len(args) == 2 {
				reaction := args[1]
				return state.ActiveConnection().ToggleMessageReaction(selectedMessage, reaction)
			} else {
				return errors.New("Please use more arguments. /react <reaction name>")
			}
		},
	},
	{
		Name:         "OpenFile",
		Type:         NATIVE,
		Description:  "If a file is attached to the current message, open it.",
		Arguments:    "",
		Permutations: []string{"openfile", "opf"},
		Handler: func(args []string, state *State) error {
			selectedMessageIndex := len(state.ActiveConnection().MessageHistory()) - 1 - state.SelectedMessageIndex
			selectedMessage := state.ActiveConnection().MessageHistory()[selectedMessageIndex]

			// Open the private image url in the browser
			if selectedMessage.File != nil {
				open.Run(selectedMessage.File.Permalink)
			} else {
				return errors.New("Selected message has no file")
			}

			return nil
		},
	},
	{
		Name:         "CopyFile",
		Type:         NATIVE,
		Description:  "If a file is attached to the current message, copy it into the system clipboard.",
		Arguments:    "",
		Permutations: []string{"copyfile", "cpf"},
		Handler: func(args []string, state *State) error {
			selectedMessageIndex := len(state.ActiveConnection().MessageHistory()) - 1 - state.SelectedMessageIndex
			selectedMessage := state.ActiveConnection().MessageHistory()[selectedMessageIndex]

			// Open the private image url in the browser
			if selectedMessage.File != nil {
				clipboard.WriteAll(selectedMessage.File.Permalink)
				state.Status.Printf("Copied %s to clipboard!", selectedMessage.File.Permalink)
			} else {
				return errors.New("Selected message has no file")
			}

			return nil
		},
	},
	{
		Name:         "OpenAttachmentLink",
		Type:         NATIVE,
		Description:  "If an attachment of the given index is on the given active, then open the link the attachment contains.",
		Arguments:    "<attachment index>",
		Permutations: []string{"attachmentlink", "atlink", "atlk"},
		Handler: func(args []string, state *State) error {
			var attachmentIndex int
			var err error
			if len(args) == 2 {
				attachmentIndex, err = strconv.Atoi(args[1])
				if err != nil {
					return err
				}
			} else {
				return errors.New("Please use more arguments. /attachmentlink <attachment index>")
			}

			selectedMessageIndex := len(state.ActiveConnection().MessageHistory()) - 1 - state.SelectedMessageIndex
			selectedMessage := state.ActiveConnection().MessageHistory()[selectedMessageIndex]

			if selectedMessage.Attachments == nil || len(*selectedMessage.Attachments) == 0 {
				return errors.New("Selected message has no attachments!")
			}

			// Open the private image url in the browser
			if (attachmentIndex - 1) >= len(*selectedMessage.Attachments) {
				return errors.New(fmt.Sprintf("Attachment index %d is too large!", attachmentIndex))
			} else if titleLink := (*selectedMessage.Attachments)[attachmentIndex-1].TitleLink; len(titleLink) > 0 {
				open.Run(titleLink)
			} else {
				return errors.New("Selected message and attachment doesn't have a link that can be opened.")
			}

			return nil
		},
	},
	{
		Name:         "OpenMessageLink",
		Type:         NATIVE,
		Description:  "Opens a link within a message.",
		Permutations: []string{"openmessagelink", "openmsglk", "olk"},
		Arguments:    "[link index]",
		Handler: func(args []string, state *State) error {
			var err error
			var linkIndex int

			if len(args) == 1 {
				linkIndex = 1
			} else if len(args) == 2 {
				linkIndex, err = strconv.Atoi(args[1])
				if err != nil {
					return err
				}
			} else {
				return errors.New("Please use more arguments. /openmessagelink <link index>")
			}

			if state.ActiveConnection() == nil {
				return errors.New("No active connection!")
			}

			log.Printf("Open link with index %d", linkIndex)

			selectedMessageIndex := len(state.ActiveConnection().MessageHistory()) - 1 - state.SelectedMessageIndex
			selectedMessage := state.ActiveConnection().MessageHistory()[selectedMessageIndex]

			var parsedMessage gateway.PrintableMessage
			err = frontend.ParseSlackMessage(selectedMessage.Text, &parsedMessage, state.ActiveConnection().UserById)
			if err != nil {
				return errors.New("Error making message print-worthy (probably because fetching user id => user name failed): " + err.Error())
			}

			// Find the link of the given index that we are looking for.
			linkCount := 0
			for _, part := range parsedMessage.Parts() {
				if part.Type == gateway.PRINTABLE_MESSAGE_LINK {
					linkCount += 1
					if linkCount == linkIndex {
						if href, ok := part.Metadata["Href"].(string); ok {
							open.Run(href)
						} else {
							return errors.New("Link href (in metadata) isn't a string!")
						}
					}
				}
			}

			return nil
		},
	},
	{
		Name:         "ExpandAttachment",
		Type:         NATIVE,
		Description:  "Expand an attachment into a modal to view the full content.",
		Arguments:    "<attachment index>",
		Permutations: []string{"expandattachment", "expattach", "exat"},
		Handler: func(args []string, state *State) error {
			var attachmentIndex int
			var err error
			if len(args) == 2 {
				attachmentIndex, err = strconv.Atoi(args[1])
				if err != nil {
					return err
				}
			} else {
				return errors.New("Please use more arguments. /expandattachment <attachment index>")
			}

			selectedMessageIndex := len(state.ActiveConnection().MessageHistory()) - 1 - state.SelectedMessageIndex
			selectedMessage := state.ActiveConnection().MessageHistory()[selectedMessageIndex]

			if selectedMessage.Attachments == nil || len(*selectedMessage.Attachments) == 0 {
				return errors.New("Selected message has no attachments!")
			}

			if (attachmentIndex - 1) >= len(*selectedMessage.Attachments) {
				return errors.New(fmt.Sprintf("Attachment index %d is too large!", attachmentIndex))
			} else if attachment := (*selectedMessage.Attachments)[attachmentIndex-1]; len(attachment.Body) > 0 {
				// Open a modal with the atatchment content.
				state.Mode = "modl"
				state.Modal.Reset()
				state.Modal.Title = attachment.Title
				state.Modal.Body = attachment.Body
			} else {
				return errors.New("Selected message and attachment doesn't have a link that can be opened.")
			}

			return nil
		},
	},
	{
		Name:         "ResendMessage",
		Type:         NATIVE,
		Description:  "Resend an unconfirmed message (ie, a message that only exists locally.)",
		Arguments:    "",
		Permutations: []string{"resendmessage", "resend"},
		Handler: func(args []string, state *State) error {
			selectedMessageIndex := len(state.ActiveConnection().MessageHistory()) - 1 - state.SelectedMessageIndex
			selectedMessage := state.ActiveConnection().MessageHistory()[selectedMessageIndex]

			if selectedMessage.Confirmed == true {
				return errors.New("Message is already confirmed (ie, has already been sent to the server.)")
			}

			// Can only resend messages that originally were sent by the logged in user.
			if self := state.ActiveConnection().Self(); selectedMessage.Sender != nil && self != nil && selectedMessage.Sender.Name != self.Name {
				return errors.New("Message was not originally sent by you.")
			}

			responseMessage, err := state.ActiveConnection().SendMessage(
				selectedMessage,
				state.ActiveConnection().SelectedChannel(),
			)

			if err != nil {
				state.Status.Errorf("Error sending message: %s", err)
			} else {
				// Mark message as confirmed
				selectedMessage.Confirmed = true

				// Got a response command? Append it to the message history.
				if responseMessage != nil {
					state.ActiveConnection().AppendMessageHistory(*responseMessage)
				}
			}

			return nil
		},
	},
	{
		Name:         "EditMessage",
		Type:         NATIVE,
		Description:  "Open a modal to let the user edit the given message.",
		Arguments:    "",
		Permutations: []string{"editmessage", "edit"},
		Handler: func(args []string, state *State) error {
			selectedMessageIndex := len(state.ActiveConnection().MessageHistory()) - 1 - state.SelectedMessageIndex
			selectedMessage := state.ActiveConnection().MessageHistory()[selectedMessageIndex]

			if selectedMessage.Confirmed == true && len(state.ActiveConnection().Self().Name) == 0 {
				// When offline, .ActiveConnection().Self().Name is an empty string.
				// TODO: Does this happen in other cases (ie, the user is online?)
				return errors.New("Offline, cannot edit messages that have already been sent.")
			} else if selectedMessage.Sender != nil && selectedMessage.Sender.Name == state.ActiveConnection().Self().Name {
				// Open a modal with the message content.
				state.Mode = "modl"
				state.Modal.Reset()
				state.Modal.Title = fmt.Sprintf("Edit message %d", selectedMessage.Timestamp)
				state.Modal.SetContent(selectedMessage.Text)
				state.Modal.Editable = true
				return nil
			} else {
				return errors.New("Given message was not created by you, and therefore cannot be edited.")
			}
		},
	},

	//
	// MOVE FORWARD / BACKWARD MESSAGES
	//
	{
		Name:         "MoveBackMessage",
		Type:         NATIVE,
		Description:  "Move selected message back to the previous message in time.",
		Arguments:    "",
		Permutations: []string{"movebackmessage"},
		Handler: func(args []string, state *State) error {
			if state.ActiveConnection() != nil && state.SelectedMessageIndex > 0 {
				state.SelectedMessageIndex -= 1

				// If the message history is less than a page, then don't move the bottom displayed
				// item.
				if state.BottomDisplayedItem == 0 && !state.RenderedAllMessages {
					return nil
				}

				if state.BottomDisplayedItem > 0 && state.SelectedMessageIndex < state.BottomDisplayedItem+messageScrollPadding {
					state.BottomDisplayedItem -= 1
				}
				log.Printf("Selecting message %d", state.SelectedMessageIndex)
				return nil
			} else {
				return errors.New("Can't move back a message, no such message!")
			}
		},
	},
	{
		Name:         "MoveForwardMessage",
		Type:         NATIVE,
		Description:  "Move selected message forward to the next message in time.",
		Arguments:    "",
		Permutations: []string{"moveforwardmessage"},
		Handler: func(args []string, state *State) error {
			if state.ActiveConnection() != nil && state.SelectedMessageIndex < len(state.ActiveConnection().MessageHistory())-1 {
				state.SelectedMessageIndex += 1

				// If the message history is less than a page, then don't move the bottom displayed
				// item.
				if state.BottomDisplayedItem == 0 && !state.RenderedAllMessages {
					return nil
				}

				if state.RenderedAllMessages && state.SelectedMessageIndex >= state.RenderedMessageNumber-messageScrollPadding {
					state.BottomDisplayedItem += 1
				}
				log.Printf("Selecting message %d, bottom index %d", state.SelectedMessageIndex, state.BottomDisplayedItem)
				return nil
			} else {
				return errors.New("Can't move forward a message, no such message!")
			}
		},
	},

	//
	// CHANGE THE ACTIVE CHANNEL
	//
	{
		Name:         "Pick",
		Type:         NATIVE,
		Description:  "Pick another connection / channel.",
		Arguments:    "<connection name> <channel name>",
		Permutations: []string{"pick", "p"},
		Handler: func(args []string, state *State) error {
			var connectionName string
			var channelName string
			if len(args) == 3 {
				connectionName = args[1]
				channelName = args[2]
			} else {
				return errors.New("Please specify more args. /pick <connection name> <channel name>")
			}

			setConnection := false
			for connectionIndex, connection := range state.Connections {
				if connection.Name() == connectionName {
					state.SetActiveConnection(connectionIndex)
					setConnection = true
					break
				}
			}
			if !setConnection {
				return errors.New("No such connection: " + connectionName)
			}

			setChannel := false
			for _, channel := range state.ActiveConnection().Channels() {
				if channel.Name == channelName {
					state.ActiveConnection().SetSelectedChannel(&channel)
					setChannel = true
					break
				}
			}
			if !setChannel {
				return errors.New("No such channel: " + channelName)
			}

			return nil
		},
	},
	{
		Name:         "Join",
		Type:         NATIVE,
		Description:  "Join a new channel.",
		Arguments:    "[connection name] [channel name]",
		Permutations: []string{"join"},
		Handler: func(args []string, state *State) error {
			var connectionName string
			var channelName string
			if len(args) == 3 { // /join "connection name" "channel name"
				connectionName = args[1]
				channelName = args[2]
			} else if len(args) == 2 { // /join "channel name"
				connectionName = state.ActiveConnection().Name()
				channelName = args[1]
			} else {
				connectionName = state.ActiveConnection().Name()
				if channel := state.ActiveConnection().SelectedChannel(); channel != nil {
					channelName = channel.Name
				} else {
					return errors.New("Didn't pass a channel and no channel is selected. Explicitly pass a channel?")
				}
			}

			// Get connection reference from name.
			var connection *gateway.Connection
			for _, conn := range state.Connections {
				if conn.Name() == connectionName {
					connection = &conn
					break
				}
			}
			if connection == nil {
				return errors.New("No such connection: " + connectionName)
			}

			// Get channel reference from name inside the connection.
			var channel *gateway.Channel
			for _, ch := range state.ActiveConnection().Channels() {
				if ch.Name == channelName {
					channel = &ch
					break
				}
			}
			if channel == nil {
				return errors.New("No such channel: " + channelName)
			}

			// Dereference gaurded above when looking for a connection.
			_, err := (*connection).JoinChannel(channel)
			return err
		},
	},
	{
		Name:         "Leave",
		Type:         NATIVE,
		Description:  "Leave a channel.",
		Arguments:    "[connection name] [channel name]",
		Permutations: []string{"leave"},
		Handler: func(args []string, state *State) error {
			var connectionName string
			var channelName string
			if len(args) == 3 { // /leave "connection name" "channel name"
				connectionName = args[1]
				channelName = args[2]
			} else if len(args) == 2 { // /leave "channel name"
				connectionName = state.ActiveConnection().Name()
				channelName = args[1]
			} else {
				connectionName = state.ActiveConnection().Name()
				if channel := state.ActiveConnection().SelectedChannel(); channel != nil {
					channelName = channel.Name
				} else {
					return errors.New("Didn't pass a channel and no channel is selected. Explicitly pass a channel?")
				}
			}

			// Get connection reference from name.
			var connection *gateway.Connection
			for _, conn := range state.Connections {
				if conn.Name() == connectionName {
					connection = &conn
					break
				}
			}
			if connection == nil {
				return errors.New("No such connection: " + connectionName)
			}

			// Get channel reference from name inside the connection.
			var channel *gateway.Channel
			for _, ch := range state.ActiveConnection().Channels() {
				if ch.Name == channelName {
					channel = &ch
					break
				}
			}
			if channel == nil {
				return errors.New("No such channel: " + channelName)
			}

			// Dereference gaurded above when looking for a connection.
			_, err := (*connection).LeaveChannel(channel)
			return err
		},
	},

	//
	// OPEN IN SLACK
	//
	{
		Name:         "OpenInSlack",
		Type:         NATIVE,
		Description:  "Open the current channel in the slack app.",
		Arguments:    "",
		Permutations: []string{"slack", "ops"},
		Handler: func(args []string, state *State) error {
			team := state.ActiveConnection().Team()
			channel := state.ActiveConnection().SelectedChannel()
			if team == nil || channel == nil {
				return errors.New("Selected team or channel was nil.")
			}
			open.Run("slack://channel?team=" + team.Id + "&id=" + channel.Id)
			return nil
		},
	},

	//
	// BUILT INTO SLACK
	//
	{
		Type:         SLACK,
		Name:         "Apps",
		Permutations: []string{"apps"},
		Arguments:    "[search term]",
		Description:  "Search for Slack Apps in the App Directory",
	},
	{
		Type:         SLACK,
		Name:         "Away",
		Permutations: []string{"away"},
		Arguments:    "Toggle your away status",
	},
	{
		Type:         SLACK,
		Name:         "Dnd",
		Permutations: []string{"dnd"},
		Arguments:    "[some description of time]",
		Description:  "Starts or ends a Do Not Disturb session",
	},
	{
		Type:         SLACK,
		Name:         "Feed",
		Permutations: []string{"feed"},
		Arguments:    "help [or subscribe, list, remove...]",
		Description:  "Manage RSS subscriptions",
	},
	{
		Type:         SLACK,
		Name:         "Invite",
		Permutations: []string{"invite"},
		Arguments:    "@user [channel]",
		Description:  "Invite another member to a channel",
	},
	{
		Type:         SLACK,
		Name:         "Invite people",
		Permutations: []string{"invite_people"},
		Arguments:    "[name@example.com, ...]",
		Description:  "Invite people to your Slack team",
	},
	{
		Type:         SLACK,
		Name:         "Me",
		Permutations: []string{"me"},
		Arguments:    "your message",
		Description:  "Displays action text",
	},
	{
		Type:         SLACK,
		Name:         "Msg",
		Permutations: []string{"msg", "dm"},
		Arguments:    "[your message]",
	},
	{
		Type:         SLACK,
		Name:         "Mute",
		Permutations: []string{"mute"},
		Arguments:    "[channel]",
		Description:  "Mutes [channel] or the current channel",
	},
	{
		Type:         SLACK,
		Name:         "Remind",
		Permutations: []string{"remind"},
		Arguments:    "[@someone or #channel] [what] [when]",
		Description:  "Set a reminder",
	},
	{
		Type:         SLACK,
		Name:         "Rename",
		Permutations: []string{"rename"},
		Arguments:    "[new name]",
		Description:  "Rename a channel",
	},
	{
		Type:         SLACK,
		Name:         "Shrug",
		Permutations: []string{"shrug"},
		Arguments:    "[your message]",
		Description:  "Appends ¯\\_(ツ)_/¯ to your message",
	},
	{
		Type:         SLACK,
		Name:         "Status",
		Permutations: []string{"status"},
		Arguments:    "[clear] or [:your_new_status_emoji:] [your new status message]",
		Description:  "Set or clear your custom status",
	},
	{
		Type:         SLACK,
		Name:         "Who",
		Permutations: []string{"who"},
		Description:  "List users in the current channel or group",
	},
}

func GetCommand(name string) *Command {
	for _, command := range COMMANDS {
		if command.Name == name {
			return &command
		}
	}
	return nil
}

// Given a command, a list of arguments to pass to the command, and the state, run the command.
func RunCommand(command Command, args []string, state *State) error {
	if command.Type == NATIVE && command.Handler != nil {
		return command.Handler(args, state)
	} else if command.Type == NATIVE && command.Handler == nil {
		return errors.New(fmt.Sprintf("The command %s doesn't have an associated handler function.", command.Name))
	} else {
		message := gateway.Message{
			Sender: state.ActiveConnection().Self(),
			Text:   strings.Join(args, " "),
		}

		// Sometimes, a message could have a response. This is for example true in the
		// case of slash commands, sometimes.
		responseMessage, err := state.ActiveConnection().SendMessage(
			message,
			state.ActiveConnection().SelectedChannel(),
		)

		if err != nil {
			return err
		} else if responseMessage != nil {
			// Got a response command? Append it to the message history.
			state.ActiveConnection().AppendMessageHistory(*responseMessage)
		}
		return nil
	}
}

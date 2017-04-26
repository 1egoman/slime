package gateway

import (
	"log"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/url"

	"strings"

	"golang.org/x/net/websocket"
)

func Slack(token string) *SlackConnection {
	return &SlackConnection{
		token: token,
	}
}

// SlackConnection meets the connection interface.
type SlackConnection struct {
	url   string
	token string
	conn  *websocket.Conn

	// Create two message channels, one for incoming messages and one for outgoing messages.
	incoming chan Event
	outgoing chan Event

	self User
	team Team

	// Internal state to store all channels and a pointer to the active one.
	channels        []Channel
	selectedChannel *Channel

	// Internal state to store message history of the active channel
	messageHistory []Message
}

func (c *SlackConnection) requestConnectionUrl() {
	// Make request to slack's api to get websocket credentials
	// https://api.slack.com/methods/rtm.connect
	resp, err := http.Get("https://slack.com/api/rtm.connect?token=" + c.token)

	if err != nil {
		log.Fatal(err)
	}

	// Decode json body.
	body, _ := ioutil.ReadAll(resp.Body)
	var connectionBuffer struct {
		Ok   bool   `json:"ok"`
		Url  string `json:"url"`
		Team Team   `json:"team"`
		Self User   `json:"self"`
	}
	err = json.Unmarshal(body, &connectionBuffer)
	if err != nil {
		log.Fatal("Slack response: "+string(body))
	}

	// Add response data to struct
	c.url = connectionBuffer.Url
	c.self = connectionBuffer.Self
	c.team = connectionBuffer.Team
}

// Return the name of the team.
func (c *SlackConnection) Name() string {
	if c.Team() != nil && len(c.Team().Name) != 0 {
		return c.Team().Name
	} else {
		return "(slack loading...)"
	}
}

// Connect to the slack persistent socket.
func (c *SlackConnection) Connect() error {
	log.Println("Requesting slack team connection url...")
	// Create buffered channels to listen and send messages on
	c.incoming = make(chan Event, 1)
	c.outgoing = make(chan Event, 1)

	// Request a connection url with the token in the struct
	c.requestConnectionUrl()
	log.Printf("Got slack connection url for team %s: %s", c.Team().Name, c.url)

	// FIXME: what does this mean?
	origin := "http://localhost/"

	// Create a connection to the websocket
	var err error
	c.conn, err = websocket.Dial(c.url, "", origin)
	if err != nil {
		return err
	}
	log.Printf("Slack connection %s made!", c.Team().Name)

	// When messages are received, add them to the incoming buffer.
	go func(incoming chan Event) {
		var msgRaw = make([]byte, 512)
		var msg map[string]interface{}
		var n int

		for {
			// Listen for messages, and when some are received, write them to a channel.
			if n, err = c.conn.Read(msgRaw); err != nil {
				log.Fatal(err)
			}

			// Decode into a struct so that we can check message type later
			json.Unmarshal(msgRaw[:n], &msg)
			log.Printf("INCOMING %s: %s", c.Team().Name, msgRaw[:n])
			incoming <- Event{
				Direction: "incoming",
				Type:      msg["type"].(string),
				Data:      msg,
			}
		}
	}(c.incoming)

	// When messages are in the outgoing buffer waiting to be sent, send them.
	go func(outgoing chan Event) {
		// Add a sequential message id to each message sent, so replies can later be tracked.
		messageId := 0

		var event Event
		for {
			// Assemble the message to send.
			event = <-outgoing
			event.Data["type"] = event.Type
			messageId++
			event.Data["id"] = messageId

			// Marshal to json
			data, err := json.Marshal(event.Data)
			if err != nil {
				log.Fatal(err)
			}
			log.Printf("OUTGOING %s: %s", c.Team().Name, data)

			// Send it.
			if _, err = c.conn.Write(data); err != nil {
				log.Fatal(err)
			}
		}
	}(c.outgoing)

	return nil
}

// Called when the connection becomes active
func (c *SlackConnection) Refresh() error {
	var err error

	// Fetch details about all channels
	c.channels, err = c.FetchChannels()
	if err != nil {
		return err
	}

	// Fetch details about the currently logged in user
	var user *User
	user, err = c.UserById(c.Self().Id)
	if err != nil {
		return err
	} else {
		c.self = *user
	}

	// Select the first channel, by default
	if len(c.channels) > 0 {
		c.selectedChannel = &c.channels[0]

		// Fetch Message history, if the emssage history is empty.
		if len(c.messageHistory) == 0 {
			log.Printf(
				"Fetching message history for team %s and channel %s",
				c.Team().Name,
				c.SelectedChannel().Name,
			)
			c.messageHistory, err = c.FetchChannelMessages(*c.selectedChannel)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// Fetch all channels for the given team
func (c *SlackConnection) FetchChannels() ([]Channel, error) {
	log.Printf("Fetching list of channels for team %s", c.Team().Name)
	resp, err := http.Get("https://slack.com/api/channels.list?token=" + c.token)
	if err != nil {
		return nil, err
	}

	body, _ := ioutil.ReadAll(resp.Body)
	var slackChannelBuffer struct {
		Channels []struct {
			Id        string `json:"id"`
			Name      string `json:"name"`
			CreatorId string `json:"creator"`
			Created   int    `json:"created"`
		} `json:"channels"`
	}
	json.Unmarshal(body, &slackChannelBuffer)

	// Convert to more generic message format
	var channelBuffer []Channel
	var creator *User
	for _, channel := range slackChannelBuffer.Channels {
		creator, err = c.UserById(channel.CreatorId)
		if err != nil {
			return nil, err
		}
		channelBuffer = append(channelBuffer, Channel{
			Id:      channel.Id,
			Name:    channel.Name,
			Creator: creator,
			Created: channel.Created,
		})
	}

	return channelBuffer, nil
}

// Given a channel, return all messages within that channel.
func (c *SlackConnection) FetchChannelMessages(channel Channel) ([]Message, error) {
	log.Printf("Fetching channel messages for team %s", c.Team().Name)
	resp, err := http.Get("https://slack.com/api/channels.history?token=" + c.token + "&channel=" + channel.Id + "&count=100")
	if err != nil {
		return nil, err
	}

	// Parse slack messages
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var slackMessageBuffer struct {
		Messages []struct {
			Ts        string     `json:"ts"`
			UserId    string     `json:"user"`
			Text      string     `json:"text"`
			/* Reactions struct{ */
			/* 	Name string `json:"name"` */
				Users []string 
			/* } `json:"reactions"` */
		} `json:"messages"`
		hasMore bool `json:"has_more"`
	}
	if err = json.Unmarshal(body, &slackMessageBuffer); err != nil {
		log.Fatal(err)
		return nil, err
	}

	// Convert to more generic message format
	var messageBuffer []Message
	var sender *User
	cachedUsers := make(map[string]*User)
	for i := len(slackMessageBuffer.Messages) - 1; i >= 0; i-- { // loop backwards to reverse the final slice
		msg := slackMessageBuffer.Messages[i]

		// Get the sender of the message
		// Since we're likely to have a lot of the same users, cache them.
		if cachedUsers[msg.UserId] != nil {
			sender = cachedUsers[msg.UserId]
		} else {
			sender, err = c.UserById(msg.UserId)
			cachedUsers[msg.UserId] = sender
			if err != nil {
				return nil, err
			}
		}

		messageBuffer = append(messageBuffer, Message{
			Sender:    sender,
			Text:      msg.Text,
			Reactions: []Reaction{},
			Hash:      msg.Ts,
		})
	}

	return messageBuffer, nil
}

func (c *SlackConnection) UserById(id string) (*User, error) {
	resp, err := http.Get("https://slack.com/api/users.info?token=" + c.token + "&user=" + id)
	if err != nil {
		return nil, err
	}

	// Parse slack user buffer
	body, _ := ioutil.ReadAll(resp.Body)
	var slackUserBuffer struct {
		User struct {
			Id      string `json:"id"`
			Name    string `json:"name"`
			Color   string `json:"color"`
			Profile struct {
				Status   string `json:"color"`
				RealName string `json:"real_name"`
				Email    string `json:"email"`
				Phone    string `json:"phone"`
				Skype    string `json:"skype"`
				Image    string `json:"image_24"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err = json.Unmarshal(body, &slackUserBuffer); err != nil {
		return nil, err
	}

	// Convert to a generic User
	return &User{
		Id:       slackUserBuffer.User.Id,
		Name:     slackUserBuffer.User.Name,
		Color:    slackUserBuffer.User.Color,
		Avatar:   slackUserBuffer.User.Profile.Image,
		Status:   slackUserBuffer.User.Profile.Status,
		RealName: slackUserBuffer.User.Profile.RealName,
		Email:    slackUserBuffer.User.Profile.Email,
		Skype:    slackUserBuffer.User.Profile.Skype,
		Phone:    slackUserBuffer.User.Profile.Phone,
	}, nil
}

func (c *SlackConnection) MessageHistory() []Message {
	return c.messageHistory;
}
func (c *SlackConnection) AppendMessageHistory(message Message) {
	c.messageHistory = append(c.messageHistory, message)
}
func (c *SlackConnection) ClearMessageHistory() {
	c.messageHistory = []Message{}
}

// Send a given message to a given channel. Also, is able to process slash commands.
// Returns an optional pointer to a response message and an error.
func (c *SlackConnection) SendMessage(message Message, channel *Channel) (*Message, error) {
	if strings.HasPrefix(message.Text, "/") {
		log.Printf("Sending slash command to team %s on channel %s", c.Team().Name)
		// If the message starts with a slash, it's a slash command.
		command := strings.Split(message.Text, " ")
		text := url.QueryEscape(strings.Join(command[1:], " "))
		resp, err := http.Get("https://slack.com/api/chat.command?token=" + c.token + "&channel=" + channel.Id + "&command=" + url.QueryEscape(command[0]) + "&text=" + text)
		if err != nil {
			return nil, err
		}

		body, _ := ioutil.ReadAll(resp.Body)
		var commandResponse struct {
			Response string `json:"response"`
		}
		err = json.Unmarshal(body, &commandResponse)
		if err != nil {
			return nil, err
		}

		// Return a response message if the response 
		if len(commandResponse.Response) > 0 {
			return &Message{
				Text: commandResponse.Response,
				Sender: &User{Name: "slackbot"},
			}, nil
		} else {
			return nil, nil
		}
	} else {
		log.Printf("Sending message to team %s on channel %s", c.Team().Name, channel.Name)

		// Otherwise just a plain message
		_, err := http.Get("https://slack.com/api/chat.postMessage?token=" + c.token + "&channel=" + channel.Id + "&text=" + url.QueryEscape(message.Text) + "&link_names=true&parse=full&unfurl_links=true&as_user=true")
		return nil, err
	}
}

func (c *SlackConnection) SelectedChannel() *Channel {
	return c.selectedChannel
}

func (c *SlackConnection) Incoming() chan Event {
	return c.incoming
}
func (c *SlackConnection) Outgoing() chan Event {
	return c.outgoing
}
func (c *SlackConnection) Team() *Team {
	return &c.team
}
func (c *SlackConnection) Channels() []Channel {
	return c.channels
}
func (c *SlackConnection) Self() *User {
	return &c.self
}

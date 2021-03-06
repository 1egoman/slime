package gateway

// A Connection is used to represent a message source.
type Connection interface {
	// Each connection has a name.
	Name() string
	Status() ConnectionStatus

	Connect() error
	Disconnect() error

	// Called to "refetch" any persistent resources, such as channels.
	Refresh(bool) error

	// Get incoming and outgoing message buffers
	Incoming() chan Event
	Outgoing() chan Event

	MessageHistory() []Message
	SetMessageHistory([]Message)
	AppendMessageHistory(message Message)
	PrependMessageHistory(message Message)
	DeleteMessageHistory(index int)
	ClearMessageHistory()
	SendMessage(Message, *Channel) (*Message, error)
	ParseMessage(map[string]interface{}, map[string]*User) (*Message, error)
	ToggleMessageReaction(Message, string) error

	// Fetch a slice of all channels that are available on this connection
	Channels() []Channel
	SetChannels([]Channel)
	FetchChannels() ([]Channel, error)
	SelectedChannel() *Channel
	SetSelectedChannel(*Channel)
	JoinChannel(*Channel) (*Channel, error)
	LeaveChannel(*Channel) (*Channel, error)

	// Fetch the team associated with this connection.
	Team() *Team
	SetTeam(Team)

	// Fetch user that is authenticated
	Self() *User
	SetSelf(User)

	// Given a channel, fetch the message history for that channel. Optionally, provide a timestamp
	// to fetch all messages after.
	FetchChannelMessages(Channel, *string) ([]Message, error)

	UserById(string) (*User, error)

	UserOnline(user *User) bool
	SetUserOnline(user *User, status bool)

	// Post a large block of text in a given channel
	PostText(title string, body string) error

	// Upload a file into a given channel
	PostBinary(title string, filename string, content []byte) error

	// Manage which users are typing.
	TypingUsers() *TypingUsers
}

// Events are emitted when data comes in from a connection
// and sent when data is to be sent to a connection.
// ie, when another user sends a message, an event would come in:
// Event{
//     Direction: "incoming",
//     Type: "message",
//     Data: map[string]interface{
//       "text": "Hello World!",
//       ...
//     },
// }
type Event struct {
	Direction string // "incoming" or "outgoing"

	Type string `json:"type"`
	Data map[string]interface{}

	// Properties that an event may be associated with.
	Channel Channel
	User    User
}

// A user is a human or bot that sends messages on within channel.
type User struct {
	Id       string `json:"id"`
	Name     string `json:"name"`
	Color    string `json:"color"`
	Avatar   string `json:"avatar"`
	Status   string `json:"status"`
	RealName string `json:"real_name"`
	Email    string `json:"email"`
	Skype    string `json:"skype"`
	Phone    string `json:"phone"`
}

// A Team is a collection of channels.
type Team struct {
	Id     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
}

type ChannelType int

const (
	TYPE_CHANNEL              ChannelType = iota // A channel is a open conversation between a large number of people
	TYPE_DIRECT_MESSAGE                          // A DM is a message connection between two people
	TYPE_GROUP_DIRECT_MESSAGE                    // A Group DM is a DM between greater than two people
)

// A Channel is a independent stream of messages sent by users.
type Channel struct {
	Id         string      `json:"id"`
	Name       string      `json:"name"`
	Creator    *User       `json:"creator"`
	Created    int         `json:"created"`
	IsMember   bool        `json:"is_member"`
	IsArchived bool        `json:"is_archived"`
	SubType    ChannelType `json:"subtype"`
}

// A Reaction is an optional subcollection of a message.
type Reaction struct {
	Name  string  `json:"name"`
	Users []*User `json:"users"`
}

// A File is an optional key on a message.
type File struct {
	Id         string `json:"id"`
	Name       string `json:"name"`
	Filetype   string `json:"type"`
	User       *User  `json:"user"`
	PrivateUrl string `json:"url_private"`
	Permalink  string `json:"permalink"`
}

// A Message is a blob of text or media sent by a User within a Channel.
type Message struct {
	Sender      *User         `json:"sender"`
	Text        string        `json:"text"`
	Reactions   []Reaction    `json:"reactions"`
	Hash        string        `json:"hash"`
	Timestamp   int           `json:"timestamp"` // This value is in seconds!
	File        *File         `json:"file,omitempty"`
	Attachments *[]Attachment `json:"attachments,omitempty"`
	// Has a message been confirmed as existing from the server, or is it preemptive?
	Confirmed   bool          `json:"confirmed"`
	// Cache message tokens on the message.
	Tokens      *[][]PrintableMessagePart `json:"tokens"`
}

type Attachment struct {
	Title     string
	TitleLink string
	Body string
	Color     string
	Fields    []AttachmentField
}

type AttachmentField struct {
	Title string
	Value string
	Short bool
}

type ConnectionStatus int

const (
	DISCONNECTED ConnectionStatus = iota
	CONNECTING
	CONNECTED
	FAILED // When the gateway errors, then move to this state.
)

package craken

type commandBindingPositionals string
type commandBindingSource string
type commandBindingValueType string
type commandOutputMode string
type commandTransport string

const (
	commandBindingPositionalsJoin commandBindingPositionals = "join"
)

const (
	commandBindingSourceBearerToken commandBindingSource = "bearer-token"
	commandBindingSourceFlag        commandBindingSource = "flag"
	commandBindingSourceLiteral     commandBindingSource = "literal"
	commandBindingSourceOption      commandBindingSource = "option"
	commandBindingSourceResolved    commandBindingSource = "resolved"
	commandBindingSourceText        commandBindingSource = "text"
)

const (
	commandBindingValueTypeInteger commandBindingValueType = "integer"
	commandBindingValueTypeJSON    commandBindingValueType = "json"
)

const (
	commandOutputModeBytes commandOutputMode = "bytes"
	commandOutputModeJSON  commandOutputMode = "json"
	commandOutputModeTable commandOutputMode = "table"
)

const (
	commandTransportDownload  commandTransport = "download"
	commandTransportHTTP      commandTransport = "http"
	commandTransportMultipart commandTransport = "multipart"
	commandTransportWebSocket commandTransport = "websocket"
)

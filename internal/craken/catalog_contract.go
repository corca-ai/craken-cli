package craken

type commandBindingPositionals string
type commandBindingResolver string
type commandBindingSource string
type commandBindingValueType string
type commandExecutionOutput string
type commandTransport string

const (
	commandBindingPositionalsJoin commandBindingPositionals = "join"
)

const (
	commandBindingResolverAgent       commandBindingResolver = "agent"
	commandBindingResolverChannel     commandBindingResolver = "channel"
	commandBindingResolverParticipant commandBindingResolver = "participant"
	commandBindingResolverWorkspace   commandBindingResolver = "workspace"
)

const (
	commandBindingSourceFlag    commandBindingSource = "flag"
	commandBindingSourceLiteral commandBindingSource = "literal"
	commandBindingSourceOption  commandBindingSource = "option"
	commandBindingSourceText    commandBindingSource = "text"
)

const (
	commandBindingValueTypeInteger commandBindingValueType = "integer"
	commandBindingValueTypeJSON    commandBindingValueType = "json"
)

const (
	commandExecutionOutputAgentJobWatch commandExecutionOutput = "agent-job-watch"
	commandExecutionOutputBytes         commandExecutionOutput = "bytes"
	commandExecutionOutputJSON          commandExecutionOutput = "json"
	commandExecutionOutputMessages      commandExecutionOutput = "messages"
	commandExecutionOutputWikiRecent    commandExecutionOutput = "wiki-recent"
	commandExecutionOutputWikiVersion   commandExecutionOutput = "wiki-version"
	commandExecutionOutputWikiVersions  commandExecutionOutput = "wiki-versions"
)

const (
	commandTransportDownload  commandTransport = "download"
	commandTransportHTTP      commandTransport = "http"
	commandTransportMultipart commandTransport = "multipart"
	commandTransportWebSocket commandTransport = "websocket"
)

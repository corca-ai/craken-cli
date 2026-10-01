package craken

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

var errStreamComplete = errors.New("stream output complete")
var errStreamInactive = errors.New("stream inactive")

// Bad data, failed output, and failed checkpoint writes cannot be recovered by
// opening another transport; retrying those could duplicate output endlessly.
type streamDataError struct{ err error }

func (e *streamDataError) Error() string { return e.err.Error() }
func (e *streamDataError) Unwrap() error { return e.err }

// stream transport receives its writer explicitly; no global output state.
func runStreamConnections(ctx context.Context, endpoint *url.URL, dialer *websocket.Dialer, output *streamOutput, idleMS int, reconnect bool, cmd command, stdout, stderr io.Writer) error {
	retries, err := numberOption(cmd, "max-retries", 5)
	if err != nil {
		return err
	}
	delayMS, err := numberOption(cmd, "retry-delay-ms", 250)
	if err != nil {
		return err
	}
	if retries > 100 || delayMS < 1 || delayMS > 5000 {
		return fmt.Errorf("--max-retries must be 0..100 and --retry-delay-ms must be 1..5000")
	}
	if output.wait > 0 {
		output.waitUntil = time.Now().Add(output.wait)
	}
	attempts := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if output.wait > 0 && !time.Now().Before(output.waitUntil) {
			return streamInactivity(stderr, output)
		}
		if output.plan != nil && (reconnect || output.save != nil) {
			query := endpoint.Query()
			query.Set(output.plan.ResumeQuery, fmt.Sprint(output.cursor))
			endpoint.RawQuery = query.Encode()
		}
		beforeCursor, beforeSeen := output.cursor, output.seen
		response, readErr := dialAndReadStream(ctx, endpoint, dialer, output, idleMS, stdout)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(readErr, errStreamComplete) {
			return nil
		}
		if errors.Is(readErr, errStreamInactive) || (output.wait > 0 && streamTimedOut(readErr)) {
			return streamInactivity(stderr, output)
		}
		if !reconnect && websocket.IsCloseError(readErr, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
			return nil
		}
		if !reconnect || !retryableStreamError(response, readErr) {
			return fmt.Errorf("websocket subscription failed: %w", readErr)
		}
		if output.cursor > beforeCursor || output.seen > beforeSeen {
			attempts = 0
		}
		if attempts >= retries {
			return fmt.Errorf("websocket subscription exhausted %d retries: %w", retries, readErr)
		}
		attempts++
		delay := streamRetryDelay(time.Duration(delayMS)*time.Millisecond, attempts)
		if _, err = fmt.Fprintf(stderr, "stream disconnected: %v; retry %d/%d in %s (cursor %d)\n", readErr, attempts, retries, delay, output.cursor); err != nil {
			return err
		}
		if output.wait > 0 {
			delay = min(delay, time.Until(output.waitUntil))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func dialAndReadStream(ctx context.Context, endpoint *url.URL, dialer *websocket.Dialer, output *streamOutput, idleMS int, stdout io.Writer) (*http.Response, error) {
	dialContext := ctx
	var cancel context.CancelFunc
	if output.wait > 0 {
		dialContext, cancel = context.WithDeadline(ctx, output.waitUntil)
		defer cancel()
	}
	connection, response, err := dialer.DialContext(dialContext, endpoint.String(), http.Header{})
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		if response != nil {
			return response, fmt.Errorf("server returned HTTP %d: %w", response.StatusCode, err)
		}
		return response, err
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	return response, readStream(connection, output, idleMS, stdout)
}

func retryableStreamError(response *http.Response, err error) bool {
	var dataError *streamDataError
	if errors.As(err, &dataError) {
		return false
	}
	if response != nil && response.StatusCode != http.StatusSwitchingProtocols {
		return response.StatusCode >= 500 || response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests
	}
	var closed *websocket.CloseError
	if errors.As(err, &closed) {
		return closed.Code != websocket.ClosePolicyViolation && closed.Code != websocket.CloseProtocolError && closed.Code != websocket.CloseUnsupportedData && closed.Code != websocket.CloseInvalidFramePayloadData
	}
	return err != nil
}

func streamRetryDelay(base time.Duration, attempt int) time.Duration {
	delay := min(base*time.Duration(1<<min(attempt-1, 5)), 5*time.Second)
	return min(delay+time.Duration(rand.Int64N(int64(delay/4)+1)), 5*time.Second)
}

func streamTimedOut(err error) bool {
	var networkError net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout())
}

func streamInactivity(stderr io.Writer, output *streamOutput) error {
	kind := "transport idle timeout"
	if output.wait > 0 && !time.Now().Before(output.waitUntil) {
		kind = "matching-message wait timeout"
	}
	_, err := fmt.Fprintln(stderr, kind+"; stream complete without further records")
	return err
}

package mcpserver

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// clientGoneMidRequest serves with serve, connects a client, and then
// disconnects it the way an exiting client process does: nothing reads the
// server's output any more, and its last request is still pending when stdin
// closes. ok is false when the server shut down before that request arrived,
// which happens now and then; the caller retries.
func clientGoneMidRequest(t *testing.T, serve func(io.ReadCloser, io.WriteCloser) error) (ok bool, err error) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serve(inR, outW) }()
	if _, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(context.Background(), &mcp.IOTransport{Reader: outR, Writer: inW}, nil); err != nil {
		t.Fatal(err)
	}
	outR.CloseWithError(io.ErrClosedPipe)
	_, werr := inW.Write([]byte(`{"jsonrpc":"2.0","id":99,"method":"tools/list"}` + "\n"))
	inW.Close()
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop after the client disconnected")
	}
	return werr == nil, err
}

// A client that exits mid-request makes the go-sdk end Run cleanly (newer
// versions) or with its "server is closing" error or the closed pipe it was
// writing a reply to (older ones); audd mcp treats all of these as a normal
// disconnect.
func TestServeClientGoneMidRequest(t *testing.T) {
	opts := Options{Run: func(context.Context, []string) Result { return Result{} }}
	run := func(in io.ReadCloser, out io.WriteCloser) error {
		return New(opts).Run(context.Background(), &mcp.IOTransport{Reader: in, Writer: out})
	}
	serve := func(in io.ReadCloser, out io.WriteCloser) error {
		return Serve(context.Background(), opts, in, out)
	}
	for _, c := range []struct {
		name  string
		serve func(io.ReadCloser, io.WriteCloser) error
		check func(error) bool
	}{
		// go-sdk v1.4 returned a disconnect error here; newer versions return nil.
		{"go-sdk Run", run, func(err error) bool { return err == nil || isDisconnect(err) }},
		{"Serve", serve, func(err error) bool { return err == nil }},
	} {
		tested := false
		for i := 0; i < 20 && !tested; i++ {
			ok, err := clientGoneMidRequest(t, c.serve)
			if !ok {
				continue
			}
			tested = true
			if !c.check(err) {
				t.Errorf("%s returned %T %v after the client disconnected", c.name, err, err)
			}
		}
		if !tested {
			t.Errorf("%s: could not reproduce a disconnect with a request in flight", c.name)
		}
	}
}

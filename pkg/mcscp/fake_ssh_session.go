package mcscp

import (
	"context"
	"io"
	"net"

	"github.com/gliderlabs/ssh"
	"github.com/materials-commons/gomcdb/mcmodel"
)

// Implement a fake ssh.Session interface for testing purposes. For the mcscp.Handler implementation
// all that we need implemented is the Context() function with a "mcuser" key that contains a valid
// *mcmodel.User entry. All other methods can be stubbed out. Comments for each of the methods
// were carried over from the ssh.Session interface definition.
type fakeSSHSession struct {
	c context.Context
}

func newFakeSshSession() fakeSSHSession {
	u := &mcmodel.User{Slug: "testslug", ID: 1}
	return fakeSSHSession{c: context.WithValue(context.Background(), "mcuser", u)}
}

// User returns the username used when establishing the SSH connection.
func (s fakeSSHSession) User() string {
	return ""
}

// RemoteAddr returns the net.Addr of the client side of the connection.
func (s fakeSSHSession) RemoteAddr() net.Addr {
	return nil
}

// LocalAddr returns the net.Addr of the server side of the connection.
func (s fakeSSHSession) LocalAddr() net.Addr {
	return nil
}

// Environ returns a copy of strings representing the environment set by the
// user for this session, in the form "key=value".
func (s fakeSSHSession) Environ() []string {
	return []string{}
}

// Exit sends an exit status and then closes the session.
func (s fakeSSHSession) Exit(code int) error {
	return nil
}

// Command returns a shell parsed slice of arguments that were provided by the
// user. Shell parsing splits the command string according to POSIX shell rules,
// which considers quoting not just whitespace.
func (s fakeSSHSession) Command() []string {
	return []string{}
}

// RawCommand returns the exact command that was provided by the user.
func (s fakeSSHSession) RawCommand() string {
	return ""
}

// Subsystem returns the subsystem requested by the user.
func (s fakeSSHSession) Subsystem() string {
	return ""
}

// PublicKey returns the PublicKey used to authenticate. If a public key was not
// used it will return nil.
func (s fakeSSHSession) PublicKey() ssh.PublicKey {
	return nil
}

// Context returns the connection's context. The returned context is always
// non-nil and holds the same data as the Context passed into auth
// handlers and callbacks.
//
// The context is canceled when the client's connection closes or I/O
// operation fails.
func (s fakeSSHSession) Context() context.Context {
	return s.c
}

// Permissions returns a copy of the Permissions object that was available for
// setup in the auth handlers via the Context.
func (s fakeSSHSession) Permissions() ssh.Permissions {
	return ssh.Permissions{}
}

// Pty returns PTY information, a channel of window size changes, and a boolean
// of whether or not a PTY was accepted for this session.
func (s fakeSSHSession) Pty() (ssh.Pty, <-chan ssh.Window, bool) {
	return ssh.Pty{}, nil, false
}

// Signals registers a channel to receive signals sent from the client. The
// channel must handle signal sends or it will block the SSH request loop.
// Registering nil will unregister the channel from signal sends. During the
// time no channel is registered signals are buffered up to a reasonable amount.
// If there are buffered signals when a channel is registered, they will be
// sent in order on the channel immediately after registering.
func (s fakeSSHSession) Signals(c chan<- ssh.Signal) {

}

// Break regisers a channel to receive notifications of break requests sent
// from the client. The channel must handle break requests, or it will block
// the request handling loop. Registering nil will unregister the channel.
// During the time that no channel is registered, breaks are ignored.
func (s fakeSSHSession) Break(c chan<- bool) {

}

func (s fakeSSHSession) Read(data []byte) (int, error) {
	//TODO implement me
	panic("implement me")
}

func (s fakeSSHSession) Write(data []byte) (int, error) {
	//TODO implement me
	panic("implement me")
}

func (s fakeSSHSession) Close() error {
	//TODO implement me
	panic("implement me")
}

func (s fakeSSHSession) CloseWrite() error {
	//TODO implement me
	panic("implement me")
}

func (s fakeSSHSession) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	//TODO implement me
	panic("implement me")
}

func (s fakeSSHSession) Stderr() io.ReadWriter {
	//TODO implement me
	panic("implement me")
}

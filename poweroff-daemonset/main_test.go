package main

import (
	"bufio"
	"context"
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestShutdownAcknowledgement(t *testing.T) {
	for _, reply := range []string{"accepted\n", "rejected\n", ""} {
		t.Run(reply, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "host.sock")
			l, err := net.Listen("unix", path)
			require.NoError(t, err)
			defer func() { require.NoError(t, l.Close()) }()
			done := make(chan error, 1)
			go func() {
				c, err := l.Accept()
				if err != nil {
					done <- err
					return
				}
				defer func() {
					if err := c.Close(); err != nil {
						t.Error(err)
					}
				}()
				_, err = bufio.NewReader(c).ReadString('\n')
				if err == nil {
					_, err = c.Write([]byte(reply))
				}
				done <- err
			}()
			err = sendShutdown(context.Background(), path)
			if reply == "accepted\n" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NoError(t, <-done)
		})
	}
	require.Error(t, sendShutdown(context.Background(), filepath.Join(t.TempDir(), "missing")))
}
func TestShutdownRejectsGET(t *testing.T) {
	w := httptest.NewRecorder()
	shutdownHandler(w, httptest.NewRequest(http.MethodGet, "/shutdown", nil))
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

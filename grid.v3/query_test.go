package grid

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/lytics/grid/grid.v3/testetcd"
	"github.com/lytics/retry"
)

func TestQuery(t *testing.T) {
	const (
		nrPeers = 2
		backoff = 10 * time.Second
		timeout = 1 * time.Second
	)

	etcd, cleanup := testetcd.StartAndConnect(t)
	defer cleanup()

	c, err := NewClient(etcd, ClientCfg{Namespace: "testing"})
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= nrPeers; i++ {
		s, err := NewServer(etcd, ServerCfg{Namespace: "testing"})
		if err != nil {
			t.Fatal(err)
		}

		lis, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			t.Fatal(err)
		}

		done := make(chan error, 1)
		go func() {
			defer close(done)
			err := s.Serve(lis)
			if err != nil {
				done <- err
			}
		}()
		defer s.Stop()

		time.Sleep(1 * time.Second)

		// Check for server as a peer.
		var peers []*QueryEvent
		retry.X(6, backoff, func() bool {
			peers, err = c.Query(timeout, Peers)
			t.Logf("peers: %v", peers)
			return err != nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(peers) != i {
			t.Fatalf("expected number of peers: %v, found: %v", i, len(peers))
		}
	}
}

func TestQueryWatch(t *testing.T) {
	const (
		nrPeers = 2
		backoff = 10 * time.Second
		timeout = 1 * time.Second
	)

	etcd, cleanup := testetcd.StartAndConnect(t)
	defer cleanup()

	c, err := NewClient(etcd, ClientCfg{Namespace: "testing"})
	if err != nil {
		t.Fatal(err)
	}

	initialPeers, watch, err := c.QueryWatch(context.Background(), Peers)
	if err != nil {
		t.Fatal(err)
	}
	if len(initialPeers) != 0 {
		t.Fatal("expected 0 peers")
	}

	// Start servers one at a time in the background.
	go func() {
		for i := 1; i <= nrPeers; i++ {
			s, err := NewServer(etcd, ServerCfg{Namespace: "testing"})
			if err != nil {
				t.Fatal(err)
			}

			lis, err := net.Listen("tcp", "localhost:0")
			if err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			go func() {
				err := s.Serve(lis)
				if err != nil {
					done <- err
				}
			}()
			defer s.Stop()

			// Sleep before starting next peer.
			time.Sleep(1 * time.Second)
		}
	}()

	// Monitor the watch channel to confirm that started
	// servers are eventually found.
	found := make(map[string]bool)
	for {
		select {
		case <-time.After(10 * time.Second):
			t.Fatalf("expected number of peers: %v, found: %v", nrPeers, len(found))
		case e := <-watch:
			if e.Type == EntityFound {
				found[e.Name()] = true
				t.Logf("found peer: %v", e.Name())
			}
			if len(found) == 2 {
				return
			}
		}
	}
}

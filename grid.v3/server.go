package grid

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"io"
	"log"
	"net"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	etcdv3 "github.com/coreos/etcd/clientv3"
	"github.com/lytics/grid/grid.v3/registry"
	netcontext "golang.org/x/net/context"
	"google.golang.org/grpc"
)

const (
	contextKey = "grid-context-key-xboKEsHA26"
)

type contextVal struct {
	server    *Server
	actorID   string
	actorName string
}

// Logger used for logging when non-nil, default is nil.
var Logger *log.Logger

// Server of a grid.
type Server struct {
	mu        sync.Mutex
	g         Grid
	ctx       context.Context
	cancel    func()
	cfg       ServerCfg
	etcd      *etcdv3.Client
	grpc      *grpc.Server
	stop      sync.Once
	registry  *registry.Registry
	mailboxes map[string]*Mailbox
}

// NewServer for the grid. The namespace must contain only characters
// in the set: [a-zA-Z0-9-_] and no other.
func NewServer(etcd *etcdv3.Client, cfg ServerCfg) (*Server, error) {
	setServerCfgDefaults(&cfg)

	if !isNameValid(cfg.Namespace) {
		return nil, ErrInvalidNamespace
	}
	if etcd == nil {
		return nil, ErrInvalidEtcd
	}
	return &Server{
		cfg:  cfg,
		etcd: etcd,
		grpc: grpc.NewServer(),
	}, nil
}

// SetDefinition of the server's grid. If never called
// then this server will not create actors, will not
// start a leader, and can be used only for serving
// mailboxes.
func (s *Server) SetDefinition(g Grid) {
	s.g = g
}

// Serve the grid on the listener. The listener address type must be
// net.TCPAddr, otherwise an error will be returned.
//
// If an actor by the definition of NewActorDef("leader") is defined
// by the grid, it will automatically get started as a singletion in
// the grid, ie: only one will every be running. This leader can be
// though of as an entry point.
func (s *Server) Serve(lis net.Listener) error {
	// Create a registry client, through which other
	// entities like peers, actors, and mailboxes
	// will be discovered.
	r, err := registry.New(s.etcd)
	if err != nil {
		return err
	}
	s.registry = r
	s.registry.Timeout = s.cfg.Timeout
	s.registry.LeaseDuration = s.cfg.LeaseDuration

	// Create a context that each actor this leader creates
	// will receive. When the server is stopped, it will
	// call the cancel function, which should cause all the
	// actors it is responsible for to shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	ctx = context.WithValue(ctx, contextKey, &contextVal{
		server: s,
	})
	s.ctx = ctx
	s.cancel = cancel

	// Start the registry and monitor that it is
	// running correctly.
	registryErrors := s.monitorRegistry(lis.Addr())

	// Peer's name is the registry's name.
	name := s.registry.Name()

	// Namespaced name, which just includes the namespace.
	nsName, err := namespaceName(Peers, s.cfg.Namespace, name)
	if err != nil {
		return err
	}

	// Register the namespace name, other peers can search
	// for this to discover each other.
	timeoutC, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	err = s.registry.Register(timeoutC, nsName)
	cancel()
	if err != nil {
		return err
	}

	// Create the mailboxes map.
	s.mu.Lock()
	s.mailboxes = make(map[string]*Mailbox)
	s.mu.Unlock()

	// Start a mailbox, this is critical because starting
	// actors in a grid is just done via a normal request
	// sending the message ActorDef to a listening peer's
	// mailbox.
	mailbox, err := NewMailbox(s, name, 100)
	if err != nil {
		return err
	}
	go s.runMailbox(mailbox)

	// Start the leader actor, and monitor, ie: make sure
	// that it's running.
	leaderErrors := s.monitorLeader()

	// gRPC dance to start the gRPC server. The Serve
	// method blocks still stopped via a call to Stop.
	RegisterWireServer(s.grpc, s)
	err = s.grpc.Serve(lis)
	// Something in gRPC returns the "use of..." error
	// message even though it stopped fine. Catch that
	// error and don't pass it up.
	if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
		return err
	} else {
		err = nil
	}

	// If the leader or registry caused errors, report them.
	select {
	case err = <-leaderErrors:
	case err = <-registryErrors:
	default:
	}

	return err
}

// Stop the server, blocking until all mailboxes registered with
// this server have called their close method.
func (s *Server) Stop() {
	zeroMailboxes := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.mailboxes) == 0
	}

	s.stop.Do(func() {
		if s.cancel == nil {
			return
		}
		s.cancel()

		t0 := time.Now()
		for {
			time.Sleep(200 * time.Millisecond)
			if zeroMailboxes() {
				break
			}
			if Logger != nil && time.Now().Sub(t0) > 20*time.Second {
				t0 = time.Now()
				for _, mailbox := range s.mailboxes {
					Logger.Printf("%v: waiting for mailbox to close: %v", s.cfg.Namespace, mailbox)
				}
			}
		}

		s.registry.Stop()
		s.grpc.Stop()
	})
}

// Process a request and return a response. Implements the interface for
// gRPC definition of the wire service. Consider this a private method.
func (s *Server) Process(c netcontext.Context, d *Delivery) (*Delivery, error) {
	getMailbox := func() (*Mailbox, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		m, ok := s.mailboxes[d.Receiver]
		return m, ok
	}

	mailbox, ok := getMailbox()
	if !ok {
		return nil, ErrUnknownMailbox
	}

	// Write the bytes of the request into the byte
	// buffer for decoding.
	var buf bytes.Buffer
	n, err := buf.Write(d.Data)
	if err != nil {
		return nil, err
	}
	if n != len(d.Data) {
		return nil, io.ErrUnexpectedEOF
	}

	// Decode the request into an actual
	// type.
	env := &envelope{}
	dec := gob.NewDecoder(&buf)
	err = dec.Decode(env)
	if err != nil {
		return nil, err
	}
	// This actually converts between the "context" and
	// "golang.org/x/net/context" types of Context so
	// that method signatures are satisfied.
	req := &request{}
	req.msg = env.Msg
	req.context = context.WithValue(c, "", "")
	req.response = make(chan []byte)

	// Send the filled envelope to the actual
	// receiver. Also note that the receiver
	// can stop listenting when it wants, so
	// some defualt or timeout always needs
	// to exist here.
	select {
	case mailbox.c <- req:
	default:
		return nil, ErrReceiverBusy
	}

	// Wait for the receiver to send back a
	// reply, or the context to finish.
	select {
	case <-c.Done():
		return nil, ErrContextFinished
	case data := <-req.response:
		return &Delivery{
			Data: data,
		}, nil
	}
}

// runMailbox for this server.
func (s *Server) runMailbox(mailbox *Mailbox) {
	defer mailbox.Close()
	for {
		select {
		case <-s.ctx.Done():
			return
		case req := <-mailbox.C:
			switch msg := req.Msg().(type) {
			case *ActorDef:
				err := s.startActorC(req.Context(), msg)
				if err != nil {
					req.Respond(&ResponseMsg{
						Succeeded: false,
						Error:     err.Error(),
					})
				} else {
					req.Respond(&ResponseMsg{
						Succeeded: true,
					})
				}
			}
		}
	}
}

// monitorRegistry for errors in the background.
func (s *Server) monitorRegistry(addr net.Addr) <-chan error {
	fault := make(chan error, 1)
	regFaults, err := s.registry.Start(addr)
	if err != nil {
		fault <- err
		s.Stop()
		return fault
	}
	go func() {
		select {
		case <-s.ctx.Done():
		case err := <-regFaults:
			if err == nil {
				return
			}
			select {
			case fault <- err:
			default:
			}
			s.Stop()
		}
	}()
	return fault
}

// monitorLeader starts a leader and keeps tyring to start
// a leader thereafter. If the leader should die on any
// host then some peer will eventually have it start again.
func (s *Server) monitorLeader() <-chan error {
	start := func(def *ActorDef) error {
		var err error
		for i := 0; i < 6; i++ {
			select {
			case <-s.ctx.Done():
				return nil
			default:
			}
			time.Sleep(1 * time.Second)
			err = s.startActor(s.cfg.Timeout, def)
			if err != nil && strings.Contains(err.Error(), registry.ErrAlreadyRegistered.Error()) {
				return nil
			}
		}
		return err
	}

	fault := make(chan error, 1)
	go func() {
		timer := time.NewTimer(0 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-timer.C:
				err := start(NewActorDef("leader"))
				if err == ErrActorCreationNotSupported {
					return
				}
				if err == ErrNilActorDefinition {
					if Logger != nil {
						Logger.Printf("skipping leader startup since leader definition returned nil")
					}
					return
				}
				if err != nil {
					select {
					case fault <- fmt.Errorf("leader start failed: %v", err):
					default:
					}
					s.Stop()
				}
				timer.Reset(30 * time.Second)
			}
		}

	}()
	return fault
}

// startActor in the current process. This method does not communicate with another
// system to choose where to run the actor. Calling this method will start the
// actor on the current host in the current process.
func (s *Server) startActor(timeout time.Duration, def *ActorDef) error {
	timeoutC, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.startActorC(timeoutC, def)
}

// startActorC in the current process. This method does not communicate with another
// system to choose where to run the actor. Calling this method will start the
// actor on the current host in the current process.
func (s *Server) startActorC(c context.Context, def *ActorDef) error {
	if s.g == nil {
		return ErrActorCreationNotSupported
	}

	if !isNameValid(def.Type) {
		return ErrInvalidActorType
	}
	if !isNameValid(def.Name) {
		return ErrInvalidActorName
	}

	nsName, err := namespaceName(Actors, s.cfg.Namespace, def.Name)
	if err != nil {
		return err
	}

	actor, err := s.g.MakeActor(def)
	if err != nil {
		return err
	}
	if actor == nil {
		return ErrNilActorDefinition
	}

	// Register the actor. This acts as a distributed mutex to
	// prevent an actor from starting twice on one system or
	// many systems.
	timeout, cancel := context.WithTimeout(c, s.cfg.Timeout)
	err = s.registry.Register(timeout, nsName)
	cancel()
	if err != nil {
		return err
	}

	// The actor's context contains its full id, it's name and the
	// full registration, which contains the actor's namespace.
	actorCtx := context.WithValue(s.ctx, contextKey, &contextVal{
		server:    s,
		actorID:   nsName,
		actorName: def.Name,
	})

	// Start the actor, unregister the actor in case of failure
	// and capture panics that the actor raises.
	go func() {
		defer func() {
			timeout, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout)
			s.registry.Deregister(timeout, nsName)
			cancel()
		}()
		defer func() {
			if err := recover(); err != nil {
				if Logger != nil {
					stack := niceStack(debug.Stack())
					log.Printf("panic in namespace: %v, actor: %v, recovered from: %v, stack: %v",
						s.cfg.Namespace, def.Name, err, stack)
				}
			}
		}()
		actor.Act(actorCtx)
	}()

	return nil
}
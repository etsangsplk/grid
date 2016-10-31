package grid

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/lytics/grid/grid.v3/discovery"
	"github.com/lytics/grid/grid.v3/message"
)

const (
	contextKey = "grid-context-key-xboKEsHA26"
)

type contextVal struct {
	r         *registration
	actorID   string
	actorName string
}

var (
	ErrInvalidContext    = errors.New("invalid context")
	ErrUnknownResponse   = errors.New("unknown response")
	ErrInvalidNamespace  = errors.New("invalid namespace")
	ErrAlreadyRegistered = errors.New("already registered")
)

var (
	Logger *log.Logger
)

var (
	mu       *sync.Mutex
	dice     *rand.Rand
	registry map[string]*registration
)

func init() {
	mu = &sync.Mutex{}
	dice = NewSeededRand()
	registry = make(map[string]*registration)
}

type registration struct {
	id        string
	g         Grid
	mm        *message.Messenger
	co        *discovery.Coordinator
	namespace string
}

func (r *registration) ID() string {
	return r.id
}

// Grid of actors.
type Grid interface {
	Namespace() string
	MakeActor(def *ActorDef) (Actor, error)
}

// Peers in the given namespace. The names can be used in
// RequestActorStart to start actors on a peer.
func Peers(c context.Context, namespace string) ([]string, error) {
	mu.Lock()
	defer mu.Unlock()

	r, ok := registry[namespace]
	if !ok {
		return nil, ErrInvalidNamespace
	}

	regs, err := r.co.FindRegistrations(c, "grid-"+namespace)
	if err != nil {
		return nil, err
	}

	peers := make([]string, 0)
	for _, reg := range regs {
		peers = append(peers, reg.Key)
	}

	return peers, nil
}

// Register the grid.
func Register(co *discovery.Coordinator, mm *message.Messenger, g Grid) error {
	mu.Lock()
	defer mu.Unlock()

	r, ok := registry[g.Namespace()]
	if ok {
		return ErrAlreadyRegistered
	}

	r = &registration{
		id: fmt.Sprintf("grid-%v-%v", g.Namespace(), co.Address()),
		g:  g,
		co: co,
		mm: mm,
	}
	registry[g.Namespace()] = r

	sub, err := mm.Subscribe(co.Context(), r.ID(), 10)
	if err != nil {
		return err
	}

	go func() {
		for {
			select {
			case <-co.Context().Done():
				timeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				sub.Unsubscribe(timeout)
				cancel()
			case e := <-sub.Mailbox():
				switch msg := e.Msg.(type) {
				case *ActorDef:
					err := startActor(e.Context(), msg)
					if err != nil {
						e.Respond(&ResponseMsg{
							Succeeded: false,
							Error:     err.Error(),
						})
					} else {
						e.Respond(&ResponseMsg{
							Succeeded: true,
						})
					}
				}
			}
		}
	}()

	return nil
}

// RequestActorStart with the actor definition on the peer.
func RequestActorStart(c context.Context, peer string, def *ActorDef) error {
	if err := ValidateActorDef(def); err != nil {
		return err
	}

	r, err := getRegistry(def.Namespace)
	if err != nil {
		return err
	}

	e, err := r.mm.Request(c, peer, def)
	if err != nil {
		return err
	}

	switch msg := e.Msg.(type) {
	case *ResponseMsg:
		return msg.Err()
	default:
		return ErrUnknownResponse
	}
}

// startActor in the current process. This method does not
// communicate or RPC with another system to choose where
// to run the actor. Calling this method will start the
// actor on the current host in the current process.
func startActor(c context.Context, def *ActorDef) error {
	if err := ValidateActorDef(def); err != nil {
		return err
	}

	r, err := getRegistry(def.Namespace)
	if err != nil {
		return err
	}

	actor, err := r.g.MakeActor(def)
	if err != nil {
		return err
	}

	// Register the actor. This acts as a distributed mutex to
	// prevent an actor from starting twice on one system or
	// many systems.
	timeout, cancel := context.WithTimeout(c, 10*time.Second)
	err = r.co.Register(timeout, def.regID())
	cancel()
	if err != nil {
		return err
	}

	// The actor's context contains its full id, it's name and the
	// full registration, which contains the actors namespace.
	actorCtx := context.WithValue(r.co.Context(), contextKey, &contextVal{
		r:         r,
		actorID:   def.ID(),
		actorName: def.Name,
	})

	// Start the actor, unregister the actor in case of failure
	// and capture panics that the actor raises.
	go func() {
		defer func() {
			timeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = r.co.Deregister(timeout, def.ID())
			cancel()
		}()
		defer func() {
			if err := recover(); err != nil {
				if Logger != nil {
					log.Printf("panic in actor: %v, recovered with: %v", def.ID(), err)
				}
			}
		}()
		actor.Act(actorCtx)
	}()

	return nil
}

func getRegistry(namespace string) (*registration, error) {
	mu.Lock()
	defer mu.Unlock()

	r, ok := registry[namespace]
	if !ok {
		return nil, ErrInvalidNamespace
	}

	return r, nil
}

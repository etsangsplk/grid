package main

import (
	"log"
	"strings"
	"time"

	"github.com/lytics/grid"
	"github.com/lytics/grid/condition"
)

func NewLeaderActor(def *grid.ActorDef, conf *Conf) grid.Actor {
	return &LeaderActor{def: def, conf: conf}
}

type LeaderActor struct {
	def  *grid.ActorDef
	conf *Conf
}

func (a *LeaderActor) ID() string {
	return a.def.ID()
}

func (a *LeaderActor) Flow() Flow {
	return Flow(a.def.Settings["flow"])
}

func (a *LeaderActor) Act(g grid.Grid, exit <-chan bool) bool {
	c, err := grid.NewConn(a.ID(), g.Nats())
	if err != nil {
		log.Fatalf("%v: error: %v", a.ID(), err)
	}
	defer c.Close()

	state := NewLeaderState()
	s := condition.NewState(g.Etcd(), 5*time.Minute, g.Name(), "state", a.Flow().Name(), a.ID())
	err = s.Init(state)
	if err != nil {
		_, err := s.Fetch(state)
		if err != nil {
			log.Fatalf("%v: failed to init or fetch state: %v", a.ID(), err)
		}
	}
	log.Printf("%v: starting with state: %v, index: %v", a.ID(), state, s.Index())

	w := condition.NewCountWatch(g.Etcd(), exit, g.Name(), "consumers", a.Flow().Name())
	<-w.WatchUntil(a.conf.NrConsumers)

	log.Printf("%v: all consumers joined", a.ID())

	for {
		select {
		case <-exit:
			_, err := s.Store(state)
			if err != nil {
				log.Printf("%v: failed to store state on exit: %v", a.ID(), err)
			}
			return true
		case err := <-w.WatchError():
			log.Printf("%v: fatal: %v", a.ID(), err)
			_, err = s.Store(state)
			if err != nil {
				log.Printf("%v: failed to store state on exit: %v", a.ID(), err)
			}
			return false
		case <-w.WatchUntil(0):
			for p, n := range state.ProducerCounts {
				log.Printf("%v: producer: %v, sent: %v, consumers received: %v, delta: %v", a.ID(), p, n, state.ConsumerCounts[p], n-state.ConsumerCounts[p])
			}
			return true
		case m := <-c.ReceiveC():
			switch m := m.(type) {
			case ResultMsg:
				if strings.Contains(m.From, "producer") {
					state.ProducerCounts[m.Producer] = m.Count
				}
				if strings.Contains(m.From, "consumer") {
					state.ConsumerCounts[m.Producer] += m.Count
				}
			}
		}
	}
}

type LeaderState struct {
	Start          time.Time
	ConsumerCounts map[string]int
	ProducerCounts map[string]int
}

func NewLeaderState() *LeaderState {
	return &LeaderState{ConsumerCounts: make(map[string]int), ProducerCounts: make(map[string]int)}
}

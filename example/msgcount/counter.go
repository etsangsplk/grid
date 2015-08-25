package main

import (
	"log"
	"strconv"
	"strings"

	"github.com/lytics/grid"
	"github.com/lytics/grid/condition"
)

func NewCounterActor(id string, conf *Conf) grid.Actor {
	return &CounterActor{id: id, conf: conf}
}

type CounterActor struct {
	id   string
	conf *Conf
}

func (a *CounterActor) ID() string {
	return a.id
}

func (a *CounterActor) Act(g grid.Grid, exit <-chan bool) bool {
	nr, err := a.NumberFromName()
	if err != nil {
		log.Printf("error: %v: failed to find my number: %v", a.id, err)
	}
	c := grid.NewConn(a.id, g.Nats())
	counts := make(map[string]map[int]bool)

	readerexit := condition.ActorJoinExit(g.Etcd(), exit, a.conf.GridName, "reader", a.conf.NrReaders)
	for {
		select {
		case <-exit:
			return true
		case r := <-readerexit:
			bucket := counts[r]
			missing := 0
			for i := 0; i < a.conf.NrMessages; i++ {
				if i%a.conf.NrCounters == nr {
					if !bucket[i] {
						missing++
					}
				}
			}
			log.Printf("%v: from actor: %v, missing: %v", a.id, r, missing)
		case m := <-c.ReceiveC():
			switch m := m.(type) {
			case CntMsg:
				bucket, ok := counts[m.From]
				if !ok {
					bucket = make(map[int]bool)
					counts[m.From] = bucket
				}
				bucket[m.Number] = true
			}
		}
	}
}

func (a *CounterActor) NumberFromName() (int, error) {
	parts := strings.Split(a.id, ".")
	nr, err := strconv.Atoi(parts[2])
	if err != nil {
		return -1, err
	}
	return nr, nil
}

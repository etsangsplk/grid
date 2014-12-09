package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"runtime"
	"strings"
	"time"

	"github.com/Shopify/sarama"
	"github.com/lytics/grid"
	metrics "github.com/rcrowley/go-metrics"
)

const (
	Key          = ""
	GridName     = "loadtest-grid"
	ConsumerName = "loadtest-grid-console-consumer"
)

type NumMesg struct {
	Data int
}

func NewNumMesg(i int) *NumMesg {
	return &NumMesg{i}
}

type numcoder struct {
	*json.Encoder
	*json.Decoder
}

func (c *numcoder) New() interface{} {
	return &NumMesg{}
}

func NewNumMesgDecoder(r io.Reader) grid.Decoder {
	return &numcoder{nil, json.NewDecoder(r)}
}

func NewNumMesgEncoder(w io.Writer) grid.Encoder {
	return &numcoder{json.NewEncoder(w), nil}
}

var peercnt = flag.Int("peercnt", 1, "the expected number of peers that will take part in the grid.")
var mode = flag.Int("mode", 1, "the mode to run this process in.  [1] Run as the load_test grid [2] Generate messages (producer)")
var kafka = flag.String("kafka", "localhost:10092", `listof kafka brokers.  example: "localhost:10092,localhost:10093"`)
var khosts []string

/*
	topology map:

	(generateTestMessages)
	 \->add1
	    \->(add)->mulBy2
	              \->(mul)->divBy2
	                	     \->(div)->sub1
	                		            \->(sub)->collector
	                		                       \->(coll)-->StdOut
*/
func main() {
	flag.Parse()

	khosts = strings.Split(*kafka, ",")

	go logMetrics()
	if *mode == 1 {
		runtime.GOMAXPROCS(3)

		kconf := grid.DefaultKafkaConfig()
		kconf.Brokers = khosts

		g, err := grid.NewWithKafkaConfig("loadtest", *peercnt, kconf)
		if err != nil {
			log.Fatalf("error: example: failed to create grid: %v", err)
		}

		g.AddDecoder(NewNumMesgDecoder, "add1", "mulBy2", "divBy2", "sub1", "collector")
		g.AddEncoder(NewNumMesgEncoder, "add1", "mulBy2", "divBy2", "sub1", "collector")

		err = g.Add("add1", 3, add, "add1")
		if err != nil {
			log.Fatalf("error: example: %v", err)
		}

		err = g.Add("mulBy2", 3, mul, "mulBy2")
		if err != nil {
			log.Fatalf("error: example: %v", err)
		}

		err = g.Add("divBy2", 3, div, "divBy2")
		if err != nil {
			log.Fatalf("error: example: %v", err)
		}

		err = g.Add("sub1", 3, sub, "sub1")
		if err != nil {
			log.Fatalf("error: example: %v", err)
		}

		err = g.Add("collector", 3, collector, "collector")
		if err != nil {
			log.Fatalf("error: example: %v", err)
		}

		g.Start()

		g.Wait()
	} else if *mode == 2 {
		generateTestMessages()
	}
}

func logMetrics() {
	ticker := time.NewTicker(time.Second * 10)

	for now := range ticker.C {
		fmt.Println("------------ ", now, " ---------------")
		bytes, _ := json.MarshalIndent(metrics.DefaultRegistry, " ", " ")
		fmt.Println(string(bytes))
	}
}

func generateTestMessages() {
	client, err := sarama.NewClient(ConsumerName, khosts, sarama.NewClientConfig())
	if err != nil {
		log.Fatalf("failed to create kafka client: %v", err)
	}
	defer client.Close()

	producer, err := sarama.NewProducer(client, nil)
	if err != nil {
		log.Fatalf("failed to create kafka producer: %v", err)
	}
	defer producer.Close()

	log.Println("Starting to generate test messages.")

	gen := metrics.NewMeter()
	metrics.GetOrRegister("gen.msg.counter", gen)

	for i := 1; i < 10000000; i++ {
		data := struct {
			Data int
		}{
			i,
		}
		if bytes, err := json.Marshal(data); err != nil {
			log.Printf("error: %v", err)
		} else {
			key := fmt.Sprintf("key-%d", i)

			select {
			case producer.Input() <- &sarama.MessageToSend{
				Topic: "add1",
				Key:   sarama.StringEncoder(key),
				Value: sarama.ByteEncoder(bytes),
			}:
				gen.Mark(1)
			case err := <-producer.Errors():
				panic(err.Err)
			}
		}
	}
}

func add(in <-chan grid.Event) <-chan grid.Event {
	fmt.Println("add started.")
	out := make(chan grid.Event)
	go func() {
		defer close(out)
		for e := range in {
			switch mesg := e.Message().(type) {
			case *NumMesg:
				outmsg := 1 + mesg.Data
				key := fmt.Sprintf("%d", mesg.Data)
				out <- grid.NewWritable("mulBy2", key, NewNumMesg(outmsg))
			default:
				log.Printf("example: unknown message: %T :: %v", mesg, mesg)
			}
		}
	}()

	return out
}

func mul(in <-chan grid.Event) <-chan grid.Event {
	fmt.Println("mul started.")
	out := make(chan grid.Event)
	go func() {
		defer close(out)
		for e := range in {
			switch mesg := e.Message().(type) {
			case *NumMesg:
				outmsg := 2 * mesg.Data
				key := fmt.Sprintf("%d", mesg.Data)
				out <- grid.NewWritable("divBy2", key, NewNumMesg(outmsg))
			default:
				log.Printf("example: unknown message: %T :: %v", mesg, mesg)
			}
		}
	}()

	return out
}

func div(in <-chan grid.Event) <-chan grid.Event {
	fmt.Println("div started.")
	out := make(chan grid.Event)
	go func() {
		defer close(out)
		for e := range in {
			switch mesg := e.Message().(type) {
			case *NumMesg:
				outmsg := mesg.Data / 2
				key := fmt.Sprintf("%d", mesg.Data)
				out <- grid.NewWritable("sub1", key, NewNumMesg(outmsg))
			default:
				log.Printf("example: unknown message: %T :: %v", mesg, mesg)
			}
		}
	}()

	return out
}

func sub(in <-chan grid.Event) <-chan grid.Event {
	fmt.Println("sub started.")
	out := make(chan grid.Event)

	go func() {
		defer close(out)
		for e := range in {
			switch mesg := e.Message().(type) {
			case *NumMesg:
				outmsg := mesg.Data - 1
				key := fmt.Sprintf("%d", mesg.Data)
				out <- grid.NewWritable("collector", key, NewNumMesg(outmsg))
			default:
				log.Printf("example: unknown message: %T :: %v", mesg, mesg)
			}
		}
	}()

	return out
}

func collector(in <-chan grid.Event) <-chan grid.Event {
	fmt.Println("collector started.")
	out := make(chan grid.Event)

	go func() {
		defer close(out)

		for e := range in {
			switch mesg := e.Message().(type) {
			case *NumMesg:
			default:
				log.Printf("example: unknown message: %T :: %v", mesg, mesg)
			}
		}
	}()

	return out
}

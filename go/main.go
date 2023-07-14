package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hamba/avro"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sr"
)

var (
	dir      = flag.String("dir", "../data", "Directory containing .csv files")
	brokers  = flag.String("brokers", "localhost:9092", "Kafka API bootstrap servers")
	registry = flag.String("registry", "http://localhost:8081", "Schema Registry URL")
	topic    = flag.String("topic", "nasdaq_historical", "Produce events to this topic")
	delay    = flag.Int("delay", 100, "Milliseconds delay in between producing events")
	encoding = flag.String("encoding", "json", "Encode the values as 'avro'|'json'")
)

var (
	schema sr.SubjectSchema
	serde  sr.Serde
)

type NasdaqHistorical struct {
	Date   string `avro:"date"`
	Last   string `avro:"last"`
	Volume string `avro:"volume"`
	Open   string `avro:"open"`
	High   string `avro:"high"`
	Low    string `avro:"low"`
}

func (n *NasdaqHistorical) Parse(r string) {
	p := strings.Split(r, ",")
	n.Date = p[0]
	n.Last = p[1]
	n.Volume = p[2]
	n.Open = p[3]
	n.High = p[4]
	n.Low = p[5]
}

func (n *NasdaqHistorical) String() string {
	data, _ := json.Marshal(n)
	return fmt.Sprintf("%s\n", data)
}

func createTopic(admin *kadm.Client, topic string) {
	resp, _ := admin.CreateTopics(context.Background(), 1, 1, nil, topic)
	for _, ctr := range resp {
		if ctr.Err != nil {
			log.Printf("Unable to create topic '%s': %s", ctr.Topic, ctr.Err)
		} else {
			log.Printf("Created topic '%s'", ctr.Topic)
		}
	}
}

func write(client *kgo.Client, path *string, wg *sync.WaitGroup) {
	defer wg.Done()

	log.Printf("Reading .csv file: %s", *path)
	ticker := strings.Split(*path, "_")[1]

	file, err := os.Open(*path)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Scan() // Skip the header row
	for scanner.Scan() {
		var day = NasdaqHistorical{}
		day.Parse(scanner.Text())
		var payload []byte
		if schema.Type == sr.TypeAvro {
			payload = serde.MustEncode(day)
		} else {
			payload, _ = json.Marshal(day)
		}
		r := &kgo.Record{
			Topic: *topic,
			Key:   []byte(ticker),
			Value: payload,
		}
		if err := client.ProduceSync(context.Background(), r).FirstErr(); err != nil {
			log.Printf("Synchronous produce error: %s", err.Error())
		} else {
			log.Printf("[Produced] Topic: %s, Offset: %d, Key: %s, Value: %s",
				r.Topic, r.Offset, r.Key, r.Value)
		}
		time.Sleep(time.Duration(*delay) * time.Millisecond)
	}

	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
}

func read(client *kgo.Client, wg *sync.WaitGroup) {
	defer wg.Done()
	ctx := context.Background()

	for {
		fetches := client.PollFetches(ctx)
		if errors := fetches.Errors(); len(errors) > 0 {
			for _, e := range errors {
				log.Printf("Poll error: %v", e)
			}
		}
		iter := fetches.RecordIter()
		for !iter.Done() {
			record := iter.Next()
			var nh NasdaqHistorical
			if schema.Type == sr.TypeAvro {
				err := serde.Decode(record.Value, &nh)
				if err != nil {
					log.Fatalf("Error decoding Avro value: %v", err)
				}
			} else {
				json.Unmarshal(record.Value, &nh)
			}
			log.Printf("[Consumed] Topic: %s, Offset: %d, Key: %s, Value: %s",
				record.Topic, record.Offset, record.Key, nh.String())
		}
		// Simulate consumer lag
		time.Sleep(time.Duration(2) * time.Second)
		err := client.CommitUncommittedOffsets(ctx)
		if err != nil {
			log.Printf("Unable to commit offsets: %v", err)
		}
		client.AllowRebalance()
	}
}

// Register the schema for the Nasdaq Historical type in Redpanda Schema Registry.
func registerSchema(schemaText string, schemaType sr.SchemaType) sr.SubjectSchema {
	src, err := sr.NewClient(sr.URLs(*registry))
	if err != nil {
		log.Fatalf("Unable to create Schema Registry client: %v", err)
	}
	ss, err := src.CreateSchema(context.Background(), *topic+"-value", sr.Schema{
		Schema: schemaText,
		Type:   schemaType,
	})
	if err != nil {
		log.Fatalf("Unable to create schema: %v", err)
	}
	log.Printf("Registered %s schema ID: %d", ss.Type, ss.ID)
	return ss
}

func main() {
	flag.Parse()

	var t sr.SchemaType
	t.UnmarshalText([]byte(*encoding))
	log.Printf("Encoding: %v", t)

	// Set default schema type to JSON
	schema.Type = sr.TypeJSON
	if t == sr.TypeAvro {
		// Read Avro schema from file
		avroText, err := os.ReadFile(*dir + "/stock.avsc")
		if err != nil {
			log.Fatalf("Unable to read Avro schema file: %v", err)
		}
		schema = registerSchema(string(avroText), sr.TypeAvro)

		// Setup serializer and deserializer
		avroSchema, err := avro.Parse(string(avroText))
		if err != nil {
			log.Fatalf("Unable to parse Avro schema: %v", err)
		}
		serde.Register(
			schema.ID,
			NasdaqHistorical{},
			sr.EncodeFn(func(v any) ([]byte, error) {
				return avro.Marshal(avroSchema, v)
			}),
			sr.DecodeFn(func(b []byte, v any) error {
				return avro.Unmarshal(avroSchema, b, v)
			}),
		)
	}

	opts := []kgo.Opt{}
	opts = append(opts,
		kgo.SeedBrokers(strings.Split(*brokers, ",")...),
		kgo.ConsumeTopics(*topic),
		kgo.ConsumerGroup("demo-pack"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	)
	client, err := kgo.NewClient(opts...)
	if err != nil {
		log.Fatalf("Unable to create client: %v", err)
	}
	if err = client.Ping(context.Background()); err != nil {
		log.Fatalf("Unable to connect: %s", err.Error())
	}

	admin := kadm.NewClient(client)
	defer admin.Close()
	createTopic(admin, *topic)

	files, err := os.ReadDir(*dir)
	if err != nil {
		log.Fatalf("Unable to read directory: %s", *dir)
	}

	var wg sync.WaitGroup
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".csv") {
			wg.Add(1)
			csv := filepath.Join(*dir, f.Name())
			go write(client, &csv, &wg)
		}
		wg.Add(1)
		go read(client, &wg)
	}
	wg.Wait()
	log.Printf("Done!")
}

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

type NasdaqHistorical struct {
	Date   string `csv:"Date"`
	Last   string `csv:"Close/Last"`
	Volume string `csv:"Volume"`
	Open   string `csv:"Open"`
	High   string `csv:"High"`
	Low    string `csv:"Low"`
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

func write(client *kgo.Client, topic *string, delay *int, path *string, wg *sync.WaitGroup) {
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
		day := NasdaqHistorical{}
		day.Parse(scanner.Text())
		dayJson, _ := json.Marshal(day)

		r := &kgo.Record{
			Topic: *topic,
			Key:   []byte(ticker),
			Value: []byte(string(dayJson)),
		}
		if err := client.ProduceSync(context.Background(), r).FirstErr(); err != nil {
			log.Printf("Synchronous produce error: %s", err.Error())
		} else {
			log.Printf("Produced: [Topic: %s \tKey: %s \tValue: %s]", r.Topic, r.Key, r.Value)
		}
		time.Sleep(time.Duration(*delay) * time.Millisecond)
	}

	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
}

func read(client *kgo.Client, topic *string, wg *sync.WaitGroup) {
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
			log.Printf("Consumed: [Topic: %s \tOffset: %d \tKey: %s \tValue: %s]",
				record.Topic, record.Offset, record.Key, record.Value)
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

func main() {
	dir := flag.String("dir", "../data", "directory containing .csv files")
	brokers := flag.String("brokers", "localhost:19092", "Kafka API bootstrap servers")
	topic := flag.String("topic", "nasdaq_historical", "Produce events to this topic")
	delay := flag.Int("delay", 100, "Milliseconds delay in between producing events")
	flag.Parse()

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

	files, err := ioutil.ReadDir(*dir)
	if err != nil {
		log.Fatalf("Unable to read directory: %s", *dir)
	}

	var wg sync.WaitGroup
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".csv") {
			wg.Add(1)
			csv := filepath.Join(*dir, f.Name())
			go write(client, topic, delay, &csv, &wg)
		}
		wg.Add(1)
		go read(client, topic, &wg)
	}
	wg.Wait()
	log.Printf("Done!")
}

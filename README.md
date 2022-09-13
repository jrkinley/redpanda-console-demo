# Redpanda Console Demo

https://docs.redpanda.com/docs/console/

## Setup environment

Start a local Redpanda cluster and Redpanda Console using the provided Docker Compose file:

```shell
docker-compose up -d
[+] Running 5/5
 ⠿ Network redpanda-console_default      Created
 ⠿ Container redpanda1                   Started
 ⠿ Container redpanda2                   Started
 ⠿ Container redpanda3                   Started
 ⠿ Container redpanda-console-console-1  Started
```

Once the containers have started, Redpanda Console will be available at http://localhost:8080.

## Produce JSON data

The [data](./data/) folder contains historical stock data for four symbols. The data can be downloaded from the [Nasdaq](https://www.nasdaq.com/market-activity/stocks/coke/historical) website in CSV format.

Run the [Go](./go/main.go) application to read the data files in parallel, transform each line from CSV to JSON, and send the messages to a Redpanda topic named `nasdaq_historical`. The application intentionally slows the stream down by adding a small delay after producing each message, and another delay before committing offsets on the consumer side to simulate lag. This example does not use the Schema Registry.

```shell
cd go
go run main.go -brokers localhost:19092,localhost:29092,localhost:39092
```

![Redpanda Console Topic View](./topic.png)

## Produce Protobuf data

The [Protobuf](https://developers.google.com/protocol-buffers/) example uses the same historical stock data as above, but uses [Confluent's Protobuf Schema Serializer and Deserializer](https://docs.confluent.io/platform/current/schema-registry/serdes-develop/serdes-protobuf.html) to send Protobuf messages to a Redpanda topic named `nasdaq_historical_proto`. The Java-based library registers the Protobuf schema in the Schema Registry, which is subsequently used by Redpanda Console to [deserialize messages](https://docs.redpanda.com/docs/console/features/record-deserialization/) into JSON for [filtering](https://docs.redpanda.com/docs/console/features/programmable-push-filters/).

```shell
cd proto
mvn clean compile assembly:single
java -jar target/protobuf-example-1.0.0-jar-with-dependencies.jar
```

![Redpanda Console Schema Registry](./schema.png)

## Push Filters

Use Redpanda Console's [push filters](https://docs.redpanda.com/docs/console/features/programmable-push-filters/) to search for specific messages in the `nasdaq_historical` or `nasdaq_historical_proto` topics. Note that you might have to provide a custom offset to see the results.

1. Filter the topic by the key `NVDA` and the record year `2022`:

```javascript
var parts = value.Date.split("/");
return (key == "NVDA") && (parts[2] == "2022")
```

2. Include only the records that have had a 10% increase in value:

```javascript
var open = parseFloat(value.Open.slice(1));
var close = parseFloat(value.Last.slice(1));
return 100/open*close > 110
```

![Redpanda Console Push Filters](./filter.png)

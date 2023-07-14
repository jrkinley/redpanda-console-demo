package com.redpanda;

import java.io.BufferedReader;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Arrays;
import java.util.Collections;
import java.util.Optional;
import java.util.Properties;

import org.apache.commons.cli.CommandLine;
import org.apache.commons.cli.CommandLineParser;
import org.apache.commons.cli.DefaultParser;
import org.apache.commons.cli.HelpFormatter;
import org.apache.commons.cli.Option;
import org.apache.commons.cli.Options;
import org.apache.commons.cli.ParseException;
import org.apache.kafka.clients.admin.AdminClient;
import org.apache.kafka.clients.admin.NewTopic;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.clients.producer.Callback;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.clients.producer.RecordMetadata;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.apache.kafka.common.serialization.StringSerializer;

import nasdaq.historical.v1.Stock.NasdaqHistorical;
import io.confluent.kafka.serializers.protobuf.KafkaProtobufDeserializer;
import io.confluent.kafka.serializers.protobuf.KafkaProtobufSerializer;

public class ProtobufExample {
    private static String getSymbol(String filename) {
        String[] parts = filename.split("_");
        if (parts.length == 3) {
            return parts[1];
        } else {
            return null;
        }
    }

    public static void main(String[] args) throws InterruptedException {
        Options options = new Options();
        options.addOption(new Option("f", "file", true, "Path to .csv file containing Nasdaq historical data"));
        options.addOption(new Option("b", "brokers", true, "Kafka API bootstrap servers"));
        options.addOption(new Option("r", "registry", true, "Schema Registry address"));
        options.addOption(new Option("t", "topic", true, "Produce events to this topic"));

        CommandLineParser parser = new DefaultParser();
        HelpFormatter formatter = new HelpFormatter();
        CommandLine cmd = null;
        try {
            cmd = parser.parse(options, args);
        } catch (ParseException e) {
            System.out.println(e.getMessage());
            formatter.printHelp("produce-proto", options);
            System.exit(1);
        }

        final String topic = cmd.getOptionValue("topic", "nasdaq_historical_proto");
        final String inputFile = cmd.getOptionValue("file", "HistoricalData_NVDA_5Y.csv");

        Properties props = new Properties();
        props.put("bootstrap.servers", cmd.getOptionValue("brokers", "localhost:9092"));
        props.put("schema.registry.url", cmd.getOptionValue("registry", "http://localhost:8081"));
        props.put("key.serializer", StringSerializer.class.getName());
        props.put("value.serializer", KafkaProtobufSerializer.class.getName());
        props.put("key.deserializer", StringDeserializer.class.getName());
        props.put("value.deserializer", KafkaProtobufDeserializer.class.getName());
        props.put("group.id", "proto-pack");
        props.put("auto.offset.reset", "earliest");
        props.put("enable.auto.commit", "false");
        props.put("specific.protobuf.value.type", NasdaqHistorical.class);

        final AdminClient admin = AdminClient.create(props);
        try {
            admin.deleteTopics(Collections.singletonList(topic));
            final NewTopic newTopic = new NewTopic(topic, Optional.empty(), Optional.empty());
            admin.createTopics(Collections.singletonList(newTopic));
        } finally {
            admin.close();
        }

        Runnable write = () -> {
            // Compile .proto to generate classes:
            // protoc -I=src/main/java/ --java_out=src/main/java/ ../data/stock.proto
            final KafkaProducer<String, NasdaqHistorical> producer = new KafkaProducer<String, NasdaqHistorical>(props);
            String symbol = getSymbol(inputFile);

            try {
                InputStream is = ProtobufExample.class.getClassLoader().getResourceAsStream(inputFile);
                InputStreamReader sr = new InputStreamReader(is, StandardCharsets.UTF_8);
                BufferedReader reader = new BufferedReader(sr);
                reader.readLine(); // Ignore header
                String line = reader.readLine();
                while (line != null) {
                    String[] parts = line.split(",");
                    NasdaqHistorical stock = NasdaqHistorical.newBuilder()
                                                            .setDate(parts[0])
                                                            .setLast(parts[1])
                                                            .setVolume(Float.parseFloat(parts[2]))
                                                            .setOpen(parts[3])
                                                            .setHigh(parts[4])
                                                            .setLow(parts[5])
                                                            .build();
                    ProducerRecord<String, NasdaqHistorical> record = new ProducerRecord<String, NasdaqHistorical>(
                        topic, symbol, stock
                    );
                    producer.send(record, new Callback() {
                        @Override
                        public void onCompletion(RecordMetadata metadata, Exception exception) {
                            if (exception != null) {
                                exception.printStackTrace();
                            } else {
                                System.out.printf("Produced: [Topic: %s \tPartition: %d \tOffset: %d]%n",
                                    metadata.topic(), metadata.partition(), metadata.offset());
                            }
                        }
                    });
                    Thread.sleep(100);
                    line = reader.readLine();
                }
                reader.close();
            } catch (Exception e) {
                e.printStackTrace();
            } finally {
                producer.flush();
                producer.close();
            }
        };

        Runnable read = () -> {
            final KafkaConsumer<String, NasdaqHistorical> consumer = new KafkaConsumer<String, NasdaqHistorical>(props);        
            consumer.subscribe(Arrays.asList(topic));
            try {
                while (true) {
                    ConsumerRecords<String, NasdaqHistorical> records = consumer.poll(Duration.ofMillis(100));
                    for (ConsumerRecord<String, NasdaqHistorical> record : records) {
                        System.out.printf("Consumed: [Topic: %s \tPartition: %d \tOffset: %d \tKey: %s]%n",
                            record.topic(), record.partition(), record.offset(), record.key());
                    }
                    Thread.sleep(2000);
                }
            } catch (Exception e) {
                e.printStackTrace();
            } finally {
                System.out.println("Stopped consumer");
                consumer.close();
            }
        };

        Thread t1 = new Thread(write);
        t1.start();
        Thread t2 = new Thread(read);
        t2.start();
        t1.join();
        t2.join();
    }
}

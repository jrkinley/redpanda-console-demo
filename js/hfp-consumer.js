import fetch from "node-fetch";
import chalk from "chalk";
import parseArgs from "minimist";
import { Kafka } from "kafkajs";
import avro from "avro-js";

let args = parseArgs(process.argv.slice(2))
const help = `
  ${chalk.green("hfp-consumer.js")}
    Consumes Digitransit High-Frequency Positioning (HFP) events from a Redpanda topic.
    This application assumes the events are serialised as Avro, and will retrieve the
    Avro schema from the Redpanda schema registry for deserialisation.

    See: https://digitransit.fi/en/developers/apis/4-realtime-api/vehicle-positions/

  ${chalk.bold("USAGE")}

    > node hfp-consumer.js --help
    > node hfp-consumer.js [-b host:port] [-t topic_name] [-r registry_url]
 
  ${chalk.bold("OPTIONS")}

    -h, --help      Shows this help message

    -b, --brokers   Comma-separated list of the host and port for each broker
                      default: localhost:9092

    -t, --topic     Topic where events are sent
                      default: digitransit-hfp

    -r, --registry  Schema registry URL
                      default: http://localhost:8081
`

if (args.help || args.h) {
  console.log(help)
  process.exit(0)
}
  
const brokers = (args.b || args.brokers || "localhost:9092").split(",")
const topicName = args.t || args.topic || "digitransit-hfp"
const schemaRegistry = args.r || args.registry || "http://localhost:8081"
var cachedType = null

/**
 * Retrieve a schema from the schema registry by id
 * @param {*} id  Id of the schema. Retrieved from an Avro serialised message with schema
 *                registry encoding.
 * @returns       Avro protocol type
 */
async function getSchemaById(id) {
  const url = `${schemaRegistry}/schemas/ids/${id}`
  console.log(chalk.bgGrey("GET: " + url))
  const response = await fetch(url)
  if (!response.ok) {
    console.error(chalk.red("Error: ") + await response.text())
    process.exit(1)
  }
  const json = await response.json()
  const schema = json["schema"]
  const type = avro.parse(schema)
  console.log(chalk.green("Retrieved schema: ") + JSON.stringify(schema))
  return type
}

/* Connect to Redpanda and consume the HFP events */
const redpanda = new Kafka({"brokers": brokers});
const consumer = redpanda.consumer({
  groupId: "hfp-group",
  sessionTimeout: 60000
})
await consumer.connect()
await consumer.subscribe({
  topic: topicName,
  fromBeginning: true
})
await consumer.run({
  autoCommit: false,
  eachMessage: async ({ topic, partition, message }) => {
    const payload = message.value
    const _ = payload.subarray(0, 1) // Magic byte
    const schemaId = payload.subarray(1, 5).readInt32BE(0)
    const msgBuf = payload.subarray(5, payload.length)
    if (cachedType == null) {
      cachedType = await getSchemaById(schemaId)
    }
    const plain = cachedType.fromBuffer(msgBuf);
    console.log(chalk.green("Consumed message: ") + JSON.stringify(plain))
  }
})

// /* Disconnect on CTRL+C */
process.on("SIGINT", async () => {
  try {
    console.log(chalk.bgGrey("Disconnecting..."))
    await consumer.disconnect()
    process.exit(0)
  } catch (_) {
    process.exit(1)
  }
})

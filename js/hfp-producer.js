import fs from "fs"
import mqtt from "mqtt"
import chalk from "chalk"
import parseArgs from "minimist"
import { Kafka } from "kafkajs"
import avro from "avro-js"
import fetch from "node-fetch";

let args = parseArgs(process.argv.slice(2))
const help = `
  ${chalk.green("hfp-producer.js")}
    Connects to the Digitransit High-Frequency Positioning (HFP) API and subscribes to
    near real time public transport vehicle movements in Helsinki. The MQTT message
    payloads are provided as UTF-8 encoded JSON strings, which are serialised as Avro
    messages before sending to a Redpanda topic. The Avro schema is stored in the
    Redpanda schema registry. 
    
    See: https://digitransit.fi/en/developers/apis/4-realtime-api/vehicle-positions/

  ${chalk.bold("USAGE")}

    > node hfp-producer.js --help
    > node hfp-producer.js [-b host:port] [-t topic_name] [-r registry_url]
 
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

// Connect to Redpanda and create topic
const redpanda = new Kafka({"brokers": brokers})
const producer = redpanda.producer()
await producer.connect()
console.log(chalk.green(`Connected to Redpanda at: ${brokers}`))

const admin = redpanda.admin()
await admin.connect()
const allTopics = await admin.listTopics()
if (topicName in allTopics) {
  await admin.deleteTopics({topics: [topicName]})
}
await admin.createTopics({
  topics: [{ 
    topic: topicName,
    numPartitions: 3,
    replicationFactor: 3
  }]
})
await admin.disconnect()

// Register Avro schema
const subject = "hfp-vp-value"
const deleteSubject = `${schemaRegistry}/subjects/${subject}`
var response = await fetch(deleteSubject, {
  method: "DELETE"
})
if (response.ok) {
  console.log(await response.text())
}

const schema = fs.readFileSync("vp.avsc", "utf-8")
const type = avro.parse(schema)
const post = `${schemaRegistry}/subjects/hfp-vp-value/versions`
console.log(`POST: ${post}`);
var response = await fetch(post, {
    method: "POST",
    body: JSON.stringify({
      "schema": schema
    }),
    headers: {"Content-Type": "application/json"}
  }
)
if (!response.ok) {
  console.error(chalk.red(`Error registering schema: ${await response.text()}`));
  process.exit(1);
}
var response = await response.json()
const schemaId = response["id"]
console.log(chalk.green(`Registered schema: ${schemaId}`))

// Connect to the HFP MQTT API and subscibe to vehicle position (vp) events
const hslHost = "mqtt.hsl.fi"
const hslPort = 8883
const hslTopic = "/hfp/v2/journey/ongoing/vp/#"
const mqttOptions = {
  host: hslHost,
  port: hslPort,
  protocol: "mqtts"
}
const client  = mqtt.connect(mqttOptions)

client.subscribe(hslTopic)
client.on("connect", function () {
  console.log(`Connected to mqtts://${hslHost}:${hslPort}${hslTopic}`)
})

const defaultOffset = 0
const magicByte = Buffer.alloc(1)

client.on("message", async function (topic, message) {
  // Serialise as Avro and send to Redpanda
  var obj = JSON.parse(message)
  if (!("VP" in obj)) {
    return
  }
  obj = obj["VP"]
  const msg = {
    "desi": obj["desi"],
    "oper": obj["oper"],
    "veh": obj["veh"],
    "tst": obj["tst"],
    "spd": obj["spd"]||-1,
    "lat": obj["lat"]||-1,
    "long": obj["long"]||-1,
    "dl": obj["dl"]||-1,
    "start": obj["start"],
    "stop": obj["stop"] != null ? obj["stop"].toString() : "",
    "route": obj["route"],
  }

  try {
    const msgBuf = type.toBuffer(msg)
    const idBuf = Buffer.alloc(4)
    idBuf.writeInt32BE(schemaId, defaultOffset)
    const payload = Buffer.concat([
      magicByte, idBuf, msgBuf
    ])
    const meta = await producer.send({
      topic: topicName,
      messages: [{ value: payload }],
    })
    console.log(`${chalk.green("Produced")}: ${JSON.stringify(meta)}`)
  } catch (e) {
    console.log(`${chalk.red("Error producing message")}: ${msg}`)
    console.error(e)
    process.exit(1)
  }
})

/* Disconnect on CTRL+C */
process.on("SIGINT", async () => {
  try {
    console.log("Disconnecting...")
    client.end()
    await producer.disconnect();
    process.exit(0);
  } catch (e) {
    console.error(e.toString())
    process.exit(1);
  }
});

import { Redis } from "ioredis";
import type { IPCMessage } from "./types.js";

const CONSUMER_GROUP = "kraclaw-agent";
const READ_TIMEOUT = 500;

function outputKey(group: string): string {
  return `kraclaw:ipc:${group}:output`;
}

function inputKey(group: string): string {
  return `kraclaw:ipc:${group}:input`;
}

function closeKey(group: string): string {
  return `kraclaw:ipc:${group}:close`;
}

function notifyChannel(): string {
  return "kraclaw:ipc:notify";
}

export class IPCClient {
  private redis: Redis;
  private group: string;
  private consumer: string;

  constructor(redisUrl: string, group: string) {
    this.redis = new Redis(redisUrl, { maxRetriesPerRequest: null });
    this.group = group;
    this.consumer = `agent-${group}`;
  }

  private async ensureConsumerGroup(stream: string): Promise<void> {
    try {
      await this.redis.xgroup("CREATE", stream, CONSUMER_GROUP, "0", "MKSTREAM");
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      if (!msg.includes("BUSYGROUP")) {
        throw err;
      }
    }
  }

  async readInput(): Promise<IPCMessage | null> {
    const stream = inputKey(this.group);
    await this.ensureConsumerGroup(stream);

    // xreadgroup returns [streamName, [[entryId, [field, value, ...]], ...]][]
    const results = await this.redis.xreadgroup(
      "GROUP",
      CONSUMER_GROUP,
      this.consumer,
      "COUNT",
      "1",
      "BLOCK",
      READ_TIMEOUT,
      "STREAMS",
      stream,
      ">"
    ) as [string, [string, string[]][]][] | null;

    if (!results || results.length === 0) {
      return null;
    }

    const entries = results[0][1];
    if (!entries || entries.length === 0) {
      return null;
    }

    const entryId = entries[0][0];
    const fields = entries[0][1];

    // Fields come as [key, value, key, value, ...]
    let data: string | undefined;
    for (let i = 0; i < fields.length; i += 2) {
      if (fields[i] === "data") {
        data = fields[i + 1];
        break;
      }
    }

    if (!data) {
      console.error("stream entry missing data field", { entryId });
      await this.redis.xack(stream, CONSUMER_GROUP, entryId);
      return null;
    }

    await this.redis.xack(stream, CONSUMER_GROUP, entryId);

    const msg: IPCMessage = JSON.parse(data);
    msg.id = entryId;
    return msg;
  }

  async publishOutput(msg: IPCMessage): Promise<void> {
    const stream = outputKey(this.group);
    await this.ensureConsumerGroup(stream);

    const data = JSON.stringify(msg);
    await this.redis.xadd(stream, "*", "data", data);
    await this.redis.publish(notifyChannel(), this.group);
  }

  async checkClose(): Promise<boolean> {
    const exists = await this.redis.exists(closeKey(this.group));
    return exists > 0;
  }

  async close(): Promise<void> {
    await this.redis.quit();
  }
}

import {
  connect,
  NatsConnection,
  NatsError,
  RetentionPolicy,
  StorageType,
  DeliverPolicy,
  AckPolicy,
} from "nats";
import { createHash } from "crypto";
import type { IPCMessage } from "./types.js";
import type { JetStreamClient, JetStreamManager } from "nats";

// MalformedMessageError is thrown when an IPC message fails to parse. The
// caller should distinguish this from "no message waiting" so that malformed
// messages do not count toward the empty-poll giving-up threshold.
export class MalformedMessageError extends Error {
  constructor(msg?: string) {
    super(msg ?? "malformed ipc message");
    this.name = "MalformedMessageError";
  }
}

// JetStream API error codes (see nats.js source: jetstream/jsapi_types).
// These are stable wire-level codes returned by the NATS server.
const JS_ERR_STREAM_NAME_IN_USE = 10058;
const JS_ERR_CONSUMER_NAME_EXISTS = 10148;

// isStreamExistsError checks whether a JetStream "stream already exists" error
// was returned. Prefers the typed NatsError.api_error.err_code (10058) when
// available; falls back to defensive string matching for older nats.js
// releases that may not surface the code consistently. nats.js v3+ introduces
// richer typed errors — revisit this when upgrading.
function isStreamExistsError(err: unknown): boolean {
  if (err instanceof NatsError && err.api_error?.err_code === JS_ERR_STREAM_NAME_IN_USE) {
    return true;
  }
  const s = err instanceof Error ? err.message : String(err);
  return s.includes("stream already exists") || s.includes("STREAM_EXISTS") || s.includes("stream name already in use");
}

// isConsumerExistsError checks whether a JetStream "consumer already exists"
// error was returned. See isStreamExistsError for rationale.
function isConsumerExistsError(err: unknown): boolean {
  if (err instanceof NatsError && err.api_error?.err_code === JS_ERR_CONSUMER_NAME_EXISTS) {
    return true;
  }
  const s = err instanceof Error ? err.message : String(err);
  return s.includes("consumer already exists") || s.includes("CONSUMER_EXISTS") || s.includes("consumer name already in use");
}

// Max age for IPC stream: 1 hour (must match server-side NATSBroker)
const IPC_STREAM_MAX_AGE = 60 * 60 * 1_000_000_000; // nanoseconds

// Sanitize group JID: first 16 bytes of SHA-256 hex (32 hex chars total)
function sanitizeGroupJID(jid: string): string {
  const hash = createHash("sha256").update(jid).digest("hex");
  return hash.substring(0, 32); // 16 bytes = 32 hex chars
}

// Sanitize agent ID: replace non-alphanumeric/dash/underscore with underscore, max 32 chars
function sanitizeAgentID(id: string): string {
  let safe = id.replace(/[^a-zA-Z0-9_-]/g, "_");
  if (safe.length > 32) {
    safe = safe.substring(0, 32);
  }
  return safe;
}

function isNotFoundError(err: unknown): boolean {
  if (err instanceof Error) {
    const msg = err.message.toLowerCase();
    return msg.includes("not found") || msg.includes("404");
  }
  return String(err).toLowerCase().includes("not found");
}

export class IPCClient {
  private nc: NatsConnection | null = null;
  private jsClient: JetStreamClient | null = null;
  private jsManager: JetStreamManager | null = null;
  private groupJID: string;
  private agentID: string;
  private natsUrl: string;
  private pendingReadInput: Promise<IPCMessage | null> | null = null;

  constructor(natsUrl: string, groupJID: string, agentID: string = "node") {
    this.natsUrl = natsUrl;
    this.groupJID = groupJID;
    this.agentID = agentID;
  }

  // Establish connection to NATS server
  async connect(): Promise<void> {
    if (this.nc !== null) {
      return;
    }

    try {
      this.nc = await connect({
        servers: [this.natsUrl],
      });
    } catch (err) {
      throw new Error(
        `ipc client: failed to connect to NATS: ${err instanceof Error ? err.message : String(err)}`
      );
    }

    try {
      this.jsClient = this.nc.jetstream();
      this.jsManager = await this.nc.jetstreamManager();
    } catch (err) {
      if (this.nc) await this.nc.close();
      this.nc = null;
      throw new Error(
        `ipc client: failed to create jetstream client: ${err instanceof Error ? err.message : String(err)}`
      );
    }
  }

  private sanitized(): string {
    return sanitizeGroupJID(this.groupJID);
  }

  private streamName(): string {
    return "KRACLAW_IPC_" + this.sanitized().toUpperCase();
  }

  private inputSubject(): string {
    return (
      "kraclaw.ipc." +
      this.sanitized() +
      "." +
      sanitizeAgentID(this.agentID) +
      ".input"
    );
  }

  private outputSubject(): string {
    return (
      "kraclaw.ipc." +
      this.sanitized() +
      "." +
      sanitizeAgentID(this.agentID) +
      ".output"
    );
  }

  // ensureStream creates the IPC stream if it does not exist.
  // The server creates it first, but the agent calls this defensively.
  private async ensureStream(): Promise<void> {
    if (!this.jsManager) {
      throw new Error("jetstream manager not initialized");
    }

    const sanitized = this.sanitized();
    const streamName = this.streamName();

    try {
      // Try to get existing stream
      await this.jsManager.streams.info(streamName);
    } catch (err) {
      if (!isNotFoundError(err)) {
        throw new Error(
          `ipc ensure stream: ${err instanceof Error ? err.message : String(err)}`
        );
      }
      // Stream doesn't exist, create it
      try {
        await this.jsManager.streams.add({
          name: streamName,
          subjects: [
            "kraclaw.ipc." + sanitized + ".*.input",
            "kraclaw.ipc." + sanitized + ".*.output",
          ],
          retention: RetentionPolicy.Limits, // LimitsPolicy: respects MaxAge
          storage: StorageType.File,
          max_age: IPC_STREAM_MAX_AGE,
          num_replicas: 1,
        });
      } catch (createErr) {
        // Race condition: stream was created by server or another agent instance
        if (!isStreamExistsError(createErr)) {
          throw createErr;
        }
      }
    }
  }

  // readInput reads the next input message, waiting up to 5 seconds for one to arrive.
  // Note: on the first call, consumer and stream setup adds additional network latency
  // beyond the 5-second window.
  // A pending promise serializes concurrent callers so only one pull is in-flight at a time.
  async readInput(): Promise<IPCMessage | null> {
    if (this.pendingReadInput) {
      return this.pendingReadInput;
    }
    this.pendingReadInput = this.doReadInput().finally(() => {
      this.pendingReadInput = null;
    });
    return this.pendingReadInput;
  }

  // doReadInput performs the actual read operation
  private async doReadInput(): Promise<IPCMessage | null> {
    if (!this.jsClient || !this.jsManager) {
      throw new Error("jetstream client not initialized - call connect() first");
    }

    await this.ensureStream();

    const streamName = this.streamName();
    const consumerName = "agent-" + sanitizeAgentID(this.agentID);

    // Ensure durable pull consumer exists; create if absent, skip if already exists.
    try {
      // Try to get existing consumer
      await this.jsManager.consumers.info(streamName, consumerName);
    } catch (err) {
      if (!isNotFoundError(err)) {
        throw new Error(
          `ipc read input: consumer info: ${err instanceof Error ? err.message : String(err)}`
        );
      }
      // Create new consumer if it doesn't exist
      try {
        await this.jsManager.consumers.add(streamName, {
          durable_name: consumerName,
          filter_subject: this.inputSubject(),
          deliver_policy: DeliverPolicy.All, // DeliverAllPolicy: receive all messages from stream history
          ack_policy: AckPolicy.Explicit,
        });
      } catch (createErr) {
        // Race condition: consumer was created by another agent instance
        if (!isConsumerExistsError(createErr)) {
          throw createErr;
        }
      }
    }

    const consumer = await this.jsClient.consumers.get(streamName, consumerName);
    const jmsg = await consumer.next({ expires: 5_000 });
    if (!jmsg) return null;

    let parsed;
    try {
      parsed = JSON.parse(new TextDecoder().decode(jmsg.data));
    } catch (parseErr) {
      const errMsg = parseErr instanceof Error ? parseErr.message : String(parseErr);
      console.error("ipc read input: failed to parse JSON", {
        error: errMsg,
        data: new TextDecoder().decode(jmsg.data).substring(0, 200),
      });
      await jmsg.ack();
      throw new MalformedMessageError(errMsg);
    }

    const msg: IPCMessage = {
      group: this.groupJID,
      type: parsed.type,
      payload: parsed.payload || {},
      id: String(jmsg.info.streamSequence),
    };

    await jmsg.ack();
    return msg;
  }

  // publishOutput publishes a message from the agent to the server
  async publishOutput(msg: IPCMessage): Promise<void> {
    if (!this.jsClient) {
      throw new Error("jetstream client not initialized - call connect() first");
    }

    await this.ensureStream();

    try {
      const ipcMsg = {
        group: this.groupJID,
        agent_id: this.agentID,
        type: msg.type,
        timestamp: new Date().toISOString(),
        payload: msg.payload || {},
      };

      const data = new TextEncoder().encode(JSON.stringify(ipcMsg));
      await this.jsClient.publish(this.outputSubject(), data);
    } catch (err) {
      throw new Error(
        `ipc publish output: ${err instanceof Error ? err.message : String(err)}`
      );
    }
  }

  // Close closes the NATS connection
  async close(): Promise<void> {
    if (this.pendingReadInput) {
      await this.pendingReadInput.catch((err) => {
        console.error("ipc close: pending read failed during shutdown", {
          error: err instanceof Error ? err.message : String(err),
        });
      });
      this.pendingReadInput = null;
    }
    if (this.nc) {
      await this.nc.close();
      this.nc = null;
      this.jsClient = null;
      this.jsManager = null;
    }
  }
}
